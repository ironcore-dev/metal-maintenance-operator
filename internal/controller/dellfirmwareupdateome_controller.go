/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	"github.com/go-logr/logr"
	"github.com/ironcore-dev/controller-utils/clientutils"
	managerconsole "github.com/ironcore-dev/maintenance-operator/ManagerConsole"
	mgrClient "github.com/ironcore-dev/maintenance-operator/ManagerConsole/client"
	"github.com/ironcore-dev/maintenance-operator/ManagerConsole/ome"
	maintenancev1alpha1 "github.com/ironcore-dev/maintenance-operator/api/v1alpha1"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	DellFirmwareUpdateFinalizer = "maintenance.ironcore.dev/dellfirmwareupdateome"
)

// DELLFirmwareUpdateOMEReconciler reconciles a DELLFirmwareUpdateOME object
type DELLFirmwareUpdateOMEReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	ManagerNamespace string
	omeConfig        *managerconsole.Config
	ResyncInterval   time.Duration
}

// +kubebuilder:rbac:groups=maintenance.maintenance.ironcore.dev,resources=dellfirmwareupdateomes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=maintenance.maintenance.ironcore.dev,resources=dellfirmwareupdateomes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=maintenance.maintenance.ironcore.dev,resources=dellfirmwareupdateomes/finalizers,verbs=update
// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=servers,verbs=get;list;watch
// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=servermaintenances,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=servermaintenances/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;delete

func (r *DELLFirmwareUpdateOMEReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	dellOME := &maintenancev1alpha1.DELLFirmwareUpdateOME{}
	if err := r.Get(ctx, req.NamespacedName, dellOME); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	log.V(1).Info("Reconciling DELLFirmwareUpdateOME")

	return r.reconcileExists(ctx, log, dellOME)
}

func (r *DELLFirmwareUpdateOMEReconciler) reconcileExists(
	ctx context.Context,
	log logr.Logger,
	dellOME *maintenancev1alpha1.DELLFirmwareUpdateOME,
) (ctrl.Result, error) {
	// if object is being deleted - reconcile deletion
	if r.shouldDelete(log, dellOME) {
		log.V(1).Info("Object is being deleted")
		return r.delete(ctx, log, dellOME)
	}

	return r.reconcile(ctx, log, dellOME)
}

func (r *DELLFirmwareUpdateOMEReconciler) shouldDelete(
	log logr.Logger,
	dellOME *maintenancev1alpha1.DELLFirmwareUpdateOME,
) bool {
	if dellOME.DeletionTimestamp.IsZero() {
		return false
	}

	if controllerutil.ContainsFinalizer(dellOME, DellFirmwareUpdateFinalizer) &&
		dellOME.Status.State == maintenancev1alpha1.FirmwareUpdateStateInProgress {
		log.V(1).Info("Postponing delete as firmware update is in progress")
		return false
	}
	return true
}

func (r *DELLFirmwareUpdateOMEReconciler) delete(
	ctx context.Context,
	log logr.Logger,
	dellOME *maintenancev1alpha1.DELLFirmwareUpdateOME,
) (ctrl.Result, error) {
	log.V(1).Info("Ensuring that the finalizer is removed")
	if modified, err := clientutils.PatchEnsureNoFinalizer(ctx, r.Client, dellOME, DellFirmwareUpdateFinalizer); err != nil || modified {
		return ctrl.Result{}, err
	}

	log.V(1).Info("DELLFirmwareUpdateOME is deleted")
	return ctrl.Result{}, nil
}

func (r *DELLFirmwareUpdateOMEReconciler) cleanupServerMaintenanceReferences(
	ctx context.Context,
	log logr.Logger,
	dellOME *maintenancev1alpha1.DELLFirmwareUpdateOME,
) error {
	if dellOME.Spec.ServerMaintenanceRefs == nil {
		return nil
	}
	// try to get the serverMaintenances created
	serverMaintenances, errs := r.getReferredServerMaintenances(ctx, log, dellOME.Spec.ServerMaintenanceRefs)

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

	if len(missingServerMaintenanceRef) != len(dellOME.Spec.ServerMaintenanceRefs) {
		// delete the serverMaintenance if not marked for deletion already
		for _, serverMaintenance := range serverMaintenances {
			if serverMaintenance.DeletionTimestamp.IsZero() && metav1.IsControlledBy(serverMaintenance, dellOME) {
				log.V(1).Info("Deleting server maintenance", "ServerMaintenance", serverMaintenance.Name, "State", serverMaintenance.Status.State)
				if err := r.Delete(ctx, serverMaintenance); err != nil {
					log.V(1).Info("Failed to delete server maintenance", "ServerMaintenance", serverMaintenance.Name)
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
		err := r.patchMaintenanceRequestRef(ctx, log, dellOME, nil)
		if err != nil {
			return fmt.Errorf("failed to clean up serverMaintenance ref in DELLFirmwareUpdateOME: %w", err)
		}
		log.V(1).Info("ServerMaintenance ref all cleaned up")
	}
	return errors.Join(finalErr...)
}

func (r *DELLFirmwareUpdateOMEReconciler) reconcile(
	ctx context.Context,
	log logr.Logger,
	dellOME *maintenancev1alpha1.DELLFirmwareUpdateOME,
) (ctrl.Result, error) {
	if shouldIgnoreReconciliation(dellOME) {
		log.V(1).Info("Skipped DELLFirmwareUpdateOME reconciliation")
		return ctrl.Result{}, nil
	}
	if modified, err := clientutils.PatchEnsureFinalizer(ctx, r.Client, dellOME, DellFirmwareUpdateFinalizer); err != nil || modified {
		return ctrl.Result{}, err
	}
	return r.ensureStateTransition(ctx, log, dellOME)
}

func (r *DELLFirmwareUpdateOMEReconciler) ensureStateTransition(
	ctx context.Context,
	log logr.Logger,
	dellOME *maintenancev1alpha1.DELLFirmwareUpdateOME,
) (ctrl.Result, error) {
	serverList, err := r.getServersBySelector(ctx, &dellOME.Spec.ServerSelector)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to get servers by selector: %w", err)
	}
	servers := r.verifyServersSelected(log, serverList)

	omeURL, err := url.Parse(dellOME.Spec.OMEURL)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to parse OME URL: %v error %w", dellOME.Spec.OMEURL, err)
	}
	// Create session with Dell OME
	config := &mgrClient.Config{
		URL:                 omeURL,
		InsecureSkipVerify:  r.omeConfig.InsecureSkipVerify,
		TLSHandshakeTimeout: r.omeConfig.TLSHandshakeTimeout,
		ReuseConnections:    r.omeConfig.ReuseConnections,
	}

	omeSecret, err := r.getReferredSecret(ctx, log, dellOME.Spec.SecretRef)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to get referred secret: %w", err)
	}
	authTkn := &mgrClient.AuthToken{
		Username: omeSecret[maintenancev1alpha1.SecretUsernameKeyName],
		Password: omeSecret[maintenancev1alpha1.SecretPasswordKeyName],
		AuthType: mgrClient.DellToken,
	}
	consoleClient, err := managerconsole.GetDellConsole(config, authTkn)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to connect to DELL OME at: %v, error: %w", dellOME.Spec.OMEURL, err)
	}
	switch dellOME.Status.State {
	case "", maintenancev1alpha1.FirmwareUpdateStatePending:
		return r.handlePendingState(ctx, log, dellOME, servers, serverList)
	case maintenancev1alpha1.FirmwareUpdateStateInProgress:
		return r.handleInProgressState(ctx, log, dellOME, servers, consoleClient)
	case maintenancev1alpha1.FirmwareUpdateStateCompleted:
		return r.handleCompletedState(ctx, log, dellOME, servers, consoleClient)
	case maintenancev1alpha1.FirmwareUpdateStateFailed:
		return r.handleFailedState(ctx, log, dellOME)
	}
	return ctrl.Result{}, nil
}

func (r *DELLFirmwareUpdateOMEReconciler) handlePendingState(
	ctx context.Context,
	log logr.Logger,
	dellOME *maintenancev1alpha1.DELLFirmwareUpdateOME,
	servers []metalv1alpha1.Server,
	serverList *metalv1alpha1.ServerList,
) (ctrl.Result, error) {
	log.V(1).Info("Reconciling Pending state")
	if len(servers) == 0 {
		log.V(1).Info("No servers found matching the Spec's selector", "Selector", dellOME.Spec.ServerSelector)
		err := r.updateStatus(ctx, log, dellOME, maintenancev1alpha1.FirmwareUpdateStateCompleted, int32(len(servers)), dellOME.Status.UpdateTask)
		return ctrl.Result{}, err
	}

	if len(servers) != len(serverList.Items) {
		log.V(1).Info("Some servers are not Dell", "TotalServers", len(serverList.Items))
		err := r.updateStatus(ctx, log, dellOME, maintenancev1alpha1.FirmwareUpdateStateFailed, int32(len(servers)), dellOME.Status.UpdateTask)
		return ctrl.Result{}, err
	}
	err := r.updateStatus(ctx, log, dellOME, maintenancev1alpha1.FirmwareUpdateStateInProgress, int32(len(servers)), dellOME.Status.UpdateTask)
	return ctrl.Result{}, err
}

func (r *DELLFirmwareUpdateOMEReconciler) handleInProgressState(
	ctx context.Context,
	log logr.Logger,
	dellOME *maintenancev1alpha1.DELLFirmwareUpdateOME,
	servers []metalv1alpha1.Server,
	consoleClient *ome.OME,
) (ctrl.Result, error) {
	log.V(1).Info("Reconciling InProgress state")

	catalog, baseLine, currentServers, TargetNonComplient, waitForJobCompletion, err := r.handlePreUpdateTasks(log, dellOME, servers, consoleClient)
	if err != nil {
		return ctrl.Result{}, err
	}
	if len(currentServers) != len(servers) {
		log.V(1).Info("Some servers have been updated, while firmware update in progress. Ignoring them", "TotalServers", len(servers), "FoundInOME", len(currentServers))
		return ctrl.Result{}, r.updateStatus(ctx, log, dellOME, dellOME.Status.State, int32(len(currentServers)), dellOME.Status.UpdateTask)
	}
	if waitForJobCompletion {
		return ctrl.Result{RequeueAfter: r.ResyncInterval}, nil
	}
	if len(TargetNonComplient) == 0 {
		log.V(1).Info("all devices Compliant, no firmware upgrade needed")
		err := r.updateStatus(ctx, log, dellOME, maintenancev1alpha1.FirmwareUpdateStateCompleted, dellOME.Status.ServerCount, dellOME.Status.UpdateTask)
		return ctrl.Result{}, err
	}

	// if server needs upgrade, request maintenance on those servers alone
	// ensure the serverMaintenance are created for the selected servers
	if len(dellOME.Spec.ServerMaintenanceRefs) != len(currentServers) {
		log.V(1).Info("Not all servers have Maintenance", "ServerMaintenanceRefs", dellOME.Spec.ServerMaintenanceRefs, "Servers", currentServers)
		if requeue, err := r.requestMaintenanceOnServers(ctx, log, dellOME, currentServers); err != nil || requeue {
			return ctrl.Result{}, err
		}
	}

	// check if the maintenance is granted
	if ok := r.checkIfMaintenanceGranted(log, dellOME, currentServers); !ok {
		log.V(1).Info("Waiting for maintenance to be granted before continuing with updating bmc version")
		return ctrl.Result{}, err
	}

	if dellOME.Status.UpdateTask == nil {
		jobDetails, err := r.performFirmwareUpdate(log, dellOME, consoleClient, baseLine, catalog, TargetNonComplient)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to trigger firmware update: %w", err)
		}
		err = r.updateStatus(ctx, log, dellOME, dellOME.Status.State, dellOME.Status.ServerCount, &maintenancev1alpha1.DellJob{Id: jobDetails.Id, Name: jobDetails.JobName})
		return ctrl.Result{}, err
	}

	jobId := dellOME.Status.UpdateTask.Id
	completed, err := r.checkJobCompletion(consoleClient, jobId)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to check for Firmware Update job completion: %w", err)
	}
	if !completed {
		log.V(1).Info("Firmware Update job not yet completed, will check again later", "TaskId", baseLine.TaskId)
		return ctrl.Result{RequeueAfter: r.ResyncInterval}, nil
	}

	// refresh the baseline to get the latest compliance status, to not repeat the upgrade if needed
	err = consoleClient.RunJobNow([]int{baseLine.TaskId})
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to check for Baseline Refresh job completion: %w", err)
	}
	completed, err = r.checkJobCompletion(consoleClient, baseLine.TaskId)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to check for Baseline Refresh job completion: %w", err)
	}
	if !completed {
		log.V(1).Info("Baseline refresh job not yet completed, will check again later", "TaskId", baseLine.TaskId)
		return ctrl.Result{RequeueAfter: r.ResyncInterval}, nil
	}

	err = r.updateStatus(ctx, log, dellOME, maintenancev1alpha1.FirmwareUpdateStateCompleted, dellOME.Status.ServerCount, dellOME.Status.UpdateTask)
	return ctrl.Result{}, err

}

func (r *DELLFirmwareUpdateOMEReconciler) handleCompletedState(
	ctx context.Context,
	log logr.Logger,
	dellOME *maintenancev1alpha1.DELLFirmwareUpdateOME,
	servers []metalv1alpha1.Server,
	consoleClient *ome.OME,
) (ctrl.Result, error) {
	log.V(1).Info("Reconciling Completed state")
	// cleanup the serverMaintenance references if any
	if err := r.cleanupServerMaintenanceReferences(ctx, log, dellOME); err != nil {
		log.V(1).Error(err, "failed to cleanup serverMaintenance references")
		return ctrl.Result{}, err
	}
	// check if any servers needs firmware upgrade
	_, _, _, TargetNonComplient, waitForJobCompletion, err := r.handlePreUpdateTasks(log, dellOME, servers, consoleClient)
	if err != nil {
		return ctrl.Result{}, err
	}
	if waitForJobCompletion {
		return ctrl.Result{RequeueAfter: r.ResyncInterval}, nil
	}
	if len(TargetNonComplient) > 0 {
		log.V(1).Info("Some devices are not Compliant, firmware upgrade needed")
		err = r.updateStatus(ctx, log, dellOME, maintenancev1alpha1.FirmwareUpdateStatePending, int32(len(servers)), dellOME.Status.UpdateTask)
		return ctrl.Result{}, err
	}
	log.V(1).Info("DELLFirmwareUpdateOME reconciliation completed")
	return ctrl.Result{}, nil

}

func (r *DELLFirmwareUpdateOMEReconciler) handleFailedState(
	ctx context.Context,
	log logr.Logger,
	dellOME *maintenancev1alpha1.DELLFirmwareUpdateOME,
) (ctrl.Result, error) {
	log.V(1).Info("Firmware Update has failed, manual intervention needed")
	if shouldRetryReconciliation(dellOME) {
		log.V(1).Info("Retrying Firmware Update reconciliation")
		dellOMEBase := dellOME.DeepCopy()
		dellOME.Status.State = maintenancev1alpha1.FirmwareUpdateStatePending
		dellOME.Status.UpdateTask = nil
		dellOME.Status.Conditions = nil
		dellOME.Status.ServerCount = 0
		annotations := dellOME.GetAnnotations()
		delete(annotations, metalv1alpha1.OperationAnnotation)
		dellOME.SetAnnotations(annotations)
		if err := r.Status().Patch(ctx, dellOME, client.MergeFrom(dellOMEBase)); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to patch DELLFirmwareUpdateOME status for retrying: %w", err)
		}
		return ctrl.Result{}, nil
	}
	return ctrl.Result{}, nil
}

func (r *DELLFirmwareUpdateOMEReconciler) handlePreUpdateTasks(
	log logr.Logger,
	dellOME *maintenancev1alpha1.DELLFirmwareUpdateOME,
	servers []metalv1alpha1.Server,
	consoleClient *ome.OME,
) (*ome.DellCatalogDetails, *ome.DellBaseline, []metalv1alpha1.Server, []ome.DellTarget, bool, error) {

	catalog, err := r.createOrGetCatalog(log, dellOME, consoleClient)
	if err != nil {
		return nil, nil, nil, nil, false, fmt.Errorf("failed to create catalog: %w", err)
	}
	completed, err := r.checkJobCompletion(consoleClient, catalog.TaskId)
	if err != nil {
		return nil, nil, nil, nil, false, fmt.Errorf("failed to check for catalog creation job completion: %w", err)
	}
	if !completed {
		log.V(1).Info("Catalog creation job not yet completed, will check again later", "TaskId", catalog.TaskId)
		return catalog, nil, nil, nil, true, nil
	}

	baseLine, currentServers, err := r.handleBaselineOperations(log, dellOME, consoleClient, catalog, servers)
	if err != nil {
		return catalog, baseLine, currentServers, nil, false, fmt.Errorf("failed to create or patch or get baseline: %w", err)
	}
	completed, err = r.checkJobCompletion(consoleClient, baseLine.TaskId)
	if err != nil {
		return catalog, baseLine, currentServers, nil, false, fmt.Errorf("failed to check for Baseline creation job completion: %w", err)
	}
	if !completed {
		log.V(1).Info("Baseline creation job not yet completed, will check again later", "TaskId", baseLine.TaskId)
		return catalog, baseLine, currentServers, nil, true, nil
	}

	TargetNonComplient, err := r.checkComplianceReport(log, consoleClient, baseLine)
	if err != nil {
		return catalog, baseLine, currentServers, TargetNonComplient, false, fmt.Errorf("failed to get compliance report: %w", err)
	}
	return catalog, baseLine, currentServers, TargetNonComplient, false, nil
}

func (r *DELLFirmwareUpdateOMEReconciler) verifyServersSelected(
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
		if server.Status.Manufacturer != string(managerconsole.ManufacturerDell) {
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

func (r *DELLFirmwareUpdateOMEReconciler) requestMaintenanceOnServers(
	ctx context.Context,
	log logr.Logger,
	dellOME *maintenancev1alpha1.DELLFirmwareUpdateOME,
	servers []metalv1alpha1.Server,
) (bool, error) {

	// if Server maintenance ref is already given. no further action required.
	if dellOME.Spec.ServerMaintenanceRefs != nil && len(dellOME.Spec.ServerMaintenanceRefs) == len(servers) {
		return false, nil
	}

	// if user gave some server with serverMaintenance but not all
	// we want to request maintenance for the missing servers only.
	// find the servers which has maintenance and do not create maintenance for them.
	serverWithMaintenances := make(map[string]bool, len(servers))
	if dellOME.Spec.ServerMaintenanceRefs != nil {
		// we fetch all the references already in the Spec (self created/provided by user)
		serverMaintenances, err := r.getReferredServerMaintenances(ctx, log, dellOME.Spec.ServerMaintenanceRefs)
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
	if err := clientutils.ListAndFilterControlledBy(ctx, r.Client, dellOME, serverMaintenancesList); err != nil {
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
			serverMaintenance.Spec.Policy = dellOME.Spec.ServerMaintenancePolicy
			serverMaintenance.Spec.ServerPower = metalv1alpha1.PowerOn
			serverMaintenance.Spec.ServerRef = &corev1.LocalObjectReference{Name: server.Name}
			if serverMaintenance.Status.State != metalv1alpha1.ServerMaintenanceStateInMaintenance && serverMaintenance.Status.State != "" {
				serverMaintenance.Status.State = ""
			}
			return controllerutil.SetControllerReference(dellOME, serverMaintenance, r.Client.Scheme())
		})
		if err != nil {
			log.V(1).Error(err, "failed to create or patch serverMaintenance", "Server", server.Name)
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

	err := r.patchMaintenanceRequestRef(ctx, log, dellOME, serverMaintenanceRefs)
	if err != nil {
		return false, fmt.Errorf("failed to patch serverMaintenance ref on DELLFirmwareUpdateOME %w", err)
	}

	log.V(1).Info("Patched serverMaintenanceMap on DELLFirmwareUpdateOME")

	return true, nil
}

func (r *DELLFirmwareUpdateOMEReconciler) checkIfMaintenanceGranted(
	log logr.Logger,
	dellOME *maintenancev1alpha1.DELLFirmwareUpdateOME,
	servers []metalv1alpha1.Server,
) bool {

	if dellOME.Spec.ServerMaintenanceRefs == nil {
		return true
	}

	if len(dellOME.Spec.ServerMaintenanceRefs) != len(servers) {
		log.V(1).Info("Not all servers have Maintenance", "ServerMaintenanceRefs", dellOME.Spec.ServerMaintenanceRefs, "Servers", servers)
		return false
	}

	notInMaintenanceState := make(map[string]bool, len(servers))
	for _, server := range servers {
		if server.Status.State == metalv1alpha1.ServerStateMaintenance {
			serverMaintenanceRef, ok := r.getServerMaintenanceRefForServer(dellOME.Spec.ServerMaintenanceRefs, server.Spec.ServerMaintenanceRef.UID)
			if server.Spec.ServerMaintenanceRef == nil || !ok || server.Spec.ServerMaintenanceRef.UID != serverMaintenanceRef.UID {
				// server in maintenance for other tasks. or
				// server maintenance ref is wrong in either server or bmcVersion
				// wait for update on the server obj
				log.V(1).Info("Server is already in maintenance for other tasks",
					"Server", server.Name,
					"ServerMaintenanceRef on Server", server.Spec.ServerMaintenanceRef,
					"ServerMaintenanceRef on DELLFirmwareUpdateOME", serverMaintenanceRef,
				)
				notInMaintenanceState[server.Name] = false
			}
		} else {
			// we still need to wait for server to enter maintenance
			// wait for update on the server obj
			log.V(1).Info("Server not yet in maintenance", "Server", server.Name, "State", server.Status.State, "MaintenanceRef", server.Spec.ServerMaintenanceRef)
			notInMaintenanceState[server.Name] = false
		}
	}

	if len(notInMaintenanceState) > 0 {
		log.V(1).Info("Some servers not yet in maintenance", "req Servermaintenances on servers", dellOME.Spec.ServerMaintenanceRefs)
		return false
	}

	return true
}

func (r *DELLFirmwareUpdateOMEReconciler) checkJobCompletion(
	console *ome.OME,
	TaskId int,
) (bool, error) {
	jobDetails, err := console.GetJobDetails(TaskId)
	if err != nil {
		return false, fmt.Errorf("failed to get job details for TaskId %d with error %w", TaskId, err)
	}

	if jobDetails.Status.JobStatusID == ome.JobStatusSuccess {
		return true, nil
	}

	return false, nil
}

func (r *DELLFirmwareUpdateOMEReconciler) getServerMaintenanceRefForServer(
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

func (r *DELLFirmwareUpdateOMEReconciler) getReferredSecret(
	ctx context.Context,
	log logr.Logger,
	secretRef *corev1.LocalObjectReference,
) (map[string]string, error) {
	if secretRef == nil {
		return nil, nil
	}
	key := client.ObjectKey{Name: secretRef.Name}
	secret := &corev1.Secret{}
	if err := r.Get(ctx, key, secret); err != nil {
		log.V(1).Error(err, "failed to get referred Secret obj", "secret name", secretRef.Name)
		return nil, err
	}

	return secret.StringData, nil
}

func (r *DELLFirmwareUpdateOMEReconciler) getServersBySelector(
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

func (r *DELLFirmwareUpdateOMEReconciler) getReferredServerMaintenances(
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
			log.V(1).Error(err, "failed to get referred serverMaintenance obj", serverMaintenanceRef.ServerMaintenanceRef.Name)
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

func (r *DELLFirmwareUpdateOMEReconciler) patchMaintenanceRequestRef(
	ctx context.Context,
	log logr.Logger,
	dellOME *maintenancev1alpha1.DELLFirmwareUpdateOME,
	serverMaintenanceRefs []metalv1alpha1.ServerMaintenanceRefItem,
) error {
	dellOMEBase := dellOME.DeepCopy()

	if serverMaintenanceRefs == nil {
		dellOME.Spec.ServerMaintenanceRefs = nil
	} else {
		dellOME.Spec.ServerMaintenanceRefs = serverMaintenanceRefs
	}

	if err := r.Patch(ctx, dellOME, client.MergeFrom(dellOMEBase)); err != nil {
		log.V(1).Error(err, "failed to patch DELLFirmwareUpdateOME with ServerMaintenances ref")
		return err
	}

	return nil
}

func (r *DELLFirmwareUpdateOMEReconciler) handleBaselineOperations(
	log logr.Logger,
	dellOME *maintenancev1alpha1.DELLFirmwareUpdateOME,
	console *ome.OME,
	catalog *ome.DellCatalogDetails,
	servers []metalv1alpha1.Server,
) (*ome.DellBaseline, []metalv1alpha1.Server, error) {

	// function to create baseline payload from devices
	// used to create or patch baseline on OME
	createBaselinePayload := func(devices *ome.ODataList[ome.DellDeviceData]) *ome.DellBaseline {
		payload := &ome.DellBaseline{
			Name:             dellOME.Spec.BaselineConfig.Name,
			Description:      dellOME.Spec.BaselineConfig.Description,
			Is64Bit:          dellOME.Spec.BaselineConfig.BitType == maintenancev1alpha1.BitType64,
			RepositoryId:     catalog.Repository.Id,
			CatalogId:        catalog.Id,
			DowngradeEnabled: dellOME.Spec.BaselineConfig.DowngradeEnabled == maintenancev1alpha1.DowngradableUpdate,
			Targets:          []ome.DellTarget{},
		}

		for _, device := range devices.Value {
			payload.Targets = append(payload.Targets, ome.DellTarget{
				Id: device.Id,
				Type: ome.DellTargetType{
					Id:   device.Type,
					Name: ome.DeviceTypeMap[device.Type],
				},
			})
		}
		return payload
	}

	// function to get devices (OME device list) from Server Resource
	getDevicesFromServers := func(servers []metalv1alpha1.Server) (*ome.ODataList[ome.DellDeviceData], error) {
		serverSKU := []string{}
		for _, server := range servers {
			serverSKU = append(serverSKU, server.Status.SKU)
		}
		if len(serverSKU) == 0 {
			return nil, fmt.Errorf("no servers found to get devices from OMe")
		}
		devices, err := console.GetDevicesFromSKU(serverSKU)
		if err != nil {
			return nil, fmt.Errorf("failed to get devices from OMe for SKU %v, error %w", serverSKU, err)
		}
		return devices, nil
	}

	// function to check if existing baseline needs to be patched or is upto date with servers provided
	existingBaseline := func(baseline ome.DellBaseline, servers []metalv1alpha1.Server) (*ome.DellBaseline, []metalv1alpha1.Server, error) {
		devices, err := getDevicesFromServers(servers)
		if err != nil {
			log.V(1).Error(err, "failed to get devices from OME for servers selected")
			return nil, servers, err
		}
		equal := slices.EqualFunc(devices.Value, baseline.Targets, func(a ome.DellDeviceData, b ome.DellTarget) bool {
			return a.Id == b.Id
		})

		if equal {
			log.V(1).Info("Baseline targets match the servers selected, reusing existing baseline", "Baseline", baseline.Id, "Name", baseline.Name)
			return &baseline, servers, nil
		}

		if dellOME.Status.UpdateTask != nil {
			log.V(1).Info("Baseline is in use, cannot patch the baseline", "Baseline", baseline.Id, "Name", baseline.Name)

			// filter out the servers based on the devices in the baseline
			currentServers := make([]metalv1alpha1.Server, 0, len(devices.Value))

			serverSKUMap := make(map[string]metalv1alpha1.Server, len(servers))
			for _, server := range servers {
				serverSKUMap[server.Status.SKU] = server
			}

			devicesIdMap := make(map[int]ome.DellDeviceData, len(baseline.Targets))
			for _, device := range devices.Value {
				devicesIdMap[device.Id] = device
			}
			// add only the devices which are in the selected server spec & in the baseline
			// this is to handle the case where the server spec has changed and some servers are removed
			// but the baseline is still in use by those servers and firmware update has been started.
			// we will rack subset of server which are in the selected server spec and in the baseline
			for _, baselineDevice := range baseline.Targets {
				if device, ok := devicesIdMap[baselineDevice.Id]; !ok {
					// this device has been removed from the selected Server Spec
					// TODO: should we move to failed state if servers are removed?
					continue
				} else {
					// this device is still in the selected server Spec
					// add the server to current servers
					if server, ok := serverSKUMap[device.SKU]; ok {
						currentServers = append(currentServers, server)
					}
				}
			}
			return &baseline, currentServers, nil
		}

		payload := createBaselinePayload(devices)
		log.V(1).Info("Baseline targets do not match the servers selected, need to patch the baseline", "Baseline", baseline.Id, "Name", baseline.Name)
		PatchedBaseline, err := console.PatchBaseline(baseline.Id, payload)
		return PatchedBaseline, servers, err
	}

	baselineList, err := console.GetAllBaseline()
	if err != nil {
		log.V(1).Error(err, "failed to get baselines from OME")
		return nil, servers, err
	}
	for _, baseline := range baselineList.Value {
		if baseline.Name == dellOME.Spec.BaselineConfig.Name &&
			dellOME.Spec.BaselineConfig.Name != "" &&
			catalog.Id != 0 && baseline.CatalogId == catalog.Id &&
			catalog.Repository.Id != 0 && baseline.RepositoryId == catalog.Repository.Id {
			log.V(1).Info("Found existing baseline on OME", "Baseline", baseline.Id, "Name", baseline.Name)
			return existingBaseline(baseline, servers)
		}
	}

	devices, err := getDevicesFromServers(servers)
	if err != nil {
		log.V(1).Error(err, "failed to get devices from OME for servers selected")
		return nil, servers, err
	}
	payload := createBaselinePayload(devices)
	dellBaselineDetails, err := console.CreateBaseline(payload)
	if err != nil {
		log.V(1).Error(err, "failed to create baseline on OME")
		return nil, servers, err
	}
	log.V(1).Info("Created baseline on OME", "Baseline", dellBaselineDetails)
	return dellBaselineDetails, servers, nil
}

func (r *DELLFirmwareUpdateOMEReconciler) createOrGetCatalog(
	log logr.Logger,
	dellOME *maintenancev1alpha1.DELLFirmwareUpdateOME,
	console *ome.OME,
) (*ome.DellCatalogDetails, error) {
	catalogList, err := console.GetAllCatalogs()
	if err != nil {
		log.V(1).Error(err, "failed to get catalogs from OME")
		return nil, err
	}

	for _, catalog := range catalogList.Value {
		// if user has provided catalogRepositoryName, use that to find the catalog
		if catalog.Repository.Name == dellOME.Spec.CatalogRepositoryName && dellOME.Spec.CatalogRepositoryName != "" {
			log.V(1).Info("Found existing catalog on OME", "Catalog", catalog.Id, "Name", catalog.Repository.Name)
			err = console.RefreshCatalog([]int{catalog.Id})
			return &catalog, err
		}
		// if user has provided createCatalog spec, use that to find the catalog if controller has already created it
		if dellOME.Spec.CreateCatalog != nil &&
			catalog.Repository.Name == dellOME.Spec.CreateCatalog.Repository.Name &&
			catalog.Filename == dellOME.Spec.CreateCatalog.FileName &&
			catalog.SourcePath == dellOME.Spec.CreateCatalog.SourcePath {
			log.V(1).Info("Found existing catalog on OME matching the created catalog spec", "Catalog", catalog.Id, "Name", catalog.Repository.Name)
			err = console.RefreshCatalog([]int{catalog.Id})
			return &catalog, err
		}
	}

	// if user has provided catalogRepositoryName, and we did not find it, return error
	if dellOME.Spec.CatalogRepositoryName != "" {
		return nil, fmt.Errorf("catalog RepositoryName %s not found on OME", dellOME.Spec.CatalogRepositoryName)
	}

	// if user has not provided createCatalog spec, return error as we do not know what catalog to create
	if dellOME.Spec.CreateCatalog == nil {
		return nil, fmt.Errorf("createCatalog Spec Not Provided to create catalog on OME")
	}

	payload := &ome.DellCatalogDetails{
		Filename:   dellOME.Spec.CreateCatalog.FileName,
		SourcePath: dellOME.Spec.CreateCatalog.SourcePath,
		Repository: ome.DellCatalogRepository{
			Name:             dellOME.Spec.CreateCatalog.Repository.Name,
			Description:      dellOME.Spec.CreateCatalog.Repository.Description,
			Source:           dellOME.Spec.CreateCatalog.Repository.Source,
			DomainName:       dellOME.Spec.CreateCatalog.Repository.DomainName,
			Username:         dellOME.Spec.CreateCatalog.Repository.Username,
			Password:         dellOME.Spec.CreateCatalog.Repository.Password,
			CheckCertificate: dellOME.Spec.CreateCatalog.Repository.CheckCertificate == maintenancev1alpha1.CheckCertificateHTTPS,
			RepositoryType:   dellOME.Spec.CreateCatalog.Repository.RepositoryType,
		},
	}
	dellCatalogDetails, err := console.CreateCatalog(payload)
	if err != nil {
		log.V(1).Error(err, "failed to create catalog on OME")
		return nil, err
	}
	log.V(1).Info("Created catalog on OME", "Catalog", dellCatalogDetails)
	err = console.RefreshCatalog([]int{dellCatalogDetails.Id})
	return dellCatalogDetails, err
}

func (r *DELLFirmwareUpdateOMEReconciler) checkComplianceReport(
	log logr.Logger,
	console *ome.OME,
	baseline *ome.DellBaseline,
) ([]ome.DellTarget, error) {
	complianceReports, err := console.GetComplianceReportForBaseline(baseline.Id)
	if err != nil {
		log.V(1).Error(err, "failed to get compliance reports from OME")
		return nil, err
	}
	devicesIdBaselineMap := make(map[int]ome.DellTargetType, len(baseline.Targets))
	for _, target := range baseline.Targets {
		devicesIdBaselineMap[target.Id] = target.Type
	}
	dellTarget := make([]ome.DellTarget, 0)
	for _, complianceReport := range complianceReports.Value {
		currentSources := ""
		for _, componentReport := range complianceReport.ComponentComplianceReports {
			// if the componnent is already compliant or unknown, skip
			if componentReport.UpdateAction != "OK" && componentReport.UpdateAction != "UNKNOWN" {
				log.V(1).Info("Component needs update", "Component Report", componentReport)
				if currentSources == "" {
					currentSources = componentReport.SourceName
				} else {
					currentSources = currentSources + ";" + componentReport.SourceName
				}
			}
		}
		if currentSources == "" {
			// all components are compliant, skip this device
			continue
		}
		currentTarget := ome.DellTarget{
			Id: complianceReport.DeviceId,
			TargetType: ome.DellTargetType{
				Id:   devicesIdBaselineMap[complianceReport.DeviceId].Id,
				Name: devicesIdBaselineMap[complianceReport.DeviceId].Name,
			},
			Data: currentSources,
		}
		dellTarget = append(dellTarget, currentTarget)
	}

	return dellTarget, nil
}

func (r *DELLFirmwareUpdateOMEReconciler) performFirmwareUpdate(
	log logr.Logger,
	dellOME *maintenancev1alpha1.DELLFirmwareUpdateOME,
	console *ome.OME,
	baseline *ome.DellBaseline,
	catalog *ome.DellCatalogDetails,
	target []ome.DellTarget,
) (*ome.DellJob, error) {
	if len(target) == 0 {
		log.V(1).Info("No target devices need firmware update")
		return nil, nil
	}

	payload := &ome.DellFirmwareUpdatePayload{
		JobName:        dellOME.Name + "-firmware-update",
		JobDescription: "Firmware update job created by DELLFirmwareUpdateOME " + dellOME.Name,
		Targets:        target,
		Schedule:       dellOME.Spec.FirmwareUpgradeConfig.Schedule,
		State:          "Enabled",
		JobType: ome.DellJobType{
			JobTypeID: ome.JobTypeMap[dellOME.Spec.FirmwareUpgradeConfig.JobTypeName],
			JobType:   dellOME.Spec.FirmwareUpgradeConfig.JobTypeName,
		},
		Params: []ome.DellParams{
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
				Value: dellOME.Spec.FirmwareUpgradeConfig.OperationName,
			},
			{
				Key:   "complianceUpdate",
				Value: strconv.FormatBool(dellOME.Spec.FirmwareUpgradeConfig.ComplianceUpdate == maintenancev1alpha1.ComplianceUpdate),
			},
			{
				Key:   "signVerify",
				Value: strconv.FormatBool(dellOME.Spec.FirmwareUpgradeConfig.SignVerify == maintenancev1alpha1.SignVerify),
			},
			{
				Key:   "stagingValue",
				Value: strconv.FormatBool(dellOME.Spec.FirmwareUpgradeConfig.StagingValue == maintenancev1alpha1.StagingFirmwareStaged),
			},
		},
	}
	return console.CreateFirmwareUpdateJob(payload)
}

func (r *DELLFirmwareUpdateOMEReconciler) updateStatus(
	ctx context.Context,
	log logr.Logger,
	dellOME *maintenancev1alpha1.DELLFirmwareUpdateOME,
	state maintenancev1alpha1.FirmwareUpdateState,
	serverCount int32,
	upgradeTask *maintenancev1alpha1.DellJob,
) error {
	if dellOME.Status.State == state && upgradeTask == nil {
		return nil
	}

	dellOMEBase := dellOME.DeepCopy()
	dellOME.Status.State = state
	dellOME.Status.UpdateTask = upgradeTask
	dellOME.Status.ServerCount = serverCount

	if err := r.Status().Patch(ctx, dellOME, client.MergeFrom(dellOMEBase)); err != nil {
		return fmt.Errorf("failed to patch BMCVersion status: %w", err)
	}

	log.V(1).Info("Updated DELLFirmwareUpdateOME state ",
		"State", state,
		"Upgrade Task", dellOME.Status.UpdateTask,
	)

	return nil
}

func (r *DELLFirmwareUpdateOMEReconciler) enqueueByServerRefs(
	ctx context.Context,
	obj client.Object,
) []ctrl.Request {
	log := ctrl.LoggerFrom(ctx)
	host := obj.(*metalv1alpha1.Server)

	// return early if hosts are not required states
	if host.Status.State != metalv1alpha1.ServerStateMaintenance || host.Spec.ServerMaintenanceRef == nil {
		return nil
	}

	dellOMEList := &maintenancev1alpha1.DELLFirmwareUpdateOMEList{}
	if err := r.List(ctx, dellOMEList); err != nil {
		log.Error(err, "failed to list DELLFirmwareUpdateOME")
		return nil
	}
	var req []ctrl.Request

	for _, dellOME := range dellOMEList.Items {
		// if we dont have maintenance request on this dellOME we do not want to queue changes from servers.
		if dellOME.Spec.ServerMaintenanceRefs == nil {
			continue
		}
		if dellOME.Status.State == maintenancev1alpha1.FirmwareUpdateStateCompleted || dellOME.Status.State == maintenancev1alpha1.FirmwareUpdateStateFailed {
			continue
		}
		serverMaintenanceRef, ok := r.getServerMaintenanceRefForServer(dellOME.Spec.ServerMaintenanceRefs, host.Spec.ServerMaintenanceRef.UID)
		if ok && serverMaintenanceRef != nil {
			req = append(req, ctrl.Request{
				NamespacedName: types.NamespacedName{Namespace: dellOME.Namespace, Name: dellOME.Name},
			})
		}
	}
	return req
}

// SetupWithManager sets up the controller with the Manager.
func (r *DELLFirmwareUpdateOMEReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&maintenancev1alpha1.DELLFirmwareUpdateOME{}).
		Owns(&metalv1alpha1.ServerMaintenance{}).
		Watches(&metalv1alpha1.Server{}, handler.EnqueueRequestsFromMapFunc(r.enqueueByServerRefs)).
		Complete(r)
}
