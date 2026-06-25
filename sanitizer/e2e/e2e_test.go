// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"time"

	"github.com/ironcore-dev/sanitizer/config"
	"github.com/ironcore-dev/sanitizer/reporter"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/vincent-petithory/dataurl"
)

// qemuTarget captures the arch-specific bits of a QEMU invocation.
type qemuTarget struct {
	cmd     string   // qemu-system-* binary
	machine []string // -machine / -cpu / -accel flags
	console string   // kernel console= device (ttyS0, ttyAMA0, ...)
}

func targetFor(arch string) qemuTarget {
	switch arch {
	case "amd64":
		return qemuTarget{
			cmd:     "qemu-system-x86_64",
			machine: []string{"-machine", "q35", "-accel", "tcg"},
			console: "ttyS0",
		}
	case "arm64":
		return qemuTarget{
			cmd:     "qemu-system-aarch64",
			machine: []string{"-machine", "virt", "-cpu", "max"},
			console: "ttyAMA0",
		}
	default:
		Fail(fmt.Sprintf("unsupported sanitizer architecture %q", arch))
		return qemuTarget{}
	}
}

func diskArgs(disks []string) []string {
	var args []string
	for i, d := range disks {
		id := "d" + strconv.Itoa(i)
		args = append(args,
			"-drive", "file="+d+",format=raw,if=none,id="+id,
			"-device", "virtio-blk-pci,drive="+id,
		)
	}
	return args
}

func startVM(ignitionURL string, disks []string) *vm {
	ctx, cancel := context.WithCancel(context.Background())

	arch := os.Getenv("SANITIZER_ARCH")
	if arch == "" {
		arch = runtime.GOARCH
	}
	tgt := targetFor(arch)

	args := []string{
		"-kernel", kernelPath,
		"-initrd", initramfsPath,
		"-append", "console=" + tgt.console + " panic=1 ignition.config.url=" + ignitionURL,
		"-m", "512",
		"-no-reboot",
		"-display", "none",
		"-monitor", "none",
		"-serial", "stdio",
		"-netdev", "user,id=n0",
		"-device", "virtio-net-pci,netdev=n0",
	}
	args = append(args, tgt.machine...)
	args = append(args, diskArgs(disks)...)

	cmd := exec.CommandContext(ctx, tgt.cmd, args...)
	buf := gbytes.NewBuffer()
	cmd.Stdout = io.MultiWriter(buf, GinkgoWriter)
	cmd.Stderr = GinkgoWriter
	Expect(cmd.Start()).To(Succeed())

	return &vm{cmd: cmd, stdout: buf, disks: disks, cancel: cancel}
}

func (v *vm) stop() {
	v.cancel()
	_ = v.cmd.Wait()
}

func makeDisk(sizeMB int, fillWith byte) string {
	GinkgoHelper()

	f, err := os.CreateTemp(GinkgoT().TempDir(), "sanitizer-test-*.img")
	Expect(err).NotTo(HaveOccurred())
	defer func() { _ = f.Close() }()

	buf := make([]byte, 1<<20)
	for i := range buf {
		buf[i] = fillWith
	}
	for i := 0; i < sizeMB; i++ {
		_, err := f.Write(buf)
		Expect(err).NotTo(HaveOccurred())
	}
	return f.Name()
}

type callbackServer struct {
	server   *httptest.Server
	received chan any
}

func startCallbackServer() *callbackServer {
	GinkgoHelper()
	c := &callbackServer{received: make(chan any, 1)}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /ignition", c.handleIgnition)

	mux.HandleFunc("POST /results", func(w http.ResponseWriter, req *http.Request) {
		res := &reporter.Result{}
		_ = json.NewDecoder(req.Body).Decode(res)
		c.received <- res
		w.WriteHeader(http.StatusOK)
	})

	ln, err := net.Listen("tcp", "0.0.0.0:0")
	Expect(err).NotTo(HaveOccurred())
	c.server = &httptest.Server{
		Listener: ln,
		Config:   &http.Server{Handler: mux},
	}
	c.server.Start()
	return c
}

func (c *callbackServer) handleIgnition(w http.ResponseWriter, req *http.Request) {
	cfg := &config.Config{
		ReportURL: fmt.Sprintf("%s/results", c.URL()),
	}
	cfgData, err := json.Marshal(cfg)
	Expect(err).NotTo(HaveOccurred())

	ignition := &config.IgnitionShim{
		Storage: config.IgnitionShimStorage{
			Files: []config.IgnitionShimFile{
				{
					Path: "/sanitizer/config",
					Contents: config.IgnitionShimContents{
						Source: dataurl.New(cfgData, "application/json").String(),
					},
				},
			},
		},
	}
	ignitionData, err := json.Marshal(ignition)
	Expect(err).NotTo(HaveOccurred())

	_, err = w.Write(ignitionData)
	Expect(err).NotTo(HaveOccurred())
}

func (c *callbackServer) URL() string {
	_, port, _ := net.SplitHostPort(c.server.Listener.Addr().String())
	return "http://10.0.2.2:" + port // qemu magic address
}

var _ = Describe("Sanitizer", func() {
	var (
		disk string
		srv  *callbackServer
		v    *vm
	)
	BeforeEach(func() {
		disk = makeDisk(64, 0xAB) // 64 MiB filled with 0xAB
		srv = startCallbackServer()
		v = startVM(fmt.Sprintf("%s/ignition", srv.URL()), []string{disk})
	})

	AfterEach(func() {
		v.stop()
		srv.server.Close()
		_ = os.Remove(disk)
	})

	It("should wipe a virtio-blk disk and report success", func() {
		By("waiting for the vm stdout to report sanitization successful")
		Eventually(func(g Gomega) {
			Expect(v.stdout).NotTo(gbytes.Say("Error sanitizing"))
			g.Expect(v.stdout).To(gbytes.Say("Sanitization successful"))
		}).WithTimeout(1 * time.Minute).Should(Succeed())

		By("waiting for the HTTP server to receive a success message")
		Eventually(srv.received).Should(Receive(Equal(&reporter.Result{Status: "Success"})))
	})
})
