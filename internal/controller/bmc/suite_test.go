// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package bmc

import (
	"context"
	"fmt"
	"net/http"
	"net/netip"
	"path/filepath"
	goruntime "runtime"
	"testing"
	"time"

	"github.com/ironcore-dev/controller-utils/modutils"
	bmcv1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/bmc/v1alpha1"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	"github.com/ironcore-dev/metal-operator/bmc"
	"github.com/ironcore-dev/metal-operator/bmc/mock/server"
	"github.com/ironcore-dev/metal-operator/bmcutils"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	. "sigs.k8s.io/controller-runtime/pkg/envtest/komega"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	// +kubebuilder:scaffold:imports
)

const (
	pollingInterval      = 50 * time.Millisecond
	eventuallyTimeout    = 5 * time.Second
	consistentlyDuration = 1 * time.Second
	MockServerIP         = "127.0.0.1"
	MockServerPort       = 8000
)

var (
	testEnv     *envtest.Environment
	cfg         *rest.Config
	k8sClient   client.Client
	mockServers []*server.MockServer
)

func TestControllers(t *testing.T) {
	SetDefaultConsistentlyPollingInterval(pollingInterval)
	SetDefaultEventuallyPollingInterval(pollingInterval)
	SetDefaultEventuallyTimeout(eventuallyTimeout)
	SetDefaultConsistentlyDuration(consistentlyDuration)
	RegisterFailHandler(Fail)
	RunSpecs(t, "BMC Controller Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	By("bootstrapping test environment")
	testEnv = &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join("..", "..", "..", "config", "crd", "bases"),
			filepath.Join(modutils.Dir("github.com/ironcore-dev/metal-operator", "config", "crd", "bases")),
		},
		ErrorIfCRDPathMissing: true,
		BinaryAssetsDirectory: filepath.Join("..", "..", "..", "bin", "k8s",
			fmt.Sprintf("1.36.0-%s-%s", goruntime.GOOS, goruntime.GOARCH)),
	}

	var err error
	cfg, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())
	DeferCleanup(testEnv.Stop)

	Expect(metalv1alpha1.AddToScheme(scheme.Scheme)).NotTo(HaveOccurred())
	Expect(bmcv1alpha1.AddToScheme(scheme.Scheme)).NotTo(HaveOccurred())
	// +kubebuilder:scaffold:scheme

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())

	SetClient(k8sClient)
})

// SetupTest wires the per-spec manager with the BMCUserReconciler plus a small
// helper reconciler that mirrors the metal-operator BMCReconciler side-effect
// the tests depend on: whenever a BMC is created, a matching Server object is
// materialized under the name bmcutils.GetServerNameFromBMCandIndex(0, bmc).
// It also starts the Redfish mock server on :8000 (a fixed port required by the
// tests' BMC Spec) and appends it to mockServers.
//
// The redfishMockServers parameter is accepted to match the signature used in
// metal-operator's suite so the test file body stays byte-identical; when nil
// the default single mock on MockServerPort is started.
func SetupTest(redfishMockServers []netip.AddrPort) *corev1.Namespace {
	ns := &corev1.Namespace{}

	BeforeEach(func(ctx SpecContext) {
		mgrCtx, cancel := context.WithCancel(context.Background())
		DeferCleanup(cancel)

		*ns = corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "test-",
			},
		}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed(), "failed to create test namespace")
		DeferCleanup(k8sClient.Delete, ns)

		k8sManager, err := ctrl.NewManager(cfg, ctrl.Options{
			Scheme: scheme.Scheme,
			Controller: config.Controller{
				// Need to skip unique controller name validation since every spec spins up
				// a fresh manager that re-registers the same controller names.
				SkipNameValidation: new(true),
			},
			Metrics: metricsserver.Options{BindAddress: "0"},
		})
		Expect(err).NotTo(HaveOccurred(), "failed to create k8s manager")

		Expect((&BMCUserReconciler{
			Client:             k8sManager.GetClient(),
			Scheme:             k8sManager.GetScheme(),
			DefaultProtocol:    metalv1alpha1.HTTPProtocolScheme,
			SkipCertValidation: true,
			BMCOptions: bmc.Options{
				PowerPollingInterval: 50 * time.Millisecond,
				PowerPollingTimeout:  200 * time.Millisecond,
				BasicAuth:            true,
			},
		}).SetupWithManager(k8sManager)).To(Succeed())

		Expect((&serverFromBMCReconciler{
			Client: k8sManager.GetClient(),
			Scheme: k8sManager.GetScheme(),
		}).SetupWithManager(k8sManager)).To(Succeed())

		if len(redfishMockServers) > 0 {
			mockServers = make([]*server.MockServer, 0, len(redfishMockServers))
			for _, addr := range redfishMockServers {
				By(fmt.Sprintf("Starting the mock Redfish servers %v", addr))
				ms := server.NewMockServer(GinkgoLogr, addr.String(), server.WithAuth())
				mockServers = append(mockServers, ms)
				Expect(k8sManager.Add(manager.RunnableFunc(func(ctx context.Context) error {
					if err := ms.Start(ctx); err != nil {
						return fmt.Errorf("failed to start mock Redfish server %v: %w", addr, err)
					}
					<-ctx.Done()
					return nil
				}))).To(Succeed())
			}
		} else {
			By("Starting the default mock Redfish server")
			ms := server.NewMockServer(GinkgoLogr, fmt.Sprintf(":%d", MockServerPort), server.WithAuth())
			mockServers = []*server.MockServer{ms}
			Expect(k8sManager.Add(manager.RunnableFunc(func(ctx context.Context) error {
				if err := ms.Start(ctx); err != nil {
					return fmt.Errorf("failed to start mock Redfish server: %w", err)
				}
				<-ctx.Done()
				return nil
			}))).To(Succeed())
		}

		go func() {
			defer GinkgoRecover()
			Expect(k8sManager.Start(mgrCtx)).To(Succeed(), "failed to start manager")
		}()

		// Wait for the default mock to be reachable before letting specs run — otherwise
		// the first reconcile of a BMC may race the mock's listen and fail with connection
		// refused.
		Eventually(func() error {
			resp, err := http.Get(fmt.Sprintf("http://%s:%d/redfish/v1/", MockServerIP, MockServerPort))
			if err != nil {
				return err
			}
			_ = resp.Body.Close()
			return nil
		}, 5*time.Second, 50*time.Millisecond).Should(Succeed(), "mock server did not become ready")
	})

	return ns
}

// EnsureCleanState waits until cluster-scoped resources this suite mutates are
// gone before the next spec begins. Ported from metal-operator's suite helper;
// list types trimmed to those the BMCUser tests actually create.
func EnsureCleanState() {
	GinkgoHelper()

	objectLists := []client.ObjectList{
		&metalv1alpha1.BMCList{},
		&metalv1alpha1.BMCSecretList{},
		&metalv1alpha1.ServerList{},
		&bmcv1alpha1.BMCUserList{},
	}

	for _, list := range objectLists {
		Eventually(ObjectList(list)).Should(HaveField("Items", HaveLen(0)))
	}
}

// serverFromBMCReconciler is a minimal stand-in for metal-operator's
// BMCReconciler.discoverServers: when a BMC exists, ensure a Server object
// named bmcutils.GetServerNameFromBMCandIndex(0, bmc) exists and is
// controller-owned by the BMC. The bmcuser controller test relies on this
// Server being auto-created so its AfterEach cleanup succeeds.
type serverFromBMCReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func (r *serverFromBMCReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	bmcObj := &metalv1alpha1.BMC{}
	if err := r.Get(ctx, req.NamespacedName, bmcObj); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !bmcObj.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	srv := &metalv1alpha1.Server{
		ObjectMeta: metav1.ObjectMeta{
			Name: bmcutils.GetServerNameFromBMCandIndex(0, bmcObj),
		},
	}
	if _, err := controllerutil.CreateOrPatch(ctx, r.Client, srv, func() error {
		srv.Spec.BMCRef = &corev1.LocalObjectReference{Name: bmcObj.Name}
		return controllerutil.SetControllerReference(bmcObj, srv, r.Scheme)
	}); err != nil && !apierrors.IsAlreadyExists(err) {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *serverFromBMCReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&metalv1alpha1.BMC{}).
		Owns(&metalv1alpha1.Server{}).
		Named("test-server-from-bmc").
		Complete(r)
}
