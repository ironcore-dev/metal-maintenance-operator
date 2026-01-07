// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"

	maintenancealpha1 "github.com/ironcore-dev/maintenance-operator/api/v1alpha1"
	"github.com/ironcore-dev/maintenance-operator/internal/hwmgr"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// ConsoleReconciler reconciles a Console object
type ConsoleReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=maintenance.metal.ironcore.dev,resources=consoles,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=maintenance.metal.ironcore.dev,resources=consoles/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=maintenance.metal.ironcore.dev,resources=consoles/finalizers,verbs=update

func (r *ConsoleReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling Console", "name", req.NamespacedName)
	console := &maintenancealpha1.Console{}
	if err := r.Get(ctx, req.NamespacedName, console); err != nil {
		logger.Error(err, "unable to fetch Console")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if console.GetDeletionTimestamp() != nil {
		return r.delete(ctx, console)
	}
	return r.reconcileExists(ctx, console)
}

func (r *ConsoleReconciler) reconcileExists(ctx context.Context, console *maintenancealpha1.Console) (ctrl.Result, error) {
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
		metalBmc := metalv1alpha1.BMC{}
		if err := r.Get(ctx, client.ObjectKey{Name: server.Spec.BMCRef.Name, Namespace: server.Namespace}, &metalBmc); err != nil {
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
			if err := r.Get(ctx, client.ObjectKey{Name: metalBmc.Spec.BMCSecretRef.Name, Namespace: metalBmc.Namespace}, &bmcSecret); err != nil {
				errs = append(errs, err)
				logger.Error(err, "unable to get BMC secret for server", "server", server.Name)
				continue
			}
			node := strings.Split(server.Name, "-")
			hostname := fmt.Sprintf("%sr-%s.cc.qa-de-1.cloud.sap", node[0], node[1])
			if err := consoleClient.ImportServer(hostname, metalBmc.Status.IP, bmcSecret.StringData["username"], bmcSecret.StringData["password"]); err != nil {
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

func (r *ConsoleReconciler) delete(ctx context.Context, console *maintenancealpha1.Console) (ctrl.Result, error) {
	serverList, err := r.getServerList(ctx, console)
	if err != nil {
		return ctrl.Result{}, err
	}
	var errs []error
	cclient, err := r.createConsoleClient(ctx, console, nil)
	if err != nil {
		return ctrl.Result{}, err
	}
	for _, server := range serverList.Items {
		metalBmc := metalv1alpha1.BMC{}
		if err := r.Get(ctx, client.ObjectKey{Name: server.Spec.BMCRef.Name, Namespace: server.Namespace}, &metalBmc); err != nil {
			log.FromContext(ctx).Error(err, "unable to get BMC for server", "server", server.Name)
			errs = append(errs, err)
			continue
		}
		if err := cclient.RemoveServer(server.Spec.BMC.Address, metalBmc.Status.IP); err != nil {
			log.FromContext(ctx).Error(err, "unable to remove server from console", "server", server.Name)
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return ctrl.Result{}, errors.Join(errs...)
	}

	return ctrl.Result{}, nil
}

func (r *ConsoleReconciler) createConsoleClient(
	ctx context.Context,
	console *maintenancealpha1.Console,
	secret *corev1.Secret,
) (*hwmgr.Client, error) {
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
	return hwmgr.New(console.Spec.Manufacturer, hwmgr.ClientOptions{
		Endpoint: console.Spec.ConsoleURL,
		Username: string(username),
		Password: string(password),
		Token:    string(token),
	})
}

func (r *ConsoleReconciler) getConsoleSecret(
	ctx context.Context,
	console *maintenancealpha1.Console,
) (*corev1.Secret, error) {
	secret := &corev1.Secret{}
	if console.Spec.BMCCredentialSecretRef.Name == "" {
		return nil, fmt.Errorf("no credential secret ref specified")
	}
	if err := r.Get(ctx, client.ObjectKey{Name: console.Spec.BMCCredentialSecretRef.Name, Namespace: console.Namespace}, secret); err != nil {
		log.FromContext(ctx).Error(err, "unable to get console credential secret")
		return nil, err
	}
	return secret, nil
}

func (r *ConsoleReconciler) getServerList(
	ctx context.Context,
	console *maintenancealpha1.Console,
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

func (r *ConsoleReconciler) updateSecretToken(
	ctx context.Context,
	secret *corev1.Secret,
	consoleClient *hwmgr.Client,
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

func (r *ConsoleReconciler) updateStatus(
	ctx context.Context,
	consoleClient *hwmgr.Client,
	console *maintenancealpha1.Console,
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

func (r *ConsoleReconciler) enqueueRequestsForServer(ctx context.Context, obj client.Object) []ctrl.Request {
	var requests []ctrl.Request
	consoleList := &maintenancealpha1.ConsoleList{}
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
func (r *ConsoleReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&maintenancealpha1.Console{}).
		Watches(&metalv1alpha1.Server{},
			handler.EnqueueRequestsFromMapFunc(r.enqueueRequestsForServer)).
		Named("servermanagement").
		Complete(r)
}
