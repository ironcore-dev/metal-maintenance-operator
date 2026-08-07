// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package baseboard

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
	baseboardv1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/baseboard/v1alpha1"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	"github.com/ironcore-dev/metal-operator/bmc"
	"github.com/ironcore-dev/metal-operator/bmc/mock/server"
	"github.com/ironcore-dev/metal-operator/pkg/bmcutils"
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
	RunSpecs(t, "Baseboard Controller Suite")
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
			fmt.Sprintf("1.36.2-%s-%s", goruntime.GOOS, goruntime.GOARCH)),
	}

	var err error
	cfg, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())
	DeferCleanup(testEnv.Stop)

	Expect(metalv1alpha1.AddToScheme(scheme.Scheme)).NotTo(HaveOccurred())
	Expect(baseboardv1alpha1.AddToScheme(scheme.Scheme)).NotTo(HaveOccurred())
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
// tests' BMC Spec).
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

// removeFinalizers patches all objects in list to remove their finalizers so
// envtest's API server can complete deletion (no external controller runs here).
func removeFinalizers[T any, PT interface {
	*T
	client.Object
}](ctx context.Context, items []T) {
	GinkgoHelper()
	for i := range items {
		obj := PT(&items[i])
		if len(obj.GetFinalizers()) == 0 {
			continue
		}
		patch := client.MergeFrom(obj.DeepCopyObject().(client.Object))
		obj.SetFinalizers(nil)
		Expect(client.IgnoreNotFound(k8sClient.Patch(ctx, obj, patch))).To(Succeed())
	}
}

// EnsureCleanState actively deletes all cluster-scoped resources this suite
// mutates and then waits for them to be gone. Active deletion is required because
// envtest does not run the garbage collector, so owner-reference-based cascading
// deletion never fires automatically.
//
// Order matters: BMCUser first (its finalizer may need BMC), then BMC (so
// serverFromBMCReconciler stops reconciling), then Server and BMCSecret.
func EnsureCleanState(ctx context.Context) {
	GinkgoHelper()

	bmcUserList := &baseboardv1alpha1.BMCUserList{}
	Expect(k8sClient.List(ctx, bmcUserList)).To(Succeed())
	removeFinalizers[baseboardv1alpha1.BMCUser, *baseboardv1alpha1.BMCUser](ctx, bmcUserList.Items)
	for i := range bmcUserList.Items {
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, &bmcUserList.Items[i]))).To(Succeed())
	}
	Eventually(ObjectList(&baseboardv1alpha1.BMCUserList{})).Should(HaveField("Items", HaveLen(0)))

	// Strip the metal.ironcore.dev/bmc finalizer (set by metal-operator's BMCReconciler,
	// which is not running here) so the API server can complete BMC deletion.
	// Wait until BMC is gone so serverFromBMCReconciler stops reconciling and won't
	// recreate Server objects after we delete them below.
	bmcList := &metalv1alpha1.BMCList{}
	Expect(k8sClient.List(ctx, bmcList)).To(Succeed())
	removeFinalizers[metalv1alpha1.BMC, *metalv1alpha1.BMC](ctx, bmcList.Items)
	for i := range bmcList.Items {
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, &bmcList.Items[i]))).To(Succeed())
	}
	Eventually(ObjectList(&metalv1alpha1.BMCList{})).Should(HaveField("Items", HaveLen(0)))

	serverList := &metalv1alpha1.ServerList{}
	Expect(k8sClient.List(ctx, serverList)).To(Succeed())
	removeFinalizers[metalv1alpha1.Server, *metalv1alpha1.Server](ctx, serverList.Items)
	for i := range serverList.Items {
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, &serverList.Items[i]))).To(Succeed())
	}

	bmcSecretList := &metalv1alpha1.BMCSecretList{}
	Expect(k8sClient.List(ctx, bmcSecretList)).To(Succeed())
	for i := range bmcSecretList.Items {
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, &bmcSecretList.Items[i]))).To(Succeed())
	}

	Eventually(ObjectList(&metalv1alpha1.ServerList{})).Should(HaveField("Items", HaveLen(0)))
	Eventually(ObjectList(&metalv1alpha1.BMCList{})).Should(HaveField("Items", HaveLen(0)))
	Eventually(ObjectList(&metalv1alpha1.BMCSecretList{})).Should(HaveField("Items", HaveLen(0)))
}

// serverFromBMCReconciler is a minimal stand-in for metal-operator's
// BMCReconciler.discoverServers: when a BMC exists, ensure a Server object
// named bmcutils.GetServerNameFromBMCandIndex(0, bmc) exists and is
// controller-owned by the BMC.
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
