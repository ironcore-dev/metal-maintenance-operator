// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package maintenance

import (
	"context"
	"errors"
	"fmt"
	"time"

	maintenancealpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/maintenance/v1alpha1"
	"github.com/ironcore-dev/metal-maintenance-operator/internal/hwmgr"
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
// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=servers,verbs=get;list;watch
// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=bmcs,verbs=get;list;watch
// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=bmcsecrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;patch

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

	// Step 1: Check pending operations first
	requeueAfter, err := r.reconcilePendingOperations(ctx, console)
	if err != nil {
		logger.Error(err, "unable to reconcile pending operations")
		return ctrl.Result{}, err
	}
	if requeueAfter > 0 {
		logger.Info("Requeuing to check pending operations", "after", requeueAfter)
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}

	// Step 2: Get servers and client
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
	logger.Info("Successfully created console client", "manufacturer", console.Spec.Manufacturer)

	if err := r.updateSecretToken(ctx, secret, consoleClient); err != nil {
		logger.Error(err, "unable to update console credential secret with token")
		return ctrl.Result{}, err
	}

	managedServers, err := consoleClient.ListServers()
	if err != nil {
		logger.Error(err, "unable to list servers from console")
		return ctrl.Result{}, err
	}

	// Step 3: Start new operations for unmanaged servers
	if err := r.startNewOperations(ctx, console, serverList, managedServers, consoleClient); err != nil {
		logger.Error(err, "unable to start new operations")
		return ctrl.Result{}, err
	}

	// Step 4: Update status
	return r.updateStatus(ctx, consoleClient, console)
}

// reconcilePendingOperations checks the status of pending operations and updates them.
// Returns the duration after which to requeue if there are pending operations, or 0 if none.
func (r *ConsoleReconciler) reconcilePendingOperations(ctx context.Context, console *maintenancealpha1.Console) (time.Duration, error) {
	logger := log.FromContext(ctx)

	if len(console.Status.PendingOperations) == 0 {
		return 0, nil
	}

	// Get console client to check job statuses
	secret, err := r.getConsoleSecret(ctx, console)
	if err != nil {
		return 0, fmt.Errorf("unable to get console secret: %w", err)
	}

	consoleClient, err := r.createConsoleClient(ctx, console, secret)
	if err != nil {
		return 0, fmt.Errorf("unable to create console client: %w", err)
	}

	const operationTimeout = 15 * time.Minute
	const pollInterval = 10 * time.Second
	now := metav1.Now()

	updatedOps := make([]maintenancealpha1.PendingOperation, 0, len(console.Status.PendingOperations))
	hasUpdates := false

	for _, op := range console.Status.PendingOperations {
		// Check if operation has timed out
		if now.Sub(op.StartTime.Time) > operationTimeout {
			logger.Info("Operation timed out", "server", op.ServerName, "operation", op.OperationType, "jobID", op.JobID)
			op.Status = maintenancealpha1.JobStatusTimedOut
			op.Message = "Operation exceeded 15 minute timeout"
			op.LastChecked = now
			hasUpdates = true
			continue
		}

		// If jobID is empty, operation was synchronous and should be removed
		if op.JobID == "" {
			logger.Info("Removing synchronous operation", "server", op.ServerName, "operation", op.OperationType)
			hasUpdates = true
			continue
		}

		// Query job status
		jobInfo, err := consoleClient.GetJobStatus(op.JobID)
		if err != nil {
			logger.Error(err, "Error checking job status", "jobID", op.JobID, "server", op.ServerName)
			op.Message = fmt.Sprintf("Error checking status: %v", err)
			op.LastChecked = now
			updatedOps = append(updatedOps, op)
			continue
		}

		op.LastChecked = now
		op.Message = jobInfo.Message

		if consoleClient.IsJobComplete(jobInfo) {
			if consoleClient.IsJobSuccessful(jobInfo) {
				logger.Info("Operation completed successfully", "server", op.ServerName, "operation", op.OperationType, "jobID", op.JobID)
				op.Status = maintenancealpha1.JobStatusCompleted
				hasUpdates = true
				// Don't add to updatedOps - remove completed operations
				continue
			} else {
				logger.Info("Operation failed", "server", op.ServerName, "operation", op.OperationType, "jobID", op.JobID)
				op.Status = maintenancealpha1.JobStatusFailed
				hasUpdates = true
				// Don't add to updatedOps - remove failed operations for now
				// TODO: Consider retry logic in future
				continue
			}
		} else {
			// Still running
			if op.Status != maintenancealpha1.JobStatusRunning {
				op.Status = maintenancealpha1.JobStatusRunning
				hasUpdates = true
			}
			updatedOps = append(updatedOps, op)
		}
	}

	// Update Console status if there were changes
	if hasUpdates {
		consoleBase := console.DeepCopy()
		console.Status.PendingOperations = updatedOps
		if err := r.Status().Patch(ctx, console, client.MergeFrom(consoleBase)); err != nil {
			return 0, fmt.Errorf("unable to update Console status: %w", err)
		}
		logger.Info("Updated pending operations", "remaining", len(updatedOps))
	}

	// If there are still pending operations, requeue
	if len(updatedOps) > 0 {
		return pollInterval, nil
	}

	return 0, nil
}

// startNewOperations initiates import operations for servers that are not managed and not pending.
func (r *ConsoleReconciler) startNewOperations(
	ctx context.Context,
	console *maintenancealpha1.Console,
	serverList *metalv1alpha1.ServerList,
	managedServers []hwmgr.Device,
	consoleClient *hwmgr.Client,
) error {
	logger := log.FromContext(ctx)

	// Build map of servers with pending operations
	pendingMap := make(map[string]bool)
	for _, op := range console.Status.PendingOperations {
		pendingMap[op.ServerName] = true
	}

	// Build map of managed hostnames
	managedMap := make(map[string]bool)
	for _, device := range managedServers {
		managedMap[device.Hostname] = true
	}

	newOperations := []maintenancealpha1.PendingOperation{}

	for _, server := range serverList.Items {
		// Skip if already pending
		if pendingMap[server.Name] {
			continue
		}
		metalBmc := &metalv1alpha1.BMC{}
		if err := r.Get(ctx, client.ObjectKey{Name: server.Spec.BMCRef.Name, Namespace: server.Namespace}, metalBmc); err != nil {
			logger.Error(err, "unable to get BMC for server", "server", server.Name)
			continue
		}
		hostname, err := r.getHostname(ctx, &server, metalBmc)
		if err != nil {
			logger.Error(err, "unable to fetch BMC hostname for server", "server", server.Name)
			continue
		}
		// Skip if already managed
		if managedMap[hostname] {
			continue
		}
		bmcSecret := metalv1alpha1.BMCSecret{}
		if err := r.Get(ctx, client.ObjectKey{Name: metalBmc.Spec.BMCSecretRef.Name, Namespace: metalBmc.Namespace}, &bmcSecret); err != nil {
			logger.Error(err, "unable to get BMC secret for server", "server", server.Name)
			continue
		}
		// Start async import
		jobID, err := consoleClient.ImportServerAsync(hostname, metalBmc.Status.IP, bmcSecret.StringData["username"], bmcSecret.StringData["password"])
		if err != nil {
			logger.Error(err, "unable to start import for server", "server", server.Name)
			continue
		}
		// Create pending operation
		now := metav1.Now()
		operation := maintenancealpha1.PendingOperation{
			ServerName:    server.Name,
			Hostname:      hostname,
			IP:            metalBmc.Status.IP.String(),
			OperationType: maintenancealpha1.OperationTypeImport,
			JobID:         jobID,
			Status:        maintenancealpha1.JobStatusPending,
			StartTime:     now,
			LastChecked:   now,
			RetryCount:    0,
			Message:       "Import operation initiated",
		}
		newOperations = append(newOperations, operation)
		logger.Info("Started import operation", "server", server.Name, "hostname", hostname, "jobID", jobID)
	}

	// Update Console status if new operations were started
	if len(newOperations) > 0 {
		consoleBase := console.DeepCopy()
		console.Status.PendingOperations = append(console.Status.PendingOperations, newOperations...)
		if err := r.Status().Patch(ctx, console, client.MergeFrom(consoleBase)); err != nil {
			return fmt.Errorf("unable to update Console status with new operations: %w", err)
		}
		logger.Info("Added new pending operations", "count", len(newOperations))
	}

	return nil
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

	// Only query console if we have servers and a client
	managedMap := make(map[string]bool)
	if totalServers > 0 && consoleClient != nil {
		managedDevices, err := consoleClient.ListServers()
		if err != nil {
			log.FromContext(ctx).Error(err, "unable to list servers from console")
			return ctrl.Result{}, err
		}
		for _, device := range managedDevices {
			managedMap[device.Hostname] = true
		}
	}

	for _, server := range serverList.Items {
		hostname, err := r.getHostname(ctx, &server, nil)
		if err != nil {
			log.FromContext(ctx).Error(err, "unable to fetch BMC hostname for server", "server", server.Name)
			unmanagedServers++
			continue
		}
		if managedMap[hostname] {
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

func (r *ConsoleReconciler) getServerBMC(ctx context.Context, server metalv1alpha1.Server) (*metalv1alpha1.BMC, error) {
	bmc := &metalv1alpha1.BMC{}
	if err := r.Get(ctx, client.ObjectKey{Name: server.Spec.BMCRef.Name, Namespace: server.Namespace}, bmc); err != nil {
		log.FromContext(ctx).Error(err, "unable to get BMC for server", "server", server.Name)
		return nil, err
	}
	return bmc, nil
}

func (r *ConsoleReconciler) getHostname(ctx context.Context, server *metalv1alpha1.Server, bmc *metalv1alpha1.BMC) (string, error) {
	if bmc == nil {
		var err error
		bmc, err = r.getServerBMC(ctx, *server)
		if err != nil {
			log.FromContext(ctx).Error(err, "unable to get BMC for server", "server", server.Name)
			return "", err
		}
	}
	if bmc.Spec.Hostname == nil || *bmc.Spec.Hostname == "" {
		err := fmt.Errorf("hostname is empty in BMC spec")
		log.FromContext(ctx).Error(err, "unable to get hostname for server", "server", server.Name)
		return "", err
	}
	return *bmc.Spec.Hostname, nil
}

func (r *ConsoleReconciler) enqueueRequestsForServer(ctx context.Context, obj client.Object) []ctrl.Request {
	var requests []ctrl.Request
	consoleList := &maintenancealpha1.ConsoleList{}
	if err := r.List(ctx, consoleList); err != nil {
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
