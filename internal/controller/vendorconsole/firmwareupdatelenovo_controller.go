// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package vendorconsole

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	vendorconsolev1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/vendorconsole/v1alpha1"
	"github.com/ironcore-dev/metal-maintenance-operator/internal/hwmgr"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	"github.com/ironcore-dev/metal-operator/bmc"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// FirmwareUpdateLenovoFinalizer is added to FirmwareUpdateLenovo objects so
// the controller gets a chance to tear down ServerMaintenance children and
// close the LXCA session before the CR disappears.
const FirmwareUpdateLenovoFinalizer = "vendorconsole.metal.ironcore.dev/firmwareupdatelenovo"

// defaultResyncInterval controls how often the controller polls LXCA while a
// long-running LXCA task is in progress.
const defaultResyncInterval = 30 * time.Second

// FirmwareUpdateLenovoReconciler reconciles a FirmwareUpdateLenovo object.
type FirmwareUpdateLenovoReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// ResyncInterval controls how often we requeue while polling LXCA. If
	// zero, defaultResyncInterval is used.
	ResyncInterval time.Duration
}

// +kubebuilder:rbac:groups=vendorconsole.metal.ironcore.dev,resources=firmwareupdatelenovoes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=vendorconsole.metal.ironcore.dev,resources=firmwareupdatelenovoes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=vendorconsole.metal.ironcore.dev,resources=firmwareupdatelenovoes/finalizers,verbs=update
// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=servers,verbs=get;list;watch
// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=servermaintenances,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=servermaintenances/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;patch

// Reconcile drives a FirmwareUpdateLenovo through Pending → InProgress →
// Completed / Failed by talking to a Lenovo XClarity Administrator.
func (r *FirmwareUpdateLenovoReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	fw := &vendorconsolev1alpha1.FirmwareUpdateLenovo{}
	if err := r.Get(ctx, req.NamespacedName, fw); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if shouldIgnoreReconciliation(fw) {
		logger.Info("Ignoring FirmwareUpdateLenovo due to ignore-reconciliation annotation")
		return ctrl.Result{}, nil
	}

	if fw.GetDeletionTimestamp() != nil {
		return r.reconcileDelete(ctx, fw)
	}

	if !controllerutil.ContainsFinalizer(fw, FirmwareUpdateLenovoFinalizer) {
		base := fw.DeepCopy()
		controllerutil.AddFinalizer(fw, FirmwareUpdateLenovoFinalizer)
		if err := r.Patch(ctx, fw, client.MergeFrom(base)); err != nil {
			return ctrl.Result{}, fmt.Errorf("adding finalizer: %w", err)
		}
		return ctrl.Result{Requeue: true}, nil
	}

	return r.reconcileState(ctx, fw)
}

func (r *FirmwareUpdateLenovoReconciler) reconcileState(
	ctx context.Context, fw *vendorconsolev1alpha1.FirmwareUpdateLenovo,
) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	switch fw.Status.State {
	case "", vendorconsolev1alpha1.FirmwareUpdateStatePending:
		return r.handlePendingState(ctx, fw)
	case vendorconsolev1alpha1.FirmwareUpdateStateInProgress:
		return r.handleInProgressState(ctx, fw)
	case vendorconsolev1alpha1.FirmwareUpdateStateCompleted:
		return r.handleCompletedState(ctx, fw)
	case vendorconsolev1alpha1.FirmwareUpdateStateFailed:
		return r.handleFailedState(ctx, fw)
	default:
		logger.Info("Unknown state; treating as Pending", "state", fw.Status.State)
		return r.handlePendingState(ctx, fw)
	}
}

// handlePendingState validates that the selector picks Lenovo servers and
// transitions the CR to InProgress.
func (r *FirmwareUpdateLenovoReconciler) handlePendingState(
	ctx context.Context, fw *vendorconsolev1alpha1.FirmwareUpdateLenovo,
) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	servers, err := r.getSelectedServers(ctx, fw)
	if err != nil {
		return ctrl.Result{}, err
	}

	if len(servers) == 0 {
		logger.Info("Server selector matched zero servers; marking Completed")
		return r.patchStatus(ctx, fw, func(s *vendorconsolev1alpha1.FirmwareUpdateLenovoStatus) {
			s.State = vendorconsolev1alpha1.FirmwareUpdateStateCompleted
			s.ServerCount = 0
			meta := metav1.Condition{
				Type:    vendorconsolev1alpha1.FirmwareUpgradeCompletedCondition,
				Status:  metav1.ConditionTrue,
				Reason:  "NoServersMatched",
				Message: "Server selector matched no servers; nothing to do.",
			}
			upsertCondition(&s.Conditions, meta)
		})
	}

	if nonLenovo := nonLenovoServers(servers); len(nonLenovo) > 0 {
		logger.Info("Selector matched non-Lenovo servers; marking Failed",
			"nonLenovoCount", len(nonLenovo))
		return r.patchStatus(ctx, fw, func(s *vendorconsolev1alpha1.FirmwareUpdateLenovoStatus) {
			s.State = vendorconsolev1alpha1.FirmwareUpdateStateFailed
			s.ServerCount = int32(len(servers))
			upsertCondition(&s.Conditions, metav1.Condition{
				Type:    vendorconsolev1alpha1.FirmwareUpgradeCompletedCondition,
				Status:  metav1.ConditionFalse,
				Reason:  "NonLenovoServer",
				Message: fmt.Sprintf("selector matched non-Lenovo servers: %s", strings.Join(nonLenovo, ", ")),
			})
		})
	}

	if unknown := unknownManufacturerServers(servers); len(unknown) > 0 {
		logger.Info("Waiting for Server.Status.Manufacturer to be populated",
			"pendingServers", unknown)
		return ctrl.Result{RequeueAfter: r.resync()}, nil
	}

	return r.patchStatus(ctx, fw, func(s *vendorconsolev1alpha1.FirmwareUpdateLenovoStatus) {
		s.State = vendorconsolev1alpha1.FirmwareUpdateStateInProgress
		s.ServerCount = int32(len(servers))
	})
}

// handleInProgressState drives the actual firmware update flow.
func (r *FirmwareUpdateLenovoReconciler) handleInProgressState(
	ctx context.Context, fw *vendorconsolev1alpha1.FirmwareUpdateLenovo,
) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	servers, err := r.getSelectedServers(ctx, fw)
	if err != nil {
		return ctrl.Result{}, err
	}
	if len(servers) == 0 {
		// The selector was emptied out from under us; keep going by ending
		// in Completed rather than looping forever.
		return r.patchStatus(ctx, fw, func(s *vendorconsolev1alpha1.FirmwareUpdateLenovoStatus) {
			s.State = vendorconsolev1alpha1.FirmwareUpdateStateCompleted
			s.ServerCount = 0
			upsertCondition(&s.Conditions, metav1.Condition{
				Type:    vendorconsolev1alpha1.FirmwareUpgradeCompletedCondition,
				Status:  metav1.ConditionTrue,
				Reason:  "NoServersRemaining",
				Message: "no matching Lenovo servers remain",
			})
		})
	}

	lx, err := r.getLenovoClient(ctx, fw)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Step 1: firmware repository import. If we haven't started one, start
	// it. Otherwise wait for it to complete.
	requeue, err := r.ensureRepositoryImport(ctx, fw, lx)
	if err != nil {
		return r.failWith(ctx, fw, "RepositoryImportFailed", err)
	}
	if requeue {
		return ctrl.Result{RequeueAfter: r.resync()}, nil
	}

	// Step 2: compliance policy.
	policyID, err := r.ensureCompliancePolicy(ctx, fw, lx)
	if err != nil {
		return r.failWith(ctx, fw, "CompliancePolicyFailed", err)
	}

	// Step 3: resolve LXCA UUIDs for the selected servers.
	uuids, missing, err := r.resolveDeviceUUIDs(ctx, lx, servers)
	if err != nil {
		return r.failWith(ctx, fw, "DeviceLookupFailed", err)
	}
	if len(missing) > 0 {
		logger.Info("Some servers are not yet managed by LXCA; waiting", "missing", missing)
		return ctrl.Result{RequeueAfter: r.resync()}, nil
	}

	// Step 4: assign compliance policy to selected devices (idempotent).
	if err := lx.AssignCompliancePolicy(fw.Spec.CompliancePolicy.Name, uuids); err != nil {
		return r.failWith(ctx, fw, "PolicyAssignmentFailed", err)
	}

	// Step 5: request maintenance windows on each server. Wait until all
	// are InMaintenance before kicking the flash job.
	ready, err := r.ensureMaintenance(ctx, fw, servers)
	if err != nil {
		return r.failWith(ctx, fw, "MaintenanceFailed", err)
	}
	if !ready {
		return ctrl.Result{RequeueAfter: r.resync()}, nil
	}

	// Step 6: submit the firmware update job if we haven't already.
	if fw.Status.UpdateJobID == "" {
		activation := string(fw.Spec.UpdateAction)
		if activation == "" {
			activation = string(vendorconsolev1alpha1.UpdateActivationImmediate)
		}
		jobID, err := lx.ApplyFirmwareUpdate(uuids, fw.Spec.CompliancePolicy.Name, activation)
		if err != nil {
			return r.failWith(ctx, fw, "ApplyFailed", err)
		}
		return r.patchStatus(ctx, fw, func(s *vendorconsolev1alpha1.FirmwareUpdateLenovoStatus) {
			s.UpdateJobID = jobID
			s.CompliancePolicyID = policyID
		})
	}

	// Step 7: poll the update job.
	info, err := lx.GetTaskStatus(fw.Status.UpdateJobID)
	if err != nil {
		// Transient LXCA blips shouldn't Fail the CR — requeue.
		logger.Error(err, "unable to fetch task status; requeueing")
		return ctrl.Result{RequeueAfter: r.resync()}, nil
	}
	switch hwmgr.FirmwareJobStatus(info.Status) {
	case hwmgr.FirmwareJobStatusFailed:
		return r.failWith(ctx, fw, "UpdateJobFailed",
			fmt.Errorf("LXCA task %s reported %s: %s", fw.Status.UpdateJobID, info.Status, info.Message))
	case hwmgr.FirmwareJobStatusSuccess:
		return r.patchStatus(ctx, fw, func(s *vendorconsolev1alpha1.FirmwareUpdateLenovoStatus) {
			s.State = vendorconsolev1alpha1.FirmwareUpdateStateCompleted
			upsertCondition(&s.Conditions, metav1.Condition{
				Type:    vendorconsolev1alpha1.FirmwareUpgradeCompletedCondition,
				Status:  metav1.ConditionTrue,
				Reason:  "UpdateJobSucceeded",
				Message: fmt.Sprintf("LXCA task %s completed successfully", s.UpdateJobID),
			})
		})
	default:
		return ctrl.Result{RequeueAfter: r.resync()}, nil
	}
}

// handleCompletedState tears down ServerMaintenance children and closes the
// LXCA session. Left cheap so we don't hammer LXCA once we're done.
func (r *FirmwareUpdateLenovoReconciler) handleCompletedState(
	ctx context.Context, fw *vendorconsolev1alpha1.FirmwareUpdateLenovo,
) (ctrl.Result, error) {
	if err := r.cleanupMaintenance(ctx, fw); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// handleFailedState honours the retry annotation to reset back to Pending.
func (r *FirmwareUpdateLenovoReconciler) handleFailedState(
	ctx context.Context, fw *vendorconsolev1alpha1.FirmwareUpdateLenovo,
) (ctrl.Result, error) {
	if !shouldRetryFailed(fw) {
		return ctrl.Result{}, nil
	}
	if err := r.cleanupMaintenance(ctx, fw); err != nil {
		return ctrl.Result{}, err
	}
	if err := clearRetryAnnotation(ctx, r.Client, fw); err != nil {
		return ctrl.Result{}, err
	}
	return r.patchStatus(ctx, fw, func(s *vendorconsolev1alpha1.FirmwareUpdateLenovoStatus) {
		s.State = vendorconsolev1alpha1.FirmwareUpdateStatePending
		s.UpdateJobID = ""
		s.RepositoryJobID = ""
		s.CompliancePolicyID = ""
		s.Conditions = nil
	})
}

func (r *FirmwareUpdateLenovoReconciler) reconcileDelete(
	ctx context.Context, fw *vendorconsolev1alpha1.FirmwareUpdateLenovo,
) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	if !controllerutil.ContainsFinalizer(fw, FirmwareUpdateLenovoFinalizer) {
		return ctrl.Result{}, nil
	}
	if fw.Status.State == vendorconsolev1alpha1.FirmwareUpdateStateInProgress && fw.Status.UpdateJobID != "" {
		logger.Info("Deleting while an LXCA task is still running; attempting to cancel", "jobID", fw.Status.UpdateJobID)
		if lx, err := r.getLenovoClient(ctx, fw); err == nil {
			if cerr := lx.CancelTask(fw.Status.UpdateJobID); cerr != nil {
				logger.Error(cerr, "unable to cancel LXCA task")
			}
			_ = lx.CloseSession()
		}
	}
	if err := r.cleanupMaintenance(ctx, fw); err != nil {
		return ctrl.Result{}, err
	}
	base := fw.DeepCopy()
	controllerutil.RemoveFinalizer(fw, FirmwareUpdateLenovoFinalizer)
	if err := r.Patch(ctx, fw, client.MergeFrom(base)); err != nil {
		return ctrl.Result{}, fmt.Errorf("removing finalizer: %w", err)
	}
	return ctrl.Result{}, nil
}

// --- helpers ---------------------------------------------------------------

func (r *FirmwareUpdateLenovoReconciler) resync() time.Duration {
	if r.ResyncInterval > 0 {
		return r.ResyncInterval
	}
	return defaultResyncInterval
}

// patchStatus applies `mutate` to the status subresource via a merge-patch.
func (r *FirmwareUpdateLenovoReconciler) patchStatus(
	ctx context.Context,
	fw *vendorconsolev1alpha1.FirmwareUpdateLenovo,
	mutate func(*vendorconsolev1alpha1.FirmwareUpdateLenovoStatus),
) (ctrl.Result, error) {
	base := fw.DeepCopy()
	mutate(&fw.Status)
	if err := r.Status().Patch(ctx, fw, client.MergeFrom(base)); err != nil {
		return ctrl.Result{}, fmt.Errorf("patching status: %w", err)
	}
	return ctrl.Result{Requeue: true}, nil
}

// failWith transitions the CR to Failed and records the reason/message on
// the UpdateCompleted condition.
func (r *FirmwareUpdateLenovoReconciler) failWith(
	ctx context.Context, fw *vendorconsolev1alpha1.FirmwareUpdateLenovo, reason string, cause error,
) (ctrl.Result, error) {
	log.FromContext(ctx).Error(cause, "FirmwareUpdateLenovo failed", "reason", reason)
	return r.patchStatus(ctx, fw, func(s *vendorconsolev1alpha1.FirmwareUpdateLenovoStatus) {
		s.State = vendorconsolev1alpha1.FirmwareUpdateStateFailed
		upsertCondition(&s.Conditions, metav1.Condition{
			Type:    vendorconsolev1alpha1.FirmwareUpgradeCompletedCondition,
			Status:  metav1.ConditionFalse,
			Reason:  reason,
			Message: cause.Error(),
		})
	})
}

func (r *FirmwareUpdateLenovoReconciler) getSelectedServers(
	ctx context.Context, fw *vendorconsolev1alpha1.FirmwareUpdateLenovo,
) ([]metalv1alpha1.Server, error) {
	selector, err := metav1.LabelSelectorAsSelector(&fw.Spec.ServerSelector)
	if err != nil {
		return nil, fmt.Errorf("invalid selector: %w", err)
	}
	list := &metalv1alpha1.ServerList{}
	if err := r.List(ctx, list, &client.ListOptions{LabelSelector: selector}); err != nil {
		return nil, fmt.Errorf("listing servers: %w", err)
	}
	return list.Items, nil
}

// nonLenovoServers returns the names of servers whose Status.Manufacturer is
// set but not "Lenovo". Servers whose Status.Manufacturer is empty are treated
// as "not yet known" (the metal-operator populates it asynchronously via
// Redfish inventory) and reported separately by unknownManufacturerServers so
// the controller can requeue instead of prematurely failing.
func nonLenovoServers(servers []metalv1alpha1.Server) []string {
	var out []string
	for _, s := range servers {
		if s.Status.Manufacturer == "" {
			continue
		}
		if !strings.EqualFold(s.Status.Manufacturer, string(bmc.ManufacturerLenovo)) {
			out = append(out, s.Name)
		}
	}
	return out
}

// unknownManufacturerServers returns servers whose Status.Manufacturer hasn't
// been populated yet. The controller requeues while any such server is in the
// selection.
func unknownManufacturerServers(servers []metalv1alpha1.Server) []string {
	var out []string
	for _, s := range servers {
		if s.Status.Manufacturer == "" {
			out = append(out, s.Name)
		}
	}
	return out
}

func (r *FirmwareUpdateLenovoReconciler) getLenovoClient(
	ctx context.Context, fw *vendorconsolev1alpha1.FirmwareUpdateLenovo,
) (*hwmgr.LenovoClient, error) {
	secret := &corev1.Secret{}
	if fw.Spec.SecretRef.Name == "" {
		return nil, fmt.Errorf("spec.secretRef.name is required")
	}
	if err := r.Get(ctx, client.ObjectKey{Name: fw.Spec.SecretRef.Name, Namespace: fw.Namespace}, secret); err != nil {
		return nil, fmt.Errorf("loading credential secret: %w", err)
	}
	username := string(secret.Data[vendorconsolev1alpha1.SecretUsernameKeyName])
	password := string(secret.Data[vendorconsolev1alpha1.SecretPasswordKeyName])
	token := string(secret.Data[vendorconsolev1alpha1.SecretTokenKeyName])
	if username == "" || password == "" {
		return nil, fmt.Errorf("credential secret %s missing username or password", fw.Spec.SecretRef.Name)
	}

	lx, err := hwmgr.NewLenovoClient(hwmgr.ClientOptions{
		Endpoint:           fw.Spec.LXCAURL,
		Username:           username,
		Password:           password,
		Token:              token,
		InsecureSkipVerify: true,
	})
	if err != nil {
		return nil, fmt.Errorf("creating Lenovo client: %w", err)
	}
	fresh, err := lx.GetAuthToken()
	if err != nil {
		return nil, fmt.Errorf("authenticating against LXCA: %w", err)
	}
	if fresh != token {
		base := secret.DeepCopy()
		if secret.Data == nil {
			secret.Data = map[string][]byte{}
		}
		secret.Data[vendorconsolev1alpha1.SecretTokenKeyName] = []byte(fresh)
		if err := r.Patch(ctx, secret, client.MergeFrom(base)); err != nil {
			return nil, fmt.Errorf("persisting refreshed LXCA token: %w", err)
		}
	}
	return lx, nil
}

// ensureRepositoryImport starts a firmware repository import if none has been
// initiated for this CR, then polls its status. Returns (requeue, err); when
// requeue is true and err is nil the caller should requeue after the resync
// interval.
func (r *FirmwareUpdateLenovoReconciler) ensureRepositoryImport(
	ctx context.Context,
	fw *vendorconsolev1alpha1.FirmwareUpdateLenovo,
	lx *hwmgr.LenovoClient,
) (bool, error) {
	if fw.Spec.FirmwarePayload.SourceType == vendorconsolev1alpha1.FirmwarePayloadSourceUpload {
		return false, fmt.Errorf("firmware payload sourceType=Upload is not implemented")
	}
	if fw.Spec.FirmwarePayload.SourceType != vendorconsolev1alpha1.FirmwarePayloadSourceURL {
		return false, fmt.Errorf("unsupported firmware payload sourceType %q", fw.Spec.FirmwarePayload.SourceType)
	}

	if fw.Status.RepositoryJobID == "" {
		jobID, err := lx.ImportFirmwarePayload(fw.Spec.FirmwarePayload.URL, fw.Spec.FirmwarePayload.Checksum)
		if err != nil {
			return false, err
		}
		if _, err := r.patchStatus(ctx, fw, func(s *vendorconsolev1alpha1.FirmwareUpdateLenovoStatus) {
			// If LXCA didn't hand us a job id, mark the field as "started"
			// so we don't kick off the import a second time on the next
			// reconcile; GetRepositoryStatus will drive us forward from
			// there.
			if jobID == "" {
				jobID = "-"
			}
			s.RepositoryJobID = jobID
		}); err != nil {
			return false, err
		}
		return true, nil
	}

	// Repository imports are singletons on LXCA — we probe the status
	// endpoint rather than the (potentially fabricated) task id.
	info, err := lx.GetRepositoryStatus()
	if err != nil {
		return false, err
	}
	switch hwmgr.FirmwareJobStatus(info.Status) {
	case hwmgr.FirmwareJobStatusFailed:
		return false, fmt.Errorf("LXCA repository import failed: %s", info.Message)
	case hwmgr.FirmwareJobStatusSuccess:
		return false, nil
	default:
		return true, nil
	}
}

func (r *FirmwareUpdateLenovoReconciler) ensureCompliancePolicy(
	ctx context.Context,
	fw *vendorconsolev1alpha1.FirmwareUpdateLenovo,
	lx *hwmgr.LenovoClient,
) (string, error) {
	if fw.Status.CompliancePolicyID != "" {
		return fw.Status.CompliancePolicyID, nil
	}
	policies, err := lx.ListCompliancePolicies()
	if err != nil {
		return "", err
	}
	for _, p := range policies {
		if p.Name == fw.Spec.CompliancePolicy.Name {
			id := p.ID
			if id == "" {
				id = p.Name
			}
			if _, err := r.patchStatus(ctx, fw, func(s *vendorconsolev1alpha1.FirmwareUpdateLenovoStatus) {
				s.CompliancePolicyID = id
			}); err != nil {
				return "", err
			}
			return id, nil
		}
	}
	id, err := lx.CreateCompliancePolicy(fw.Spec.CompliancePolicy.Name, fw.Spec.CompliancePolicy.Description)
	if err != nil {
		return "", err
	}
	if _, err := r.patchStatus(ctx, fw, func(s *vendorconsolev1alpha1.FirmwareUpdateLenovoStatus) {
		s.CompliancePolicyID = id
	}); err != nil {
		return "", err
	}
	return id, nil
}

// resolveDeviceUUIDs maps each Server to its LXCA node UUID. Servers that
// aren't yet managed by LXCA are returned in `missing`.
func (r *FirmwareUpdateLenovoReconciler) resolveDeviceUUIDs(
	ctx context.Context, lx *hwmgr.LenovoClient, servers []metalv1alpha1.Server,
) (uuids []string, missing []string, err error) {
	nodes, err := lx.ListServers()
	if err != nil {
		return nil, nil, fmt.Errorf("listing LXCA nodes: %w", err)
	}
	byHost := make(map[string]string, len(nodes))
	for _, n := range nodes {
		byHost[strings.ToLower(n.Hostname)] = n.UUID
		byHost[strings.ToLower(n.Name)] = n.UUID
	}
	for _, s := range servers {
		hostname, err := r.hostnameForServer(ctx, &s)
		if err != nil || hostname == "" {
			missing = append(missing, s.Name)
			continue
		}
		uuid, ok := byHost[strings.ToLower(hostname)]
		if !ok || uuid == "" {
			missing = append(missing, s.Name)
			continue
		}
		uuids = append(uuids, uuid)
	}
	return uuids, missing, nil
}

func (r *FirmwareUpdateLenovoReconciler) hostnameForServer(
	ctx context.Context, s *metalv1alpha1.Server,
) (string, error) {
	if s.Spec.BMCRef == nil || s.Spec.BMCRef.Name == "" {
		return "", nil
	}
	metalBmc := &metalv1alpha1.BMC{}
	if err := r.Get(ctx, client.ObjectKey{Name: s.Spec.BMCRef.Name, Namespace: s.Namespace}, metalBmc); err != nil {
		return "", err
	}
	if metalBmc.Spec.Hostname == nil {
		return "", nil
	}
	return *metalBmc.Spec.Hostname, nil
}

// ensureMaintenance creates a ServerMaintenance per Server if not present
// and returns true when all of them report InMaintenance.
func (r *FirmwareUpdateLenovoReconciler) ensureMaintenance(
	ctx context.Context,
	fw *vendorconsolev1alpha1.FirmwareUpdateLenovo,
	servers []metalv1alpha1.Server,
) (bool, error) {
	policy := fw.Spec.ServerMaintenancePolicy
	if policy == "" {
		policy = metalv1alpha1.ServerMaintenancePolicyEnforced
	}

	existing := map[string]vendorconsolev1alpha1.MaintenanceRef{}
	for _, ref := range fw.Status.MaintenanceRefs {
		existing[ref.ServerName] = ref
	}

	newRefs := make([]vendorconsolev1alpha1.MaintenanceRef, 0, len(servers))
	ready := true
	for _, s := range servers {
		ref, ok := existing[s.Name]
		var sm *metalv1alpha1.ServerMaintenance
		if ok {
			sm = &metalv1alpha1.ServerMaintenance{}
			err := r.Get(ctx, client.ObjectKey{Name: ref.MaintenanceName, Namespace: fw.Namespace}, sm)
			if err != nil {
				if !apierrors.IsNotFound(err) {
					return false, fmt.Errorf("fetching ServerMaintenance %s: %w", ref.MaintenanceName, err)
				}
				sm = nil
			}
		}
		if sm == nil {
			created, err := r.createServerMaintenance(ctx, fw, &s, policy)
			if err != nil {
				return false, err
			}
			sm = created
			ref = vendorconsolev1alpha1.MaintenanceRef{
				ServerName:      s.Name,
				MaintenanceName: created.Name,
			}
			ready = false
		}
		newRefs = append(newRefs, ref)

		switch sm.Status.State {
		case metalv1alpha1.ServerMaintenanceStateInMaintenance:
			// good
		case metalv1alpha1.ServerMaintenanceStateFailed:
			return false, fmt.Errorf("ServerMaintenance %s for %s reported Failed", sm.Name, s.Name)
		default:
			ready = false
		}
	}

	if !sameRefs(fw.Status.MaintenanceRefs, newRefs) {
		if _, err := r.patchStatus(ctx, fw, func(s *vendorconsolev1alpha1.FirmwareUpdateLenovoStatus) {
			s.MaintenanceRefs = newRefs
		}); err != nil {
			return false, err
		}
	}
	return ready, nil
}

func sameRefs(a, b []vendorconsolev1alpha1.MaintenanceRef) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (r *FirmwareUpdateLenovoReconciler) createServerMaintenance(
	ctx context.Context,
	fw *vendorconsolev1alpha1.FirmwareUpdateLenovo,
	server *metalv1alpha1.Server,
	policy metalv1alpha1.ServerMaintenancePolicy,
) (*metalv1alpha1.ServerMaintenance, error) {
	sm := &metalv1alpha1.ServerMaintenance{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "lenovo-firmware-",
			Namespace:    fw.Namespace,
			Annotations: map[string]string{
				metalv1alpha1.ServerMaintenanceReasonAnnotationKey: fmt.Sprintf("Lenovo firmware update by FirmwareUpdateLenovo/%s", fw.Name),
			},
		},
		Spec: metalv1alpha1.ServerMaintenanceSpec{
			Policy:      policy,
			ServerRef:   &corev1.LocalObjectReference{Name: server.Name},
			ServerPower: "On",
		},
	}
	if err := controllerutil.SetControllerReference(fw, sm, r.Scheme); err != nil {
		return nil, fmt.Errorf("setting owner ref on ServerMaintenance: %w", err)
	}
	if err := r.Create(ctx, sm); err != nil {
		return nil, fmt.Errorf("creating ServerMaintenance for server %s: %w", server.Name, err)
	}
	return sm, nil
}

// cleanupMaintenance removes ServerMaintenance children this CR created.
func (r *FirmwareUpdateLenovoReconciler) cleanupMaintenance(
	ctx context.Context, fw *vendorconsolev1alpha1.FirmwareUpdateLenovo,
) error {
	var errs []error
	for _, ref := range fw.Status.MaintenanceRefs {
		sm := &metalv1alpha1.ServerMaintenance{
			ObjectMeta: metav1.ObjectMeta{Name: ref.MaintenanceName, Namespace: fw.Namespace},
		}
		if err := r.Delete(ctx, sm); err != nil && !apierrors.IsNotFound(err) {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	if len(fw.Status.MaintenanceRefs) > 0 {
		if _, err := r.patchStatus(ctx, fw, func(s *vendorconsolev1alpha1.FirmwareUpdateLenovoStatus) {
			s.MaintenanceRefs = nil
		}); err != nil {
			return err
		}
	}
	return nil
}

// upsertCondition inserts or updates a metav1.Condition by type, preserving
// LastTransitionTime when Status stays the same.
func upsertCondition(conditions *[]metav1.Condition, cond metav1.Condition) {
	if cond.LastTransitionTime.IsZero() {
		cond.LastTransitionTime = metav1.Now()
	}
	if cond.ObservedGeneration == 0 {
		cond.ObservedGeneration = 1
	}
	for i, existing := range *conditions {
		if existing.Type == cond.Type {
			if existing.Status == cond.Status {
				cond.LastTransitionTime = existing.LastTransitionTime
			}
			(*conditions)[i] = cond
			return
		}
	}
	*conditions = append(*conditions, cond)
}

// enqueueFromServer requeues any FirmwareUpdateLenovo whose selector matches
// the changed Server.
func (r *FirmwareUpdateLenovoReconciler) enqueueFromServer(
	ctx context.Context, obj client.Object,
) []ctrl.Request {
	list := &vendorconsolev1alpha1.FirmwareUpdateLenovoList{}
	if err := r.List(ctx, list); err != nil {
		return nil
	}
	var requests []ctrl.Request
	for _, fw := range list.Items {
		selector, err := metav1.LabelSelectorAsSelector(&fw.Spec.ServerSelector)
		if err != nil {
			continue
		}
		if selector.Matches(labels.Set(obj.GetLabels())) {
			requests = append(requests, ctrl.Request{NamespacedName: client.ObjectKey{
				Name:      fw.Name,
				Namespace: fw.Namespace,
			}})
		}
	}
	return requests
}

// SetupWithManager sets up the controller with the Manager.
func (r *FirmwareUpdateLenovoReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&vendorconsolev1alpha1.FirmwareUpdateLenovo{}).
		Owns(&metalv1alpha1.ServerMaintenance{}).
		Watches(&metalv1alpha1.Server{},
			handler.EnqueueRequestsFromMapFunc(r.enqueueFromServer),
			builder.WithPredicates(predicate.LabelChangedPredicate{})).
		Named("firmwareupdatelenovo").
		Complete(r)
}
