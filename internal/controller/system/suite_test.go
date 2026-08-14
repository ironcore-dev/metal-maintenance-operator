// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package system

import (
	"context"
	"fmt"
	"net/netip"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/ironcore-dev/controller-utils/conditionutils"
	"github.com/ironcore-dev/controller-utils/modutils"
	maintenancev1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/maintenance/v1alpha1"
	systemv1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/system/v1alpha1"
	constants "github.com/ironcore-dev/metal-maintenance-operator/internal/constants"
	maintenancectrl "github.com/ironcore-dev/metal-maintenance-operator/internal/controller/maintenance"
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
	MockServerIP         = "127.0.0.1"
	MockServerPort       = int32(8200)
)

var (
	cfg         *rest.Config
	k8sClient   client.Client
	testEnv     *envtest.Environment
	mockServers []*mockserver.MockServer

	mockUpServerBiosVersion = "P79 v1.45 (12/06/2017)"
	trueValue               = "true"
)

func TestControllers(t *testing.T) {
	SetDefaultConsistentlyPollingInterval(pollingInterval)
	SetDefaultEventuallyPollingInterval(pollingInterval)
	SetDefaultEventuallyTimeout(eventuallyTimeout)
	SetDefaultConsistentlyDuration(consistentlyDuration)
	RegisterFailHandler(Fail)
	RunSpecs(t, "System Controller Suite")
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
	Expect(maintenancev1alpha1.AddToScheme(scheme.Scheme)).NotTo(HaveOccurred())
	Expect(systemv1alpha1.AddToScheme(scheme.Scheme)).NotTo(HaveOccurred())
	// +kubebuilder:scaffold:scheme

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())

	SetClient(k8sClient)
})

func registerIndexFields(ctx context.Context, indexer client.FieldIndexer) error {
	if err := indexer.IndexField(ctx, &systemv1alpha1.BIOSSettings{}, constants.ServerRefField, func(obj client.Object) []string {
		settings := obj.(*systemv1alpha1.BIOSSettings)
		if settings.Spec.ServerRef == nil {
			return nil
		}
		return []string{settings.Spec.ServerRef.Name}
	}); err != nil {
		return err
	}
	if err := indexer.IndexField(ctx, &metalv1alpha1.Server{}, constants.BMCRefField, func(obj client.Object) []string {
		server := obj.(*metalv1alpha1.Server)
		if server.Spec.BMCRef == nil {
			return nil
		}
		return []string{server.Spec.BMCRef.Name}
	}); err != nil {
		return err
	}
	if err := indexer.IndexField(ctx, &maintenancev1alpha1.ServerMaintenance{}, constants.ServerRefField, func(obj client.Object) []string {
		sm, ok := obj.(*maintenancev1alpha1.ServerMaintenance)
		if !ok || sm.Spec.ServerRef == nil {
			return nil
		}
		return []string{sm.Spec.ServerRef.Name}
	}); err != nil {
		return err
	}
	return nil
}

func SetupTest(redfishMockServers []netip.AddrPort, mockServerOpts ...mockserver.Option) *corev1.Namespace {
	ns := &corev1.Namespace{}

	BeforeEach(func(ctx SpecContext) {
		mgrCtx, cancel := context.WithCancel(context.Background())
		DeferCleanup(cancel)

		*ns = corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "test-"},
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
		Expect(err).ToNot(HaveOccurred())
		Expect(registerIndexFields(mgrCtx, k8sManager.GetFieldIndexer())).To(Succeed())

		accessor := conditionutils.NewAccessor(conditionutils.AccessorOptions{})

		Expect((&BIOSSettingsReconciler{
			Client:             k8sManager.GetClient(),
			ManagerNamespace:   ns.Name,
			DefaultProtocol:    metalv1alpha1.HTTPProtocolScheme,
			SkipCertValidation: true,
			Scheme:             k8sManager.GetScheme(),
			ResyncInterval:     10 * time.Millisecond,
			Conditions:         accessor,
			BMCOptions: bmc.Options{
				PowerPollingInterval: 50 * time.Millisecond,
				PowerPollingTimeout:  200 * time.Millisecond,
				BasicAuth:            true,
			},
			TimeoutExpiry:       6 * time.Second,
			RebootTimeoutExpiry: 6 * time.Second,
		}).SetupWithManager(k8sManager)).To(Succeed())

		Expect((&BIOSVersionReconciler{
			Client:             k8sManager.GetClient(),
			ManagerNamespace:   ns.Name,
			DefaultProtocol:    metalv1alpha1.HTTPProtocolScheme,
			SkipCertValidation: true,
			Scheme:             k8sManager.GetScheme(),
			ResyncInterval:     10 * time.Millisecond,
			Conditions:         accessor,
			BMCOptions: bmc.Options{
				PowerPollingInterval: 50 * time.Millisecond,
				PowerPollingTimeout:  200 * time.Millisecond,
				BasicAuth:            true,
			},
			RebootTimeoutExpiry: 6 * time.Second,
		}).SetupWithManager(k8sManager)).To(Succeed())

		Expect((&BIOSSettingsSetReconciler{
			Client:         k8sManager.GetClient(),
			Scheme:         k8sManager.GetScheme(),
			ResyncInterval: 10 * time.Millisecond,
		}).SetupWithManager(k8sManager)).To(Succeed())

		Expect((&BIOSVersionSetReconciler{
			Client:         k8sManager.GetClient(),
			Scheme:         k8sManager.GetScheme(),
			ResyncInterval: 10 * time.Millisecond,
		}).SetupWithManager(k8sManager)).To(Succeed())

		Expect((&maintenancectrl.ServerMaintenanceReconciler{
			Client: k8sManager.GetClient(),
			Scheme: k8sManager.GetScheme(),
		}).SetupWithManager(k8sManager)).To(Succeed())

		Expect((&FirmwareUpdateDellReconciler{
			Client:             k8sManager.GetClient(),
			ManagerNamespace:   ns.Name,
			DefaultProtocol:    metalv1alpha1.HTTPProtocolScheme,
			SkipCertValidation: true,
			Scheme:             k8sManager.GetScheme(),
			ResyncInterval:     10 * time.Millisecond,
			Conditions:         accessor,
			BMCOptions: bmc.Options{
				PowerPollingInterval: 50 * time.Millisecond,
				PowerPollingTimeout:  200 * time.Millisecond,
				BasicAuth:            true,
			},
			MaxRepositoryPasses: 5,
		}).SetupWithManager(k8sManager)).To(Succeed())

		// simcontrollers.BMCReconciler/ServerReconciler mimic metal-operator's real
		// BMC/Server controllers - which live in metal-operator's internal package
		// and can't be imported - by syncing status (PowerState, FirmwareVersion,
		// maintenance state) from the mock Redfish server and from
		// Server.Spec.ServerMaintenanceRef, so tests don't need to manually patch
		// those fields.
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

		// simcontrollers.ServerClaimReconciler mimics metal-operator's real
		// ServerClaimReconciler - which also lives in metal-operator's internal
		// package - by claiming the referenced Server and propagating the
		// claim's desired power state once the Server reaches Reserved.
		Expect((&simcontrollers.ServerClaimReconciler{
			Client:         k8sManager.GetClient(),
			ResyncInterval: 10 * time.Millisecond,
		}).SetupWithManager(k8sManager)).To(Succeed())

		opts := append([]mockserver.Option{mockserver.WithAuth()}, mockServerOpts...)
		if len(redfishMockServers) > 0 {
			mockServers = make([]*mockserver.MockServer, 0, len(redfishMockServers))
			for _, serverAddr := range redfishMockServers {
				By(fmt.Sprintf("Starting mock Redfish server %v", serverAddr))
				ms := mockserver.NewMockServer(GinkgoLogr, serverAddr.String(), opts...)
				mockServers = append(mockServers, ms)
				Expect(k8sManager.Add(manager.RunnableFunc(func(ctx context.Context) error {
					if err := ms.Start(ctx); err != nil {
						return fmt.Errorf("failed to start mock Redfish server %v", serverAddr)
					}
					<-ctx.Done()
					return nil
				}))).Should(Succeed())
			}
		} else {
			By("Starting the default mock Redfish server")
			ms := mockserver.NewMockServer(GinkgoLogr, fmt.Sprintf(":%d", MockServerPort), opts...)
			mockServers = []*mockserver.MockServer{ms}
			Expect(k8sManager.Add(manager.RunnableFunc(func(ctx context.Context) error {
				if err := ms.Start(ctx); err != nil {
					return fmt.Errorf("failed to start mock Redfish server: %w", err)
				}
				<-ctx.Done()
				return nil
			}))).Should(Succeed())
		}

		go func() {
			defer GinkgoRecover()
			Expect(k8sManager.Start(mgrCtx)).To(Succeed(), "failed to start manager")
		}()
	})

	return ns
}

func EnsureCleanState() {
	GinkgoHelper()

	objectLists := []client.ObjectList{
		&metalv1alpha1.BMCList{},
		&metalv1alpha1.BMCSecretList{},
		&metalv1alpha1.ServerList{},
		&maintenancev1alpha1.ServerMaintenanceList{},
		&systemv1alpha1.BIOSSettingsList{},
		&systemv1alpha1.BIOSSettingsSetList{},
		&systemv1alpha1.BIOSVersionList{},
		&systemv1alpha1.BIOSVersionSetList{},
		&systemv1alpha1.FirmwareUpdateDellList{},
		&maintenancev1alpha1.ServerMaintenanceList{},
	}

	for _, list := range objectLists {
		// A background simulated controller (e.g. BMCReconciler's
		// discoverServers, which runs on a fast ResyncInterval) can still be
		// mid-flight when a test issues its one-shot AfterEach deletes, and
		// re-create/patch an object right after the test deleted it. Re-issue
		// the delete on every poll for anything still around so cleanup
		// converges instead of racing a single one-shot delete.
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
