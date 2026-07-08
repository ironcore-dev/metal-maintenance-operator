// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
//
// SPDX-License-Identifier: Apache-2.0

package vendorconsole

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"time"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/go-logr/logr"
	"github.com/ironcore-dev/controller-utils/clientutils"
	"github.com/ironcore-dev/controller-utils/conditionutils"
	maintenancev1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/vendorconsole/v1alpha1"
	"github.com/ironcore-dev/metal-maintenance-operator/internal/hwmgr"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	DellFirmwareUpdateFinalizer       = "vendorconsole.metal.ironcore.dev/firmwareupdatedell"
	FirmwareUpgradeCompletedCondition = "UpdateCompleted"
)

var errNoSecretData = errors.New("no secret data found for accessing OME console")

// FirmwareUpdateDELLReconciler reconciles a FirmwareUpdateDELL object
type FirmwareUpdateDELLReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	ManagerNamespace string
	OMEConfig        *hwmgr.MgrConfig
	ResyncInterval   time.Duration
}

type catalogTaskStruct struct {
	catalog             *hwmgr.DellCatalogDetails
	baselineTask        *baselineTaskStruct
	targets             []hwmgr.DellTarget
	jobCompletionStatus bool
}

type baselineTaskStruct struct {
	baseline            *hwmgr.DellBaseline
	selectedServers     []metalv1alpha1.Server
	jobCompletionStatus bool
}

type SelectedServerListChangedDuringUpdate struct {
	missingServersFromBaseline []string
	ServersInSpec              []string
}

func (e *SelectedServerListChangedDuringUpdate) Error() string {
	return fmt.Sprintf("selected server list changed during update: missing Server {%v}. servers selected from Spec {%v}", e.missingServersFromBaseline, e.ServersInSpec)
}

// +kubebuilder:rbac:groups=vendorconsole.metal.ironcore.dev,resources=firmwareupdatedells,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=vendorconsole.metal.ironcore.dev,resources=firmwareupdatedells/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=vendorconsole.metal.ironcore.dev,resources=firmwareupdatedells/finalizers,verbs=update
// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=servers,verbs=get;list;watch
// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=servermaintenances,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=servermaintenances/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;patch;watch;delete

func (r *FirmwareUpdateDELLReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	firmwareDell := &maintenancev1alpha1.FirmwareUpdateDELL{}
	if err := r.Get(ctx, req.NamespacedName, firmwareDell); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	log.V(1).Info("Reconciling FirmwareUpdateDELL")

	return r.reconcileExists(ctx, firmwareDell)
}

func (r *FirmwareUpdateDELLReconciler) reconcileExists(
	ctx context.Context,
	firmwareDell *maintenancev1alpha1.FirmwareUpdateDELL,
) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	// if object is being deleted - reconcile deletion
	if r.shouldDelete(log, firmwareDell) {
		log.V(1).Info("Object is being deleted")
		return r.delete(ctx, firmwareDell)
	}

	return r.reconcile(ctx, firmwareDell)
}

func (r *FirmwareUpdateDELLReconciler) shouldDelete(
	log logr.Logger,
	firmwareDell *maintenancev1alpha1.FirmwareUpdateDELL,
) bool {
	if firmwareDell.DeletionTimestamp.IsZero() {
		return false
	}
	if controllerutil.ContainsFinalizer(firmwareDell, DellFirmwareUpdateFinalizer) &&
		firmwareDell.Status.State == maintenancev1alpha1.FirmwareUpdateStateInProgress && firmwareDell.Status.UpdateTask != nil {
		log.V(1).Info("Postponing delete as firmware update is in progress")
		return false
	}
	return true
}

func (r *FirmwareUpdateDELLReconciler) delete(
	ctx context.Context,
	firmwareDell *maintenancev1alpha1.FirmwareUpdateDELL,
) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	consoleClient, err := r.getVendorConsoleClient(ctx, firmwareDell)
	if err != nil {
		if errors.Is(err, errNoSecretData) || apierrors.IsNotFound(err) {
			log.V(1).Info("Secret not found during delete, removing finalizer without closing OME session")
		} else {
			return ctrl.Result{}, err
		}
	}
	if consoleClient != nil {
		defer consoleClient.CloseSession(ctx) // nolint:errcheck
	}
	log.V(1).Info("Ensuring that the finalizer is removed")
	if modified, err := clientutils.PatchEnsureNoFinalizer(ctx, r.Client, firmwareDell, DellFirmwareUpdateFinalizer); err != nil || modified {
		return ctrl.Result{}, err
	}
	log.V(1).Info("FirmwareUpdateDELL resource is deleted")
	return ctrl.Result{}, nil
}

func (r *FirmwareUpdateDELLReconciler) cleanupServerMaintenanceReferences(
	ctx context.Context,
	firmwareDell *maintenancev1alpha1.FirmwareUpdateDELL,
) error {
	log := ctrl.LoggerFrom(ctx)
	if firmwareDell.Spec.ServerMaintenanceRefs == nil {
		return nil
	}
	// try to get the serverMaintenances created
	serverMaintenances, errs := r.getReferredServerMaintenances(ctx, firmwareDell.Spec.ServerMaintenanceRefs)

	var finalErr []error
	var missingServerMaintenanceRef []error

	if len(errs) > 0 {
		for _, err := range errs {
			if apierrors.IsNotFound(err) {
				missingServerMaintenanceRef = append(missingServerMaintenanceRef, err)
			} else {
				finalErr = append(finalErr, err)
			}
		}
	}

	if len(missingServerMaintenanceRef) != len(firmwareDell.Spec.ServerMaintenanceRefs) {
		// delete the serverMaintenance if not marked for deletion already
		for _, serverMaintenance := range serverMaintenances {
			if serverMaintenance.DeletionTimestamp.IsZero() && metav1.IsControlledBy(serverMaintenance, firmwareDell) {
				log.V(1).Info("Deleting server maintenance", "ServerMaintenance", serverMaintenance.Name, "State", serverMaintenance.Status.State)
				if err := r.Delete(ctx, serverMaintenance); err != nil {
					log.V(1).Info("Failed to delete server maintenance", "ServerMaintenance", serverMaintenance.Name, "error", err)
					finalErr = append(finalErr, err)
				}
			} else {
				log.V(1).Info(
					"ServerMaintenance not deleted",
					"ServerMaintenance", serverMaintenance.Name,
					"State", serverMaintenance.Status.State,
					"Owner", serverMaintenance.OwnerReferences,
				)
			}
		}
	}
	if len(finalErr) == 0 {
		// all serverMaintenance are deleted
		err := r.patchMaintenanceRequestRef(ctx, firmwareDell, nil)
		if err != nil {
			return fmt.Errorf("failed to clean up serverMaintenance ref in FirmwareUpdateDELL: %w", err)
		}
		log.V(1).Info("ServerMaintenance ref all cleaned up")
	}
	return errors.Join(finalErr...)
}

func (r *FirmwareUpdateDELLReconciler) reconcile(
	ctx context.Context,
	firmwareDell *maintenancev1alpha1.FirmwareUpdateDELL,
) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	if shouldIgnoreReconciliation(firmwareDell) {
		log.V(1).Info("Skipped FirmwareUpdateDELL reconciliation")
		return ctrl.Result{}, nil
	}
	if modified, err := clientutils.PatchEnsureFinalizer(ctx, r.Client, firmwareDell, DellFirmwareUpdateFinalizer); err != nil || modified {
		return ctrl.Result{}, err
	}
	return r.ensureStateTransition(ctx, firmwareDell)
}

func (r *FirmwareUpdateDELLReconciler) ensureStateTransition(
	ctx context.Context,
	firmwareDell *maintenancev1alpha1.FirmwareUpdateDELL,
) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	serverList, err := r.getServersBySelector(ctx, &firmwareDell.Spec.ServerSelector)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to get servers by selector: %w", err)
	}
	servers := r.verifyServersSelected(log, serverList)

	if len(servers) == 0 && firmwareDell.Status.State != maintenancev1alpha1.FirmwareUpdateStateCompleted {
		log.V(1).Info("No servers found matching the Spec's selector", "Selector", firmwareDell.Spec.ServerSelector)
		return ctrl.Result{}, nil
	}

	consoleClient, err := r.getVendorConsoleClient(ctx, firmwareDell)
	if err != nil {
		return ctrl.Result{}, err
	}

	switch firmwareDell.Status.State {
	case "", maintenancev1alpha1.FirmwareUpdateStatePending:
		return r.handlePendingState(ctx, firmwareDell, servers, serverList)
	case maintenancev1alpha1.FirmwareUpdateStateInProgress:
		return r.handleInProgressState(ctx, firmwareDell, servers, consoleClient)
	case maintenancev1alpha1.FirmwareUpdateStateCompleted:
		return r.handleCompletedState(ctx, firmwareDell, servers, consoleClient)
	case maintenancev1alpha1.FirmwareUpdateStateFailed:
		err = consoleClient.CloseSession(ctx)
		if err != nil {
			log.V(1).Info("Failed to close OME session", "error", err)
		}
		return r.handleFailedState(ctx, firmwareDell)
	}
	return ctrl.Result{}, nil
}

func (r *FirmwareUpdateDELLReconciler) handlePendingState(
	ctx context.Context,
	firmwareDell *maintenancev1alpha1.FirmwareUpdateDELL,
	servers []metalv1alpha1.Server,
	serverList *metalv1alpha1.ServerList,
) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	log.V(1).Info("Reconciling Pending state")
	if len(servers) == 0 {
		log.V(1).Info("No servers found matching the Spec's selector", "Selector", firmwareDell.Spec.ServerSelector)
		err := r.updateStatus(ctx, firmwareDell, maintenancev1alpha1.FirmwareUpdateStateCompleted, int32(len(servers)), firmwareDell.Status.UpdateTask)
		return ctrl.Result{}, err
	}

	if len(servers) != len(serverList.Items) {
		log.V(1).Info("Some servers are not Dell", "TotalServers", len(serverList.Items))
		err := r.updateStatus(ctx, firmwareDell, maintenancev1alpha1.FirmwareUpdateStateFailed, int32(len(servers)), firmwareDell.Status.UpdateTask)
		return ctrl.Result{}, err
	}
	err := r.updateStatus(ctx, firmwareDell, maintenancev1alpha1.FirmwareUpdateStateInProgress, int32(len(servers)), firmwareDell.Status.UpdateTask)
	return ctrl.Result{}, err
}

func (r *FirmwareUpdateDELLReconciler) handleInProgressState(
	ctx context.Context,
	firmwareDell *maintenancev1alpha1.FirmwareUpdateDELL,
	servers []metalv1alpha1.Server,
	consoleClient *hwmgr.DellClient,
) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	log.V(1).Info("Reconciling InProgress state")

	result, err := r.handlePreUpdateTasks(ctx, firmwareDell, servers, consoleClient)
	if err != nil {
		var serverListChangedErr *SelectedServerListChangedDuringUpdate
		if errors.As(err, &serverListChangedErr) {
			log.Error(err, "Server list changed during update, moving to failed state")
			err := r.updateStatus(ctx, firmwareDell, maintenancev1alpha1.FirmwareUpdateStateFailed, firmwareDell.Status.ServerCount, firmwareDell.Status.UpdateTask)
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, err
	}
	// wait for the catalog/baseline job to complete
	if !result.jobCompletionStatus || !result.baselineTask.jobCompletionStatus {
		return ctrl.Result{RequeueAfter: r.ResyncInterval}, nil
	}
	log.V(1).Info("Catalog and baseline job completed")
	// if no servers needs upgrade, mark the update as completed
	if len(result.targets) == 0 {
		log.V(1).Info("All devices Compliant, no further firmware upgrade needed")
		err := r.updateStatus(ctx, firmwareDell, maintenancev1alpha1.FirmwareUpdateStateCompleted, firmwareDell.Status.ServerCount, firmwareDell.Status.UpdateTask)
		return ctrl.Result{}, err
	}
	log.V(1).Info("Needs upgrade on some devices", "TargetNonComplient", result.targets)
	if len(result.baselineTask.selectedServers) != len(servers) {
		log.V(1).Info("Some servers have been updated, while firmware update in progress. Ignoring them", "TotalServers", len(servers), "FoundInOME", len(result.baselineTask.selectedServers))
		if len(result.baselineTask.selectedServers) != int(firmwareDell.Status.ServerCount) {
			return ctrl.Result{}, r.updateStatus(ctx, firmwareDell, firmwareDell.Status.State, int32(len(result.baselineTask.selectedServers)), firmwareDell.Status.UpdateTask)
		}
	}

	// if server needs upgrade, request maintenance on those servers alone
	// ensure the serverMaintenance are created for the selected servers
	if len(firmwareDell.Spec.ServerMaintenanceRefs) != len(result.baselineTask.selectedServers) {
		log.V(1).Info("Not all servers have Maintenance", "ServerMaintenanceRefs", firmwareDell.Spec.ServerMaintenanceRefs, "Servers", result.baselineTask.selectedServers)
		if requeue, err := r.requestMaintenanceOnServers(ctx, firmwareDell, result.baselineTask.selectedServers); err != nil || requeue {
			return ctrl.Result{}, err
		}
	}

	// check if the maintenance is granted
	state := r.getMaintenanceState(ctx, firmwareDell, result.baselineTask.selectedServers)
	if state == metalv1alpha1.ServerMaintenanceStateFailed {
		log.V(1).Info("Some servers' maintenance request has been denied, moving to failed state")
		return ctrl.Result{}, r.updateStatus(ctx, firmwareDell, maintenancev1alpha1.FirmwareUpdateStateFailed, firmwareDell.Status.ServerCount, firmwareDell.Status.UpdateTask)
	}
	if state != metalv1alpha1.ServerMaintenanceStateInMaintenance {
		log.V(1).Info("Waiting for maintenance to be granted before continuing with updating firmware versions", "state", state)
		return ctrl.Result{}, err
	}

	if firmwareDell.Status.UpdateTask == nil {
		jobDetails, err := r.performFirmwareUpdate(ctx, firmwareDell, consoleClient, result.baselineTask.baseline, result.catalog, result.targets)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to trigger firmware update: %w", err)
		}
		err = r.updateStatus(ctx, firmwareDell, firmwareDell.Status.State, firmwareDell.Status.ServerCount, &maintenancev1alpha1.DellJob{Id: jobDetails.Id, Name: jobDetails.JobName})
		return ctrl.Result{}, err
	}

	jobId := firmwareDell.Status.UpdateTask.Id

	jobDetails, err := consoleClient.GetJobDetails(ctx, jobId)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to get job details for TaskId %d with error %w", jobId, err)
	}
	if slices.Contains(hwmgr.JobStatusFailed, jobDetails.Status.JobStatusID) {
		log.V(1).Info("Firmware Update job has failed", "jobDetails", jobDetails, "Status", jobDetails.Status.JobStatus)
		return ctrl.Result{}, r.updateStatus(ctx, firmwareDell, maintenancev1alpha1.FirmwareUpdateStateFailed, firmwareDell.Status.ServerCount, firmwareDell.Status.UpdateTask)
	}
	if hwmgr.JobStatusSuccess != jobDetails.Status.JobStatusID {
		log.V(1).Info("Firmware Update job not yet completed, will check again later", "jobDetails", jobDetails)
		return ctrl.Result{RequeueAfter: r.ResyncInterval}, nil
	}

	acc := conditionutils.NewAccessor(conditionutils.AccessorOptions{})
	condition := &metav1.Condition{}
	condFound, err := acc.FindSlice(firmwareDell.Status.Conditions, FirmwareUpgradeCompletedCondition, condition)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to fetch Condition %s. error: %v", FirmwareUpgradeCompletedCondition, err)
	}

	if !condFound || condition.Status != metav1.ConditionTrue {
		log.V(1).Info("Refreshing the baseline to get the latest compliance status", "Baseline TaskID", result.baselineTask.baseline.TaskId)
		err = consoleClient.RunJobNow(ctx, []int{result.baselineTask.baseline.TaskId})
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to check for Baseline Refresh job completion: %w", err)
		}
		condition.Type = FirmwareUpgradeCompletedCondition
		if err := acc.Update(
			condition,
			conditionutils.UpdateStatus(corev1.ConditionTrue),
			conditionutils.UpdateReason("FirmwareUpgradeCompletedCondition"),
			conditionutils.UpdateMessage("Firmware Upgrade is completed. Triggered refresh of compliance report."),
		); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to update starting setting update condition: %w", err)
		}
		err = r.updateConditions(ctx, firmwareDell, condition)
		return ctrl.Result{}, err
	}
	completed, err := r.checkJobCompletion(ctx, consoleClient, result.baselineTask.baseline.TaskId)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to check for Baseline Refresh job completion: %w", err)
	}
	if !completed {
		log.V(1).Info("Baseline refresh job Rerun not yet completed, will check again later", "TaskId", result.baselineTask.baseline.TaskId)
		return ctrl.Result{RequeueAfter: r.ResyncInterval}, nil
	}
	log.V(1).Info("Task is completed", "firmware", jobDetails)
	err = r.updateStatus(ctx, firmwareDell, maintenancev1alpha1.FirmwareUpdateStateCompleted, firmwareDell.Status.ServerCount, firmwareDell.Status.UpdateTask)
	return ctrl.Result{}, err
}

func (r *FirmwareUpdateDELLReconciler) handleCompletedState(
	ctx context.Context,
	firmwareDell *maintenancev1alpha1.FirmwareUpdateDELL,
	servers []metalv1alpha1.Server,
	consoleClient *hwmgr.DellClient,
) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	log.V(1).Info("Reconciling Completed state")
	// check if any servers needs firmware upgrade
	result, err := r.handlePreUpdateTasks(ctx, firmwareDell, servers, consoleClient)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !result.jobCompletionStatus || !result.baselineTask.jobCompletionStatus {
		return ctrl.Result{RequeueAfter: r.ResyncInterval}, nil
	}
	if len(result.targets) > 0 {
		log.V(1).Info("Some devices are not Compliant, firmware upgrade needed")
		err = r.updateStatus(ctx, firmwareDell, maintenancev1alpha1.FirmwareUpdateStatePending, int32(len(servers)), nil)
		if err != nil {
			return ctrl.Result{}, err
		}
		err = r.updateConditions(ctx, firmwareDell, nil)
		return ctrl.Result{}, err
	}
	// cleanup the serverMaintenance references if any
	if err := r.cleanupServerMaintenanceReferences(ctx, firmwareDell); err != nil {
		log.Error(err, "failed to cleanup serverMaintenance references")
		return ctrl.Result{}, err
	}

	log.V(1).Info("FirmwareUpdateDELL reconciliation completed")
	err = consoleClient.CloseSession(ctx)
	if err != nil {
		log.V(1).Info("Failed to close OME session", "error", err)
	}
	return ctrl.Result{}, nil
}

func (r *FirmwareUpdateDELLReconciler) handleFailedState(
	ctx context.Context,
	firmwareDell *maintenancev1alpha1.FirmwareUpdateDELL,
) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	log.V(1).Info("Firmware Update has failed, manual intervention needed")
	if shouldRetryReconciliation(firmwareDell) {
		log.V(1).Info("Retrying Firmware Update reconciliation")
		firmwareDellBase := firmwareDell.DeepCopy()
		firmwareDell.Status.State = maintenancev1alpha1.FirmwareUpdateStatePending
		firmwareDell.Status.UpdateTask = nil
		firmwareDell.Status.Conditions = nil
		firmwareDell.Status.ServerCount = 0
		annotations := firmwareDell.GetAnnotations()
		delete(annotations, metalv1alpha1.OperationAnnotation)
		firmwareDell.SetAnnotations(annotations)
		if err := r.Status().Patch(ctx, firmwareDell, client.MergeFrom(firmwareDellBase)); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to patch FirmwareUpdateDELL status for retrying: %w", err)
		}
		return ctrl.Result{}, nil
	}
	return ctrl.Result{}, nil
}

func (r *FirmwareUpdateDELLReconciler) handlePreUpdateTasks(
	ctx context.Context,
	firmwareDell *maintenancev1alpha1.FirmwareUpdateDELL,
	servers []metalv1alpha1.Server,
	consoleClient *hwmgr.DellClient,
) (*catalogTaskStruct, error) {
	log := ctrl.LoggerFrom(ctx)
	log.V(1).Info("Handling pre-update tasks: catalog and baseline creation/checks")

	result := catalogTaskStruct{}
	var err error
	result.catalog, err = r.createOrGetCatalog(ctx, firmwareDell, consoleClient)
	if err != nil {
		return &result, fmt.Errorf("failed to create catalog: %w", err)
	}
	if result.catalog != nil && result.catalog.Id != 0 && (firmwareDell.Status.Catalog == nil || result.catalog.Id != firmwareDell.Status.Catalog.Id) {
		log.V(1).Info("Patching catalog details in status", "CatalogId", result.catalog.Id)
		firmwareDellBase := firmwareDell.DeepCopy()
		firmwareDell.Status.Catalog = &maintenancev1alpha1.DellCatalog{
			Id: result.catalog.Id,
		}
		if err := r.Status().Patch(ctx, firmwareDell, client.MergeFrom(firmwareDellBase)); err != nil {
			return &result, fmt.Errorf("failed to patch firmwareDell Catalog status: %w", err)
		}
		return &result, nil
	}
	result.jobCompletionStatus, err = r.checkJobCompletion(ctx, consoleClient, result.catalog.TaskId)
	if err != nil {
		return &result, fmt.Errorf("failed to check for catalog creation job completion: %w", err)
	}
	if !result.jobCompletionStatus {
		log.V(1).Info("Catalog creation job not yet completed, will check again later", "TaskId", result.catalog.TaskId, "result", &result)
		return &result, nil
	}
	log.V(1).Info("Catalog operation completed", "Catalog", result.catalog.Id, "Status", result.catalog.Status, "FileName", result.catalog.Filename)
	baseLineDetails, err := r.handleBaselineOperations(ctx, firmwareDell, consoleClient, result.catalog, servers)
	result.baselineTask = baseLineDetails
	if err != nil {
		return &result, fmt.Errorf("failed to create or patch or get baseline: %w", err)
	}
	if baseLineDetails != nil && baseLineDetails.baseline.Id != 0 && (firmwareDell.Status.Baseline == nil || baseLineDetails.baseline.Id != firmwareDell.Status.Baseline.Id) {
		log.V(1).Info("Patching basline details in status", "BaselineId", baseLineDetails.baseline.Id)
		firmwareDellBase := firmwareDell.DeepCopy()
		firmwareDell.Status.Baseline = &maintenancev1alpha1.DellBaseline{
			Id: baseLineDetails.baseline.Id,
		}
		if err := r.Status().Patch(ctx, firmwareDell, client.MergeFrom(firmwareDellBase)); err != nil {
			return &result, fmt.Errorf("failed to patch firmwareDell Baseline status: %w", err)
		}
		return &result, nil
	}
	result.baselineTask.jobCompletionStatus, err = r.checkJobCompletion(ctx, consoleClient, baseLineDetails.baseline.TaskId)
	if err != nil {
		return &result, fmt.Errorf("failed to check for Baseline creation job completion: %w", err)
	}
	if !result.baselineTask.jobCompletionStatus {
		log.V(1).Info("Baseline creation job not yet completed, will check again later", "TaskId", baseLineDetails.baseline.TaskId, "result", result)
		return &result, nil
	}
	log.V(1).Info("Baseline operation completed", "Baseline ID", baseLineDetails.baseline.Id, "Status", baseLineDetails.jobCompletionStatus, "BaselineName", baseLineDetails.baseline.Name)
	result.targets, err = r.checkComplianceReport(ctx, consoleClient, baseLineDetails.baseline)
	if err != nil {
		return &result, fmt.Errorf("failed to get compliance report: %w", err)
	}
	return &result, nil
}

func (r *FirmwareUpdateDELLReconciler) verifyServersSelected(
	log logr.Logger,
	serverList *metalv1alpha1.ServerList,
) []metalv1alpha1.Server {
	if serverList == nil || len(serverList.Items) == 0 {
		log.V(1).Info("No servers found matching the Spec's selector")
		return nil
	}
	servers := make([]metalv1alpha1.Server, 0)
	var nonDellServer []string
	for _, server := range serverList.Items {
		if server.Status.Manufacturer != string(hwmgr.ManufacturerDell) {
			log.V(1).Info("Skipping server as it is not Dell", "Server", server.Name, "Manufacturer", server.Status.Manufacturer)
			nonDellServer = append(nonDellServer, server.Name)
			continue
		}
		servers = append(servers, server)
	}
	if len(servers) != len(serverList.Items) {
		log.V(1).Info("Some servers are not Dell, ignoring them", "TotalServers", len(serverList.Items), "Not DellServers", nonDellServer)
	}
	return servers
}

func (r *FirmwareUpdateDELLReconciler) requestMaintenanceOnServers(
	ctx context.Context,
	firmwareDell *maintenancev1alpha1.FirmwareUpdateDELL,
	servers []metalv1alpha1.Server,
) (bool, error) {
	log := ctrl.LoggerFrom(ctx)

	// if Server maintenance ref is already given. no further action required.
	if firmwareDell.Spec.ServerMaintenanceRefs != nil && len(firmwareDell.Spec.ServerMaintenanceRefs) == len(servers) {
		return false, nil
	}

	// if user gave some server with serverMaintenance but not all
	// we want to request maintenance for the missing servers only.
	// find the servers which has maintenance and do not create maintenance for them.
	serverWithMaintenances := make(map[string]bool, len(servers))
	if firmwareDell.Spec.ServerMaintenanceRefs != nil {
		// we fetch all the references already in the Spec (self created/provided by user)
		serverMaintenances, err := r.getReferredServerMaintenances(ctx, firmwareDell.Spec.ServerMaintenanceRefs)
		if err != nil {
			return false, errors.Join(err...)
		}
		for _, serverMaintenance := range serverMaintenances {
			serverWithMaintenances[serverMaintenance.Spec.ServerRef.Name] = true
		}
	}

	// we also fetch all the references owned by this Resource.
	// This is needed in case we are reconciling before we have patched the references.
	// possible when we reconcile after CreateOrPatch, before ref have been written
	serverMaintenancesList := &metalv1alpha1.ServerMaintenanceList{}
	if err := clientutils.ListAndFilterControlledBy(ctx, r.Client, firmwareDell, serverMaintenancesList); err != nil {
		return false, err
	}
	for _, serverMaintenance := range serverMaintenancesList.Items {
		serverWithMaintenances[serverMaintenance.Spec.ServerRef.Name] = true
	}

	var errs []error
	serverMaintenanceRefs := make([]metalv1alpha1.ServerMaintenanceRefItem, 0, len(servers))
	for _, server := range servers {
		if _, ok := serverWithMaintenances[server.Name]; ok {
			continue
		}
		serverMaintenance := &metalv1alpha1.ServerMaintenance{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:    r.ManagerNamespace,
				GenerateName: "dell-ome-maintenance-",
			},
		}

		opResult, err := controllerutil.CreateOrPatch(ctx, r.Client, serverMaintenance, func() error {
			serverMaintenance.Spec.Policy = firmwareDell.Spec.ServerMaintenancePolicy
			serverMaintenance.Spec.ServerPower = metalv1alpha1.PowerOn
			serverMaintenance.Spec.ServerRef = &corev1.LocalObjectReference{Name: server.Name}
			if serverMaintenance.Status.State != metalv1alpha1.ServerMaintenanceStateInMaintenance && serverMaintenance.Status.State != "" {
				serverMaintenance.Status.State = ""
			}
			return controllerutil.SetControllerReference(firmwareDell, serverMaintenance, r.Client.Scheme())
		})
		if err != nil {
			log.Error(err, "failed to create or patch serverMaintenance", "Server", server.Name)
			errs = append(errs, err)
			continue
		}
		log.V(1).Info("Created serverMaintenance", "ServerMaintenance", serverMaintenance.Name, "ServerMaintenance label", serverMaintenance.Labels, "Operation", opResult)

		serverMaintenanceRefs = append(
			serverMaintenanceRefs,
			metalv1alpha1.ServerMaintenanceRefItem{
				ServerMaintenanceRef: &metalv1alpha1.ObjectReference{
					Namespace: serverMaintenance.Namespace,
					Name:      serverMaintenance.Name,
				}})
	}

	if len(errs) > 0 {
		return false, errors.Join(errs...)
	}

	err := r.patchMaintenanceRequestRef(ctx, firmwareDell, serverMaintenanceRefs)
	if err != nil {
		return false, fmt.Errorf("failed to patch serverMaintenance ref on FirmwareUpdateDELL %w", err)
	}

	log.V(1).Info("Patched serverMaintenanceMap on FirmwareUpdateDELL")

	return true, nil
}

func (r *FirmwareUpdateDELLReconciler) getMaintenanceState(
	ctx context.Context,
	firmwareDell *maintenancev1alpha1.FirmwareUpdateDELL,
	servers []metalv1alpha1.Server,
) metalv1alpha1.ServerMaintenanceState {
	log := ctrl.LoggerFrom(ctx)
	if firmwareDell.Spec.ServerMaintenanceRefs == nil {
		return ""
	}

	if len(firmwareDell.Spec.ServerMaintenanceRefs) != len(servers) {
		log.V(1).Info("Not all servers have Maintenance", "ServerMaintenanceRefs", firmwareDell.Spec.ServerMaintenanceRefs, "Servers", servers)
		return ""
	}

	serverMaintenances, errs := r.getReferredServerMaintenances(ctx, firmwareDell.Spec.ServerMaintenanceRefs)
	if errs != nil {
		log.Error(errors.Join(errs...), "failed to get referred serverMaintenances")
		return ""
	}
	notInMaintenanceState := make(map[string]metalv1alpha1.ServerMaintenanceState)
	for _, maintenance := range serverMaintenances {
		if maintenance.Status.State == metalv1alpha1.ServerMaintenanceStateFailed {
			// fail immediately if any of the server maintenance request failed, as we can not proceed further
			log.V(1).Info("ServerMaintenance in failed state", "Server", maintenance.Spec.ServerRef.Name, "ServerMaintenance", maintenance.Name)
			return metalv1alpha1.ServerMaintenanceStateFailed
		}
		// this gives us the waiting time for the server to be prepared for maintenance by ServerMaintenance controller
		if maintenance.Status.State != metalv1alpha1.ServerMaintenanceStateInMaintenance {
			log.V(1).Info("ServerMaintenance not yet in maintenance state", "Server", maintenance.Spec.ServerRef.Name, "ServerMaintenance", maintenance.Name, "State", maintenance.Status.State)
			notInMaintenanceState[maintenance.Spec.ServerRef.Name] = maintenance.Status.State
		}
	}
	if len(notInMaintenanceState) > 0 {
		log.V(1).Info("Some serverMaintenances not yet in maintenance", "req Servermaintenances", firmwareDell.Spec.ServerMaintenanceRefs)
		return metalv1alpha1.ServerMaintenanceStatePending
	}

	serverNotInMaintenenaceState := make(map[string]metalv1alpha1.ServerState)
	for _, server := range servers {
		if server.Status.State == metalv1alpha1.ServerStateMaintenance {
			serverMaintenanceRef, ok := r.getServerMaintenanceRefForServer(firmwareDell.Spec.ServerMaintenanceRefs, server.Spec.ServerMaintenanceRef.Name)
			if server.Spec.ServerMaintenanceRef == nil || !ok || server.Spec.ServerMaintenanceRef.Name != serverMaintenanceRef.Name {
				// server in maintenance for other tasks. or
				// server maintenance ref is wrong in either server or firmwareDell Spec
				// wait for update on the server obj
				log.V(1).Info("Server is already in maintenance for other tasks",
					"Server", server.Name,
					"ServerMaintenanceRef on Server", server.Spec.ServerMaintenanceRef,
					"ServerMaintenanceRef on FirmwareUpdateDELL", serverMaintenanceRef,
				)
				serverNotInMaintenenaceState[server.Name] = server.Status.State
			}
		} else {
			// we still need to wait for server to enter maintenance
			// wait for update on the server obj
			log.V(1).Info("Server not yet in maintenance", "Server", server.Name, "State", server.Status.State, "MaintenanceRef", server.Spec.ServerMaintenanceRef)
			serverNotInMaintenenaceState[server.Name] = server.Status.State
		}
	}

	if len(serverNotInMaintenenaceState) > 0 {
		log.V(1).Info("Some servers not yet in maintenance", "req Servermaintenances on servers", firmwareDell.Spec.ServerMaintenanceRefs)
		return metalv1alpha1.ServerMaintenanceStatePending
	}

	return metalv1alpha1.ServerMaintenanceStateInMaintenance
}

func (r *FirmwareUpdateDELLReconciler) checkJobCompletion(
	ctx context.Context,
	console *hwmgr.DellClient,
	TaskId int,
) (bool, error) {
	jobDetails, err := console.GetJobDetails(ctx, TaskId)
	if err != nil {
		return false, fmt.Errorf("failed to get job details for TaskId %d with error %w", TaskId, err)
	}

	if jobDetails.Status.JobStatusID == hwmgr.JobStatusSuccess {
		return true, nil
	}

	return false, nil
}

func (r *FirmwareUpdateDELLReconciler) getServerMaintenanceRefForServer(
	serverMaintenanceRefs []metalv1alpha1.ServerMaintenanceRefItem,
	serverMaintenanceName string,
) (*metalv1alpha1.ObjectReference, bool) {
	for _, serverMaintenanceRef := range serverMaintenanceRefs {
		if serverMaintenanceRef.ServerMaintenanceRef.Name == serverMaintenanceName {
			return serverMaintenanceRef.ServerMaintenanceRef, true
		}
	}
	return nil, false
}

func (r *FirmwareUpdateDELLReconciler) getReferredSecretData(
	ctx context.Context,
	secretRef *corev1.LocalObjectReference,
) (map[string]string, error) {
	secret, err := r.getReferredSecret(ctx, secretRef)
	if err != nil {
		return nil, err
	}
	if len(secret.Data) == 0 {
		return secret.StringData, nil
	}
	stringData := make(map[string]string, len(secret.Data))
	for k, v := range secret.Data {
		stringData[k] = string(v)
	}
	return stringData, nil
}

func (r *FirmwareUpdateDELLReconciler) getReferredSecret(
	ctx context.Context,
	secretRef *corev1.LocalObjectReference,
) (*corev1.Secret, error) {
	if secretRef == nil {
		return nil, nil
	}
	key := client.ObjectKey{Name: secretRef.Name, Namespace: r.ManagerNamespace}
	secret := &corev1.Secret{}
	if err := r.Get(ctx, key, secret); err != nil {
		log := ctrl.LoggerFrom(ctx)
		log.Error(err, "failed to get referred Secret obj", "secret name", secretRef.Name)
		return nil, err
	}
	return secret, nil
}

func (r *FirmwareUpdateDELLReconciler) getServersBySelector(
	ctx context.Context,
	selector *metav1.LabelSelector,
) (*metalv1alpha1.ServerList, error) {
	labelSelector, err := metav1.LabelSelectorAsSelector(selector)
	if err != nil {
		return nil, err
	}
	serverList := &metalv1alpha1.ServerList{}
	if err := r.List(ctx, serverList, client.MatchingLabelsSelector{Selector: labelSelector}); err != nil {
		return nil, err
	}
	return serverList, nil
}

func (r *FirmwareUpdateDELLReconciler) getReferredServerMaintenances(
	ctx context.Context,
	serverMaintenanceRefs []metalv1alpha1.ServerMaintenanceRefItem,
) ([]*metalv1alpha1.ServerMaintenance, []error) {
	log := ctrl.LoggerFrom(ctx)
	serverMaintenances := make([]*metalv1alpha1.ServerMaintenance, 0, len(serverMaintenanceRefs))
	var errs []error
	cnt := 0
	for _, serverMaintenanceRef := range serverMaintenanceRefs {
		key := client.ObjectKey{Name: serverMaintenanceRef.ServerMaintenanceRef.Name, Namespace: r.ManagerNamespace}
		serverMaintenance := &metalv1alpha1.ServerMaintenance{}
		if err := r.Get(ctx, key, serverMaintenance); err != nil {
			log.Error(err, "failed to get referred serverMaintenance obj", "serverMaintenance", serverMaintenanceRef.ServerMaintenanceRef.Name)
			errs = append(errs, err)
			continue
		}
		serverMaintenances = append(serverMaintenances, serverMaintenance)
		cnt = cnt + 1
	}

	if len(errs) > 0 {
		return serverMaintenances, errs
	}

	return serverMaintenances, nil
}

func (r *FirmwareUpdateDELLReconciler) patchMaintenanceRequestRef(
	ctx context.Context,
	firmwareDell *maintenancev1alpha1.FirmwareUpdateDELL,
	serverMaintenanceRefs []metalv1alpha1.ServerMaintenanceRefItem,
) error {
	log := ctrl.LoggerFrom(ctx)
	firmwareDellBase := firmwareDell.DeepCopy()

	if serverMaintenanceRefs == nil {
		firmwareDell.Spec.ServerMaintenanceRefs = nil
	} else {
		firmwareDell.Spec.ServerMaintenanceRefs = serverMaintenanceRefs
	}

	if err := r.Patch(ctx, firmwareDell, client.MergeFrom(firmwareDellBase)); err != nil {
		log.Error(err, "failed to patch FirmwareUpdateDELL with ServerMaintenances ref")
		return err
	}

	return nil
}

func existingBaseline(
	ctx context.Context,
	firmwareDell *maintenancev1alpha1.FirmwareUpdateDELL,
	console *hwmgr.DellClient,
	catalog *hwmgr.DellCatalogDetails,
	baseline hwmgr.DellBaseline,
	servers []metalv1alpha1.Server,
) (*baselineTaskStruct, error) {
	log := ctrl.LoggerFrom(ctx)
	// function to check if existing baseline needs to be patched or is upto date with servers provided
	result := &baselineTaskStruct{}
	result.baseline = &baseline
	result.selectedServers = servers
	// get the devices from OME for the servers provided in spec
	devices, err := getDevicesFromServers(ctx, console, servers)
	if err != nil {
		log.Error(err, "failed to get devices from OME for servers selected")
		return result, err
	}
	equal := slices.EqualFunc(devices, baseline.Targets, func(a hwmgr.DellDeviceData, b hwmgr.DellTarget) bool {
		return a.Id == b.Id
	})

	if equal {
		log.V(1).Info("Baseline targets match the servers selected, reusing existing baseline", "Baseline", baseline.Id, "Name", baseline.Name)
		return result, nil
	}

	if firmwareDell.Status.UpdateTask != nil && firmwareDell.Status.State == maintenancev1alpha1.FirmwareUpdateStateInProgress {
		log.V(1).Info("Baseline is in use with active update InProgress, cannot patch the baseline", "Baseline", baseline.Id, "Name", baseline.Name)

		// filter out the servers based on the devices in the baseline
		currentServers := make([]metalv1alpha1.Server, 0, len(devices))
		removedServers := make([]string, 0)

		// map of server SKU to server obj for quick lookup
		serverSKUMap := make(map[string]metalv1alpha1.Server, len(servers))
		for _, server := range servers {
			serverSKUMap[server.Status.SKU] = server
		}

		// map of device ID to device obj (from servers provided in spec) for quick lookup
		devicesIdMap := make(map[int]hwmgr.DellDeviceData, len(baseline.Targets))
		for _, device := range devices {
			devicesIdMap[device.Id] = device
		}
		// add only the devices which are in the selected server spec & in the baseline
		// this is to handle the case where the server spec has changed and some servers are removed
		// but the baseline is still in use by those servers and firmware update has been started.
		// we will rack subset of server which are in the selected server spec and in the baseline
		for _, baselineDevice := range baseline.Targets {
			// device in baseline target is not in the servers listed through spec
			if device, ok := devicesIdMap[baselineDevice.Id]; !ok {
				log.V(1).Info("Device in baseline target is not in the servers selected from spec and firmware upgrade is already in progress", "DeviceId", baselineDevice.Id)
				// this device has been removed from the selected Server Spec
				// TODO: should we move to failed state if servers are removed?
				removedServers = append(removedServers, fmt.Sprintf("device {%s}, with deviceID {%v} removed", device.Name, device.Id))
				continue
			} else {
				// this device is still in the selected server Spec
				// add the server to current servers
				if server, ok := serverSKUMap[device.SKU]; ok {
					currentServers = append(currentServers, server)
				}
			}
		}
		if len(removedServers) > 0 {
			log.V(1).Info("Some devices in the baseline target are not in the servers selected and firmware upgrade is already in progress", "RemovedDevices", removedServers)
			log.V(1).Info("This will cause some servers not part of the selected server spec to be upgraded to this firmware")
			serverInSpec := make([]string, 0, len(servers))
			for _, srv := range servers {
				serverInSpec = append(serverInSpec, srv.Name)
			}
			return nil, &SelectedServerListChangedDuringUpdate{
				missingServersFromBaseline: removedServers,
				ServersInSpec:              serverInSpec,
			}
		}
		log.V(1).Info("Firmware Upgrade of server is in Progress, some server from spec are ignored until previous upgrade is completed", "Servers in Spec", servers, "firmware uprgade inProgress servers", currentServers)
		result.selectedServers = currentServers
		return result, nil
	}

	payload := createBaselinePayload(log, firmwareDell, catalog, devices)
	log.V(1).Info("Baseline targets do not match the servers selected, patching the baseline", "Baseline", baseline.Id, "Name", baseline.Name)
	PatchedBaseline, err := console.UpdateBaseline(ctx, baseline.Id, payload)
	result.baseline = PatchedBaseline
	result.selectedServers = servers
	return result, err
}

// function to create baseline payload from devices
// used to create or patch baseline on OME
func createBaselinePayload(
	log logr.Logger,
	firmwareDell *maintenancev1alpha1.FirmwareUpdateDELL,
	catalog *hwmgr.DellCatalogDetails,
	devices []hwmgr.DellDeviceData,
) *hwmgr.DellBaseline {
	if len(devices) == 0 {
		return nil
	}
	payload := &hwmgr.DellBaseline{
		Name:             firmwareDell.Spec.BaselineConfig.Name,
		Description:      firmwareDell.Spec.BaselineConfig.Description,
		Is64Bit:          firmwareDell.Spec.BaselineConfig.BitType == maintenancev1alpha1.BitType64,
		RepositoryId:     catalog.Repository.Id,
		CatalogId:        catalog.Id,
		DowngradeEnabled: firmwareDell.Spec.BaselineConfig.DowngradeEnabled == maintenancev1alpha1.DowngradableUpdate,
		Targets:          nil,
	}
	for _, device := range devices {
		log.V(1).Info("Creating baseline payload", "device", device, "payload", payload)
		payload.Targets = append(payload.Targets, hwmgr.DellTarget{
			Id: device.Id,
			Type: &hwmgr.DellTargetType{
				Id:   device.Type,
				Name: hwmgr.DeviceTypeMap[device.Type],
			},
			TargetType: nil,
		})
		log.V(1).Info("Added device to baseline payload", "payload", payload)
	}
	return payload
}

// function to get devices (OME device list) from Server Resource
func getDevicesFromServers(
	ctx context.Context,
	console *hwmgr.DellClient,
	servers []metalv1alpha1.Server,
) ([]hwmgr.DellDeviceData, error) {
	serverSKU := make([]string, 0, len(servers))
	for _, server := range servers {
		serverSKU = append(serverSKU, server.Status.SKU)
	}
	if len(serverSKU) == 0 {
		return nil, fmt.Errorf("no servers found to get devices from OME")
	}
	devices, err := console.GetDevicesFromSKU(ctx, serverSKU)
	if err != nil {
		return nil, fmt.Errorf("failed to get devices from OMe for SKU %v, error %w", serverSKU, err)
	}
	return devices, nil
}

func (r *FirmwareUpdateDELLReconciler) handleBaselineOperations(
	ctx context.Context,
	firmwareDell *maintenancev1alpha1.FirmwareUpdateDELL,
	console *hwmgr.DellClient,
	catalog *hwmgr.DellCatalogDetails,
	servers []metalv1alpha1.Server,
) (*baselineTaskStruct, error) {
	log := ctrl.LoggerFrom(ctx)
	result := &baselineTaskStruct{}

	baselineList, err := console.GetAllBaseline(ctx)
	if err != nil {
		log.Error(err, "failed to get baselines from OME")
		return result, err
	}
	existingBaselineOme := &hwmgr.DellBaseline{}
	// check for existing baseline to reuse
	for _, baseline := range baselineList {
		if firmwareDell.Status.Baseline != nil && baseline.Id == firmwareDell.Status.Baseline.Id {
			// previously created baseline, return it
			log.V(1).Info("Found previously created baseline on OME", "Baseline", baseline.Id)
			existingBaselineOme = &baseline
			// proceed to check if the previous created baseline matches the paraters in spec
		}
		if baseline.Name == firmwareDell.Spec.BaselineConfig.Name &&
			firmwareDell.Spec.BaselineConfig.Name != "" &&
			catalog.Id != 0 && baseline.CatalogId == catalog.Id &&
			catalog.Repository.Id != 0 && baseline.RepositoryId == catalog.Repository.Id {
			log.V(1).Info("Found existing baseline on OME", "Baseline", baseline.Id, "Name", baseline.Name)
			return existingBaseline(ctx, firmwareDell, console, catalog, baseline, servers)
		}
		if existingBaselineOme != nil && existingBaselineOme.Id != 0 {
			return nil, fmt.Errorf("baselines found on OME not matching provided spec, cannot proceed. all baselines: %v \n selected baseline %v", baselineList, existingBaselineOme)
		}
	}

	log.V(1).Info("No existing baseline found on OME, creating new baseline", "baselineList", baselineList)
	// create new baseline
	devices, err := getDevicesFromServers(ctx, console, servers)
	if err != nil {
		log.Error(err, "failed to get devices from OME for servers selected")
		return result, err
	}
	payload := createBaselinePayload(log, firmwareDell, catalog, devices)
	log.V(1).Info("Creating new baseline on OME", "Payload", payload)
	dellBaselineDetails, err := console.CreateBaseline(ctx, payload)
	if err != nil {
		log.Error(err, "failed to create baseline on OME")
		return result, err
	}
	result.baseline = dellBaselineDetails
	result.selectedServers = servers
	log.V(1).Info("Created baseline on OME", "Baseline", dellBaselineDetails)
	return result, nil
}

func (r *FirmwareUpdateDELLReconciler) createOrGetCatalog(
	ctx context.Context,
	firmwareDell *maintenancev1alpha1.FirmwareUpdateDELL,
	console *hwmgr.DellClient,
) (*hwmgr.DellCatalogDetails, error) {
	log := ctrl.LoggerFrom(ctx)
	catalogList, err := console.GetAllCatalogs(ctx)
	if err != nil {
		log.Error(err, "failed to get catalogs from OME")
		return nil, err
	}

	log.V(1).Info("Checking for existing catalogs on OME", "TotalCatalogs", catalogList)
	existingCatalog := &hwmgr.DellCatalogDetails{}
	for _, catalog := range catalogList {
		if firmwareDell.Status.Catalog != nil && catalog.Id == firmwareDell.Status.Catalog.Id {
			// previously created catalog, return it
			log.V(1).Info("Found previously created catalog on OME", "Catalog", catalog.Id)
			existingCatalog = &catalog
			// proceed to check if the previous created catalog matches the paraters in spec
		}
		// if user has provided catalogRepositoryName, use that to find the catalog
		if catalog.Repository.Name == firmwareDell.Spec.CatalogRepositoryName && firmwareDell.Spec.CatalogRepositoryName != "" {
			log.V(1).Info("Found existing catalog on OME matching the catalogName in spec", "Catalog", catalog.Id, "Name", catalog.Repository.Name)
			existingCatalog = &catalog
			break
		}
		// if user has provided createCatalog spec, use that to find the catalog if controller has already created it
		if firmwareDell.Spec.CreateCatalog != nil &&
			catalog.Repository.Name == firmwareDell.Spec.CreateCatalog.Repository.Name &&
			catalog.Filename == firmwareDell.Spec.CreateCatalog.FileName &&
			catalog.SourcePath == firmwareDell.Spec.CreateCatalog.SourcePath &&
			catalog.Repository.Source == firmwareDell.Spec.CreateCatalog.Repository.Source {
			log.V(1).Info("Found existing catalog on OME matching the created catalog spec", "Catalog", catalog.Id, "Name", catalog.Repository.Name)
			existingCatalog = &catalog
			break
		}
		if firmwareDell.Spec.CreateCatalog != nil && catalog.Repository.Name == firmwareDell.Spec.CreateCatalog.Repository.Name {
			log.V(1).Info("Duplicate catalog repository Name. can not create new catalog with same name and different parameters", "Catalog", catalog.Id, "catalog", catalog)
			return nil, fmt.Errorf("catalog RepositoryName %s already exists on OME with different parameters", firmwareDell.Spec.CreateCatalog.Repository.Name)
		}
		if existingCatalog != nil && existingCatalog.Id != 0 {
			return nil, fmt.Errorf("catalogs found on OME not matching the provided spec, cannot proceed. all catalogs: %v \n created catalog %v", catalogList, existingCatalog)
		}
	}

	if existingCatalog != nil && existingCatalog.Id != 0 {
		completed, err := r.checkJobCompletion(ctx, console, existingCatalog.TaskId)
		if err != nil {
			log.Error(err, "failed to check job completion for existing catalog", "Catalog", existingCatalog.Id)
			return nil, err
		}
		if !completed {
			log.V(1).Info("Refreshing catalog as the job has not completed")
			err = console.RefreshCatalog(ctx, []int{existingCatalog.Id})
		} else {
			log.V(1).Info("Skipping catalog refresh as its previously been completed", "UpdateTask", firmwareDell.Status.UpdateTask)
		}
		// check and refresh catalog if needed
		return existingCatalog, err
	}

	// if user has provided catalogRepositoryName, and we did not find it, return error
	if firmwareDell.Spec.CatalogRepositoryName != "" {
		return nil, fmt.Errorf("catalog RepositoryName %s not found on OME", firmwareDell.Spec.CatalogRepositoryName)
	}

	// if user has not provided createCatalog spec, return error as we do not know what catalog to create
	if firmwareDell.Spec.CreateCatalog == nil {
		return nil, fmt.Errorf("createCatalog Spec Not Provided to create catalog on OME")
	}

	payload := &hwmgr.DellCatalogDetails{
		Filename:   firmwareDell.Spec.CreateCatalog.FileName,
		SourcePath: firmwareDell.Spec.CreateCatalog.SourcePath,
		Repository: hwmgr.DellCatalogRepository{
			Name:             firmwareDell.Spec.CreateCatalog.Repository.Name,
			Description:      firmwareDell.Spec.CreateCatalog.Repository.Description,
			Source:           firmwareDell.Spec.CreateCatalog.Repository.Source,
			DomainName:       firmwareDell.Spec.CreateCatalog.Repository.DomainName,
			Username:         firmwareDell.Spec.CreateCatalog.Repository.Username,
			Password:         firmwareDell.Spec.CreateCatalog.Repository.Password,
			CheckCertificate: firmwareDell.Spec.CreateCatalog.Repository.CheckCertificate == maintenancev1alpha1.CheckCertificateHTTPS,
			RepositoryType:   firmwareDell.Spec.CreateCatalog.Repository.RepositoryType,
		},
	}
	dellCatalogDetails, err := console.CreateCatalog(ctx, payload)
	if err != nil {
		log.Error(err, "failed to create catalog on OME")
		return nil, err
	}
	log.V(1).Info("Created catalog on OME", "Catalog", dellCatalogDetails)
	return dellCatalogDetails, err
}

func (r *FirmwareUpdateDELLReconciler) checkComplianceReport(
	ctx context.Context,
	console *hwmgr.DellClient,
	baseline *hwmgr.DellBaseline,
) ([]hwmgr.DellTarget, error) {
	log := ctrl.LoggerFrom(ctx)
	complianceReports, err := console.GetComplianceReportForBaseline(ctx, baseline.Id)
	if err != nil {
		log.Error(err, "failed to get compliance reports from OME")
		return nil, err
	}
	devicesIdBaselineMap := make(map[int]*hwmgr.DellTargetType, len(baseline.Targets))
	for _, target := range baseline.Targets {
		devicesIdBaselineMap[target.Id] = target.Type
	}
	dellTarget := make([]hwmgr.DellTarget, 0)
	for _, complianceReport := range complianceReports {
		currentSources := ""
		componentNames := []string{}
		for _, componentReport := range complianceReport.ComponentComplianceReports {
			// if the componnent is already compliant or unknown, skip
			if componentReport.UpdateAction != "EQUAL" && componentReport.UpdateAction != "UNKNOWN" {
				if currentSources == "" {
					currentSources = componentReport.SourceName
				} else {
					currentSources = currentSources + ";" + componentReport.SourceName
				}
				componentNames = append(componentNames, componentReport.Name)
			}
		}
		if currentSources == "" {
			// all components are compliant, skip this device
			continue
		}
		log.V(1).Info("Component needs update", "Components", componentNames, "DeviceId", complianceReport.DeviceId)
		currentTarget := hwmgr.DellTarget{
			Id: complianceReport.DeviceId,
			TargetType: &hwmgr.DellTargetType{
				Id:   devicesIdBaselineMap[complianceReport.DeviceId].Id,
				Name: devicesIdBaselineMap[complianceReport.DeviceId].Name,
			},
			Data: currentSources,
		}
		dellTarget = append(dellTarget, currentTarget)
	}

	return dellTarget, nil
}

func (r *FirmwareUpdateDELLReconciler) performFirmwareUpdate(
	ctx context.Context,
	firmwareDell *maintenancev1alpha1.FirmwareUpdateDELL,
	console *hwmgr.DellClient,
	baseline *hwmgr.DellBaseline,
	catalog *hwmgr.DellCatalogDetails,
	target []hwmgr.DellTarget,
) (*hwmgr.DellJob, error) {
	log := ctrl.LoggerFrom(ctx)
	if len(target) == 0 {
		log.V(1).Info("No target devices need firmware update")
		return nil, nil
	}

	payload := &hwmgr.DellFirmwareUpdatePayload{
		JobName:        firmwareDell.Name + "-firmware-update",
		JobDescription: "Firmware update job created by FirmwareUpdateDELL " + firmwareDell.Name,
		Targets:        target,
		Schedule:       "StartNow",
		State:          "Enabled",
		JobType: hwmgr.DellJobType{
			JobTypeID: hwmgr.JobTypeMap[firmwareDell.Spec.FirmwareUpgradeConfig.JobTypeName],
			JobType:   firmwareDell.Spec.FirmwareUpgradeConfig.JobTypeName,
		},
		Params: []hwmgr.DellParams{
			{
				Key:   "rebootType",
				Value: "3",
			},
			{
				Key:   "complianceReportId",
				Value: strconv.Itoa(baseline.Id),
			},
			{
				Key:   "repositoryId",
				Value: strconv.Itoa(catalog.Repository.Id),
			},
			{
				Key:   "catalogId",
				Value: strconv.Itoa(catalog.Id),
			},
			{
				Key:   "operationName",
				Value: firmwareDell.Spec.FirmwareUpgradeConfig.OperationName,
			},
			{
				Key:   "complianceUpdate",
				Value: strconv.FormatBool(firmwareDell.Spec.FirmwareUpgradeConfig.ComplianceUpdate == maintenancev1alpha1.ComplianceUpdate),
			},
			{
				Key:   "signVerify",
				Value: strconv.FormatBool(firmwareDell.Spec.FirmwareUpgradeConfig.SignVerify == maintenancev1alpha1.SignVerify),
			},
			{
				Key:   "stagingValue",
				Value: strconv.FormatBool(firmwareDell.Spec.FirmwareUpgradeConfig.StagingValue == maintenancev1alpha1.StagingFirmwareStaged),
			},
		},
	}
	log.V(1).Info("Creating Firmware Update Job on OME", "Payload", payload)
	return console.CreateFirmwareUpdateJob(ctx, payload)
}

func (r *FirmwareUpdateDELLReconciler) updateConditions(
	ctx context.Context,
	firmwareDell *maintenancev1alpha1.FirmwareUpdateDELL,
	condition *metav1.Condition,
) error {
	log := ctrl.LoggerFrom(ctx)
	firmwareDellBase := firmwareDell.DeepCopy()

	if condition != nil {
		log.V(1).Info("Updating with condition", "Condition", condition)
		acc := conditionutils.NewAccessor(conditionutils.AccessorOptions{})
		if err := acc.UpdateSlice(
			&firmwareDell.Status.Conditions,
			condition.Type,
			conditionutils.UpdateStatus(condition.Status),
			conditionutils.UpdateReason(condition.Reason),
			conditionutils.UpdateMessage(condition.Message),
		); err != nil {
			return fmt.Errorf("failed to patch firmwareDell condition: %w", err)
		}
	} else {
		firmwareDell.Status.Conditions = nil
	}
	log.V(1).Info("Updating FirmwareUpdateDELL conditions", "Conditions", firmwareDell.Status.Conditions)
	if err := r.Status().Patch(ctx, firmwareDell, client.MergeFrom(firmwareDellBase)); err != nil {
		return fmt.Errorf("failed to patch firmwareDell status: %w", err)
	}

	log.V(1).Info("Updated FirmwareUpdateDELL state ",
		"Conditions", firmwareDell.Status.Conditions,
	)
	return nil
}

func (r *FirmwareUpdateDELLReconciler) updateStatus(
	ctx context.Context,
	firmwareDell *maintenancev1alpha1.FirmwareUpdateDELL,
	state maintenancev1alpha1.FirmwareUpdateState,
	serverCount int32,
	upgradeTask *maintenancev1alpha1.DellJob,
) error {
	log := ctrl.LoggerFrom(ctx)
	if firmwareDell.Status.State == state && upgradeTask == nil && firmwareDell.Status.ServerCount == serverCount {
		return nil
	}

	firmwareDellBase := firmwareDell.DeepCopy()
	firmwareDell.Status.State = state
	firmwareDell.Status.UpdateTask = upgradeTask
	firmwareDell.Status.ServerCount = serverCount

	if err := r.Status().Patch(ctx, firmwareDell, client.MergeFrom(firmwareDellBase)); err != nil {
		return fmt.Errorf("failed to patch firmwareDell status: %w", err)
	}

	log.V(1).Info("Updated FirmwareUpdateDELL state ",
		"State", state,
		"Upgrade Task", firmwareDell.Status.UpdateTask,
	)

	return nil
}

func (r *FirmwareUpdateDELLReconciler) enqueueByServerRefs(
	ctx context.Context,
	obj client.Object,
) []ctrl.Request {
	log := ctrl.LoggerFrom(ctx)
	host := obj.(*metalv1alpha1.Server)

	firmwareDellList := &maintenancev1alpha1.FirmwareUpdateDELLList{}
	if err := r.List(ctx, firmwareDellList); err != nil {
		log.Error(err, "failed to list FirmwareUpdateDELL")
		return nil
	}
	reqs := make([]ctrl.Request, 0)
	for _, firmwareDell := range firmwareDellList.Items {
		selector, err := metav1.LabelSelectorAsSelector(&firmwareDell.Spec.ServerSelector)
		if err != nil {
			log.Error(err, "failed to convert label selector")
			return nil
		}
		log.V(1).Info("Checking if server matches the selector", "Server", host.Name, "Selector", selector, "host.GetLabels()", host.GetLabels())
		// if the host label matches the selector, enqueue the request
		if selector.Matches(labels.Set(host.GetLabels())) {
			reqs = append(reqs, ctrl.Request{
				NamespacedName: client.ObjectKey{
					Name:      firmwareDell.Name,
					Namespace: firmwareDell.Namespace,
				},
			})
			continue
		} else {
			// handle the case when the lable was deleted or changed and host no longer matches the selector
			// if we dont have maintenance request on this firmwareDell we do not want to queue changes from servers.
			if firmwareDell.Spec.ServerMaintenanceRefs == nil {
				continue
			}

			if firmwareDell.Status.State == maintenancev1alpha1.FirmwareUpdateStateCompleted || firmwareDell.Status.State == maintenancev1alpha1.FirmwareUpdateStateFailed {
				continue
			}
		}

		serverMaintenanceRef, ok := r.getServerMaintenanceRefForServer(firmwareDell.Spec.ServerMaintenanceRefs, host.Spec.ServerMaintenanceRef.Name)
		if ok && serverMaintenanceRef != nil {
			reqs = append(reqs, ctrl.Request{
				NamespacedName: types.NamespacedName{Namespace: firmwareDell.Namespace, Name: firmwareDell.Name},
			})
		}
	}
	return reqs
}

func (r *FirmwareUpdateDELLReconciler) getVendorConsoleClient(
	ctx context.Context,
	firmwareDell *maintenancev1alpha1.FirmwareUpdateDELL,
) (*hwmgr.DellClient, error) {
	log := ctrl.LoggerFrom(ctx)
	omeURL, err := url.Parse(firmwareDell.Spec.OMEURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse OME URL: %v error %w", firmwareDell.Spec.OMEURL, err)
	}
	// Create session with Dell OME
	config := &hwmgr.MgrConfig{
		URL:                 omeURL,
		InsecureSkipVerify:  r.OMEConfig.InsecureSkipVerify,
		TLSHandshakeTimeout: r.OMEConfig.TLSHandshakeTimeout,
		ReuseConnections:    r.OMEConfig.ReuseConnections,
	}

	omeSecret, err := r.getReferredSecretData(ctx, firmwareDell.Spec.SecretRef)
	if err != nil {
		return nil, fmt.Errorf("failed to get referred secret: %w", err)
	}
	if len(omeSecret) == 0 {
		log.V(1).Info("No secret found for accessing OME console", "Missing SecretRef", firmwareDell.Spec.SecretRef.Name)
		return nil, errNoSecretData
	}
	authTkn := &hwmgr.AuthToken{
		Username:  omeSecret[maintenancev1alpha1.SecretUsernameKeyName],
		Password:  omeSecret[maintenancev1alpha1.SecretPasswordKeyName],
		Token:     omeSecret[maintenancev1alpha1.SecretTokenKeyName],
		Session:   omeSecret[maintenancev1alpha1.SecretSessionKeyName],
		SessionId: omeSecret[maintenancev1alpha1.SecretSessionIDKeyName],
		AuthType:  hwmgr.DellToken,
	}
	consoleClient, err := hwmgr.GetDellConsole(ctx, config, authTkn)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to DELL OME at: %v, error: %w", firmwareDell.Spec.OMEURL, err)
	}
	// if the token has been refreshed, update the secret
	if omeSecret[maintenancev1alpha1.SecretTokenKeyName] != consoleClient.Client.Auth.Token ||
		omeSecret[maintenancev1alpha1.SecretSessionIDKeyName] != consoleClient.Client.Auth.SessionId {
		log.V(1).Info("Updating secret with new OME token")
		secret, err := r.getReferredSecret(ctx, firmwareDell.Spec.SecretRef)
		if err != nil {
			return nil, fmt.Errorf("failed to get referred secret: %w", err)
		}
		secretBase := secret.DeepCopy()
		if secret.Data != nil {
			secret.Data[maintenancev1alpha1.SecretTokenKeyName] = []byte(consoleClient.Client.Auth.Token)
			secret.Data[maintenancev1alpha1.SecretSessionKeyName] = []byte(consoleClient.Client.Auth.Session)
			secret.Data[maintenancev1alpha1.SecretSessionIDKeyName] = []byte(consoleClient.Client.Auth.SessionId)
		} else {
			secret.StringData[maintenancev1alpha1.SecretTokenKeyName] = consoleClient.Client.Auth.Token
			secret.StringData[maintenancev1alpha1.SecretSessionKeyName] = consoleClient.Client.Auth.Session
			secret.StringData[maintenancev1alpha1.SecretSessionIDKeyName] = consoleClient.Client.Auth.SessionId
		}
		if err := r.Patch(ctx, secret, client.MergeFrom(secretBase)); err != nil {
			return nil, fmt.Errorf("failed to update secret with new OME token: %w", err)
		}
		log.V(1).Info("Updated secret with new OME token")
	}
	return consoleClient, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *FirmwareUpdateDELLReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&maintenancev1alpha1.FirmwareUpdateDELL{}).
		Owns(&metalv1alpha1.ServerMaintenance{}).
		Watches(&metalv1alpha1.Server{},
			handler.EnqueueRequestsFromMapFunc(r.enqueueByServerRefs),
			builder.WithPredicates(predicate.LabelChangedPredicate{})).
		Complete(r)
}
