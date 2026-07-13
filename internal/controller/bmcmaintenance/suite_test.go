// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package bmcmaintenance

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
	bmcmaintenancev1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/bmcmaintenance/v1alpha1"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	"github.com/ironcore-dev/metal-operator/bmc"
	mockserver "github.com/ironcore-dev/metal-operator/bmc/mock/server"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
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
	MockServerPort       = int32(8100)
)

var (
	cfg         *rest.Config
	k8sClient   client.Client
	testEnv     *envtest.Environment
	mockServers []*mockserver.MockServer

	mockUpServerBMCVersion = "1.45.455b66-rev4"
	trueValue              = "true"
)

func TestControllers(t *testing.T) {
	SetDefaultConsistentlyPollingInterval(pollingInterval)
	SetDefaultEventuallyPollingInterval(pollingInterval)
	SetDefaultEventuallyTimeout(eventuallyTimeout)
	SetDefaultConsistentlyDuration(consistentlyDuration)
	RegisterFailHandler(Fail)
	RunSpecs(t, "BMCMaintenance Controller Suite")
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
	Expect(bmcmaintenancev1alpha1.AddToScheme(scheme.Scheme)).NotTo(HaveOccurred())
	// +kubebuilder:scaffold:scheme

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())

	SetClient(k8sClient)
})

func SetupTest(redfishMockServers []netip.AddrPort) *corev1.Namespace {
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

		accessor := conditionutils.NewAccessor(conditionutils.AccessorOptions{})

		Expect((&BMCSettingsReconciler{
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
		}).SetupWithManager(k8sManager)).To(Succeed())

		Expect((&BMCVersionReconciler{
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
		}).SetupWithManager(k8sManager)).To(Succeed())

		Expect((&BMCSettingsSetReconciler{
			Client:         k8sManager.GetClient(),
			Scheme:         k8sManager.GetScheme(),
			ResyncInterval: 10 * time.Millisecond,
		}).SetupWithManager(k8sManager)).To(Succeed())

		Expect((&BMCVersionSetReconciler{
			Client:         k8sManager.GetClient(),
			Scheme:         k8sManager.GetScheme(),
			ResyncInterval: 10 * time.Millisecond,
		}).SetupWithManager(k8sManager)).To(Succeed())

		Expect(k8sManager.GetFieldIndexer().IndexField(
			mgrCtx,
			&metalv1alpha1.ServerMaintenance{},
			"spec.serverRef.name",
			func(obj client.Object) []string {
				sm := obj.(*metalv1alpha1.ServerMaintenance)
				if sm.Spec.ServerRef == nil {
					return nil
				}
				return []string{sm.Spec.ServerRef.Name}
			},
		)).To(Succeed())

		Expect(k8sManager.GetFieldIndexer().IndexField(
			mgrCtx,
			&metalv1alpha1.Server{},
			"spec.bmcRef.name",
			func(obj client.Object) []string {
				sv := obj.(*metalv1alpha1.Server)
				if sv.Spec.BMCRef == nil {
					return nil
				}
				return []string{sv.Spec.BMCRef.Name}
			},
		)).To(Succeed())

		if len(redfishMockServers) > 0 {
			mockServers = make([]*mockserver.MockServer, 0, len(redfishMockServers))
			for _, serverAddr := range redfishMockServers {
				By(fmt.Sprintf("Starting mock Redfish server %v", serverAddr))
				ms := mockserver.NewMockServer(GinkgoLogr, serverAddr.String(), mockserver.WithAuth())
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
			ms := mockserver.NewMockServer(GinkgoLogr, fmt.Sprintf(":%d", MockServerPort), mockserver.WithAuth())
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
		&metalv1alpha1.ServerMaintenanceList{},
		&bmcmaintenancev1alpha1.BMCSettingsList{},
		&bmcmaintenancev1alpha1.BMCSettingsSetList{},
		&bmcmaintenancev1alpha1.BMCVersionList{},
		&bmcmaintenancev1alpha1.BMCVersionSetList{},
	}

	for _, list := range objectLists {
		Eventually(ObjectList(list)).Should(HaveField("Items", HaveLen(0)))
	}
}

func simulateMaintenanceGranted(server *metalv1alpha1.Server, sm *metalv1alpha1.ServerMaintenance) {
	GinkgoHelper()
	Eventually(UpdateStatus(sm, func() {
		sm.Status.State = metalv1alpha1.ServerMaintenanceStateInMaintenance
	})).Should(Succeed())
	Eventually(Update(server, func() {
		server.Spec.ServerMaintenanceRef = &metalv1alpha1.ObjectReference{
			Name:      sm.Name,
			Namespace: sm.Namespace,
		}
	})).Should(Succeed())
	Eventually(UpdateStatus(server, func() {
		server.Status.State = metalv1alpha1.ServerStateMaintenance
	})).Should(Succeed())
}

func simulateMaintenanceReleased(server *metalv1alpha1.Server) {
	GinkgoHelper()
	Eventually(Update(server, func() {
		server.Spec.ServerMaintenanceRef = nil
	})).Should(Succeed())
	prevState := metalv1alpha1.ServerStateAvailable
	if server.Spec.ServerClaimRef != nil {
		prevState = metalv1alpha1.ServerStateReserved
	}
	Eventually(UpdateStatus(server, func() {
		server.Status.State = prevState
	})).Should(Succeed())
}
