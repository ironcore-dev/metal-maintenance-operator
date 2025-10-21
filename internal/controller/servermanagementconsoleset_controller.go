// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"

	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"

	consolemaintenancev1alpha1 "github.com/ironcore-dev/maintenance-operator/api/v1alpha1"
	"github.com/ironcore-dev/maintenance-operator/internal/servermanagement"
)

// ServerManagementConsoleSetReconciler reconciles a ServerManagementConsoleSet object
type ServerManagementConsoleSetReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=console.maintenance.metal.ironcore.dev,resources=servermanagementconsoles,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=console.maintenance.metal.ironcore.dev,resources=servermanagementconsoles/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=console.maintenance.metal.ironcore.dev,resources=servermanagementconsoles/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the ServerManagementConsole object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.2/pkg/reconcile
func (r *ServerManagementConsoleSetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	console := &consolemaintenancev1alpha1.ServerManagementConsoleSet{}
	if err := r.Get(ctx, req.NamespacedName, console); err != nil {
		logger.Error(err, "unable to fetch ServerManagementConsoleSet")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if console.GetDeletionTimestamp() != nil {
		return r.delete(ctx, console)
	}
	return r.reconcileExists(ctx, console)
}

func (r *ServerManagementConsoleSetReconciler) reconcileExists(ctx context.Context, console *consolemaintenancev1alpha1.ServerManagementConsoleSet) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	selector, err := metav1.LabelSelectorAsSelector(&console.Spec.ServerSelector)
	if err != nil {
		logger.Error(err, "invalid label selector")
		return ctrl.Result{}, err
	}
	serverList := &metalv1alpha1.ServerList{}
	if err := r.List(ctx, serverList, &client.ListOptions{LabelSelector: selector}); err != nil {
		logger.Error(err, "unable to list servers")
		return ctrl.Result{}, err
	}
	secret, err := r.getConsoleSecret(ctx, console)
	if err != nil {
		logger.Error(err, "unable to get console credential secret")
		return ctrl.Result{}, err
	}
	consoleClient, err := servermanagement.New(console.Spec.Manufacturer, servermanagement.ClientOptions{
		Endpoint: console.Spec.ConsoleURL,
		Username: string(secret.Data["username"]),
		Password: string(secret.Data["password"]),
		Token:    string(secret.Data["token"]),
	})
	if err != nil {
		logger.Error(err, "unable to create server management console client")
		return ctrl.Result{}, err
	}
	token, err := consoleClient.GetAuthToken()
	if err != nil {
		logger.Error(err, "unable to get auth token from console")
		return ctrl.Result{}, err
	}

	if err := r.updateSecretToken(ctx, secret, token); err != nil {
		logger.Error(err, "unable to update console credential secret with token")
		return ctrl.Result{}, err
	}
	managedServers, err := consoleClient.ListServers()
	if err != nil {
		logger.Error(err, "unable to list servers from console")
		return ctrl.Result{}, err
	}

	for _, server := range serverList.Items {
		bmc := metalv1alpha1.BMC{}
		if err := r.Get(ctx, client.ObjectKey{Name: server.Spec.BMCRef.Name, Namespace: server.Namespace}, &bmc); err != nil {
			logger.Error(err, "unable to get BMC for server", "server", server.Name)
			continue
		}
		bmcSecret := metalv1alpha1.BMCSecret{}
		if err := r.Get(ctx, client.ObjectKey{Name: bmc.Spec.BMCSecretRef.Name, Namespace: bmc.Namespace}, &bmcSecret); err != nil {
			logger.Error(err, "unable to get BMC secret for server", "server", server.Name)
			continue
		}
		found := false
		for _, cs := range managedServers {
			if cs.Name == server.Spec.BMC.Address {
				found = true
				break
			}
		}
		if !found {
			if err := consoleClient.ImportServer(server.Spec.BMC.Address, bmc.Status.IP); err != nil {
				logger.Error(err, "unable to import server to console", "server", server.Name)
				continue
			}
		}
	}
	return r.updateStatus(ctx, consoleClient, console)
}

func (r *ServerManagementConsoleSetReconciler) delete(ctx context.Context, console *consolemaintenancev1alpha1.ServerManagementConsoleSet) (ctrl.Result, error) {
	return ctrl.Result{}, nil
}

func (r *ServerManagementConsoleSetReconciler) getConsoleSecret(
	ctx context.Context,
	console *consolemaintenancev1alpha1.ServerManagementConsoleSet,
) (*corev1.Secret, error) {
	secret := &corev1.Secret{}
	var secretName string
	switch console.Spec.Manufacturer {
	case "Dell":
		secretName = console.Spec.DellCredentialSecretRef
	case "Lenovo":
		secretName = console.Spec.LenovoCredentialSecretRef
	case "HPE":
		secretName = console.Spec.HPECredentialSecretRef
	default:
		return nil, nil
	}
	if secretName == "" {
		return nil, nil
	}
	if err := r.Get(ctx, client.ObjectKey{Name: secretName, Namespace: console.Namespace}, secret); err != nil {
		log.FromContext(ctx).Error(err, "unable to get console credential secret")
		return nil, err
	}
	return secret, nil
}

func (r *ServerManagementConsoleSetReconciler) updateSecretToken(
	ctx context.Context,
	secret *corev1.Secret,
	token string,
) error {
	secretBase := secret.DeepCopy()
	secret.Data["token"] = []byte(token)
	if err := r.Patch(ctx, secret, client.MergeFrom(secretBase)); err != nil {
		log.FromContext(ctx).Error(err, "unable to update console credential secret with token")
		return err
	}
	return nil
}

func (r *ServerManagementConsoleSetReconciler) updateStatus(
	ctx context.Context,
	consoleClient *servermanagement.ServerManagementConsole,
	console *consolemaintenancev1alpha1.ServerManagementConsoleSet,
) (ctrl.Result, error) {
	managedServers := int32(0)
	unmanagedServers := int32(0)
	totalServers := int32(0)

	serverList := &metalv1alpha1.ServerList{}
	selector, err := metav1.LabelSelectorAsSelector(&console.Spec.ServerSelector)
	if err != nil {
		log.FromContext(ctx).Error(err, "invalid label selector")
		return ctrl.Result{}, err
	}
	if err := r.List(ctx, serverList, &client.ListOptions{LabelSelector: selector}); err != nil {
		log.FromContext(ctx).Error(err, "unable to list servers")
		return ctrl.Result{}, err
	}
	totalServers = int32(len(serverList.Items))
	managedDevices, err := consoleClient.ListServers()
	if err != nil {
		log.FromContext(ctx).Error(err, "unable to list servers from console")
		return ctrl.Result{}, err
	}
	managedMap := make(map[string]bool)
	for _, device := range managedDevices {
		managedMap[device.Name] = true
	}
	for _, server := range serverList.Items {
		if managedMap[server.Spec.BMC.Address] {
			managedServers++
		} else {
			unmanagedServers++
		}
	}

	consoleBase := console.DeepCopy()
	console.Status.ManagedServers = managedServers
	console.Status.UnmanagedServers = unmanagedServers
	console.Status.TotalServers = totalServers

	if err := r.Status().Patch(ctx, console, client.MergeFrom(consoleBase)); err != nil {
		log.FromContext(ctx).Error(err, "unable to update ServerManagementConsoleSet status")
		return ctrl.Result{}, err
	}
	if totalServers > managedServers {
		// Requeue to ensure all servers are managed
		return ctrl.Result{Requeue: true}, nil
	}
	return ctrl.Result{}, nil
}

func (r *ServerManagementConsoleSetReconciler) enqueueRequestsForServer(ctx context.Context, obj client.Object) []ctrl.Request {
	var requests []ctrl.Request
	consoleList := &consolemaintenancev1alpha1.ServerManagementConsoleSetList{}
	if err := r.List(context.Background(), consoleList); err != nil {
		return nil
	}
	for _, console := range consoleList.Items {
		selector, err := metav1.LabelSelectorAsSelector(&console.Spec.ServerSelector)
		if err != nil {
			continue
		}
		if selector.Matches(labels.Set(obj.GetLabels())) {
			requests = append(requests, ctrl.Request{
				NamespacedName: client.ObjectKey{
					Name:      console.Name,
					Namespace: console.Namespace,
				},
			})
		}
	}
	return requests
}

// SetupWithManager sets up the controller with the Manager.
func (r *ServerManagementConsoleSetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&consolemaintenancev1alpha1.ServerManagementConsoleSet{}).
		Watches(&metalv1alpha1.Server{},
			handler.EnqueueRequestsFromMapFunc(r.enqueueRequestsForServer)).
		Named("servermanagementconsoleset").
		Complete(r)
}
