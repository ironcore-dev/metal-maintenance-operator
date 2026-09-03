// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package system

import (
	"context"
	"errors"
	"fmt"

	"github.com/ironcore-dev/controller-utils/conditionutils"
	systemv1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/system/v1alpha1"
	utils "github.com/ironcore-dev/metal-maintenance-operator/internal/utils"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	"github.com/ironcore-dev/metal-operator/bmc"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// ConditionRepositoryCheckIssued/Completed track the dry-run
	// (ApplyUpdate=false) InstallFromRepository call used to discover whether
	// any packages in the configured catalog are pending installation.
	ConditionRepositoryCheckIssued    = "RepositoryCheckIssued"
	ConditionRepositoryCheckCompleted = "RepositoryCheckCompleted"

	// ConditionRepositoryUpdateIssued/Completed track the apply
	// (ApplyUpdate=true) InstallFromRepository call that actually installs the
	// pending packages.
	ConditionRepositoryUpdateIssued    = "RepositoryUpdateIssued"
	ConditionRepositoryUpdateCompleted = "RepositoryUpdateCompleted"

	// ConditionComponentJobsCompleted tracks the per-component iDRAC jobs
	// spawned by the apply call.
	ConditionComponentJobsCompleted = "ComponentJobsCompleted"

	ReasonRepositoryCheckIssued     = "RepositoryCheckIssuedToBMC"
	ReasonRepositoryCheckCompleted  = "RepositoryCheckCompleted"
	ReasonRepositoryCheckFailed     = "RepositoryCheckFailed"
	ReasonRepositoryUpdateIssued    = "RepositoryUpdateIssuedToBMC"
	ReasonRepositoryUpdateCompleted = "RepositoryUpdateCompleted"
	ReasonRepositoryUpdateFailed    = "RepositoryUpdateFailed"
	ReasonComponentJobsCompleted    = "ComponentJobsCompleted"
	ReasonComponentJobFailed        = "ComponentJobFailed"
)

type dellHandler struct{}

func (dh *dellHandler) handlePending(ctx context.Context, fw *systemv1alpha1.FirmwareUpdate, bmcClient bmc.BMC, r *FirmwareUpdateReconciler, server *metalv1alpha1.Server) (bool, error) {
	updater, ok := bmcClient.(bmc.FirmwareUpdaterDell)
	if !ok {
		return false, fmt.Errorf("repository-based firmware update not supported by this vendor: %w", bmc.ErrNotSupported)
	}
	return dh.processRepositoryCheck(ctx, updater, fw, r, server)
}

func (dh *dellHandler) handleInProgress(ctx context.Context, fw *systemv1alpha1.FirmwareUpdate, bmcClient bmc.BMC, r *FirmwareUpdateReconciler, server *metalv1alpha1.Server) (bool, error) {
	updater, ok := bmcClient.(bmc.FirmwareUpdaterDell)
	if !ok {
		return false, fmt.Errorf("repository-based firmware update not supported by this vendor: %w", bmc.ErrNotSupported)
	}
	inMaintenance, err := r.handleServerMaintenance(ctx, bmcClient, fw, server)
	if err != nil {
		return false, err
	}
	if !inMaintenance {
		return false, nil
	}
	return dh.processInProgress(ctx, updater, fw, r, server)
}

func (dh *dellHandler) handleCompleted(ctx context.Context, fw *systemv1alpha1.FirmwareUpdate, bmcClient bmc.BMC, r *FirmwareUpdateReconciler, server *metalv1alpha1.Server) (bool, error) {
	updater, ok := bmcClient.(bmc.FirmwareUpdaterDell)
	if !ok {
		return false, fmt.Errorf("repository-based firmware update not supported by this vendor: %w", bmc.ErrNotSupported)
	}
	return dh.processRepositoryCheck(ctx, updater, fw, r, server)
}

// processRepositoryCheck drives the read-only dry-run RepositoryCheck used
// while the FirmwareUpdate is Pending (first-time check) or Completed
// (periodic drift-detection). The check is a plain Redfish call that neither
// changes the system nor requires a reboot, so it is safe to issue without
// ever requesting ServerMaintenance. Only once the check confirms packages
// are actually pending installation does this transition into InProgress,
// where the update is actually applied.
func (dh *dellHandler) processRepositoryCheck(ctx context.Context, updater bmc.FirmwareUpdaterDell, fw *systemv1alpha1.FirmwareUpdate, r *FirmwareUpdateReconciler, server *metalv1alpha1.Server) (bool, error) {
	checkIssued, err := utils.GetCondition(r.Conditions, fw.Status.Conditions, ConditionRepositoryCheckIssued)
	if err != nil {
		return false, err
	}
	if checkIssued.Status != metav1.ConditionTrue {
		return dh.issueRepositoryCheck(ctx, updater, fw, r, server, checkIssued)
	}

	checkCompleted, err := utils.GetCondition(r.Conditions, fw.Status.Conditions, ConditionRepositoryCheckCompleted)
	if err != nil {
		return false, err
	}
	return dh.pollRepositoryCheck(ctx, updater, fw, r, server, checkCompleted)
}

// processInProgress drives the actual repository-based firmware update once
// processRepositoryCheck has confirmed packages are pending installation.
// It applies the update and tracks the component jobs it spawns. Once the
// apply completes, control is handed back to Completed, whose dry-run check
// confirms convergence (or discovers further pending packages and re-enters
// InProgress).
func (dh *dellHandler) processInProgress(ctx context.Context, updater bmc.FirmwareUpdaterDell, fw *systemv1alpha1.FirmwareUpdate, r *FirmwareUpdateReconciler, server *metalv1alpha1.Server) (bool, error) {
	updateIssued, err := utils.GetCondition(r.Conditions, fw.Status.Conditions, ConditionRepositoryUpdateIssued)
	if err != nil {
		return false, err
	}
	if updateIssued.Status != metav1.ConditionTrue {
		return dh.issueRepositoryUpdate(ctx, updater, fw, r, server, updateIssued)
	}

	updateCompleted, err := utils.GetCondition(r.Conditions, fw.Status.Conditions, ConditionRepositoryUpdateCompleted)
	if err != nil {
		return false, err
	}
	if updateCompleted.Status != metav1.ConditionTrue {
		return dh.pollRepositoryUpdate(ctx, updater, fw, r, updateCompleted)
	}

	componentsCompleted, err := utils.GetCondition(r.Conditions, fw.Status.Conditions, ConditionComponentJobsCompleted)
	if err != nil {
		return false, err
	}
	if componentsCompleted.Status != metav1.ConditionTrue {
		return dh.trackComponentJobs(ctx, updater, fw, r, componentsCompleted)
	}

	// This pass's apply and component-job tracking are done. Hand back to
	// Completed: its dry-run check is what actually re-verifies convergence
	// (and re-enters InProgress, bounded by MaxRepositoryPasses, if further
	// packages are found pending).
	ctrl.LoggerFrom(ctx).V(1).Info("Repository update pass completed, handing back to Completed for re-verification", "Server", server.Name)
	fwBase := fw.DeepCopy()
	fw.Status.State = systemv1alpha1.FirmwareUpdateStateCompleted
	fw.Status.ObservedGeneration = fw.Generation
	fw.Status.Conditions = []metav1.Condition{}
	fw.Status.CheckJob = nil
	fw.Status.UpdateJob = nil
	fw.Status.ComponentJobs = nil
	fw.Status.BaselineJobIDs = nil
	// PassCount is intentionally preserved (not reset) here: it is only reset
	// once a repository check actually confirms convergence (no packages
	// pending), so persistently-pending catalogs remain bounded by
	// MaxRepositoryPasses across multiple apply attempts.
	return false, r.Status().Patch(ctx, fw, client.MergeFrom(fwBase))
}

func (dh *dellHandler) issueRepositoryCheck(ctx context.Context, updater bmc.FirmwareUpdaterDell, fw *systemv1alpha1.FirmwareUpdate, r *FirmwareUpdateReconciler, server *metalv1alpha1.Server, condition *metav1.Condition) (bool, error) {
	log := ctrl.LoggerFrom(ctx)
	parameters, err := buildRepositoryParameters(ctx, r, fw, false)
	if err != nil {
		return false, fmt.Errorf("failed to build repository parameters: %w", err)
	}

	jobID, isFatal, err := updater.InstallFirmwareFromRepository(ctx, server.Spec.SystemURI, parameters)
	if err != nil {
		if isFatal {
			log.Error(err, "Failed to issue repository check", "Server", server.Name)
			if condErr := r.Conditions.Update(
				condition,
				conditionutils.UpdateStatus(corev1.ConditionFalse),
				conditionutils.UpdateReason(ReasonRepositoryCheckFailed),
				conditionutils.UpdateMessage(fmt.Sprintf("Failed to issue repository check: %v", err)),
			); condErr != nil {
				return false, errors.Join(err, condErr)
			}
			return false, r.updateStatus(ctx, fw, systemv1alpha1.FirmwareUpdateStateFailed, condition)
		}
		return false, err
	}

	if err := r.Conditions.Update(
		condition,
		conditionutils.UpdateStatus(corev1.ConditionTrue),
		conditionutils.UpdateReason(ReasonRepositoryCheckIssued),
		conditionutils.UpdateMessage(fmt.Sprintf("Issued repository check job %v", jobID)),
	); err != nil {
		return false, fmt.Errorf("failed to update RepositoryCheckIssued condition: %w", err)
	}

	return false, r.patchProgress(ctx, fw, fw.Status.State, condition, func(status *systemv1alpha1.FirmwareUpdateStatus) {
		status.CheckJob = &systemv1alpha1.RepositoryJob{JobID: jobID}
	})
}

func (dh *dellHandler) pollRepositoryCheck(ctx context.Context, updater bmc.FirmwareUpdaterDell, fw *systemv1alpha1.FirmwareUpdate, r *FirmwareUpdateReconciler, server *metalv1alpha1.Server, condition *metav1.Condition) (bool, error) {
	log := ctrl.LoggerFrom(ctx)
	if fw.Status.CheckJob == nil || fw.Status.CheckJob.JobID == "" {
		return false, fmt.Errorf("missing check job ID while polling repository check")
	}

	job, err := updater.GetJob(ctx, "", fw.Status.CheckJob.JobID)
	if err != nil {
		log.V(1).Info("Failed to fetch repository check job, retrying", "error", err)
		return true, nil
	}
	repoJob := toRepositoryJob(job)

	if !job.IsTerminal() {
		return true, r.patchProgress(ctx, fw, fw.Status.State, nil, func(status *systemv1alpha1.FirmwareUpdateStatus) {
			status.CheckJob = &repoJob
		})
	}

	if job.IsFailed() {
		if err := r.Conditions.Update(
			condition,
			conditionutils.UpdateStatus(corev1.ConditionTrue),
			conditionutils.UpdateReason(ReasonRepositoryCheckFailed),
			conditionutils.UpdateMessage(fmt.Sprintf("Repository check job failed: %v", job.Message)),
		); err != nil {
			return false, fmt.Errorf("failed to update RepositoryCheckCompleted condition: %w", err)
		}
		return false, r.patchProgress(ctx, fw, systemv1alpha1.FirmwareUpdateStateFailed, condition, func(status *systemv1alpha1.FirmwareUpdateStatus) {
			status.CheckJob = &repoJob
		})
	}

	hasPending, _, err := updater.GetRepositoryUpdateList(ctx, server.Spec.SystemURI)
	if err != nil {
		log.V(1).Info("Failed to fetch repository update list, retrying", "error", err)
		return true, nil
	}

	if !hasPending {
		log.V(1).Info("Repository-based firmware update up to date", "Server", server.Name)
		if err := r.cleanupServerMaintenanceReferences(ctx, fw); err != nil {
			return false, err
		}
		// Record the successful convergence explicitly (mirroring the
		// RepositoryUpdate success path) instead of silently wiping
		// conditions, so `kubectl describe` shows why/when the last check
		// concluded rather than just the bare Completed state.
		if err := r.Conditions.Update(
			condition,
			conditionutils.UpdateStatus(corev1.ConditionTrue),
			conditionutils.UpdateReason(ReasonRepositoryCheckCompleted),
			conditionutils.UpdateMessage("Repository check found no packages pending installation"),
		); err != nil {
			return false, fmt.Errorf("failed to update RepositoryCheckCompleted condition: %w", err)
		}
		fwBase := fw.DeepCopy()
		fw.Status.State = systemv1alpha1.FirmwareUpdateStateCompleted
		fw.Status.ObservedGeneration = fw.Generation
		// Conditions are reset to only this one (rather than merged via
		// patchProgress) because the stale RepositoryCheckIssued condition
		// must not survive: processRepositoryCheck keys off it to decide
		// whether to issue a fresh check on the next periodic drift-check
		// reconcile of the Completed state.
		fw.Status.Conditions = []metav1.Condition{*condition}
		fw.Status.CheckJob = nil
		fw.Status.UpdateJob = nil
		fw.Status.ComponentJobs = nil
		fw.Status.ComponentJobsSummary = nil
		fw.Status.BaselineJobIDs = nil
		fw.Status.PassCount = 0
		return false, r.Status().Patch(ctx, fw, client.MergeFrom(fwBase))
	}

	// Packages are pending installation: bound how many times we allow a
	// check to (re-)discover pending packages before giving up.
	passCount := fw.Status.PassCount + 1
	if r.MaxRepositoryPasses > 0 && passCount > r.MaxRepositoryPasses {
		log.Info("Exceeded maximum repository update passes, marking as Failed", "PassCount", passCount, "MaxRepositoryPasses", r.MaxRepositoryPasses)
		if err := r.Conditions.Update(
			condition,
			conditionutils.UpdateStatus(corev1.ConditionTrue),
			conditionutils.UpdateReason(ReasonRepositoryCheckFailed),
			conditionutils.UpdateMessage(fmt.Sprintf("Exceeded maximum of %d repository update passes", r.MaxRepositoryPasses)),
		); err != nil {
			return false, fmt.Errorf("failed to update RepositoryCheckCompleted condition: %w", err)
		}
		fwBase := fw.DeepCopy()
		fw.Status.State = systemv1alpha1.FirmwareUpdateStateFailed
		fw.Status.ObservedGeneration = fw.Generation
		fw.Status.PassCount = passCount
		fw.Status.CheckJob = &repoJob
		fw.Status.Conditions = []metav1.Condition{*condition}
		return false, r.Status().Patch(ctx, fw, client.MergeFrom(fwBase))
	}

	// The dry-run check confirmed packages are pending installation: hand off
	// to InProgress, which is where ServerMaintenance is requested and the
	// update is actually applied. Check-phase conditions/job-tracking are
	// wiped since they no longer apply once InProgress takes over.
	log.V(1).Info("Repository check found pending packages, entering InProgress", "Server", server.Name, "PassCount", passCount)
	fwBase := fw.DeepCopy()
	fw.Status.State = systemv1alpha1.FirmwareUpdateStateInProgress
	fw.Status.ObservedGeneration = fw.Generation
	fw.Status.PassCount = passCount
	fw.Status.Conditions = []metav1.Condition{}
	fw.Status.CheckJob = nil
	return false, r.Status().Patch(ctx, fw, client.MergeFrom(fwBase))
}

func (dh *dellHandler) issueRepositoryUpdate(ctx context.Context, updater bmc.FirmwareUpdaterDell, fw *systemv1alpha1.FirmwareUpdate, r *FirmwareUpdateReconciler, server *metalv1alpha1.Server, condition *metav1.Condition) (bool, error) {
	log := ctrl.LoggerFrom(ctx)
	// Snapshot the jobs known to the BMC before issuing the apply call, so
	// newly spawned component jobs can be discovered by diffing against this
	// baseline once the apply job itself completes.
	if !fw.Status.BaselineJobsCaptured {
		jobIDs, err := updater.ListJobs(ctx, "")
		if err != nil {
			log.V(1).Info("Failed to list jobs for baseline snapshot, retrying", "error", err)
			return true, nil
		}
		if jobIDs == nil {
			jobIDs = []string{}
		}
		return true, r.patchProgress(ctx, fw, fw.Status.State, nil, func(status *systemv1alpha1.FirmwareUpdateStatus) {
			status.BaselineJobIDs = jobIDs
			status.BaselineJobsCaptured = true
		})
	}

	parameters, err := buildRepositoryParameters(ctx, r, fw, true)
	if err != nil {
		return false, fmt.Errorf("failed to build repository parameters: %w", err)
	}

	jobID, isFatal, err := updater.InstallFirmwareFromRepository(ctx, server.Spec.SystemURI, parameters)
	if err != nil {
		if isFatal {
			log.Error(err, "Failed to issue repository update", "Server", server.Name)
			if condErr := r.Conditions.Update(
				condition,
				conditionutils.UpdateStatus(corev1.ConditionFalse),
				conditionutils.UpdateReason(ReasonRepositoryUpdateFailed),
				conditionutils.UpdateMessage(fmt.Sprintf("Failed to issue repository update: %v", err)),
			); condErr != nil {
				return false, errors.Join(err, condErr)
			}
			return false, r.updateStatus(ctx, fw, systemv1alpha1.FirmwareUpdateStateFailed, condition)
		}
		return false, err
	}

	if err := r.Conditions.Update(
		condition,
		conditionutils.UpdateStatus(corev1.ConditionTrue),
		conditionutils.UpdateReason(ReasonRepositoryUpdateIssued),
		conditionutils.UpdateMessage(fmt.Sprintf("Issued repository update job %v", jobID)),
	); err != nil {
		return false, fmt.Errorf("failed to update RepositoryUpdateIssued condition: %w", err)
	}

	return false, r.patchProgress(ctx, fw, fw.Status.State, condition, func(status *systemv1alpha1.FirmwareUpdateStatus) {
		status.UpdateJob = &systemv1alpha1.RepositoryJob{JobID: jobID}
	})
}

func (dh *dellHandler) pollRepositoryUpdate(ctx context.Context, updater bmc.FirmwareUpdaterDell, fw *systemv1alpha1.FirmwareUpdate, r *FirmwareUpdateReconciler, condition *metav1.Condition) (bool, error) {
	log := ctrl.LoggerFrom(ctx)
	if fw.Status.UpdateJob == nil || fw.Status.UpdateJob.JobID == "" {
		return false, fmt.Errorf("missing update job ID while polling repository update")
	}

	job, err := updater.GetJob(ctx, "", fw.Status.UpdateJob.JobID)
	if err != nil {
		log.V(1).Info("Failed to fetch repository update job, retrying", "error", err)
		return true, nil
	}
	repoJob := toRepositoryJob(job)

	if !job.IsTerminal() {
		return true, r.patchProgress(ctx, fw, fw.Status.State, nil, func(status *systemv1alpha1.FirmwareUpdateStatus) {
			status.UpdateJob = &repoJob
		})
	}

	if job.IsFailed() {
		if err := r.Conditions.Update(
			condition,
			conditionutils.UpdateStatus(corev1.ConditionTrue),
			conditionutils.UpdateReason(ReasonRepositoryUpdateFailed),
			conditionutils.UpdateMessage(fmt.Sprintf("Repository update job failed: %v", job.Message)),
		); err != nil {
			return false, fmt.Errorf("failed to update RepositoryUpdateCompleted condition: %w", err)
		}
		return false, r.patchProgress(ctx, fw, systemv1alpha1.FirmwareUpdateStateFailed, condition, func(status *systemv1alpha1.FirmwareUpdateStatus) {
			status.UpdateJob = &repoJob
		})
	}

	if err := r.Conditions.Update(
		condition,
		conditionutils.UpdateStatus(corev1.ConditionTrue),
		conditionutils.UpdateReason(ReasonRepositoryUpdateCompleted),
		conditionutils.UpdateMessage("Repository update job completed"),
	); err != nil {
		return false, fmt.Errorf("failed to update RepositoryUpdateCompleted condition: %w", err)
	}
	return false, r.patchProgress(ctx, fw, fw.Status.State, condition, func(status *systemv1alpha1.FirmwareUpdateStatus) {
		status.UpdateJob = &repoJob
	})
}

func (dh *dellHandler) trackComponentJobs(ctx context.Context, updater bmc.FirmwareUpdaterDell, fw *systemv1alpha1.FirmwareUpdate, r *FirmwareUpdateReconciler, condition *metav1.Condition) (bool, error) {
	log := ctrl.LoggerFrom(ctx)
	jobIDs, err := updater.ListJobs(ctx, "")
	if err != nil {
		log.V(1).Info("Failed to list jobs for component tracking, retrying", "error", err)
		return true, nil
	}

	known := make(map[string]struct{}, len(fw.Status.BaselineJobIDs)+1)
	for _, id := range fw.Status.BaselineJobIDs {
		known[id] = struct{}{}
	}
	if fw.Status.UpdateJob != nil {
		known[fw.Status.UpdateJob.JobID] = struct{}{}
	}

	componentJobs := make([]systemv1alpha1.RepositoryJob, 0, len(jobIDs))
	summary := &systemv1alpha1.ComponentJobsSummary{}
	allTerminal := true
	anyFailed := false
	for _, id := range jobIDs {
		if _, ok := known[id]; ok {
			continue
		}
		job, err := updater.GetJob(ctx, "", id)
		if err != nil {
			log.V(1).Info("Failed to fetch component job, retrying", "JobID", id, "error", err)
			return true, nil
		}
		componentJobs = append(componentJobs, toRepositoryJob(job))
		summary.Total++
		switch {
		case job.IsFailed():
			anyFailed = true
			summary.Failed++
		case job.IsCompleted():
			summary.Completed++
		default:
			summary.InProgress++
		}
		if !job.IsTerminal() {
			allTerminal = false
		}
	}

	if !allTerminal {
		return true, r.patchProgress(ctx, fw, fw.Status.State, nil, func(status *systemv1alpha1.FirmwareUpdateStatus) {
			status.ComponentJobs = componentJobs
			status.ComponentJobsSummary = summary
		})
	}

	if anyFailed {
		if err := r.Conditions.Update(
			condition,
			conditionutils.UpdateStatus(corev1.ConditionTrue),
			conditionutils.UpdateReason(ReasonComponentJobFailed),
			conditionutils.UpdateMessage("One or more component firmware jobs failed"),
		); err != nil {
			return false, fmt.Errorf("failed to update ComponentJobsCompleted condition: %w", err)
		}
		return false, r.patchProgress(ctx, fw, systemv1alpha1.FirmwareUpdateStateFailed, condition, func(status *systemv1alpha1.FirmwareUpdateStatus) {
			status.ComponentJobs = componentJobs
			status.ComponentJobsSummary = summary
		})
	}

	if err := r.Conditions.Update(
		condition,
		conditionutils.UpdateStatus(corev1.ConditionTrue),
		conditionutils.UpdateReason(ReasonComponentJobsCompleted),
		conditionutils.UpdateMessage("All component firmware jobs completed"),
	); err != nil {
		return false, fmt.Errorf("failed to update ComponentJobsCompleted condition: %w", err)
	}
	return false, r.patchProgress(ctx, fw, fw.Status.State, condition, func(status *systemv1alpha1.FirmwareUpdateStatus) {
		status.ComponentJobs = componentJobs
		status.ComponentJobsSummary = summary
	})
}

// buildRepositoryParameters translates the FirmwareUpdate's Repository
// spec (and, if configured, its Secret credentials) into bmc.RepositoryUpdateParameters.
func buildRepositoryParameters(ctx context.Context, r *FirmwareUpdateReconciler, fw *systemv1alpha1.FirmwareUpdate, applyUpdate bool) (*bmc.RepositoryUpdateParameters, error) {
	if fw.Spec.Repository == nil {
		return nil, fmt.Errorf("firmware update has no repository configured")
	}
	repo := fw.Spec.Repository

	var username, password string
	if repo.CredentialsRef != nil {
		var err error
		username, password, err = utils.GetImageCredentialsForSecretRef(ctx, r.Client, repo.CredentialsRef)
		if err != nil {
			return nil, fmt.Errorf("failed to get repository credentials: %w", err)
		}
	}

	catalogFile := repo.CatalogFile
	if catalogFile == "" {
		catalogFile = "Catalog.xml"
	}

	return &bmc.RepositoryUpdateParameters{
		ShareType:              string(repo.ShareType),
		IPAddress:              repo.Address,
		ShareName:              repo.ShareName,
		CatalogFile:            catalogFile,
		UserName:               username,
		Password:               password,
		Workgroup:              repo.Workgroup,
		IgnoreCertWarning:      ptr.Deref(repo.IgnoreCertWarning, false),
		ApplyUpdate:            applyUpdate,
		RebootNeeded:           applyUpdate && repo.RebootNeeded,
		ApplySameVersions:      ptr.Deref(repo.ApplySameVersions, false),
		ApplyDowngradeVersions: ptr.Deref(repo.ApplyDowngradeVersions, false),
	}, nil
}

func toRepositoryJob(job *bmc.DellJob) systemv1alpha1.RepositoryJob {
	return systemv1alpha1.RepositoryJob{
		JobID:           job.ID,
		Name:            job.Name,
		JobType:         job.JobType,
		State:           job.State,
		Message:         job.Message,
		PercentComplete: job.PercentComplete,
	}
}
