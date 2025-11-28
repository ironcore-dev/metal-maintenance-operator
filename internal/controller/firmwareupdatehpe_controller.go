// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"reflect"
	"slices"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	vendorconsolev1alpha1 "github.com/ironcore-dev/maintenance-operator/api/v1alpha1"
	vendorconsole "github.com/ironcore-dev/maintenance-operator/vendor-console"
	vendorClient "github.com/ironcore-dev/maintenance-operator/vendor-console/client"
	"github.com/ironcore-dev/maintenance-operator/vendor-console/ov"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
)

// FirmwareUpdateHPEReconciler reconciles a FirmwareUpdateHPE object
type FirmwareUpdateHPEReconciler struct {
	client.Client
	ManagerNamespace string
	ResyncInterval   time.Duration
	OVConfig         *vendorconsole.Config
	Scheme           *runtime.Scheme
}

const (
	HPEFirmwareUpdateFinalizer  = "vendorconsole.ironcore.dev/firmwareupdatehpe"
	HPEFirmwareServerPoweredOff = "ServerPowerOffCompleted"
)

type OVFirmwareComplianceReport struct {
	report       *ov.HPEFirmwareComplianceReport
	SerialNumber string
	Server       *metalv1alpha1.Server
	OVServer     *ov.HPEServer
	Err          error
}

// +kubebuilder:rbac:groups=vendorconsole.ironcore.dev,resources=firmwareupdatehpes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=vendorconsole.ironcore.dev,resources=firmwareupdatehpes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=vendorconsole.ironcore.dev,resources=firmwareupdatehpes/finalizers,verbs=update
// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=servers,verbs=get;list;watch
// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=servermaintenances,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=servermaintenances/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;patch;watch;delete

func (r *FirmwareUpdateHPEReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	firmwareHPE := &vendorconsolev1alpha1.FirmwareUpdateHPE{}
	if err := r.Get(ctx, req.NamespacedName, firmwareHPE); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	log.V(1).Info("Reconciling FirmwareUpdateHPE")

	return r.reconcileExists(ctx, log, firmwareHPE)
}

func (r *FirmwareUpdateHPEReconciler) reconcileExists(
	ctx context.Context,
	log logr.Logger,
	firmwareHPE *vendorconsolev1alpha1.FirmwareUpdateHPE,
) (ctrl.Result, error) {
	// if object is being deleted - reconcile deletion
	if r.shouldDelete(log, firmwareHPE) {
		log.V(1).Info("Object is being deleted")
		return r.delete(ctx, log, firmwareHPE)
	}

	return r.reconcile(ctx, log, firmwareHPE)
}

func (r *FirmwareUpdateHPEReconciler) shouldDelete(
	log logr.Logger,
	firmwareHPE *vendorconsolev1alpha1.FirmwareUpdateHPE,
) bool {
	if firmwareHPE.DeletionTimestamp.IsZero() {
		return false
	}
	if controllerutil.ContainsFinalizer(firmwareHPE, HPEFirmwareUpdateFinalizer) &&
		firmwareHPE.Status.State == vendorconsolev1alpha1.FirmwareUpdateStateInProgress { // tdo: fix the condition
		log.V(1).Info("Postponing delete as firmware update is in progress")
		return false
	}
	return true
}

func (r *FirmwareUpdateHPEReconciler) delete(
	ctx context.Context,
	log logr.Logger,
	firmwareHPE *vendorconsolev1alpha1.FirmwareUpdateHPE,
) (ctrl.Result, error) {
	consoleClient, err := r.getVendorConcoleClient(ctx, log, firmwareHPE)
	if err != nil {
		return ctrl.Result{}, err
	}
	defer consoleClient.CloseSession(ctx) // nolint:errcheck
	log.V(1).Info("Ensuring that the finalizer is removed")
	if modified, err := clientutils.PatchEnsureNoFinalizer(ctx, r.Client, firmwareHPE, HPEFirmwareUpdateFinalizer); err != nil || modified {
		return ctrl.Result{}, err
	}
	log.V(1).Info("FirmwareUpdateHPE resource is deleted")
	return ctrl.Result{}, nil
}

func (r *FirmwareUpdateHPEReconciler) reconcile(
	ctx context.Context,
	log logr.Logger,
	firmwareHPE *vendorconsolev1alpha1.FirmwareUpdateHPE,
) (ctrl.Result, error) {
	if shouldIgnoreReconciliation(firmwareHPE) {
		log.V(1).Info("Skipped FirmwareUpdateHPE reconciliation")
		return ctrl.Result{}, nil
	}
	if modified, err := clientutils.PatchEnsureFinalizer(ctx, r.Client, firmwareHPE, HPEFirmwareUpdateFinalizer); err != nil || modified {
		return ctrl.Result{}, err
	}
	return r.ensureStateTransition(ctx, log, firmwareHPE)
}

func (r *FirmwareUpdateHPEReconciler) ensureStateTransition(
	ctx context.Context,
	log logr.Logger,
	firmwareHPE *vendorconsolev1alpha1.FirmwareUpdateHPE,
) (ctrl.Result, error) {
	serverList, err := r.getServersBySelector(ctx, &firmwareHPE.Spec.ServerSelector)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to get servers by selector: %w", err)
	}
	servers := r.verifyServersSelected(log, serverList)

	if len(servers) == 0 && firmwareHPE.Status.State != vendorconsolev1alpha1.FirmwareUpdateStateCompleted {
		log.V(1).Info("No HPE servers found matching the Spec's selector", "Selector", firmwareHPE.Spec.ServerSelector)
		return ctrl.Result{}, nil
	}

	if firmwareHPE.Status.ServerCount != int32(len(servers)) {
		log.V(1).Info("Server count has changed, updating status", "OldCount", firmwareHPE.Status.ServerCount, "NewCount", len(servers))
		firmwareHPEBase := firmwareHPE.DeepCopy()
		firmwareHPE.Status.ServerCount = int32(len(servers))
		if err := r.Status().Patch(ctx, firmwareHPE, client.MergeFrom(firmwareHPEBase)); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to patch firmwareHPE status: %w", err)
		}
		return ctrl.Result{}, nil
	}

	consoleClient, err := r.getVendorConcoleClient(ctx, log, firmwareHPE)
	if err != nil {
		return ctrl.Result{}, err
	}

	switch firmwareHPE.Status.State {
	case "", vendorconsolev1alpha1.FirmwareUpdateStatePending:
		return r.handlePendingState(ctx, log, firmwareHPE, servers, serverList)
	case vendorconsolev1alpha1.FirmwareUpdateStateInProgress:
		return r.handleInProgressState(ctx, log, firmwareHPE, servers, consoleClient)
	case vendorconsolev1alpha1.FirmwareUpdateStateCompleted:
		return r.handleCompletedState(ctx, log, firmwareHPE, servers, consoleClient)
	case vendorconsolev1alpha1.FirmwareUpdateStateFailed:
		err = consoleClient.CloseSession(ctx)
		if err != nil {
			log.V(1).Info("Failed to close OV session", "error", err)
		}
		return r.handleFailedState(ctx, log, firmwareHPE)
	}
	return ctrl.Result{}, nil
}

func (r *FirmwareUpdateHPEReconciler) handleInProgressState(
	ctx context.Context,
	log logr.Logger,
	firmwareHPE *vendorconsolev1alpha1.FirmwareUpdateHPE,
	servers []metalv1alpha1.Server,
	consoleClient *ov.OV,
) (ctrl.Result, error) {
	log.V(1).Info("Reconciling InProgress state")
	// check if the firmware update is needed by calling GetFirmwareComplianceReport
	nonComplientServers, err := r.checkFirmwareCompliance(ctx, log, firmwareHPE, servers, consoleClient)
	if err != nil {
		log.V(1).Error(err, "failed to check firmware compliance")
		return ctrl.Result{}, err
	}
	if len(nonComplientServers) == 0 {
		log.V(1).Info("All servers are firmware compliant")
		reconcile, err := r.handleInProgressStatusUpdate(ctx, log, firmwareHPE, firmwareHPE.Status.UpdateTask, len(nonComplientServers), int(firmwareHPE.Status.FailedServerCount))
		if err != nil {
			return ctrl.Result{}, err
		}
		if reconcile {
			log.V(1).Info("Requeuing to monitor firmware update task status")
			return ctrl.Result{RequeueAfter: r.ResyncInterval}, err
		}
		return ctrl.Result{}, err
	}
	nonComplientServersNames := make([]string, 0, len(nonComplientServers))
	for _, r := range nonComplientServers {
		nonComplientServersNames = append(nonComplientServersNames, r.Server.Name)
	}

	log.V(1).Info("Non compliant servers", "Server Name", nonComplientServersNames)
	// if the firmware update is needed, create/update the serverMaintenance references
	// if server needs upgrade, request maintenance on those servers alone
	// ensure the serverMaintenance are created for the selected servers
	if len(firmwareHPE.Spec.ServerMaintenanceRefs) != len(nonComplientServers) {
		log.V(1).Info("Not all servers have Maintenance object", "ServerMaintenanceRefs", firmwareHPE.Spec.ServerMaintenanceRefs, "Number of Servers", len(servers))
		if requeue, err := r.requestMaintenanceOnServers(ctx, log, firmwareHPE, nonComplientServers); err != nil || requeue {
			return ctrl.Result{}, err
		}
	}

	// check if the maintenance is granted
	// one the serverMaintenance references are created, monitor their status
	serverInMaintenance, failedServers := r.filterInMantenanceServers(ctx, log, firmwareHPE, nonComplientServers)
	if len(failedServers) > 0 {
		log.V(1).Info("some servers' maintenance request has been denied, will skip these servers", "FailedServers", failedServers)
	}
	if len((serverInMaintenance)) == 0 {
		log.V(1).Info("Waiting for maintenance to be granted before continuing with updating firmware versions")
		return ctrl.Result{}, nil
	}
	// once some serverMaintenance are inMaintenance, issue the FirmwareUpdate to for those servers
	updateStatus, err := r.performFirmwareUpdate(ctx, log, firmwareHPE, serverInMaintenance, consoleClient)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to trigger firmware update: %w", err)
	}
	// monitor the FirmwareUpdate task status until completed/failed
	reconcile, err := r.handleInProgressStatusUpdate(ctx, log, firmwareHPE, updateStatus, len(nonComplientServers), len(failedServers))
	if err != nil {
		return ctrl.Result{}, err
	}
	if reconcile {
		log.V(1).Info("Requeuing to monitor firmware update task status")
		return ctrl.Result{RequeueAfter: r.ResyncInterval}, nil
	}
	return ctrl.Result{}, nil
}

func (r *FirmwareUpdateHPEReconciler) handlePendingState(
	ctx context.Context,
	log logr.Logger,
	firmwareHPE *vendorconsolev1alpha1.FirmwareUpdateHPE,
	servers []metalv1alpha1.Server,
	serverList *metalv1alpha1.ServerList,
) (ctrl.Result, error) {
	log.V(1).Info("Reconciling Pending state")
	if len(servers) == 0 {
		log.V(1).Info("No servers found matching the Spec's selector", "Selector", firmwareHPE.Spec.ServerSelector)
		err := r.updateStatus(ctx, log, firmwareHPE, vendorconsolev1alpha1.FirmwareUpdateStateCompleted, firmwareHPE.Status.UpdateTask)
		return ctrl.Result{}, err
	}

	if len(servers) != len(serverList.Items) {
		log.V(1).Info("Some servers are not HPE", "TotalServers", len(serverList.Items))
		err := r.updateStatus(ctx, log, firmwareHPE, vendorconsolev1alpha1.FirmwareUpdateStateFailed, firmwareHPE.Status.UpdateTask)
		return ctrl.Result{}, err
	}
	err := r.updateStatus(ctx, log, firmwareHPE, vendorconsolev1alpha1.FirmwareUpdateStateInProgress, firmwareHPE.Status.UpdateTask)
	return ctrl.Result{}, err
}

func (r *FirmwareUpdateHPEReconciler) handleCompletedState(
	ctx context.Context,
	log logr.Logger,
	firmwareHPE *vendorconsolev1alpha1.FirmwareUpdateHPE,
	servers []metalv1alpha1.Server,
	consoleClient *ov.OV,
) (ctrl.Result, error) {
	log.V(1).Info("Reconciling Completed state")
	nonComplientServers, err := r.checkFirmwareCompliance(ctx, log, firmwareHPE, servers, consoleClient)
	if err != nil {
		log.V(1).Error(err, "failed to check firmware compliance")
		return ctrl.Result{}, err
	}
	if len(nonComplientServers) > 0 {
		log.V(1).Info("Some servers are not firmware compliant, transitioning to InProgress state", "NonCompliantServerCount", len(nonComplientServers))
		err := r.updateStatus(ctx, log, firmwareHPE, vendorconsolev1alpha1.FirmwareUpdateStateInProgress, nil)
		return ctrl.Result{}, err
	}
	// cleanup the serverMaintenance references if any
	if err := r.cleanupServerMaintenanceReferences(ctx, log, firmwareHPE); err != nil {
		log.V(1).Error(err, "failed to cleanup serverMaintenance references")
		return ctrl.Result{}, err
	}

	log.V(1).Info("FirmwareUpdateHPE reconciliation completed")
	err = consoleClient.CloseSession(ctx)
	if err != nil {
		log.V(1).Info("Failed to close OV session", "error", err)
	}
	return ctrl.Result{}, nil
}

func (r *FirmwareUpdateHPEReconciler) handleFailedState(
	ctx context.Context,
	log logr.Logger,
	firmwareHPE *vendorconsolev1alpha1.FirmwareUpdateHPE,
) (ctrl.Result, error) {
	log.V(1).Info("Firmware Update has failed, manual intervention needed")
	if shouldRetryReconciliation(firmwareHPE) {
		log.V(1).Info("Retrying Firmware Update reconciliation")
		firmwareHPEBase := firmwareHPE.DeepCopy()
		firmwareHPE.Status.State = vendorconsolev1alpha1.FirmwareUpdateStatePending
		// reset other status fields
		firmwareHPE.Status.Conditions = nil
		firmwareHPE.Status.ServerCount = 0
		annotations := firmwareHPE.GetAnnotations()
		delete(annotations, metalv1alpha1.OperationAnnotation)
		firmwareHPE.SetAnnotations(annotations)
		if err := r.Status().Patch(ctx, firmwareHPE, client.MergeFrom(firmwareHPEBase)); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to patch FirmwareUpdateHPE status for retrying: %w", err)
		}
		return ctrl.Result{}, nil
	}
	return ctrl.Result{}, nil
}

func (r *FirmwareUpdateHPEReconciler) checkFirmwareCompliance(
	ctx context.Context,
	log logr.Logger,
	firmwareHPE *vendorconsolev1alpha1.FirmwareUpdateHPE,
	servers []metalv1alpha1.Server,
	consoleClient *ov.OV,
) ([]OVFirmwareComplianceReport, error) {

	// get server UUIDS from ov based on server serial numbers
	serverSerialNumMap := make(map[string]*metalv1alpha1.Server, len(servers))
	for _, server := range servers {
		serverSerialNumMap[server.Status.SerialNumber] = &server
	}

	serverOV, err := consoleClient.GetServersFromSerialNumber(ctx, slices.Collect(maps.Keys(serverSerialNumMap)))
	if err != nil {
		return nil, fmt.Errorf("failed to get servers from OV by serial numbers: %w", err)
	}

	if len(serverOV) != len(servers) {
		log.V(1).Info("Some servers not found", "ExpectedCount", len(servers), "FoundCount", len(serverOV))
		return nil, fmt.Errorf("some servers not found in OV: expected %d, found %d", len(servers), len(serverOV))
	}

	// get the firmware UUID from the spec
	firmwareBundle, err := consoleClient.GetFirmwareBundleDetails(
		ctx,
		firmwareHPE.Spec.FirmwareBundle.Name,
		firmwareHPE.Spec.FirmwareBundle.Version,
		firmwareHPE.Spec.FirmwareBundle.UUID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get firmware bundle details: %w", err)
	}
	// get the firmware compliance report from ov
	// compare the report to check if the server is compliant
	// return the list of non-compliant servers

	complainceReport := make(chan OVFirmwareComplianceReport, len(serverOV))
	// function to handle graceful channel closing and concurrency
	func() {
		var wg sync.WaitGroup
		wg.Add(len(serverOV))
		defer close(complainceReport)
		for _, ovServer := range serverOV {
			go func(ovServer ov.HPEServer, wg *sync.WaitGroup) {
				defer wg.Done()
				report, err := consoleClient.GetFirmwareComplianceReport(ctx, ovServer.UUID, firmwareBundle.UUID)
				if err != nil {
					log.V(1).Error(err, "failed to get firmware compliance report", "SerialNumber", ovServer.SerialNumber, "ServerName", serverSerialNumMap[ovServer.SerialNumber].Name)
					complainceReport <- OVFirmwareComplianceReport{
						report:       report,
						SerialNumber: ovServer.SerialNumber,
						Server:       serverSerialNumMap[ovServer.SerialNumber],
						OVServer:     &ovServer,
						Err:          err,
					}
					return
				}
				complainceReport <- OVFirmwareComplianceReport{
					report:       report,
					SerialNumber: ovServer.SerialNumber,
					Server:       serverSerialNumMap[ovServer.SerialNumber],
					OVServer:     &ovServer,
					Err:          nil,
				}
			}(ovServer, &wg)
		}
		wg.Wait()
	}()

	errs := make([]error, 0)
	nonComplientServerReportList := make([]OVFirmwareComplianceReport, 0)
	for report := range complainceReport {
		if report.Err != nil {
			errs = append(errs, report.Err)
			continue
		}
		if report.report.ServerFirmwareUpdateRequired {
			log.V(1).Info("Server is NOT firmware compliant",
				"SerialNumber", report.SerialNumber,
				"ServerName", report.Server.Name)
			nonComplientServerReportList = append(nonComplientServerReportList, report)
		}
	}
	return nonComplientServerReportList, errors.Join(errs...)
}

func (r *FirmwareUpdateHPEReconciler) performFirmwareUpdate(
	ctx context.Context,
	log logr.Logger,
	firmwareHPE *vendorconsolev1alpha1.FirmwareUpdateHPE,
	serverReportList []OVFirmwareComplianceReport,
	consoleClient *ov.OV,
) ([]vendorconsolev1alpha1.HPEUpdateStatus, error) {
	if len(serverReportList) == 0 {
		log.V(1).Info("No servers require firmware update")
		return firmwareHPE.Status.UpdateTask, nil
	}
	// turn off the servers before performing firmware update
	currentStatusMap := make(map[string]vendorconsolev1alpha1.HPEUpdateStatus)
	if firmwareHPE.Status.UpdateTask != nil {
		for _, status := range firmwareHPE.Status.UpdateTask {
			currentStatusMap[status.ServerOVUUID] = status
		}
	}
	log.V(1).Info("Performing Firmware Update on selected servers", "ServerCount", len(serverReportList), "ServerOVUUID", maps.Keys(currentStatusMap))

	// todo: improve performance by fetching all server profiles only when required
	ServerProfileMap, err := r.getAllServerProfile(ctx, log, consoleClient, serverReportList)
	if err != nil {
		return firmwareHPE.Status.UpdateTask, fmt.Errorf("failed to get server profiles for servers: %w", err)
	}

	firmwareBundle, err := consoleClient.GetFirmwareBundleDetails(
		ctx,
		firmwareHPE.Spec.FirmwareBundle.Name,
		firmwareHPE.Spec.FirmwareBundle.Version,
		firmwareHPE.Spec.FirmwareBundle.UUID,
	)

	type tempFirmwareStatus struct {
		firmwareUpdateStatus *vendorconsolev1alpha1.HPEUpdateStatus
		err                  error
	}

	firmwareStatusChan := make(chan tempFirmwareStatus, len(serverReportList))

	func() {
		defer close(firmwareStatusChan)
		var wg sync.WaitGroup
		wg.Add(len(serverReportList))
		for _, serverReport := range serverReportList {
			go func(serverReport OVFirmwareComplianceReport, wg *sync.WaitGroup) {
				defer wg.Done()
				currentStatus, exists := currentStatusMap[serverReport.OVServer.UUID]
				if !exists {
					currentStatus = vendorconsolev1alpha1.HPEUpdateStatus{
						ServerOVUUID: serverReport.OVServer.UUID,
						ServerName:   serverReport.Server.Name,
					}
				}
				serverPowerTask := currentStatus.ServerPowerTaskStatus
				// skip server power off if already started with upgrade task
				if currentStatus.PatchFirmwareBundleStatus == nil {
					serverPowerTask, err = r.turnServerPowerOff(ctx, log, consoleClient, &serverReport, &currentStatus)
					if err != nil {
						firmwareStatusChan <- tempFirmwareStatus{
							firmwareUpdateStatus: &vendorconsolev1alpha1.HPEUpdateStatus{
								ServerPowerTaskStatus:     serverPowerTask,
								PatchFirmwareBundleStatus: currentStatus.PatchFirmwareBundleStatus,
								UpdateTaskStatus:          currentStatus.UpdateTaskStatus,
								ServerOVUUID:              serverReport.OVServer.UUID,
								ServerName:                serverReport.Server.Name,
							},
							err: err,
						}
						return
					}
					if serverPowerTask.Status != string(ov.TaskStateCompleted) {
						firmwareStatusChan <- tempFirmwareStatus{
							firmwareUpdateStatus: &vendorconsolev1alpha1.HPEUpdateStatus{
								ServerPowerTaskStatus:     serverPowerTask,
								PatchFirmwareBundleStatus: currentStatus.PatchFirmwareBundleStatus,
								UpdateTaskStatus:          currentStatus.UpdateTaskStatus,
								ServerOVUUID:              serverReport.OVServer.UUID,
								ServerName:                serverReport.Server.Name,
							},
							err: err,
						}
						return
					}
				}

				// patch the firmware bundle to the OV server template or server profile
				switch firmwareHPE.Spec.FirmwareBundleUpdateOptions {
				case vendorconsolev1alpha1.FirmwareBundleUpdateServerProfile:
					patchUpdateServerProfile := currentStatus.PatchFirmwareBundleStatus
					if currentStatus.UpdateTaskStatus == nil {
						patchUpdateServerProfile, err := r.patchUpdateServerProfile(ctx, log, consoleClient, serverReport, ServerProfileMap[serverReport.SerialNumber], firmwareBundle, &currentStatus)
						if err != nil {
							firmwareStatusChan <- tempFirmwareStatus{
								firmwareUpdateStatus: &vendorconsolev1alpha1.HPEUpdateStatus{
									ServerPowerTaskStatus:     serverPowerTask,
									PatchFirmwareBundleStatus: patchUpdateServerProfile,
									UpdateTaskStatus:          currentStatus.UpdateTaskStatus,
									ServerOVUUID:              serverReport.OVServer.UUID,
									ServerName:                serverReport.Server.Name,
								},
								err: err,
							}
							return
						}
						if patchUpdateServerProfile.Status != string(ov.TaskStateCompleted) {
							log.V(1).Info("Waiting for firmware bundle to be patched to server profile", "ServerName", serverReport.Server.Name, "SerialNumber", serverReport.SerialNumber, "PatchFirmwareBundleStatus", patchUpdateServerProfile.Status)
							firmwareStatusChan <- tempFirmwareStatus{
								firmwareUpdateStatus: &vendorconsolev1alpha1.HPEUpdateStatus{
									ServerPowerTaskStatus:     serverPowerTask,
									PatchFirmwareBundleStatus: patchUpdateServerProfile,
									UpdateTaskStatus:          currentStatus.UpdateTaskStatus,
									ServerOVUUID:              serverReport.OVServer.UUID,
									ServerName:                serverReport.Server.Name,
								},
								err: nil,
							}
							return
						}
					}
					// issue the firmware update to the ov
					upgradeTask, err := r.issueFirmwareUpgradeServerProfile(ctx, log, consoleClient, serverReport, ServerProfileMap[serverReport.SerialNumber].ProfileUUID, &currentStatus)
					if err != nil {
						firmwareStatusChan <- tempFirmwareStatus{
							firmwareUpdateStatus: &vendorconsolev1alpha1.HPEUpdateStatus{
								ServerPowerTaskStatus:     serverPowerTask,
								PatchFirmwareBundleStatus: patchUpdateServerProfile,
								UpdateTaskStatus:          upgradeTask,
								ServerOVUUID:              serverReport.OVServer.UUID,
								ServerName:                serverReport.Server.Name,
							},
							err: err,
						}
						return
					}
					log.V(1).Info("Issued firmware update to server profile", "ServerName", serverReport.Server.Name, "SerialNumber", serverReport.SerialNumber, "UpdateTaskStatus", upgradeTask.Status)
					firmwareStatusChan <- tempFirmwareStatus{
						firmwareUpdateStatus: &vendorconsolev1alpha1.HPEUpdateStatus{
							ServerPowerTaskStatus:     serverPowerTask,
							PatchFirmwareBundleStatus: patchUpdateServerProfile,
							UpdateTaskStatus:          upgradeTask,
							ServerOVUUID:              serverReport.OVServer.UUID,
							ServerName:                serverReport.Server.Name,
						},
						err: nil,
					}
					return
				case vendorconsolev1alpha1.FirmwareBundleUpdateServerProfileTemplate:
					log.V(1).Info("FirmwareBundleUpdateOptions 'ServerProfileTemplate' is not yet implemented")
				default:
					firmwareStatusChan <- tempFirmwareStatus{
						firmwareUpdateStatus: &vendorconsolev1alpha1.HPEUpdateStatus{
							ServerPowerTaskStatus:     currentStatusMap[serverReport.OVServer.UUID].ServerPowerTaskStatus,
							PatchFirmwareBundleStatus: currentStatusMap[serverReport.OVServer.UUID].PatchFirmwareBundleStatus,
							UpdateTaskStatus:          currentStatusMap[serverReport.OVServer.UUID].UpdateTaskStatus,
							ServerOVUUID:              serverReport.OVServer.UUID,
							ServerName:                serverReport.Server.Name,
						},
						err: fmt.Errorf("invalid FirmwareBundleUpdateOptions: %s", firmwareHPE.Spec.FirmwareBundleUpdateOptions),
					}
					return
				}
			}(serverReport, &wg)
		}
		wg.Wait()
	}()

	errs := make([]error, 0)
	for firmwareStatus := range firmwareStatusChan {
		if firmwareStatus.err != nil {
			log.V(1).Error(firmwareStatus.err, "failed to perform firmware update on server", "ServerName", firmwareStatus.firmwareUpdateStatus.ServerName, "ServerOVUUID", firmwareStatus.firmwareUpdateStatus.ServerOVUUID)
			errs = append(errs, firmwareStatus.err)
			continue
		}
		currentStatusMap[firmwareStatus.firmwareUpdateStatus.ServerOVUUID] = *firmwareStatus.firmwareUpdateStatus
	}
	return slices.Collect(maps.Values(currentStatusMap)), errors.Join(errs...)
}

func (r *FirmwareUpdateHPEReconciler) getAllServerProfile(
	ctx context.Context,
	log logr.Logger,
	consoleClient *ov.OV,
	serverReportList []OVFirmwareComplianceReport,
) (map[string]*ov.HPEServerProfile, error) {
	serialNumberList := make([]string, 0, len(serverReportList))
	for _, serverReport := range serverReportList {
		serialNumberList = append(serialNumberList, serverReport.OVServer.SerialNumber)
	}

	serverProfileList, err := consoleClient.GetServerProfilesFromSerialNumber(ctx, serialNumberList)
	if err != nil {
		return nil, fmt.Errorf("failed to get server profiles from OV by serial numbers: %w", err)
	}

	serverProfileMap := make(map[string]*ov.HPEServerProfile, len(serverProfileList))
	for _, serverProfile := range serverProfileList {
		serverProfileMap[serverProfile.SerialNumber] = &serverProfile
	}
	log.V(1).Info("Fetched all server profiles for selected servers", "Count", len(serverProfileMap))
	return serverProfileMap, nil
}

func (r *FirmwareUpdateHPEReconciler) issueFirmwareUpgradeServerProfile(
	ctx context.Context,
	log logr.Logger,
	consoleClient *ov.OV,
	serverReport OVFirmwareComplianceReport,
	serverProfileUUID string,
	currentStatus *vendorconsolev1alpha1.HPEUpdateStatus,
) (*vendorconsolev1alpha1.HPEJob, error) {

	if currentStatus == nil {
		currentStatus = &vendorconsolev1alpha1.HPEUpdateStatus{
			UpdateTaskStatus: nil,
			ServerOVUUID:     serverReport.OVServer.UUID,
		}
	}

	if currentStatus.UpdateTaskStatus != nil {
		// update operation already present for this server
		taskDetails, err := consoleClient.GetTask(ctx, currentStatus.UpdateTaskStatus.Id)
		if err != nil {
			log.V(1).Error(err, "failed to get power off task details", "ServerName", serverReport.Server.Name)
			return &vendorconsolev1alpha1.HPEJob{
				Id:     currentStatus.UpdateTaskStatus.Id,
				Name:   currentStatus.UpdateTaskStatus.Name,
				Status: currentStatus.UpdateTaskStatus.Status,
			}, err
		}
		return &vendorconsolev1alpha1.HPEJob{
			Id:     taskDetails.URI,
			Name:   taskDetails.Name,
			Status: string(taskDetails.TaskState),
		}, nil
	}

	// issue the firmware update to the ov
	taskURI, err := consoleClient.ServerProfileUpgradeFirmware(ctx, serverProfileUUID)
	if err != nil {
		log.V(1).Error(err, "failed to issue firmware update to server profile", "ServerName", serverReport.Server.Name)
		return &vendorconsolev1alpha1.HPEJob{
			Id:     taskURI,
			Name:   "",
			Status: "",
		}, err
	}
	taskDetails, err := consoleClient.GetTask(ctx, taskURI)
	if err != nil {
		log.V(1).Error(err, "failed to get power off task details", "ServerName", serverReport.Server.Name)
		return &vendorconsolev1alpha1.HPEJob{
			Id:     taskURI,
			Name:   "",
			Status: "",
		}, err
	}
	return &vendorconsolev1alpha1.HPEJob{
		Id:     taskDetails.URI,
		Name:   taskDetails.Name,
		Status: string(taskDetails.TaskState),
	}, nil
}

func (r *FirmwareUpdateHPEReconciler) patchUpdateServerProfile(
	ctx context.Context,
	log logr.Logger,
	consoleClient *ov.OV,
	serverReport OVFirmwareComplianceReport,
	serverProfile *ov.HPEServerProfile,
	firmwareBundle *ov.FirmwareBundleDetails,
	currentStatus *vendorconsolev1alpha1.HPEUpdateStatus,
) (*vendorconsolev1alpha1.HPEJob, error) {
	if currentStatus == nil {
		currentStatus = &vendorconsolev1alpha1.HPEUpdateStatus{
			PatchFirmwareBundleStatus: nil,
			ServerPowerTaskStatus:     &vendorconsolev1alpha1.HPEJob{},
			ServerOVUUID:              serverReport.OVServer.UUID,
		}
	}
	if serverProfile.Firmware.FirmwareBaselineURI == firmwareBundle.URI {
		// firmware bundle already patched for this server
		log.V(1).Info("Firmware bundle already patched to server profile", "ServerName", serverReport.Server.Name, "SerialNumber", serverReport.SerialNumber)
		if currentStatus.PatchFirmwareBundleStatus == nil {
			return &vendorconsolev1alpha1.HPEJob{
				Id:     "",
				Name:   "",
				Status: string(ov.TaskStateCompleted),
			}, nil
		}
		return &vendorconsolev1alpha1.HPEJob{
			Id:     currentStatus.PatchFirmwareBundleStatus.Id,
			Name:   currentStatus.PatchFirmwareBundleStatus.Name,
			Status: string(ov.TaskStateCompleted),
		}, nil
	}

	if currentStatus.PatchFirmwareBundleStatus != nil {
		// patch operation already present for this server
		taskDetails, err := consoleClient.GetTask(ctx, currentStatus.PatchFirmwareBundleStatus.Id)
		if err != nil {
			log.V(1).Error(err, "failed to get power off task details", "ServerName", serverReport.Server.Name)
			return &vendorconsolev1alpha1.HPEJob{
				Id:     currentStatus.PatchFirmwareBundleStatus.Id,
				Name:   currentStatus.PatchFirmwareBundleStatus.Name,
				Status: currentStatus.PatchFirmwareBundleStatus.Status,
			}, err
		}
		return &vendorconsolev1alpha1.HPEJob{
			Id:     taskDetails.URI,
			Name:   taskDetails.Name,
			Status: string(taskDetails.TaskState),
		}, nil
	}

	firmwarePayload := ov.FirmwareProfile{
		FirmwareBaselineURI:  firmwareBundle.URI,
		ManageFirmware:       true,
		FirmwareInstallType:  ov.FirmwareInstallTypeFirmwareOnlyOfflineMode,
		ForceInstallFirmware: false,
	}
	// patch the firmware bundle to the server profile
	log.V(1).Info("Patching firmware bundle to server profile", "ServerName", serverReport.Server.Name, "SerialNumber", serverReport.SerialNumber)
	taskURI, err := consoleClient.UpdateServerProfileFirmware(ctx, serverReport.OVServer.UUID, firmwarePayload)
	if err != nil {
		log.V(1).Error(err, "failed to patch firmware bundle to server profile", "ServerName", serverReport.Server.Name)
		return &vendorconsolev1alpha1.HPEJob{
			Id:     taskURI,
			Name:   "",
			Status: "",
		}, err
	}

	taskDetails, err := consoleClient.GetTask(ctx, taskURI)
	if err != nil {
		log.V(1).Error(err, "failed to get power off task details", "ServerName", serverReport.Server.Name)
		return &vendorconsolev1alpha1.HPEJob{
			Id:     taskURI,
			Name:   "",
			Status: "",
		}, err
	}
	return &vendorconsolev1alpha1.HPEJob{
		Id:     taskDetails.URI,
		Name:   taskDetails.Name,
		Status: string(taskDetails.TaskState),
	}, nil
}

func (r *FirmwareUpdateHPEReconciler) turnServerPowerOff(
	ctx context.Context,
	log logr.Logger,
	consoleClient *ov.OV,
	serverReport *OVFirmwareComplianceReport,
	currentStatus *vendorconsolev1alpha1.HPEUpdateStatus,
) (*vendorconsolev1alpha1.HPEJob, error) {
	if currentStatus == nil {
		currentStatus = &vendorconsolev1alpha1.HPEUpdateStatus{
			ServerPowerTaskStatus: nil,
			ServerOVUUID:          serverReport.OVServer.UUID,
		}
	}
	if serverReport.OVServer.PowerState == ov.ServerPowerStateOn && currentStatus.ServerPowerTaskStatus == nil {
		log.V(1).Info("Powering off server before firmware update", "ServerName", serverReport.Server.Name, "SerialNumber", serverReport.SerialNumber)
		taskURI, err := consoleClient.ServerPowerOff(ctx, serverReport.OVServer.UUID, ov.MomentaryPress)
		if err != nil {
			log.V(1).Error(err, "failed to power off server", "ServerName", serverReport.Server.Name)
			return &vendorconsolev1alpha1.HPEJob{
				Id:     taskURI,
				Name:   "",
				Status: "",
			}, err
		}
		taskDetails, err := consoleClient.GetTask(ctx, taskURI)
		if err != nil {
			log.V(1).Error(err, "failed to get power off task details", "ServerName", serverReport.Server.Name)
			return &vendorconsolev1alpha1.HPEJob{
				Id:     taskURI,
				Name:   taskDetails.Name,
				Status: string(taskDetails.TaskState),
			}, err
		}
		return &vendorconsolev1alpha1.HPEJob{
			Id:     taskURI,
			Name:   taskDetails.Name,
			Status: string(taskDetails.TaskState),
		}, nil
	} else if serverReport.OVServer.PowerState == ov.ServerPowerStateOff {
		log.V(1).Info("Server is already powered off", "ServerName", serverReport.Server.Name, "SerialNumber", serverReport.SerialNumber)
		// update the status as powered off, use the existing task details if available
		return &vendorconsolev1alpha1.HPEJob{
			Id:     currentStatus.ServerPowerTaskStatus.Id,
			Name:   currentStatus.ServerPowerTaskStatus.Name,
			Status: string(ov.TaskStateCompleted),
		}, nil
	} else {
		if currentStatus.ServerPowerTaskStatus == nil {
			log.V(1).Info("Missing reboot task, skipping")
			return &vendorconsolev1alpha1.HPEJob{}, fmt.Errorf("missing reboot task for server %s", serverReport.Server.Name)
		}
		taskDetails, err := consoleClient.GetTask(ctx, currentStatus.ServerPowerTaskStatus.Id)
		if err != nil {
			log.V(1).Error(err, "failed to get power off task details", "ServerName", serverReport.Server.Name)
			return &vendorconsolev1alpha1.HPEJob{
				Id:     currentStatus.ServerPowerTaskStatus.Id,
				Name:   currentStatus.ServerPowerTaskStatus.Name,
				Status: currentStatus.ServerPowerTaskStatus.Status,
			}, err
		}
		if taskDetails.TaskState != ov.TaskStateCompleted {
			log.V(1).Info("Waiting for server power off to complete before firmware update", "ServerName", serverReport.Server.Name, "task URI", taskDetails.URI, "TaskStatus", taskDetails.TaskState)
			return &vendorconsolev1alpha1.HPEJob{
				Id:     taskDetails.URI,
				Name:   taskDetails.Name,
				Status: string(taskDetails.TaskState),
			}, nil
		}
		return &vendorconsolev1alpha1.HPEJob{
			Id:     currentStatus.ServerPowerTaskStatus.Id,
			Name:   taskDetails.Name,
			Status: string(taskDetails.TaskState),
		}, nil
	}
}

func (r *FirmwareUpdateHPEReconciler) cleanupServerMaintenanceReferences(
	ctx context.Context,
	log logr.Logger,
	firmwareHPE *vendorconsolev1alpha1.FirmwareUpdateHPE,
) error {
	if firmwareHPE.Spec.ServerMaintenanceRefs == nil {
		return nil
	}
	// try to get the serverMaintenances created
	serverMaintenances, errs := r.getReferredServerMaintenances(ctx, log, firmwareHPE.Spec.ServerMaintenanceRefs)

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

	if len(missingServerMaintenanceRef) != len(firmwareHPE.Spec.ServerMaintenanceRefs) {
		// delete the serverMaintenance if not marked for deletion already
		for _, serverMaintenance := range serverMaintenances {
			if serverMaintenance.DeletionTimestamp.IsZero() && metav1.IsControlledBy(serverMaintenance, firmwareHPE) {
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
		err := r.patchMaintenanceRequestRef(ctx, log, firmwareHPE, nil)
		if err != nil {
			return fmt.Errorf("failed to clean up serverMaintenance ref in FirmwareUpdateHPE: %w", err)
		}
		log.V(1).Info("ServerMaintenance ref all cleaned up")
	}
	return errors.Join(finalErr...)
}

func (r *FirmwareUpdateHPEReconciler) getServersBySelector(
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

func (r *FirmwareUpdateHPEReconciler) getReferredServerMaintenances(
	ctx context.Context,
	log logr.Logger,
	serverMaintenanceRefs []metalv1alpha1.ServerMaintenanceRefItem,
) ([]*metalv1alpha1.ServerMaintenance, []error) {
	serverMaintenances := make([]*metalv1alpha1.ServerMaintenance, 0, len(serverMaintenanceRefs))
	var errs []error
	cnt := 0
	for _, serverMaintenanceRef := range serverMaintenanceRefs {
		key := client.ObjectKey{Name: serverMaintenanceRef.ServerMaintenanceRef.Name, Namespace: r.ManagerNamespace}
		serverMaintenance := &metalv1alpha1.ServerMaintenance{}
		if err := r.Get(ctx, key, serverMaintenance); err != nil {
			log.V(1).Error(err, "failed to get referred serverMaintenance obj", "serverMaintenance", serverMaintenanceRef.ServerMaintenanceRef.Name)
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

func (r *FirmwareUpdateHPEReconciler) patchMaintenanceRequestRef(
	ctx context.Context,
	log logr.Logger,
	firmwareHPE *vendorconsolev1alpha1.FirmwareUpdateHPE,
	serverMaintenanceRefs []metalv1alpha1.ServerMaintenanceRefItem,
) error {
	firmwareHPEBase := firmwareHPE.DeepCopy()

	if serverMaintenanceRefs == nil {
		firmwareHPE.Spec.ServerMaintenanceRefs = nil
	} else {
		firmwareHPE.Spec.ServerMaintenanceRefs = serverMaintenanceRefs
	}

	if err := r.Patch(ctx, firmwareHPE, client.MergeFrom(firmwareHPEBase)); err != nil {
		log.V(1).Error(err, "failed to patch FirmwareUpdateHPE with ServerMaintenances ref")
		return err
	}
	return nil
}

func (r *FirmwareUpdateHPEReconciler) getVendorConcoleClient(
	ctx context.Context,
	log logr.Logger,
	firmwareHPE *vendorconsolev1alpha1.FirmwareUpdateHPE,
) (*ov.OV, error) {
	ovURL, err := url.Parse(firmwareHPE.Spec.OVURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse OV URL: %v error %w", firmwareHPE.Spec.OVURL, err)
	}
	// Create session with HPE OV
	config := &vendorClient.Config{
		URL:                 ovURL,
		InsecureSkipVerify:  r.OVConfig.InsecureSkipVerify,
		TLSHandshakeTimeout: r.OVConfig.TLSHandshakeTimeout,
		ReuseConnections:    r.OVConfig.ReuseConnections,
	}

	ovSecret, err := r.getReferredSecretData(ctx, log, firmwareHPE.Spec.SecretRef)
	if err != nil {
		return nil, fmt.Errorf("failed to get referred secret: %w", err)
	}
	if len(ovSecret) == 0 {
		log.V(1).Info("No secret found for accessing OV console", "Missing SecretRef", firmwareHPE.Spec.SecretRef.Name)
		return nil, errors.New("no secret data found for accessing OV console")
	}
	authTkn := &vendorClient.AuthToken{
		Username:  ovSecret[vendorconsolev1alpha1.SecretUsernameKeyName],
		Password:  ovSecret[vendorconsolev1alpha1.SecretPasswordKeyName],
		Token:     ovSecret[vendorconsolev1alpha1.SecretTokenKeyName],
		Session:   ovSecret[vendorconsolev1alpha1.SecretSessionKeyName],
		SessionId: ovSecret[vendorconsolev1alpha1.SecretSessionIDKeyName],
		AuthType:  vendorClient.HPEToken,
	}
	consoleClient, err := vendorconsole.GetHPEConsole(ctx, config, authTkn)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to HPE OV at: %v, error: %w", firmwareHPE.Spec.OVURL, err)
	}
	// if the token has been refreshed, update the secret
	if ovSecret[vendorconsolev1alpha1.SecretTokenKeyName] != consoleClient.Client.Auth.Token ||
		ovSecret[vendorconsolev1alpha1.SecretSessionIDKeyName] != consoleClient.Client.Auth.SessionId {
		log.V(1).Info("Updating secret with new OV token")
		secret, err := r.getReferredSecret(ctx, log, firmwareHPE.Spec.SecretRef)
		if err != nil {
			return nil, fmt.Errorf("failed to get referred secret: %w", err)
		}
		secretBase := secret.DeepCopy()
		if secret.Data != nil {
			secret.Data[vendorconsolev1alpha1.SecretTokenKeyName] = []byte(consoleClient.Client.Auth.Token)
			secret.Data[vendorconsolev1alpha1.SecretSessionKeyName] = []byte(consoleClient.Client.Auth.Session)
			secret.Data[vendorconsolev1alpha1.SecretSessionIDKeyName] = []byte(consoleClient.Client.Auth.SessionId)
		} else {
			secret.StringData[vendorconsolev1alpha1.SecretTokenKeyName] = consoleClient.Client.Auth.Token
			secret.StringData[vendorconsolev1alpha1.SecretSessionKeyName] = consoleClient.Client.Auth.Session
			secret.StringData[vendorconsolev1alpha1.SecretSessionIDKeyName] = consoleClient.Client.Auth.SessionId
		}
		if err := r.Patch(ctx, secret, client.MergeFrom(secretBase)); err != nil {
			return nil, fmt.Errorf("failed to update secret with new OV token: %w", err)
		}
		log.V(1).Info("Updated secret with new OV token")
	}
	return consoleClient, nil
}

func (r *FirmwareUpdateHPEReconciler) getReferredSecretData(
	ctx context.Context,
	log logr.Logger,
	secretRef *corev1.LocalObjectReference,
) (map[string]string, error) {
	secret, err := r.getReferredSecret(ctx, log, secretRef)
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

func (r *FirmwareUpdateHPEReconciler) getReferredSecret(
	ctx context.Context,
	log logr.Logger,
	secretRef *corev1.LocalObjectReference,
) (*corev1.Secret, error) {
	if secretRef == nil {
		return nil, nil
	}
	key := client.ObjectKey{Name: secretRef.Name, Namespace: r.ManagerNamespace}
	secret := &corev1.Secret{}
	if err := r.Get(ctx, key, secret); err != nil {
		log.V(1).Error(err, "failed to get referred Secret obj", "secret name", secretRef.Name)
		return nil, err
	}
	return secret, nil
}

func (r *FirmwareUpdateHPEReconciler) getServerMaintenanceRefForServer(
	serverMaintenanceRefs []metalv1alpha1.ServerMaintenanceRefItem,
	serverMaintenanceUID types.UID,
) (*corev1.ObjectReference, bool) {
	for _, serverMaintenanceRef := range serverMaintenanceRefs {
		if serverMaintenanceRef.ServerMaintenanceRef.UID == serverMaintenanceUID {
			return serverMaintenanceRef.ServerMaintenanceRef, true
		}
	}
	return nil, false
}

func (r *FirmwareUpdateHPEReconciler) updateStatus(
	ctx context.Context,
	log logr.Logger,
	firmwareHPE *vendorconsolev1alpha1.FirmwareUpdateHPE,
	state vendorconsolev1alpha1.FirmwareUpdateState,
	updateStatus []vendorconsolev1alpha1.HPEUpdateStatus,
) error {
	if firmwareHPE.Status.State == state && reflect.DeepEqual(firmwareHPE.Status.UpdateTask, updateStatus) {
		return nil
	}

	firmwareHPEBase := firmwareHPE.DeepCopy()
	firmwareHPE.Status.State = state
	firmwareHPE.Status.UpdateTask = updateStatus

	if err := r.Status().Patch(ctx, firmwareHPE, client.MergeFrom(firmwareHPEBase)); err != nil {
		return fmt.Errorf("failed to patch firmwareHPE status: %w", err)
	}

	log.V(1).Info("Updated FirmwareUpdateHPE state ",
		"State", state)

	return nil
}

func (r *FirmwareUpdateHPEReconciler) handleInProgressStatusUpdate(
	ctx context.Context,
	log logr.Logger,
	firmwareHPE *vendorconsolev1alpha1.FirmwareUpdateHPE,
	updateStatus []vendorconsolev1alpha1.HPEUpdateStatus,
	uncomplaintServerCount int,
	failedServerMaintenance int,
) (bool, error) {
	inProgressUpgrades := []string{}
	unSuccessfulUpgrades := []string{}
	for _, status := range updateStatus {
		if status.UpdateTaskStatus != nil && slices.Contains(ov.UnSuccessfulTaskStates, ov.TaskState(status.UpdateTaskStatus.Status)) {
			log.V(1).Info("Firmware update has failed", "ServerOVUUID", status.ServerOVUUID, "Server Name", status.ServerName, "UpdateStatus", status)
			unSuccessfulUpgrades = append(unSuccessfulUpgrades, status.ServerName)
			continue
		}
		if status.PatchFirmwareBundleStatus != nil && slices.Contains(ov.UnSuccessfulTaskStates, ov.TaskState(status.PatchFirmwareBundleStatus.Status)) {
			log.V(1).Info("Firmware update has failed in Patch new firmware bundle URI", "ServerOVUUID", status.ServerOVUUID, "Server Name", status.ServerName, "UpdateStatus", status)
			unSuccessfulUpgrades = append(unSuccessfulUpgrades, status.ServerName)
			continue
		}
		if status.ServerPowerTaskStatus != nil && slices.Contains(ov.UnSuccessfulTaskStates, ov.TaskState(status.ServerPowerTaskStatus.Status)) {
			log.V(1).Info("Firmware update has failed in Turn Server power off", "ServerOVUUID", status.ServerOVUUID, "Server Name", status.ServerName, "UpdateStatus", status)
			unSuccessfulUpgrades = append(unSuccessfulUpgrades, status.ServerName)
			continue
		}
		if status.UpdateTaskStatus == nil || ov.TaskState(status.UpdateTaskStatus.Status) != ov.TaskStateCompleted {
			log.V(1).Info("Firmware update still in progress", "ServerOVUUID", status.ServerOVUUID, "Server Name", status.ServerName, "UpdateStatus", status)
			inProgressUpgrades = append(inProgressUpgrades, status.ServerName)
		}
	}
	firmwareHPEBase := firmwareHPE.DeepCopy()
	firmwareHPE.Status.InProgressServerCount = int32(len(inProgressUpgrades))
	firmwareHPE.Status.FailedServerCount = int32(len(unSuccessfulUpgrades)) + int32(failedServerMaintenance)
	firmwareHPE.Status.CompletedServerCount = firmwareHPE.Status.ServerCount - int32(uncomplaintServerCount)
	firmwareHPE.Status.UpdateTask = updateStatus
	waitForUpdate := false
	if len(inProgressUpgrades) > 0 {
		firmwareHPE.Status.State = vendorconsolev1alpha1.FirmwareUpdateStateInProgress
		waitForUpdate = true
	} else if firmwareHPE.Status.FailedServerCount > 0 && firmwareHPE.Status.FailedServerCount+firmwareHPE.Status.CompletedServerCount >= firmwareHPE.Status.ServerCount {
		log.V(1).Info("Some firmware updates have failed to upgrade firmware, moving to Failed state", "FailedServers", unSuccessfulUpgrades)
		firmwareHPE.Status.State = vendorconsolev1alpha1.FirmwareUpdateStateFailed
	} else if uncomplaintServerCount == 0 && firmwareHPE.Status.FailedServerCount == 0 {
		log.V(1).Info("Firmware update tasks completed", "Update Status", updateStatus)
		firmwareHPE.Status.State = vendorconsolev1alpha1.FirmwareUpdateStateCompleted
	}

	if err := r.Status().Patch(ctx, firmwareHPE, client.MergeFrom(firmwareHPEBase)); err != nil {
		return waitForUpdate, fmt.Errorf("failed to patch firmwareHPE status: %w", err)
	}

	log.V(1).Info("Updated FirmwareUpdateHPE state ",
		"State", firmwareHPE.Status.State,
		"FailedServers", unSuccessfulUpgrades,
		"InProgressServers", inProgressUpgrades,
		"TotalServers", len(updateStatus),
	)
	return waitForUpdate, nil
}

func (r *FirmwareUpdateHPEReconciler) verifyServersSelected(
	log logr.Logger,
	serverList *metalv1alpha1.ServerList,
) []metalv1alpha1.Server {
	if serverList == nil || len(serverList.Items) == 0 {
		log.V(1).Info("No servers found matching the Spec's selector")
		return nil
	}
	servers := make([]metalv1alpha1.Server, 0)
	var nonHPEServer []string
	for _, server := range serverList.Items {
		if server.Status.Manufacturer != string(vendorconsole.ManufacturerHPE) {
			log.V(1).Info("Skipping server as it is not HPE", "Server", server.Name, "Manufacturer", server.Status.Manufacturer)
			nonHPEServer = append(nonHPEServer, server.Name)
			continue
		}
		servers = append(servers, server)
	}
	if len(servers) != len(serverList.Items) {
		log.V(1).Info("Some servers are not HPE, ignoring them", "TotalServers", len(serverList.Items), "Not HPEServers", nonHPEServer)
	}
	return servers
}

func (r *FirmwareUpdateHPEReconciler) requestMaintenanceOnServers(
	ctx context.Context,
	log logr.Logger,
	firmwareHPE *vendorconsolev1alpha1.FirmwareUpdateHPE,
	serverReportList []OVFirmwareComplianceReport,
) (bool, error) {

	// if Server maintenance ref is already given. no further action required.
	if firmwareHPE.Spec.ServerMaintenanceRefs != nil && len(firmwareHPE.Spec.ServerMaintenanceRefs) == len(serverReportList) {
		// todo: delete all the owned serverMaintenance which are not in the Spec
		return false, nil
	}

	// if user gave some server with serverMaintenance but not all
	// we want to request maintenance for the missing servers only.
	// find the servers which has maintenance and do not create maintenance for them.
	serverWithMaintenances := make(map[string]bool, len(serverReportList))
	if firmwareHPE.Spec.ServerMaintenanceRefs != nil {
		// we fetch all the references already in the Spec (self created/provided by user)
		serverMaintenances, err := r.getReferredServerMaintenances(ctx, log, firmwareHPE.Spec.ServerMaintenanceRefs)
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
	if err := clientutils.ListAndFilterControlledBy(ctx, r.Client, firmwareHPE, serverMaintenancesList); err != nil {
		return false, err
	}
	for _, serverMaintenance := range serverMaintenancesList.Items {
		serverWithMaintenances[serverMaintenance.Spec.ServerRef.Name] = true
	}

	var errs []error
	serverMaintenanceRefs := make([]metalv1alpha1.ServerMaintenanceRefItem, 0, len(serverReportList))
	for _, report := range serverReportList {
		if _, ok := serverWithMaintenances[report.Server.Name]; ok {
			continue
		}
		serverMaintenance := &metalv1alpha1.ServerMaintenance{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:    r.ManagerNamespace,
				GenerateName: "hpe-ov-maintenance-",
			},
		}

		opResult, err := controllerutil.CreateOrPatch(ctx, r.Client, serverMaintenance, func() error {
			serverMaintenance.Spec.Policy = firmwareHPE.Spec.ServerMaintenancePolicy
			serverMaintenance.Spec.ServerPower = metalv1alpha1.PowerOff
			serverMaintenance.Spec.ServerRef = &corev1.LocalObjectReference{Name: report.Server.Name}
			if serverMaintenance.Status.State != metalv1alpha1.ServerMaintenanceStateInMaintenance && serverMaintenance.Status.State != "" {
				serverMaintenance.Status.State = ""
			}
			return controllerutil.SetControllerReference(firmwareHPE, serverMaintenance, r.Client.Scheme())
		})
		if err != nil {
			log.V(1).Error(err, "failed to create or patch serverMaintenance", "Server", report.Server.Name)
			errs = append(errs, err)
			continue
		}
		log.V(1).Info("Created serverMaintenance", "ServerMaintenance", serverMaintenance.Name, "ServerMaintenance label", serverMaintenance.Labels, "Operation", opResult)

		serverMaintenanceRefs = append(
			serverMaintenanceRefs,
			metalv1alpha1.ServerMaintenanceRefItem{
				ServerMaintenanceRef: &corev1.ObjectReference{
					APIVersion: metalv1alpha1.GroupVersion.String(),
					Kind:       "ServerMaintenance",
					Namespace:  serverMaintenance.Namespace,
					Name:       serverMaintenance.Name,
					UID:        serverMaintenance.UID,
				}})
	}

	if len(errs) > 0 {
		return false, errors.Join(errs...)
	}

	err := r.patchMaintenanceRequestRef(ctx, log, firmwareHPE, serverMaintenanceRefs)
	if err != nil {
		return false, fmt.Errorf("failed to patch serverMaintenance ref on FirmwareUpdateHPE %w", err)
	}

	log.V(1).Info("Patched serverMaintenanceMap on FirmwareUpdateHPE")

	return true, nil
}

func (r *FirmwareUpdateHPEReconciler) filterInMantenanceServers(
	ctx context.Context,
	log logr.Logger,
	firmwareHPE *vendorconsolev1alpha1.FirmwareUpdateHPE,
	serverReportList []OVFirmwareComplianceReport,
) ([]OVFirmwareComplianceReport, []OVFirmwareComplianceReport) {
	if firmwareHPE.Spec.ServerMaintenanceRefs == nil {
		return nil, nil
	}

	if len(firmwareHPE.Spec.ServerMaintenanceRefs) != len(serverReportList) {
		log.V(1).Info("Not all servers have Maintenance", "ServerMaintenanceRefs", firmwareHPE.Spec.ServerMaintenanceRefs, "Servers", serverReportList)
		return nil, nil
	}

	reportToServerMap := make(map[string]OVFirmwareComplianceReport, len(serverReportList))
	for _, report := range serverReportList {
		reportToServerMap[report.Server.Name] = report
	}

	serverMaintenances, errs := r.getReferredServerMaintenances(ctx, log, firmwareHPE.Spec.ServerMaintenanceRefs)
	if errs != nil {
		log.V(1).Error(errors.Join(errs...), "failed to get referred serverMaintenances")
		return nil, nil
	}
	inMaintenanceState := make([]OVFirmwareComplianceReport, 0)
	notInMaintenanceState := make(map[string]metalv1alpha1.ServerMaintenanceState)
	failedMaintenanceStateServerReportList := make([]OVFirmwareComplianceReport, 0)
	for _, maintenance := range serverMaintenances {
		if maintenance.Status.State == metalv1alpha1.ServerMaintenanceStateFailed {
			// fail immediately if any of the server maintenance request failed, as we can not proceed further
			log.V(1).Info("ServerMaintenance in failed state", "Server", maintenance.Spec.ServerRef.Name, "ServerMaintenance", maintenance.Name)
			failedMaintenanceStateServerReportList = append(failedMaintenanceStateServerReportList, reportToServerMap[maintenance.Spec.ServerRef.Name])
		}
		// this gives us the waiting time for the server to be prepared for maintenance by ServerMaintenance controller
		if maintenance.Status.State != metalv1alpha1.ServerMaintenanceStateInMaintenance {
			log.V(1).Info("ServerMaintenance not yet in maintenance state", "Server", maintenance.Spec.ServerRef.Name, "ServerMaintenance", maintenance.Name, "State", maintenance.Status.State)
			notInMaintenanceState[maintenance.Spec.ServerRef.Name] = maintenance.Status.State
		}
		inMaintenanceState = append(inMaintenanceState, reportToServerMap[maintenance.Spec.ServerRef.Name])
	}
	if len(inMaintenanceState) == 0 {
		log.V(1).Info("No server in in maintenance state", "req Servermaintenances", firmwareHPE.Spec.ServerMaintenanceRefs)
		return inMaintenanceState, failedMaintenanceStateServerReportList
	}

	serverNotInMaintenenaceState := make(map[string]metalv1alpha1.ServerState)
	InMaintenanceStateServerReportList := make([]OVFirmwareComplianceReport, 0)
	for _, report := range inMaintenanceState {
		if report.Server.Status.State == metalv1alpha1.ServerStateMaintenance {
			serverMaintenanceRef, ok := r.getServerMaintenanceRefForServer(firmwareHPE.Spec.ServerMaintenanceRefs, report.Server.Spec.ServerMaintenanceRef.UID)
			if report.Server.Spec.ServerMaintenanceRef == nil || !ok || report.Server.Spec.ServerMaintenanceRef.UID != serverMaintenanceRef.UID {
				// server in maintenance for other tasks. or
				// server maintenance ref is wrong in either server or firmwareHPE Spec
				// wait for update on the server obj
				log.V(1).Info("Server is already in maintenance for other tasks",
					"Server", report.Server.Name,
					"ServerMaintenanceRef on Server", report.Server.Spec.ServerMaintenanceRef,
					"ServerMaintenanceRef on FirmwareUpdateHPE", serverMaintenanceRef,
				)
				serverNotInMaintenenaceState[report.Server.Name] = report.Server.Status.State
				continue
			}
		} else {
			// we still need to wait for server to enter maintenance
			// wait for update on the server obj
			log.V(1).Info("Server not yet in maintenance", "Server", report.Server.Name, "State", report.Server.Status.State, "MaintenanceRef", report.Server.Spec.ServerMaintenanceRef)
			serverNotInMaintenenaceState[report.Server.Name] = report.Server.Status.State
			continue
		}
		InMaintenanceStateServerReportList = append(InMaintenanceStateServerReportList, report)
	}

	if len(serverNotInMaintenenaceState) > 0 {
		log.V(1).Info("Some servers not yet in maintenance", "some Servermaintenances not in maintenance", serverNotInMaintenenaceState)
		return InMaintenanceStateServerReportList, failedMaintenanceStateServerReportList
	}

	return InMaintenanceStateServerReportList, failedMaintenanceStateServerReportList
}

func (r *FirmwareUpdateHPEReconciler) enqueueByServerRefs(
	ctx context.Context,
	obj client.Object,
) []ctrl.Request {
	log := ctrl.LoggerFrom(ctx)
	host := obj.(*metalv1alpha1.Server)
	log.V(1).Info("TEMP: Enqueueing FirmwareUpdateHPE for Server change", "Server", host.Name)

	firmwareHPEList := &vendorconsolev1alpha1.FirmwareUpdateHPEList{}
	if err := r.List(ctx, firmwareHPEList); err != nil {
		log.Error(err, "failed to list FirmwareUpdateHPE")
		return nil
	}
	reqs := make([]ctrl.Request, 0)
	for _, firmwareHPE := range firmwareHPEList.Items {
		selector, err := metav1.LabelSelectorAsSelector(&firmwareHPE.Spec.ServerSelector)
		if err != nil {
			log.V(1).Error(err, "failed to convert label selector")
			return nil
		}
		log.V(1).Info("TEMP: Checking if server matches the selector", "Server", host.Name, "Selector", selector, "host.GetLabels()", host.GetLabels())
		// if the host label matches the selector, enqueue the request
		if selector.Matches(labels.Set(host.GetLabels())) {
			reqs = append(reqs, ctrl.Request{
				NamespacedName: client.ObjectKey{
					Name:      firmwareHPE.Name,
					Namespace: firmwareHPE.Namespace,
				},
			})
			continue
		} else {
			// handle the case when the lable was deleted or changed and host no longer matches the selector
			// if we dont have maintenance request on this firmwareHPE we do not want to queue changes from servers.
			if firmwareHPE.Spec.ServerMaintenanceRefs == nil {
				continue
			}

			if firmwareHPE.Status.State == vendorconsolev1alpha1.FirmwareUpdateStateCompleted || firmwareHPE.Status.State == vendorconsolev1alpha1.FirmwareUpdateStateFailed {
				continue
			}
		}

		serverMaintenanceRef, ok := r.getServerMaintenanceRefForServer(firmwareHPE.Spec.ServerMaintenanceRefs, host.Spec.ServerMaintenanceRef.UID)
		if ok && serverMaintenanceRef != nil {
			reqs = append(reqs, ctrl.Request{
				NamespacedName: types.NamespacedName{Namespace: firmwareHPE.Namespace, Name: firmwareHPE.Name},
			})
		}
	}
	return reqs
}

// SetupWithManager sets up the controller with the Manager.
func (r *FirmwareUpdateHPEReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&vendorconsolev1alpha1.FirmwareUpdateHPE{}).
		Owns(&metalv1alpha1.ServerMaintenance{}).
		Watches(&metalv1alpha1.Server{},
			handler.EnqueueRequestsFromMapFunc(r.enqueueByServerRefs),
			builder.WithPredicates(predicate.LabelChangedPredicate{})).
		Complete(r)
}
