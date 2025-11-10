// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"

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
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	"github.com/ironcore-dev/metal-operator/bmc"
)

// ServerManagementReconciler reconciles a ServerManagement object
type ServerManagementReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=vendorconsole.metal.ironcore.dev,resources=servermanagements,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=vendorconsole.metal.ironcore.dev,resources=servermanagements/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=vendorconsole.metal.ironcore.dev,resources=servermanagements/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the ServerManagement object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.2/pkg/reconcile
func (r *ServerManagementReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling ServerManagement", "name", req.NamespacedName)
	console := &consolemaintenancev1alpha1.ServerManagement{}
	if err := r.Get(ctx, req.NamespacedName, console); err != nil {
		logger.Error(err, "unable to fetch ServerManagement")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if console.GetDeletionTimestamp() != nil {
		return r.delete(ctx, console)
	}
	return r.reconcileExists(ctx, console)
}

func (r *ServerManagementReconciler) reconcileExists(ctx context.Context, console *consolemaintenancev1alpha1.ServerManagement) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	serverList, err := r.getServerList(ctx, console)
	if err != nil {
		logger.Error(err, "unable to list servers")
		return ctrl.Result{}, err
	}
	logger.Info("Found servers matching selector", "count", len(serverList.Items))
	if len(serverList.Items) == 0 {
		return r.updateStatus(ctx, nil, console)
	}
	secret, err := r.getConsoleSecret(ctx, console)
	if err != nil {
		logger.Error(err, "unable to get console credential secret")
		return ctrl.Result{}, err
	}
	consoleClient, err := r.createConsoleClient(ctx, console, secret)
	if err != nil {
		logger.Error(err, "unable to create server management console client")
		return ctrl.Result{}, err
	}
	logger.Info("Successfully created console client", consoleClient)
	if err := r.updateSecretToken(ctx, secret, consoleClient); err != nil {
		logger.Error(err, "unable to update console credential secret with token")
		return ctrl.Result{}, err
	}
	managedServers, err := consoleClient.ListServers()
	if err != nil {
		logger.Error(err, "unable to list servers from console")
		return ctrl.Result{}, err
	}
	var errs []error
	for _, server := range serverList.Items {
		bmc := metalv1alpha1.BMC{}
		if err := r.Get(ctx, client.ObjectKey{Name: server.Spec.BMCRef.Name, Namespace: server.Namespace}, &bmc); err != nil {
			errs = append(errs, err)
			logger.Error(err, "unable to get BMC for server", "server", server.Name)
			continue
		}
		found := false
		for _, cs := range managedServers {
			if cs.Hostname == fmt.Sprintf("%sr-%s.cc.qa-de-1.cloud.sap", strings.Split(server.Name, "-")[0], strings.Split(server.Name, "-")[1]) {
				found = true
				break
			}
		}
		if !found {
			bmcSecret := metalv1alpha1.BMCSecret{}
			if err := r.Get(ctx, client.ObjectKey{Name: bmc.Spec.BMCSecretRef.Name, Namespace: bmc.Namespace}, &bmcSecret); err != nil {
				errs = append(errs, err)
				logger.Error(err, "unable to get BMC secret for server", "server", server.Name)
				continue
			}
			node := strings.Split(server.Name, "-")
			hostname := fmt.Sprintf("%sr-%s.cc.qa-de-1.cloud.sap", node[0], node[1])
			if err := consoleClient.ImportServer(hostname, bmc.Status.IP, bmcSecret.StringData["username"], bmcSecret.StringData["password"]); err != nil {
				errs = append(errs, err)
				logger.Error(err, "unable to import server to console", "server", server.Name)
				continue
			}
		}
	}
	if len(errs) > 0 {
		return ctrl.Result{}, errors.Join(errs...)
	}
	return r.updateStatus(ctx, consoleClient, console)
}

func (r *ServerManagementReconciler) delete(ctx context.Context, console *consolemaintenancev1alpha1.ServerManagement) (ctrl.Result, error) {
	serverList, err := r.getServerList(ctx, console)
	if err != nil {
		return ctrl.Result{}, err
	}
	var errs []error
	cclient, err := r.createConsoleClient(ctx, console, nil)
	for _, server := range serverList.Items {
		bmc := metalv1alpha1.BMC{}
		if err := r.Get(ctx, client.ObjectKey{Name: server.Spec.BMCRef.Name, Namespace: server.Namespace}, &bmc); err != nil {
			log.FromContext(ctx).Error(err, "unable to get BMC for server", "server", server.Name)
			errs = append(errs, err)
			continue
		}
		if err := cclient.RemoveServer(server.Spec.BMC.Address, bmc.Status.IP); err != nil {
			log.FromContext(ctx).Error(err, "unable to remove server from console", "server", server.Name)
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return ctrl.Result{}, errors.Join(errs...)
	}

	return ctrl.Result{}, nil
}

func (r *ServerManagementReconciler) createConsoleClient(
	ctx context.Context,
	console *consolemaintenancev1alpha1.ServerManagement,
	secret *corev1.Secret,
) (*servermanagement.ServerManagementConsole, error) {
	var err error
	if secret == nil {
		secret, err = r.getConsoleSecret(ctx, console)
		if err != nil {
			return nil, err
		}
	}
	var ok bool
	var username []byte
	// check is username/password/token are present in secret
	if username, ok = secret.Data["username"]; !ok {
		return nil, fmt.Errorf("username not found in console credential secret")
	}
	var password []byte
	if password, ok = secret.Data["password"]; !ok {
		return nil, fmt.Errorf("password not found in console credential secret")
	}
	var token []byte
	if token, ok = secret.Data["token"]; !ok {
		token = []byte("")
	}

	log.FromContext(ctx).Info("Creating console client", "manufacturer", console.Spec.Manufacturer, "consoleURL", console.Spec.ConsoleURL)
	return servermanagement.New(console.Spec.Manufacturer, servermanagement.ClientOptions{
		Endpoint: console.Spec.ConsoleURL,
		Username: string(username),
		Password: string(password),
		Token:    string(token),
	})
}

func (r *ServerManagementReconciler) getConsoleSecret(
	ctx context.Context,
	console *consolemaintenancev1alpha1.ServerManagement,
) (*corev1.Secret, error) {
	secret := &corev1.Secret{}
	var secretName string
	switch console.Spec.Manufacturer {
	case string(bmc.ManufacturerDell):
		secretName = console.Spec.DellCredentialSecretRef.Name
	case string(bmc.ManufacturerLenovo):
		secretName = console.Spec.LenovoCredentialSecretRef.Name
	case string(bmc.ManufacturerHPE):
		secretName = console.Spec.HPECredentialSecretRef.Name
	default:
		return nil, fmt.Errorf("unsupported manufacturer: %s", console.Spec.Manufacturer)
	}
	if secretName == "" {
		return nil, fmt.Errorf("no credential secret ref specified for manufacturer: %s", console.Spec.Manufacturer)
	}
	if err := r.Get(ctx, client.ObjectKey{Name: secretName, Namespace: console.Namespace}, secret); err != nil {
		log.FromContext(ctx).Error(err, "unable to get console credential secret")
		return nil, err
	}
	return secret, nil
}

func (r *ServerManagementReconciler) getServerList(
	ctx context.Context,
	console *consolemaintenancev1alpha1.ServerManagement,
) (*metalv1alpha1.ServerList, error) {
	selector, err := metav1.LabelSelectorAsSelector(&console.Spec.ServerSelector)
	if err != nil {
		log.FromContext(ctx).Error(err, "invalid label selector")
		return nil, err
	}
	serverList := &metalv1alpha1.ServerList{}
	if err := r.List(ctx, serverList, &client.ListOptions{LabelSelector: selector}); err != nil {
		log.FromContext(ctx).Error(err, "unable to list servers")
		return nil, err
	}
	return serverList, nil
}

func (r *ServerManagementReconciler) updateSecretToken(
	ctx context.Context,
	secret *corev1.Secret,
	consoleClient *servermanagement.ServerManagementConsole,
) error {
	token, err := consoleClient.GetAuthToken()
	if err != nil {
		return err
	}
	secretBase := secret.DeepCopy()
	secret.Data["token"] = []byte(token)
	if err := r.Patch(ctx, secret, client.MergeFrom(secretBase)); err != nil {
		log.FromContext(ctx).Error(err, "unable to update console credential secret with token")
		return err
	}
	return nil
}

func (r *ServerManagementReconciler) updateStatus(
	ctx context.Context,
	consoleClient *servermanagement.ServerManagementConsole,
	console *consolemaintenancev1alpha1.ServerManagement,
) (ctrl.Result, error) {
	managedServers := 0
	unmanagedServers := 0
	totalServers := 0

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
	totalServers = len(serverList.Items)
	managedDevices, err := consoleClient.ListServers()
	if err != nil {
		log.FromContext(ctx).Error(err, "unable to list servers from console")
		return ctrl.Result{}, err
	}
	managedMap := make(map[string]bool)
	for _, device := range managedDevices {
		managedMap[device.Hostname] = true
	}
	for _, server := range serverList.Items {
		if managedMap[fmt.Sprintf("%sr-%s.cc.qa-de-1.cloud.sap", strings.Split(server.Name, "-")[0], strings.Split(server.Name, "-")[1])] {
			managedServers++
			log.FromContext(ctx).Info("Updating status", "totalServers", totalServers, "managedServersCount", (managedServers))
		} else {
			unmanagedServers++
		}
	}
	consoleBase := console.DeepCopy()
	console.Status.ManagedServers = int32(managedServers)
	console.Status.UnmanagedServers = int32(unmanagedServers)
	console.Status.TotalServers = int32(totalServers)

	if err := r.Status().Patch(ctx, console, client.MergeFrom(consoleBase)); err != nil {
		log.FromContext(ctx).Error(err, "unable to update ServerManagement status")
		return ctrl.Result{}, err
	}
	if totalServers > managedServers {
		// Requeue to ensure all servers are managed
		return ctrl.Result{Requeue: true}, nil
	}
	return ctrl.Result{}, nil
}

func (r *ServerManagementReconciler) enqueueRequestsForServer(ctx context.Context, obj client.Object) []ctrl.Request {
	var requests []ctrl.Request
	consoleList := &consolemaintenancev1alpha1.ServerManagementList{}
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
func (r *ServerManagementReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&consolemaintenancev1alpha1.ServerManagement{}).
		Watches(&metalv1alpha1.Server{},
			handler.EnqueueRequestsFromMapFunc(r.enqueueRequestsForServer)).
		Named("servermanagement").
		Complete(r)
}
