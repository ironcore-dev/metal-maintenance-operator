// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

// Package debian provides helpers for locating and extracting Linux
// kernel images from the Debian archive.
package debian

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"kernel/internal/kernel"
	"net/http"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/blakesmith/ar"
	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
)

var (
	goAchByArch = map[string]string{
		"amd64":    "amd64",
		"arm64":    "arm64",
		"armhf":    "arm",
		"386":      "386",
		"ppc64el":  "ppc64le",
		"s390x":    "s390x",
		"riscv64":  "riscv64",
		"mips64el": "mips64le",
	}
)

type Repository struct {
	archiveBaseURL string
	httpClient     *http.Client
}

var _ kernel.Reader = (*Repository)(nil)

func NewRepository(archiveBaseURL string, httpClient *http.Client) *Repository {
	return &Repository{
		archiveBaseURL: archiveBaseURL,
		httpClient:     httpClient,
	}
}

const DefaultArchiveBaseURL = "https://deb.debian.org/debian/pool/main/l/linux/"

// fetchIndex returns the raw HTML directory listing at base.
func (r *Repository) fetchIndex(ctx context.Context) ([]LinuxImageKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.archiveBaseURL, nil)
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get %s: %s (%s)", r.archiveBaseURL, resp.Status, string(data))
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	idx, err := decodeIndex(data)
	if err != nil {
		return nil, fmt.Errorf("decode index: %w", err)
	}
	return idx, nil
}

var (
	hrefRe = regexp.MustCompile(`href="([^"]+)"`)
	debRe  = regexp.MustCompile(`^([^_]+)_([^_]+)_([^_]+)\.deb$`)
	// ABI is one of:
	//   <X.Y.Z>-<N>            (old: 6.1.0-42)
	//   <X.Y.Z>+debNN[.M]      (new: 6.12.90+deb13, 6.12.90+deb13.1)
	//   <X.Y>                  (rare/exp: 7.1)
	imageRe = regexp.MustCompile(
		`^linux-image-` +
			`(?:(\d+\.\d+(?:\.\d+)?(?:-\d+|\+deb\d+(?:\.\d+)?)?)-)?` +
			`(.+?)` +
			`(-dbg|-unsigned|-signed-template)?$`)

	// Trailing arch tokens to strip from a flavor when they duplicate the
	// architecture/SoC. Order matters: longer first so we don't half-match.
	flavorArchTokens = []string{
		"-armmp-lpae", "-arm64-16k", "-powerpc64le-64k",
		"-amd64", "-arm64", "-armmp", "-686-pae", "-686",
		"-s390x", "-riscv64", "-loong64", "-powerpc64le",
	}
)

func parseLinuxImageKey(filename string) (*LinuxImageKey, error) {
	m := debRe.FindStringSubmatch(filename)
	if m == nil {
		return nil, fmt.Errorf("not a .deb filename: %q", filename)
	}
	pkg, version, arch := m[1], m[2], m[3]

	im := imageRe.FindStringSubmatch(pkg)
	if im == nil {
		return nil, fmt.Errorf("not a linux kernel image: %q", filename)
	}

	// Skip non-bootable variants: -dbg (debug symbols only) and
	// -signed-template (signing pipeline metadata). The bare suffix and
	// -unsigned are both bootable kernel images.
	switch im[3] {
	case "-dbg", "-signed-template":
		return nil, fmt.Errorf("not a bootable kernel image: %q", filename)
	}

	abi, rawFlavor := im[1], im[2]

	var kernelVersion string
	if abi != "" {
		kernelVersion = abi + "-" + rawFlavor
	}

	variant := stripTrailingArchToken(rawFlavor)
	return &LinuxImageKey{
		Filename:      filename,
		Architecture:  arch,
		Version:       version,
		ABI:           abi,
		Variant:       variant,
		KernelVersion: kernelVersion,
	}, nil
}

// stripTrailingArchToken removes the arch/SoC token at the end of the flavor
// name, leaving only the variant. Returns "" if the flavor was just the arch
// token itself (i.e., the default kernel for that arch).
func stripTrailingArchToken(flavor string) string {
	for _, tok := range flavorArchTokens {
		if strings.HasSuffix(flavor, tok) {
			return strings.TrimSuffix(flavor, tok)
		}
		if flavor == strings.TrimPrefix(tok, "-") {
			return ""
		}
	}
	// Flavors that ARE the variant (board/SoC kernels with no separate arch token):
	// rpi, marvell, octeon, loongson-3, 4kc-malta, 5kc-malta.
	return flavor
}

func decodeIndex(data []byte) ([]LinuxImageKey, error) {
	var res []LinuxImageKey
	for _, m := range hrefRe.FindAllStringSubmatch(string(data), -1) {
		filename := m[1]

		kPkg, err := parseLinuxImageKey(filename)
		if err != nil {
			continue
		}

		res = append(res, *kPkg)
	}
	return res, nil
}

type LinuxImageKey struct {
	Filename      string
	Architecture  string
	Version       string
	ABI           string
	Variant       string
	KernelVersion string
}

func KernelKeyFromKernelImagePackage(pkg *LinuxImageKey) (kernel.Key, bool) {
	arch, ok := goAchByArch[pkg.Architecture]
	if !ok {
		return kernel.Key{}, false
	}

	version := pkg.Version
	if pkg.Variant != "" {
		version += "-" + pkg.Variant
	}

	return kernel.Key{
		Architecture: arch,
		Version:      version,
	}, true
}

func (r *Repository) List(ctx context.Context, opts kernel.ListOptions) ([]kernel.Key, error) {
	kPkgs, err := r.fetchIndex(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetch index: %w", err)
	}

	var keys []kernel.Key
	for _, kPkg := range kPkgs {
		key, ok := KernelKeyFromKernelImagePackage(&kPkg)
		if !ok {
			continue
		}

		if opts.Architecture != "" && opts.Architecture != key.Architecture {
			continue
		}

		keys = append(keys, key)
	}
	return keys, nil
}

func (r *Repository) linuxImageKeyFor(ctx context.Context, key kernel.Key) (LinuxImageKey, error) {
	kPkgs, err := r.fetchIndex(ctx)
	if err != nil {
		return LinuxImageKey{}, fmt.Errorf("fetch index: %w", err)
	}

	for _, lKey := range kPkgs {
		k, ok := KernelKeyFromKernelImagePackage(&lKey)
		if !ok {
			continue
		}

		if key == k {
			return lKey, nil
		}
	}
	return LinuxImageKey{}, fmt.Errorf("kernel %s %w", key, kernel.ErrNotFound)
}

// extractDataTar extracts the data.tar.* member from a Debian .deb (ar)
// archive read from r, returning a tar reader over its contents.
func extractDataTar(r io.Reader) (*tar.Reader, error) {
	a := ar.NewReader(r)
	for {
		hdr, err := a.Next()
		if err == io.EOF {
			return nil, errors.New("data.tar member not found in .deb")
		}
		if err != nil {
			return nil, fmt.Errorf("read ar: %w", err)
		}
		name := strings.TrimRight(hdr.Name, "/")
		if !strings.HasPrefix(name, "data.tar") {
			continue
		}
		var stream io.Reader
		switch path.Ext(name) {
		case ".xz":
			x, err := xz.NewReader(a)
			if err != nil {
				return nil, fmt.Errorf("xz: %w", err)
			}
			stream = x
		case ".zst":
			z, err := zstd.NewReader(a)
			if err != nil {
				return nil, fmt.Errorf("zstd: %w", err)
			}
			stream = z
		case ".gz":
			g, err := gzip.NewReader(a)
			if err != nil {
				return nil, fmt.Errorf("gzip: %w", err)
			}
			stream = g
		default:
			return nil, fmt.Errorf("unsupported data archive: %s", name)
		}
		return tar.NewReader(stream), nil
	}
}

func (r *Repository) FetchBundle(ctx context.Context, key kernel.Key) (kernel.BundleCloser, error) {
	kPkg, err := r.linuxImageKeyFor(ctx, key)
	if err != nil {
		return nil, err
	}

	filenameURL := r.archiveBaseURL + kPkg.Filename
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, filenameURL, nil)
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do http request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GET %s: %s (%s)", filenameURL, resp.Status, string(data))
	}

	tr, err := extractDataTar(resp.Body)
	if err != nil {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("extract data tar: %w", err)
	}

	return &tarBundle{
		Closer:        resp.Body,
		linuxImageKey: kPkg,
		tar:           tr,
	}, nil
}

type tarBundle struct {
	io.Closer
	linuxImageKey LinuxImageKey
	tar           *tar.Reader

	mods           kernel.Modules
	manifestReader io.Reader
}

func readBuiltinModules(rd io.Reader) ([]string, error) {
	var (
		s    = bufio.NewScanner(rd)
		mods []string
	)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		base := filepath.Base(line)
		name, ok := strings.CutSuffix(base, ".ko")
		if !ok {
			continue
		}

		mods = append(mods, name)
	}
	if err := s.Err(); err != nil {
		return nil, err
	}
	return mods, nil
}

func (t *tarBundle) Next() (*kernel.BundleItem, error) {
	for {
		hdr, err := t.tar.Next()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return nil, err
			}

			if t.manifestReader != nil {
				return nil, io.EOF
			}

			key, _ := KernelKeyFromKernelImagePackage(&t.linuxImageKey)
			data, err := kernel.WriteManifest(&kernel.Kernel{
				Architecture:  key.Architecture,
				Version:       key.Version,
				KernelVersion: t.linuxImageKey.KernelVersion,
				Modules:       t.mods,
			})
			if err != nil {
				return nil, err
			}

			t.manifestReader = bytes.NewReader(data)
			return &kernel.BundleItem{
				Type: kernel.BundleItemTypeManifest,
			}, nil
		}

		name := strings.TrimPrefix(hdr.Name, "./")
		switch {
		case strings.HasPrefix(name, "boot/vmlinuz-"):
			return &kernel.BundleItem{
				Type: kernel.BundleItemTypeBinary,
				Name: filepath.Base(name),
			}, nil
		case strings.HasSuffix(name, ".ko.xz"):
			modName := strings.TrimSuffix(filepath.Base(name), ".ko.xz")
			t.mods.Loadable = append(t.mods.Loadable, kernel.Module{
				Name:        modName,
				Compression: kernel.ModuleCompressionXZ,
			})
			return &kernel.BundleItem{
				Type: kernel.BundleItemTypeModuleXz,
				Name: modName,
			}, nil
		case strings.HasSuffix(name, ".ko"):
			modName := strings.TrimSuffix(filepath.Base(name), ".ko")
			t.mods.Loadable = append(t.mods.Loadable, kernel.Module{
				Name: modName,
			})
			return &kernel.BundleItem{
				Type: kernel.BundleItemTypeModule,
				Name: modName,
			}, nil
		case filepath.Base(name) == "modules.builtin":
			builtin, err := readBuiltinModules(t.tar)
			if err != nil {
				return nil, fmt.Errorf("read modules.builtin: %w", err)
			}

			t.mods.Builtin = builtin
		}
	}
}

func (t *tarBundle) Read(p []byte) (n int, err error) {
	if t.manifestReader != nil {
		return t.manifestReader.Read(p)
	}
	return t.tar.Read(p)
}

func (r *Repository) Inspect(ctx context.Context, key kernel.Key) (*kernel.Kernel, error) {
	bundle, err := r.FetchBundle(ctx, key)
	if err != nil {
		return nil, err
	}

	defer func() { _ = bundle.Close() }()

	for {
		it, err := bundle.Next()
		if err != nil {
			return nil, err
		}

		switch it.Type {
		case kernel.BundleItemTypeManifest:
			data, err := io.ReadAll(bundle)
			if err != nil {
				return nil, fmt.Errorf("read manifest: %w", err)
			}

			k, err := kernel.ReadManifest(data)
			if err != nil {
				return nil, fmt.Errorf("read builtin modules list: %w", err)
			}

			return k, nil
		default:
		}
	}
}

func (r *Repository) fetchBundleItem(ctx context.Context, key kernel.Key, pred func(item *kernel.BundleItem) bool) (io.ReadCloser, error) {
	bundle, err := r.FetchBundle(ctx, key)
	if err != nil {
		return nil, err
	}

	for {
		item, err := bundle.Next()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return nil, err
			}
			return nil, kernel.ErrNotFound
		}

		if !pred(item) {
			continue
		}

		return bundle, nil
	}
}

func (r *Repository) FetchBinary(ctx context.Context, key kernel.Key) (io.ReadCloser, error) {
	return r.fetchBundleItem(ctx, key, func(item *kernel.BundleItem) bool {
		return item.Type == kernel.BundleItemTypeBinary
	})
}

func (r *Repository) FetchModule(ctx context.Context, key kernel.Key, module string) (io.ReadCloser, error) {
	return r.fetchBundleItem(ctx, key, func(item *kernel.BundleItem) bool {
		return item.Type == kernel.BundleItemTypeModule && item.Name == module
	})
}
