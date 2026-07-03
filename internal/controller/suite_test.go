// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ironcore-dev/metal-maintenance-operator/internal/hwmgr/mock"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	. "sigs.k8s.io/controller-runtime/pkg/envtest/komega"
	"sigs.k8s.io/controller-runtime/pkg/log"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/ironcore-dev/controller-utils/modutils"
	readinessv1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/readiness/v1alpha1"
	maintenancev1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/v1alpha1"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	// +kubebuilder:scaffold:imports
)

var (
	testEnv   *envtest.Environment
	cfg       *rest.Config
	k8sClient client.Client
)

const (
	sanitizationNamespace = "metal-maintenance-sanitization"
	sanitizationImage     = "metal-maintenance-sanitization:latest"
)

func TestControllers(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Controller Suite")
}

var _ = BeforeSuite(func() {
	By("bootstrapping test environment")
	testEnv = &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join("..", "..", "config", "crd", "bases"),
			filepath.Join(modutils.Dir("github.com/ironcore-dev/metal-operator", "config", "crd", "bases")),
		},
		ErrorIfCRDPathMissing: true,
		BinaryAssetsDirectory: filepath.Join("..", "..", "bin", "k8s",
			fmt.Sprintf("1.36.0-%s-%s", runtime.GOOS, runtime.GOARCH)),
	}

	var err error
	cfg, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())
	DeferCleanup(testEnv.Stop)

	Expect(metalv1alpha1.AddToScheme(scheme.Scheme)).NotTo(HaveOccurred())
	Expect(maintenancev1alpha1.AddToScheme(scheme.Scheme)).NotTo(HaveOccurred())
	Expect(readinessv1alpha1.AddToScheme(scheme.Scheme)).NotTo(HaveOccurred())
	// +kubebuilder:scaffold:scheme

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())

	log.SetLogger(GinkgoLogr)
	SetClient(k8sClient)

	Expect(k8sClient.Create(context.TODO(), &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: sanitizationNamespace},
	})).To(Succeed())

	mgrCtx, cancel := context.WithCancel(context.Background())
	DeferCleanup(cancel)

	k8sManager, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:  scheme.Scheme,
		Metrics: metricsserver.Options{BindAddress: "0"},
	})
	Expect(err).NotTo(HaveOccurred(), "failed to create k8s manager")

	Expect((&ConsoleReconciler{
		Client: k8sManager.GetClient(),
		Scheme: k8sManager.GetScheme(),
	}).SetupWithManager(k8sManager)).To(Succeed())

	Expect((&ServerSanitizationReconciler{
		Client:                k8sManager.GetClient(),
		Scheme:                k8sManager.GetScheme(),
		SanitizationNamespace: sanitizationNamespace,
		SanitizationImage:     sanitizationImage,
		SanitizationIgnitionProvider: func(
			ctx context.Context,
			server *metalv1alpha1.Server,
			sanitizationUID string,
		) ([]byte, error) {
			return fmt.Appendf(nil, "%s/%s", server.UID, sanitizationUID), nil
		},
	}).SetupWithManager(k8sManager)).To(Succeed())

	Expect((&ServerWiringReconciler{
		Client: k8sManager.GetClient(),
		Scheme: k8sManager.GetScheme(),
	}).SetupWithManager(k8sManager)).To(Succeed())

	mockServer := mock.NewMockServer(GinkgoLogr, ":8000")
	mockCtx, cancel := context.WithCancel(context.Background())
	DeferCleanup(cancel)

	go func() {
		defer GinkgoRecover()
		Expect(mockServer.Start(mockCtx)).To(Succeed(), "failed to start mock Redfish server")
	}()

	go func() {
		defer GinkgoRecover()
		Expect(k8sManager.Start(mgrCtx)).To(Succeed(), "failed to start manager")
	}()
})

func SetupNamespace() *corev1.Namespace {
	ns := &corev1.Namespace{}
	BeforeEach(func(ctx SpecContext) {
		*ns = corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "test-"},
		}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed(), "failed to create test namespace")
		DeferCleanup(k8sClient.Delete, ns)
	})
	return ns
}
