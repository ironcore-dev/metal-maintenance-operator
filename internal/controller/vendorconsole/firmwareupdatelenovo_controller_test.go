// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package vendorconsole

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	vendorconsolev1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/vendorconsole/v1alpha1"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	. "sigs.k8s.io/controller-runtime/pkg/envtest/komega"
)

// lxcaMock is an in-process LXCA stub. It implements just the endpoints the
// firmware controller talks to and exposes counters so tests can assert
// which endpoints were hit.
type lxcaMock struct {
	mu       sync.Mutex
	uuid     string
	hostname string

	repositoryStatus string
	taskStatus       string
	appliedDevices   []string
	appliedPolicy    string
	policyID         string

	compliancePosted bool

	server *httptest.Server
}

func newLXCAMock(uuid, hostname string) *lxcaMock {
	m := &lxcaMock{
		uuid:             uuid,
		hostname:         hostname,
		repositoryStatus: "Complete",
		taskStatus:       "Complete",
		policyID:         "policy-1",
	}
	m.server = httptest.NewServer(http.HandlerFunc(m.handle))
	return m
}

func (m *lxcaMock) URL() string { return m.server.URL }
func (m *lxcaMock) Close()      { m.server.Close() }
func (m *lxcaMock) SetTaskStatus(s string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.taskStatus = s
}
func (m *lxcaMock) AppliedDevices() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.appliedDevices...)
}
func (m *lxcaMock) AppliedPolicy() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.appliedPolicy
}

// LXCA endpoint paths the mock accepts. Duplicated as a constant to appease
// goconst — several handler cases key off /sessions.
const lxcaSessionsPath = "/sessions"

func (m *lxcaMock) handle(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()

	switch {
	case r.URL.Path == lxcaSessionsPath && r.Method == http.MethodPost:
		writeJSON(w, http.StatusOK, map[string]any{
			"response": map[string]any{
				"session": map[string]any{"id": "session-1", "csrf": "csrf-token", "UserId": "u"},
			},
			"result": "success",
		})
	case r.URL.Path == lxcaSessionsPath && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"result": "success"})
	case r.URL.Path == lxcaSessionsPath && r.Method == http.MethodDelete:
		w.WriteHeader(http.StatusNoContent)
	case strings.HasPrefix(r.URL.Path, "/nodes"):
		writeJSON(w, http.StatusOK, map[string]any{
			"nodeList": []map[string]any{
				{"uuid": m.uuid, "hostName": m.hostname, "name": m.hostname, "type": "RackServer"},
			},
		})
	case r.URL.Path == "/files/updateRepositories/firmware/import":
		writeJSON(w, http.StatusAccepted, map[string]any{"jobID": "repo-1"})
	case r.URL.Path == "/updateRepositories/firmware/status":
		writeJSON(w, http.StatusOK, map[string]any{"status": m.repositoryStatus})
	case r.URL.Path == "/compliancePolicies" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"policyList": []any{}})
	case r.URL.Path == "/compliancePolicies" && r.Method == http.MethodPost:
		writeJSON(w, http.StatusCreated, map[string]any{"id": m.policyID, "policyName": "policy"})
	case r.URL.Path == "/compliancePolicies/compareResult":
		m.compliancePosted = true
		w.WriteHeader(http.StatusAccepted)
	case r.URL.Path == "/updatableComponents" && r.Method == http.MethodPost:
		var req struct {
			DeviceList []struct {
				UUID       string `json:"uuid"`
				PolicyName string `json:"policyName"`
			} `json:"deviceList"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		for _, d := range req.DeviceList {
			m.appliedDevices = append(m.appliedDevices, d.UUID)
			m.appliedPolicy = d.PolicyName
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"jobID": "task-1"})
	case strings.HasPrefix(r.URL.Path, "/tasks/") && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{
			"id":     strings.TrimPrefix(r.URL.Path, "/tasks/"),
			"status": m.taskStatus,
		})
	case strings.HasPrefix(r.URL.Path, "/tasks/") && r.Method == http.MethodDelete:
		w.WriteHeader(http.StatusOK)
	default:
		http.NotFound(w, r)
	}
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

var _ = Describe("FirmwareUpdateLenovo controller", func() {
	ns := SetupNamespace()

	var (
		lxca     *lxcaMock
		hostname = "server1.example.com"
		uuid     = "abcd-1234"
	)

	BeforeEach(func() {
		lxca = newLXCAMock(uuid, hostname)
		DeferCleanup(lxca.Close)
	})

	It("marks Completed when the LXCA task succeeds", func(ctx SpecContext) {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "lenovo-creds",
				Namespace: ns.Name,
			},
			Data: map[string][]byte{
				vendorconsolev1alpha1.SecretUsernameKeyName: []byte("USERID"),
				vendorconsolev1alpha1.SecretPasswordKeyName: []byte("PASSW0RD"),
			},
		}
		Expect(k8sClient.Create(ctx, secret)).To(Succeed())

		bmcHost := hostname
		metalBmc := &metalv1alpha1.BMC{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "bmc-1",
				Namespace: ns.Name,
			},
			Spec: metalv1alpha1.BMCSpec{
				Hostname: &bmcHost,
				Endpoint: &metalv1alpha1.InlineEndpoint{
					IP: metalv1alpha1.MustParseIP("10.0.0.1"),
				},
				Protocol: metalv1alpha1.Protocol{
					Name: metalv1alpha1.ProtocolRedfish,
					Port: 443,
				},
				BMCSecretRef: corev1.LocalObjectReference{Name: "bmc-secret"},
			},
		}
		Expect(k8sClient.Create(ctx, metalBmc)).To(Succeed())

		srv := &metalv1alpha1.Server{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "server-1",
				Namespace: ns.Name,
				Labels:    map[string]string{"manufacturer": "lenovo"},
			},
			Spec: metalv1alpha1.ServerSpec{
				BMCRef: &corev1.LocalObjectReference{Name: metalBmc.Name},
			},
		}
		Expect(k8sClient.Create(ctx, srv)).To(Succeed())

		// Server manufacturer is a Status field. Set it and update the
		// status subresource.
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(srv), srv)).To(Succeed())
			srv.Status.Manufacturer = "Lenovo"
			g.Expect(k8sClient.Status().Update(ctx, srv)).To(Succeed())
		}).Should(Succeed())

		fw := &vendorconsolev1alpha1.FirmwareUpdateLenovo{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "fw-1",
				Namespace: ns.Name,
			},
			Spec: vendorconsolev1alpha1.FirmwareUpdateLenovoSpec{
				LXCAURL:   lxca.URL(),
				SecretRef: corev1.LocalObjectReference{Name: secret.Name},
				ServerSelector: metav1.LabelSelector{
					MatchLabels: map[string]string{"manufacturer": "lenovo"},
				},
				FirmwarePayload: vendorconsolev1alpha1.FirmwarePayload{
					SourceType: vendorconsolev1alpha1.FirmwarePayloadSourceURL,
					URL:        "https://firmware.example.com/uxsp.zip",
				},
				CompliancePolicy: vendorconsolev1alpha1.CompliancePolicySpec{
					Name: "policy",
				},
				UpdateAction:            vendorconsolev1alpha1.UpdateActivationImmediate,
				ServerMaintenancePolicy: metalv1alpha1.ServerMaintenancePolicyEnforced,
			},
		}
		Expect(k8sClient.Create(ctx, fw)).To(Succeed())

		// The controller creates ServerMaintenance resources; wait for them,
		// then keep them at InMaintenance so the flash step proceeds.
		Eventually(Object(fw), 15*time.Second, 100*time.Millisecond).Should(HaveField(
			"Status.State", vendorconsolev1alpha1.FirmwareUpdateStateInProgress))

		// The controller creates ServerMaintenance resources; mark each new
		// one as InMaintenance until the CR transitions to Completed. Once
		// the flash succeeds the controller cleans the SMs up, so we stop
		// looking for them at that point.
		Eventually(Object(fw), 15*time.Second, 100*time.Millisecond).Should(HaveField(
			"Status.State", vendorconsolev1alpha1.FirmwareUpdateStateInProgress))

		// Run a background poller that flips any SM to InMaintenance as
		// soon as it appears; this keeps up with the controller creating
		// them lazily.
		stop := make(chan struct{})
		go func() {
			defer GinkgoRecover()
			for {
				select {
				case <-stop:
					return
				default:
				}
				list := &metalv1alpha1.ServerMaintenanceList{}
				if err := k8sClient.List(ctx, list, client.InNamespace(ns.Name)); err == nil {
					for i := range list.Items {
						sm := &list.Items[i]
						if sm.Status.State == metalv1alpha1.ServerMaintenanceStateInMaintenance {
							continue
						}
						sm.Status.State = metalv1alpha1.ServerMaintenanceStateInMaintenance
						_ = k8sClient.Status().Update(ctx, sm)
					}
				}
				time.Sleep(50 * time.Millisecond)
			}
		}()
		DeferCleanup(func() { close(stop) })

		Eventually(Object(fw), 30*time.Second, 100*time.Millisecond).Should(HaveField(
			"Status.State", vendorconsolev1alpha1.FirmwareUpdateStateCompleted))

		Expect(lxca.AppliedDevices()).To(ContainElement(uuid))
		Expect(lxca.AppliedPolicy()).To(Equal("policy"))
	})

	It("marks Failed when the selector matches a non-Lenovo server", func(ctx SpecContext) {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "lenovo-creds-2",
				Namespace: ns.Name,
			},
			Data: map[string][]byte{
				vendorconsolev1alpha1.SecretUsernameKeyName: []byte("USERID"),
				vendorconsolev1alpha1.SecretPasswordKeyName: []byte("PASSW0RD"),
			},
		}
		Expect(k8sClient.Create(ctx, secret)).To(Succeed())

		wrongHost := "dell1.example.com"
		bmc := &metalv1alpha1.BMC{
			ObjectMeta: metav1.ObjectMeta{Name: "bmc-dell", Namespace: ns.Name},
			Spec: metalv1alpha1.BMCSpec{
				Hostname: &wrongHost,
				Endpoint: &metalv1alpha1.InlineEndpoint{
					IP: metalv1alpha1.MustParseIP("10.0.0.2"),
				},
				Protocol: metalv1alpha1.Protocol{
					Name: metalv1alpha1.ProtocolRedfish,
					Port: 443,
				},
				BMCSecretRef: corev1.LocalObjectReference{Name: "bmc-secret"},
			},
		}
		Expect(k8sClient.Create(ctx, bmc)).To(Succeed())

		srv := &metalv1alpha1.Server{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "server-dell",
				Namespace: ns.Name,
				Labels:    map[string]string{"vendor": "wrong"},
			},
			Spec: metalv1alpha1.ServerSpec{
				BMCRef: &corev1.LocalObjectReference{Name: bmc.Name},
			},
		}
		Expect(k8sClient.Create(ctx, srv)).To(Succeed())
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(srv), srv)).To(Succeed())
			srv.Status.Manufacturer = "Dell Inc."
			g.Expect(k8sClient.Status().Update(ctx, srv)).To(Succeed())
		}).Should(Succeed())

		fw := &vendorconsolev1alpha1.FirmwareUpdateLenovo{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "fw-2",
				Namespace: ns.Name,
			},
			Spec: vendorconsolev1alpha1.FirmwareUpdateLenovoSpec{
				LXCAURL:   lxca.URL(),
				SecretRef: corev1.LocalObjectReference{Name: secret.Name},
				ServerSelector: metav1.LabelSelector{
					MatchLabels: map[string]string{"vendor": "wrong"},
				},
				FirmwarePayload: vendorconsolev1alpha1.FirmwarePayload{
					SourceType: vendorconsolev1alpha1.FirmwarePayloadSourceURL,
					URL:        "https://firmware.example.com/uxsp.zip",
				},
				CompliancePolicy: vendorconsolev1alpha1.CompliancePolicySpec{
					Name: "policy",
				},
			},
		}
		Expect(k8sClient.Create(ctx, fw)).To(Succeed())

		Eventually(Object(fw), 10*time.Second, 100*time.Millisecond).Should(SatisfyAll(
			HaveField("Status.State", vendorconsolev1alpha1.FirmwareUpdateStateFailed),
			HaveField("Status.Conditions", HaveLen(1)),
		))

		var g = fmt.Sprintf("%v", fw.Status.Conditions)
		_ = g
	})
})
