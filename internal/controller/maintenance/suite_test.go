// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package maintenance

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/ironcore-dev/controller-utils/modutils"
	serverMaintenancev1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/maintenance/v1alpha1"
	readinessv1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/readiness/v1alpha1"
	"github.com/ironcore-dev/metal-maintenance-operator/internal/hwmgr/mock"
	"github.com/ironcore-dev/metal-maintenance-operator/internal/testutil/simcontrollers"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	"github.com/ironcore-dev/metal-operator/bmc"
	mockserver "github.com/ironcore-dev/metal-operator/bmc/mock/server"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
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

	testGenerateName = "test-"

	sanitizationNamespace = "metal-maintenance-sanitization"
	sanitizationImage     = "metal-maintenance-sanitization:latest"

	// BMCMockServerIP and BMCMockServerPort back the mock Redfish server that
	// simcontrollers.BMCReconciler/ServerReconciler talk to, mirroring the
	// baseboard/system packages' test setup so Server objects backed by a real
	// (mocked) BMC progress through Parked state the same way everywhere.
	BMCMockServerIP   = "127.0.0.1"
	BMCMockServerPort = int32(8101)
)

var (
	testEnv    *envtest.Environment
	cfg        *rest.Config
	k8sClient  client.Client
	mockServer *mockserver.MockServer
)

func TestControllers(t *testing.T) {
	SetDefaultConsistentlyPollingInterval(pollingInterval)
	SetDefaultEventuallyPollingInterval(pollingInterval)
	SetDefaultEventuallyTimeout(eventuallyTimeout)
	SetDefaultConsistentlyDuration(consistentlyDuration)
	RegisterFailHandler(Fail)
	RunSpecs(t, "Controller Suite")
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
			fmt.Sprintf("1.36.0-%s-%s", runtime.GOOS, runtime.GOARCH)),
	}

	var err error
	cfg, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())
	DeferCleanup(testEnv.Stop)

	Expect(metalv1alpha1.AddToScheme(scheme.Scheme)).NotTo(HaveOccurred())
	Expect(readinessv1alpha1.AddToScheme(scheme.Scheme)).NotTo(HaveOccurred())
	Expect(serverMaintenancev1alpha1.AddToScheme(scheme.Scheme)).NotTo(HaveOccurred())
	// +kubebuilder:scaffold:scheme

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())

	SetClient(k8sClient)

	Expect(k8sClient.Create(context.TODO(), &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: sanitizationNamespace},
	})).To(Succeed())

	// Mock server binds a fixed port so it is started once for the whole suite.
	mockServer := mock.NewMockServer(GinkgoLogr, ":8002")
	mockCtx, cancel := context.WithCancel(context.Background())
	DeferCleanup(cancel)

	go func() {
		defer GinkgoRecover()
		Expect(mockServer.Start(mockCtx)).To(Succeed(), "failed to start mock server")
	}()
})

// Option configures a manager during SetupTest.
type Option func(ctx SpecContext, mgr ctrl.Manager) error

// WithServerMaintenanceController registers the ServerMaintenanceReconciler and its field index.
func WithServerMaintenanceController() Option {
	return func(ctx SpecContext, mgr ctrl.Manager) error {
		if err := mgr.GetFieldIndexer().IndexField(ctx, &serverMaintenancev1alpha1.ServerMaintenance{}, serverRefField, func(rawObj client.Object) []string {
			m, ok := rawObj.(*serverMaintenancev1alpha1.ServerMaintenance)
			if !ok {
				return nil
			}
			if m.Spec.ServerRef != nil && m.Spec.ServerRef.Name != "" {
				return []string{m.Spec.ServerRef.Name}
			}
			return nil
		}); err != nil {
			return err
		}
		return (&ServerMaintenanceReconciler{
			Client: mgr.GetClient(),
			Scheme: mgr.GetScheme(),
		}).SetupWithManager(mgr)
	}
}

// WithServerSanitizationController registers the ServerSanitizationReconciler.
func WithServerSanitizationController() Option {
	return func(_ SpecContext, mgr ctrl.Manager) error {
		return (&ServerSanitizationReconciler{
			Client:                mgr.GetClient(),
			Scheme:                mgr.GetScheme(),
			SanitizationNamespace: sanitizationNamespace,
			SanitizationImage:     sanitizationImage,
			SanitizationIgnitionProvider: func(
				ctx context.Context,
				server *metalv1alpha1.Server,
				sanitizationUID string,
			) ([]byte, error) {
				return fmt.Appendf(nil, "%s/%s", server.UID, sanitizationUID), nil
			},
		}).SetupWithManager(mgr)
	}
}

func SetupTest(opts ...Option) *corev1.Namespace {
	ns := &corev1.Namespace{}
	BeforeEach(func(ctx SpecContext) {
		mgrCtx, cancel := context.WithCancel(context.Background())
		mgrDone := make(chan struct{})
		DeferCleanup(func() {
			cancel()
			<-mgrDone
		})

		*ns = corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{GenerateName: testGenerateName},
		}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed(), "failed to create test namespace")
		DeferCleanup(k8sClient.Delete, ns)

		k8sManager, err := ctrl.NewManager(cfg, ctrl.Options{
			Scheme: scheme.Scheme,
			Controller: config.Controller{
				SkipNameValidation: new(true),
			},
			Metrics: metricsserver.Options{BindAddress: "0"},
		})
		Expect(err).NotTo(HaveOccurred(), "failed to create k8s manager")

		for _, opt := range opts {
			Expect(opt(ctx, k8sManager)).To(Succeed())
		}

		// simcontrollers.BMCReconciler/ServerReconciler mimic metal-operator's real
		// BMC/Server controllers (which live in metal-operator's internal package
		// and can't be imported) by syncing status (PowerState, FirmwareVersion,
		// Parked state) from the mock Redfish server, so ServerMaintenanceReconciler's
		// real Park/Unpark annotation handshake converges the same way it would
		// against a real cluster. Tests that don't attach a BMC to their Server
		// are unaffected: both reconcilers no-op for Servers without a BMCRef.
		Expect((&simcontrollers.BMCReconciler{
			Client:             k8sManager.GetClient(),
			DefaultProtocol:    metalv1alpha1.HTTPProtocolScheme,
			SkipCertValidation: true,
			ResyncInterval:     10 * time.Millisecond,
			BMCOptions: bmc.Options{
				PowerPollingInterval: 50 * time.Millisecond,
				PowerPollingTimeout:  200 * time.Millisecond,
				BasicAuth:            true,
			},
		}).SetupWithManager(k8sManager)).To(Succeed())

		Expect((&simcontrollers.ServerReconciler{
			Client:             k8sManager.GetClient(),
			DefaultProtocol:    metalv1alpha1.HTTPProtocolScheme,
			SkipCertValidation: true,
			ResyncInterval:     10 * time.Millisecond,
			BMCOptions: bmc.Options{
				PowerPollingInterval: 50 * time.Millisecond,
				PowerPollingTimeout:  200 * time.Millisecond,
				BasicAuth:            true,
			},
		}).SetupWithManager(k8sManager)).To(Succeed())

		By("Starting the mock Redfish server")
		ms := mockserver.NewMockServer(GinkgoLogr, fmt.Sprintf(":%d", BMCMockServerPort), mockserver.WithAuth())
		mockServer = ms
		Expect(k8sManager.Add(manager.RunnableFunc(func(ctx context.Context) error {
			if err := ms.Start(ctx); err != nil {
				return fmt.Errorf("failed to start mock Redfish server: %w", err)
			}
			<-ctx.Done()
			return nil
		}))).To(Succeed())

		go func() {
			defer GinkgoRecover()
			defer close(mgrDone)
			Expect(k8sManager.Start(mgrCtx)).To(Succeed(), "failed to start manager")
		}()
	})
	return ns
}

// EnsureCleanState deletes any leftover BMC/Server/ServerMaintenance objects
// and waits for the deletion to converge, mirroring the baseboard/system
// packages: a background simulated controller (e.g. BMCReconciler's
// discoverServers) can still be mid-flight when a test's cleanup runs and
// re-create/patch an object right after it was deleted, so re-issue deletes
// on every poll instead of racing a single one-shot delete.
func EnsureCleanState() {
	GinkgoHelper()

	objectLists := []client.ObjectList{
		&metalv1alpha1.BMCList{},
		&metalv1alpha1.BMCSecretList{},
		&metalv1alpha1.ServerList{},
		&serverMaintenancev1alpha1.ServerMaintenanceList{},
	}

	for _, list := range objectLists {
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.List(context.Background(), list)).To(Succeed())
			items, err := apimeta.ExtractList(list)
			g.Expect(err).NotTo(HaveOccurred())
			for _, item := range items {
				if obj, ok := item.(client.Object); ok {
					_ = k8sClient.Delete(context.Background(), obj)
				}
			}
			g.Expect(items).To(BeEmpty())
		}).Should(Succeed())
	}
}
