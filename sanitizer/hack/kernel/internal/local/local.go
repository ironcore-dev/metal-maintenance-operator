// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package local

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"kernel/internal/kernel"
	"os"
	"path/filepath"
	"regexp"
)

const (
	KernelsDir = "kernels"
	ModulesDir = "modules"
	Binary     = "vmlinuz"
	Manifest   = "manifest.json"
)

var (
	kernelNameRegexp = regexp.MustCompile(`^([^_]+)_([a-z0-9]+)$`)
)

func kernelName(key kernel.Key) string {
	return fmt.Sprintf("%s_%s", key.Version, key.Architecture)
}

type Repository struct {
	dir string
}

func NewRepository(dir string) *Repository {
	return &Repository{
		dir: dir,
	}
}

var _ kernel.Reader = (*Repository)(nil)
var _ kernel.Writer = (*Repository)(nil)

func (r *Repository) List(_ context.Context, opts kernel.ListOptions) ([]kernel.Key, error) {
	entries, err := os.ReadDir(filepath.Join(r.dir, KernelsDir))
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("error reading kernels dir: %w", err)
		}
		return nil, nil
	}

	var keys []kernel.Key
	for _, entry := range entries {
		m := kernelNameRegexp.FindStringSubmatch(entry.Name())
		if m == nil {
			continue
		}

		version := m[1]
		arch := m[2]
		if opts.Architecture != "" && arch != opts.Architecture {
			continue
		}

		keys = append(keys, kernel.Key{
			Version:      version,
			Architecture: arch,
		})
	}
	return keys, nil
}

func (r *Repository) FetchBinary(_ context.Context, key kernel.Key) (io.ReadCloser, error) {
	f, err := os.Open(r.BinaryFilename(key))
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("error opening %s: %w", r.BinaryFilename(key), err)
		}
		return nil, fmt.Errorf("kernel %s %w", key, kernel.ErrNotFound)
	}
	return f, nil
}

var knownModuleCompressions = []kernel.ModuleCompression{kernel.ModuleCompressionXZ, kernel.ModuleCompressionNone}

func (r *Repository) FetchModule(_ context.Context, key kernel.Key, module string) (io.ReadCloser, error) {
	for _, compression := range knownModuleCompressions {
		f, err := os.Open(r.moduleFilename(key, module, compression))
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				return nil, fmt.Errorf("error opening %s: %w", r.moduleFilename(key, module, compression), err)
			}
			continue
		}
		return f, nil
	}
	return nil, fmt.Errorf("module %s/%s %w", key, module, kernel.ErrNotFound)
}

func (r *Repository) Inspect(_ context.Context, key kernel.Key) (*kernel.Kernel, error) {
	data, err := os.ReadFile(r.ManifestFilename(key))
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("error reading %s: %w", r.ManifestFilename(key), err)
		}
		return nil, fmt.Errorf("manifest %s %w", key, kernel.ErrNotFound)
	}

	return kernel.ReadManifest(data)
}

type bundle struct {
	r     *Repository
	key   kernel.Key
	items []kernel.BundleItem
	cur   io.ReadCloser
}

func (b *bundle) Next() (*kernel.BundleItem, error) {
	if len(b.items) == 0 {
		return nil, io.EOF
	}

	item := b.items[0]
	b.items = b.items[1:]
	if b.cur != nil {
		if err := b.cur.Close(); err != nil {
			return nil, err
		}
	}

	switch item.Type {
	case kernel.BundleItemTypeBinary:
		f, err := b.r.FetchBinary(context.Background(), b.key)
		if err != nil {
			return nil, err
		}

		b.cur = f
	case kernel.BundleItemTypeModule:
		f, err := b.r.FetchModule(context.Background(), b.key, item.Name)
		if err != nil {
			return nil, err
		}

		b.cur = f
	case kernel.BundleItemTypeManifest:
		k, err := b.r.Inspect(context.Background(), b.key)
		if err != nil {
			return nil, err
		}

		data, err := kernel.WriteManifest(k)
		if err != nil {
			return nil, err
		}

		b.cur = io.NopCloser(bytes.NewReader(data))
	default:
		return nil, fmt.Errorf("unknown bundle item type: %v", item.Type)
	}
	return &item, nil
}

func (b *bundle) Read(p []byte) (int, error) {
	if b.cur == nil {
		return 0, fmt.Errorf("must call Next() before Read")
	}
	return b.cur.Read(p)
}

func (b *bundle) Close() error {
	if b.cur == nil {
		return nil
	}
	return b.cur.Close()
}

func (r *Repository) FetchBundle(ctx context.Context, key kernel.Key) (kernel.BundleCloser, error) {
	k, err := r.Inspect(ctx, key)
	if err != nil {
		return nil, err
	}

	// no of loadable modules + 1 manifest + 1 binary
	items := make([]kernel.BundleItem, 0, len(k.Modules.Loadable)+2)
	for _, mod := range k.Modules.Loadable {
		switch mod.Compression {
		case "":
			items = append(items, kernel.BundleItem{
				Type: kernel.BundleItemTypeModule,
				Name: mod.Name,
			})
		case kernel.ModuleCompressionXZ:
			items = append(items, kernel.BundleItem{
				Type: kernel.BundleItemTypeModuleXz,
				Name: mod.Name,
			})
		}
	}
	items = append(items, kernel.BundleItem{
		Type: kernel.BundleItemTypeBinary,
		Name: Binary,
	})
	items = append(items, kernel.BundleItem{
		Type: kernel.BundleItemTypeManifest,
		Name: Manifest,
	})
	return &bundle{
		r:     r,
		key:   key,
		items: items,
	}, nil
}

func (r *Repository) WriteBundle(_ context.Context, key kernel.Key, bundle kernel.Bundle) error {
	kernelDir := r.KernelDir(key)
	if err := os.MkdirAll(kernelDir, 0777); err != nil {
		return fmt.Errorf("creating kernel dir %s: %w", kernelDir, err)
	}

	kernelModulesDir := filepath.Join(r.dir, KernelsDir, kernelName(key), ModulesDir)
	if err := os.MkdirAll(kernelModulesDir, 0777); err != nil {
		return fmt.Errorf("creating kernel modules dir %s: %w", kernelModulesDir, err)
	}

	for {
		it, err := bundle.Next()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return fmt.Errorf("reading bundle %s: %w", key, err)
			}
			return nil
		}

		switch it.Type {
		case kernel.BundleItemTypeBinary:
			if err := writeFileReader(r.BinaryFilename(key), bundle); err != nil {
				return fmt.Errorf("writing kernel binary: %w", err)
			}
		case kernel.BundleItemTypeModuleXz:
			if err := writeFileReader(r.moduleFilename(key, it.Name, kernel.ModuleCompressionXZ), bundle); err != nil {
				return fmt.Errorf("writing module %s: %w", it.Name, err)
			}
		case kernel.BundleItemTypeModule:
			if err := writeFileReader(r.moduleFilename(key, it.Name, kernel.ModuleCompressionNone), bundle); err != nil {
				return fmt.Errorf("writing module %s: %w", it.Name, err)
			}
		case kernel.BundleItemTypeManifest:
			if err := writeFileReader(r.ManifestFilename(key), bundle); err != nil {
				return fmt.Errorf("writing module %s: %w", it.Name, err)
			}
		default:
		}
	}
}

func writeFileReader(filename string, rd io.Reader) error {
	f, err := os.Create(filename)
	if err != nil {
		return fmt.Errorf("error creating %s: %w", filename, err)
	}
	if _, err := io.Copy(f, rd); err != nil {
		_ = f.Close()
		_ = os.Remove(filename)
		return fmt.Errorf("error copying %s: %w", filename, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(filename)
		return fmt.Errorf("error closing %s: %w", filename, err)
	}
	return nil
}

func (r *Repository) Exists(_ context.Context, key kernel.Key) (bool, error) {
	filename := r.KernelDir(key)
	_, err := os.Stat(filename)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return false, fmt.Errorf("stat %s: %w", filename, err)
		}
		return false, nil
	}
	return true, nil
}

func (r *Repository) KernelDir(key kernel.Key) string {
	return filepath.Join(r.dir, KernelsDir, kernelName(key))
}

func (r *Repository) BinaryFilename(key kernel.Key) string {
	return filepath.Join(r.dir, KernelsDir, kernelName(key), Binary)
}

func (r *Repository) moduleFilename(key kernel.Key, module string, compression kernel.ModuleCompression) string {
	var suffix string
	switch compression {
	case kernel.ModuleCompressionXZ:
		suffix = ".ko.xz"
	case kernel.ModuleCompressionNone:
		suffix = ".ko"
	default:
		suffix = ".ko"
	}
	return filepath.Join(r.dir, KernelsDir, kernelName(key), ModulesDir, module+suffix)
}

func (r *Repository) ManifestFilename(key kernel.Key) string {
	return filepath.Join(r.dir, KernelsDir, kernelName(key), Manifest)
}

func (r *Repository) ModuleFilename(key kernel.Key, module string) (string, error) {
	for _, compression := range knownModuleCompressions {
		filename := r.moduleFilename(key, module, compression)
		_, err := os.Stat(filename)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				return "", fmt.Errorf("stat %s: %w", module, err)
			}
			continue
		}
		return filename, nil
	}
	return "", fmt.Errorf("module %s/%s %w", key, module, kernel.ErrNotFound)
}
