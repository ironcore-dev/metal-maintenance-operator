// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
)

var (
	kernelPath    string
	kernelModules []kernelModule
	initramfsPath string
)

type kernelModule struct {
	name     string
	filename string
}

func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Sanitizer E2E Suite")

	SetDefaultEventuallyTimeout(1 * time.Minute)
}

func getFirstKernelAssetDir() string {
	kernelsDir := filepath.Join("..", "bin", "kernels")
	entries, err := os.ReadDir(kernelsDir)
	if err != nil {
		GinkgoLogr.Error(err, "Error reading kernels directory")
		return ""
	}

	for _, entry := range entries {
		if entry.Type().IsDir() {
			return filepath.Join(kernelsDir, entry.Name())
		}
	}
	return ""
}

func resolveKernelModules(kernelDir string, moduleNames []string) []kernelModule {
	GinkgoHelper()

	nameToIdx := make(map[string]int)
	for i, moduleName := range moduleNames {
		nameToIdx[moduleName] = i
	}

	mods := make([]kernelModule, len(moduleNames))
	Expect(filepath.Walk(filepath.Join(kernelDir, "modules"), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}

		name, ok := strings.CutSuffix(strings.TrimSuffix(filepath.Base(path), ".xz"), ".ko")
		if !ok {
			return nil
		}

		idx, ok := nameToIdx[name]
		if !ok {
			return nil
		}

		delete(nameToIdx, name)
		mods[idx] = kernelModule{
			name:     name,
			filename: path,
		}
		return nil
	})).To(Succeed())

	if len(nameToIdx) > 0 {
		missingNames := make([]string, 0, len(nameToIdx))
		for name := range nameToIdx {
			missingNames = append(missingNames, name)
		}
		Fail(fmt.Sprintf("missing modules: %v", missingNames))
	}

	return mods
}

var _ = BeforeSuite(func() {
	kernelAssetsDir := cmp.Or(getFirstKernelAssetDir(), os.Getenv("KERNEL_ASSETS"))
	Expect(kernelAssetsDir).NotTo(BeEmpty(), "set KERNEL_ASSETS to a kernel assets directory")

	kernelPath = filepath.Join(kernelAssetsDir, "vmlinuz")
	abs, err := filepath.Abs(kernelPath)
	Expect(err).NotTo(HaveOccurred())
	Expect(abs).To(BeAnExistingFile(), "kernel does not exist")
	kernelPath = abs

	if s := os.Getenv("SANITIZER_KERNEL_MODULES"); s != "" {
		kernelModules = resolveKernelModules(kernelAssetsDir, strings.Split(s, ","))
	}

	// Build initramfs once for the whole suite.
	initramfsPath = buildInitramfs()
})

func getURootBinary() string {
	binDirFilename := filepath.Join("..", "bin", "u-root")
	_, err := os.Stat(binDirFilename)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			GinkgoLogr.Error(err, "Failed to stat u-root binary directory")
		}
		uRootBinary, err := exec.LookPath("u-root")
		if err != nil {
			Fail(fmt.Sprintf("u-root binary not in path (%v)", err))
		}
		return uRootBinary
	}
	return binDirFilename
}

func buildInitramfs() string {
	out := filepath.Join(GinkgoT().TempDir(), "initramfs.cpio")

	uRootPkgPath, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}",
		"github.com/u-root/u-root").Output()
	Expect(err).NotTo(HaveOccurred(), "go list u-root module")

	initCmdPath := filepath.Join(strings.TrimSpace(string(uRootPkgPath)), "cmds", "core", "init")
	goshCmdPath := filepath.Join(strings.TrimSpace(string(uRootPkgPath)), "cmds", "core", "gosh")
	insmodCmdPath := filepath.Join(strings.TrimSpace(string(uRootPkgPath)), "cmds", "core", "insmod")

	args := []string{"-o", out}

	var prelude strings.Builder
	for _, mod := range kernelModules {
		initramfsModulePath := fmt.Sprintf("modules/%s", filepath.Base(mod.filename))
		args = append(args, fmt.Sprintf("-files=%s:%s", mod.filename, initramfsModulePath))
		_, _ = fmt.Fprintf(&prelude, "insmod /%s && ", initramfsModulePath)
	}

	args = append(args, fmt.Sprintf(`-uinitcmd=gosh -c "%ssanitizer"`, prelude.String()))

	args = append(args, initCmdPath, goshCmdPath, insmodCmdPath, "../cmd/sanitizer")

	cmd := exec.Command(getURootBinary(), args...)
	cmd.Env = append(os.Environ(), "GOOS=linux")
	cmd.Stdout = GinkgoWriter
	cmd.Stderr = GinkgoWriter
	Expect(cmd.Run()).To(Succeed())
	return out
}

type vm struct {
	cmd    *exec.Cmd
	stdout *gbytes.Buffer // captures serial output for Eventually().Should(gbytes.Say(...))
	disks  []string       // paths to scratch disk images
	cancel context.CancelFunc
}
