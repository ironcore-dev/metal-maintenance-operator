// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package system

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

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

	// ConditionRepositoryUpdatePowerOnIssued tracks a PowerOn request issued to
	// the server before applying the repository update, when the server was
	// found powered off.
	ConditionRepositoryUpdatePowerOnIssued = "RepositoryUpdatePowerOnIssued"

	// ConditionScheduledJobStuckPowerOnIssued tracks a PowerOn request issued
	// because the repository-update apply job remained in Dell's "Scheduled"
	// state (queued, awaiting a reboot/power cycle to execute) with the
	// server powered off and no observed progress for longer than
	// FirmwareUpdateReconciler.scheduledJobPowerOnTimeout().
	ConditionScheduledJobStuckPowerOnIssued = "ScheduledJobStuckPowerOnIssued"

	ReasonRepositoryCheckIssued          = "RepositoryCheckIssuedToBMC"
	ReasonRepositoryCheckCompleted       = "RepositoryCheckCompleted"
	ReasonRepositoryCheckFailed          = "RepositoryCheckFailed"
	ReasonRepositoryUpdateIssued         = "RepositoryUpdateIssuedToBMC"
	ReasonRepositoryUpdateCompleted      = "RepositoryUpdateCompleted"
	ReasonRepositoryUpdateFailed         = "RepositoryUpdateFailed"
	ReasonComponentJobsCompleted         = "ComponentJobsCompleted"
	ReasonComponentJobFailed             = "ComponentJobFailed"
	ReasonScheduledJobStuckPowerOnIssued = "ScheduledJobStuckPowerOnIssued"
)

type dellHandler struct{}

// dellStatus returns status.DellStatus, lazily initializing it if nil. Callers use this from
// within patchProgress mutate closures to safely set Dell-specific status fields.
func dellStatus(status *systemv1alpha1.FirmwareUpdateStatus) *systemv1alpha1.DellFirmwareUpdateStatus {
	if status.DellStatus == nil {
		status.DellStatus = &systemv1alpha1.DellFirmwareUpdateStatus{}
	}
	return status.DellStatus
}

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
	return dh.processInProgress(ctx, bmcClient, updater, fw, r, server)
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
	log := ctrl.LoggerFrom(ctx)
	checkIssued, err := utils.GetCondition(r.Conditions, fw.Status.Conditions, ConditionRepositoryCheckIssued)
	if err != nil {
		return false, err
	}
	if checkIssued.Status != metav1.ConditionTrue {

		checkCompleted, err := utils.GetCondition(r.Conditions, fw.Status.Conditions, ConditionRepositoryCheckCompleted)
		if err != nil {
			return false, err
		}
		repositoryCheckInterval := r.repositoryCheckInterval()
		if checkCompleted.Status == metav1.ConditionTrue &&
			checkCompleted.Reason == ReasonRepositoryCheckCompleted &&
			repositoryCheckInterval > 0 &&
			fw.Status.ObservedGeneration == fw.Generation {
			age := time.Since(checkCompleted.LastTransitionTime.Time)
			if age < repositoryCheckInterval {
				log.V(1).Info("RepositoryCheck recently completed, deferring next periodic check",
					"Server", server.Name, "Age", age, "RepositoryCheckInterval", repositoryCheckInterval)
				// This deferral is the intended, healthy steady-state for a Completed update
				// waiting out its periodic recheck cadence, not a stuck condition - refresh
				// LastProgressTime so checkProgressDeadline doesn't mistake it for one.
				fwBase := fw.DeepCopy()
				fw.Status.LastProgressTime = new(metav1.Now())
				if err := r.Status().Patch(ctx, fw, client.MergeFrom(fwBase)); err != nil {
					return false, fmt.Errorf("failed to refresh LastProgressTime while deferring periodic check: %w", err)
				}
				return true, nil
			}
			log.V(1).Info("RepositoryCheck stale, issuing periodic drift-detection check",
				"Server", server.Name, "Age", age, "RepositoryCheckInterval", repositoryCheckInterval)
		} else if checkCompleted.Status == metav1.ConditionTrue &&
			checkCompleted.Reason == ReasonRepositoryCheckCompleted &&
			fw.Status.ObservedGeneration != fw.Generation {
			log.V(1).Info("FirmwareUpdate spec changed since last RepositoryCheck, bypassing periodic check interval",
				"Server", server.Name)
		} else {
			log.V(1).Info("RepositoryCheck not yet issued, issuing dry-run check", "Server", server.Name)
		}
		return dh.issueRepositoryCheck(ctx, updater, fw, r, server, checkIssued)
	}

	checkCompleted, err := utils.GetCondition(r.Conditions, fw.Status.Conditions, ConditionRepositoryCheckCompleted)
	if err != nil {
		return false, err
	}
	log.V(1).Info("RepositoryCheck issued, polling for completion", "Server", server.Name, "JobID", checkJobID(fw))
	return dh.pollRepositoryCheck(ctx, updater, fw, r, server, checkCompleted)
}

// checkJobID returns the currently tracked repository-check job ID, or "" if
// none is recorded yet. Used only for log context.
func checkJobID(fw *systemv1alpha1.FirmwareUpdate) string {
	if fw.Status.DellStatus == nil || fw.Status.DellStatus.CheckJob == nil {
		return ""
	}
	return fw.Status.DellStatus.CheckJob.JobID
}

// updateJobID returns the currently tracked repository-update (apply) job ID,
// or "" if none is recorded yet. Used only for log context.
func updateJobID(fw *systemv1alpha1.FirmwareUpdate) string {
	if fw.Status.DellStatus == nil || fw.Status.DellStatus.UpdateJob == nil {
		return ""
	}
	return fw.Status.DellStatus.UpdateJob.JobID
}

// processInProgress drives the actual repository-based firmware update once
// processRepositoryCheck has confirmed packages are pending installation.
// It applies the update and tracks the component jobs it spawns. Once the
// apply completes, control is handed back to Completed, whose dry-run check
// confirms convergence (or discovers further pending packages and re-enters
// InProgress).
func (dh *dellHandler) processInProgress(ctx context.Context, bmcClient bmc.BMC, updater bmc.FirmwareUpdaterDell, fw *systemv1alpha1.FirmwareUpdate, r *FirmwareUpdateReconciler, server *metalv1alpha1.Server) (bool, error) {
	log := ctrl.LoggerFrom(ctx)
	updateIssued, err := utils.GetCondition(r.Conditions, fw.Status.Conditions, ConditionRepositoryUpdateIssued)
	if err != nil {
		return false, err
	}
	if updateIssued.Status != metav1.ConditionTrue {
		log.V(1).Info("RepositoryUpdate not yet issued, ensuring server is powered on before applying", "Server", server.Name)
		if requeue, err := dh.ensureServerPoweredOn(ctx, bmcClient, fw, r, server); requeue || err != nil {
			return requeue, err
		}
		log.V(1).Info("Server is powered on, issuing repository update apply call", "Server", server.Name)
		return dh.issueRepositoryUpdate(ctx, updater, fw, r, server, updateIssued)
	}

	updateCompleted, err := utils.GetCondition(r.Conditions, fw.Status.Conditions, ConditionRepositoryUpdateCompleted)
	if err != nil {
		return false, err
	}
	if updateCompleted.Status != metav1.ConditionTrue {
		log.V(1).Info("RepositoryUpdate issued, polling apply job for completion", "Server", server.Name, "JobID", updateJobID(fw))
		return dh.pollRepositoryUpdate(ctx, updater, fw, r, updateCompleted)
	}

	componentsCompleted, err := utils.GetCondition(r.Conditions, fw.Status.Conditions, ConditionComponentJobsCompleted)
	if err != nil {
		return false, err
	}
	if componentsCompleted.Status != metav1.ConditionTrue {
		log.V(1).Info("RepositoryUpdate apply job completed, tracking spawned component firmware jobs", "Server", server.Name)
		return dh.trackComponentJobs(ctx, bmcClient, updater, fw, r, server, componentsCompleted)
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
	fw.Status.DellStatus = nil
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

	log.V(1).Info("Calling InstallFirmwareFromRepository (dry-run)", "Server", server.Name, "Address", parameters.IPAddress, "ShareName", parameters.ShareName, "CatalogFile", parameters.CatalogFile)
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

	log.V(1).Info("Repository check job issued", "Server", server.Name, "JobID", jobID)
	if err := r.Conditions.Update(
		condition,
		conditionutils.UpdateStatus(corev1.ConditionTrue),
		conditionutils.UpdateReason(ReasonRepositoryCheckIssued),
		conditionutils.UpdateMessage(fmt.Sprintf("Issued repository check job %v", jobID)),
	); err != nil {
		return false, fmt.Errorf("failed to update RepositoryCheckIssued condition: %w", err)
	}

	// Explicitly flip RepositoryCheckCompleted to False while a check is
	// in flight. This makes its eventual False->True flip (on confirmed
	// convergence)
	checkCompleted, err := utils.GetCondition(r.Conditions, fw.Status.Conditions, ConditionRepositoryCheckCompleted)
	if err != nil {
		return false, err
	}
	if err := r.Conditions.Update(
		checkCompleted,
		conditionutils.UpdateStatus(corev1.ConditionFalse),
		conditionutils.UpdateReason(ReasonRepositoryCheckIssued),
		conditionutils.UpdateMessage("Repository check in progress"),
	); err != nil {
		return false, fmt.Errorf("failed to update RepositoryCheckCompleted condition: %w", err)
	}

	return false, r.patchProgress(ctx, fw, fw.Status.State, condition, func(status *systemv1alpha1.FirmwareUpdateStatus) {
		dellStatus(status).CheckJob = &systemv1alpha1.RepositoryJob{JobID: jobID}
	}, checkCompleted)
}

func (dh *dellHandler) pollRepositoryCheck(ctx context.Context, updater bmc.FirmwareUpdaterDell, fw *systemv1alpha1.FirmwareUpdate, r *FirmwareUpdateReconciler, server *metalv1alpha1.Server, condition *metav1.Condition) (bool, error) {
	log := ctrl.LoggerFrom(ctx)
	if fw.Status.DellStatus == nil || fw.Status.DellStatus.CheckJob == nil || fw.Status.DellStatus.CheckJob.JobID == "" {
		return false, fmt.Errorf("missing check job ID while polling repository check")
	}

	job, err := updater.GetJob(ctx, "", fw.Status.DellStatus.CheckJob.JobID)
	if err != nil {
		log.V(1).Info("Failed to fetch repository check job, retrying", "error", err)
		return true, nil
	}
	repoJob := toRepositoryJob(job)

	if !job.IsTerminal() {
		log.V(1).Info("Repository check job still in progress", "JobID", job.ID, "State", job.State, "PercentComplete", job.PercentComplete, "Message", job.Message)
		return true, r.patchProgress(ctx, fw, fw.Status.State, nil, func(status *systemv1alpha1.FirmwareUpdateStatus) {
			dellStatus(status).CheckJob = &repoJob
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
			dellStatus(status).CheckJob = &repoJob
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
		wasCompleted := fw.Status.State == systemv1alpha1.FirmwareUpdateStateCompleted
		fwBase := fw.DeepCopy()
		fw.Status.State = systemv1alpha1.FirmwareUpdateStateCompleted
		fw.Status.ObservedGeneration = fw.Generation
		if wasCompleted {
			// Merge the Completed condition in place and explicitly clear RepositoryCheckIssued.
			if err := r.Conditions.UpdateSlice(
				&fw.Status.Conditions,
				ConditionRepositoryCheckCompleted,
				conditionutils.UpdateStatus(condition.Status),
				conditionutils.UpdateReason(condition.Reason),
				conditionutils.UpdateMessage(condition.Message),
			); err != nil {
				return false, fmt.Errorf("failed to merge RepositoryCheckCompleted condition: %w", err)
			}
			if err := r.Conditions.UpdateSlice(
				&fw.Status.Conditions,
				ConditionRepositoryCheckIssued,
				conditionutils.UpdateStatus(corev1.ConditionFalse),
				conditionutils.UpdateReason(ReasonRepositoryCheckCompleted),
				conditionutils.UpdateMessage("Repository check concluded"),
			); err != nil {
				return false, fmt.Errorf("failed to clear RepositoryCheckIssued condition: %w", err)
			}
		} else {
			// First-ever convergence (State was Pending): reset to only this
			// condition, matching processInProgress's hand-back-to-Completed
			// wipe, which also only wipes at a genuine state transition.
			fw.Status.Conditions = []metav1.Condition{*condition}
		}
		fw.Status.DellStatus = nil
		fw.Status.PassCount = 0
		fw.Status.LastProgressTime = new(metav1.Now())
		return false, r.Status().Patch(ctx, fw, client.MergeFrom(fwBase))
	}

	// Packages are pending installation: bound how many times we allow a
	// check to (re-)discover pending packages before giving up.
	passCount := fw.Status.PassCount + 1
	if maxRepositoryPasses := r.maxRepositoryPasses(); maxRepositoryPasses > 0 && passCount > maxRepositoryPasses {
		log.Info("Exceeded maximum repository update passes, marking as Failed", "PassCount", passCount, "MaxRepositoryPasses", maxRepositoryPasses)
		if err := r.Conditions.Update(
			condition,
			conditionutils.UpdateStatus(corev1.ConditionTrue),
			conditionutils.UpdateReason(ReasonRepositoryCheckFailed),
			conditionutils.UpdateMessage(fmt.Sprintf("Exceeded maximum of %d repository update passes", maxRepositoryPasses)),
		); err != nil {
			return false, fmt.Errorf("failed to update RepositoryCheckCompleted condition: %w", err)
		}
		fwBase := fw.DeepCopy()
		fw.Status.State = systemv1alpha1.FirmwareUpdateStateFailed
		fw.Status.ObservedGeneration = fw.Generation
		fw.Status.PassCount = passCount
		dellStatus(&fw.Status).CheckJob = &repoJob
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
	if fw.Status.DellStatus != nil {
		fw.Status.DellStatus.CheckJob = nil
	}
	return false, r.Status().Patch(ctx, fw, client.MergeFrom(fwBase))
}

// handleStuckScheduledJob issues a PowerOn request to the server if one or
// more component firmware jobs are stuck in Dell's "Scheduled" state
// (queued, awaiting a reboot/power cycle to execute), the server is
// currently powered off, and no progress has been observed for longer than
// r.scheduledJobPowerOnTimeout().
func (dh *dellHandler) handleStuckScheduledJob(
	ctx context.Context,
	bmcClient bmc.BMC,
	fw *systemv1alpha1.FirmwareUpdate,
	r *FirmwareUpdateReconciler,
	server *metalv1alpha1.Server,
	scheduledJobIDs []string,
) (*metav1.Condition, error) {
	log := ctrl.LoggerFrom(ctx)
	scheduledJobPowerOnTimeout := r.scheduledJobPowerOnTimeout()
	if scheduledJobPowerOnTimeout <= 0 {
		return nil, nil
	}

	condition, err := utils.GetCondition(r.Conditions, fw.Status.Conditions, ConditionScheduledJobStuckPowerOnIssued)
	if err != nil {
		return nil, fmt.Errorf("failed to get condition for stuck scheduled job power on: %w", err)
	}
	alreadyIssued := condition.Status == metav1.ConditionTrue

	resolve := func(message string) (*metav1.Condition, error) {
		if !alreadyIssued {
			return nil, nil
		}
		if err := r.Conditions.Update(
			condition,
			conditionutils.UpdateStatus(corev1.ConditionFalse),
			conditionutils.UpdateReason(ReasonScheduledJobStuckPowerOnIssued),
			conditionutils.UpdateMessage(message),
		); err != nil {
			return nil, fmt.Errorf("failed to update ScheduledJobStuckPowerOnIssued condition: %w", err)
		}
		return condition, nil
	}

	if len(scheduledJobIDs) == 0 {
		return resolve("No component firmware jobs currently stuck in Scheduled state")
	}

	powerState, err := utils.GetServerPowerState(ctx, bmcClient, server)
	if err != nil {
		return nil, fmt.Errorf("failed to check server power state: %w", err)
	}
	if powerState == metalv1alpha1.ServerOnPowerState {
		return resolve("Server is powered on; no PowerOn action currently needed")
	}
	if powerState != metalv1alpha1.ServerOffPowerState {
		// Server is mid-transition (PoweringOn/PoweringOff) or Paused - not
		// actionable yet. Wait for it to settle into a stable On/Off state
		// before deciding whether a PowerOn is needed.
		return nil, nil
	}

	if alreadyIssued {
		// PowerOn was already issued for this still-ongoing incident and the
		// server is still off (e.g. it's still booting) - wait for it to
		// resolve instead of issuing PowerOn again.
		return nil, nil
	}

	// if scheduledJobIDs has an entry not yet reflected in
	// fw.Status.DellStatus.ComponentJobs as Scheduled, wait until it's
	// confirmed scheduled on a later reconcile before evaluating staleness -
	// LastProgressTime may be old for reasons unrelated to this newly
	// discovered job.
	notYetConfirmedScheduled := make(map[string]struct{}, len(scheduledJobIDs))
	for _, id := range scheduledJobIDs {
		notYetConfirmedScheduled[id] = struct{}{}
	}
	if fw.Status.DellStatus != nil {
		for _, job := range fw.Status.DellStatus.ComponentJobs {
			if (&bmc.DellJob{State: job.State}).IsScheduled() {
				delete(notYetConfirmedScheduled, job.JobID)
				if len(notYetConfirmedScheduled) == 0 {
					// All currently-scheduled jobs were already known from the previous reconcile.
					break
				}
			}
		}
	}
	if len(notYetConfirmedScheduled) > 0 {
		return nil, nil
	}

	if fw.Status.LastProgressTime == nil || time.Since(fw.Status.LastProgressTime.Time) < scheduledJobPowerOnTimeout {
		return nil, nil
	}

	log.Info("Component firmware job(s) stuck in Scheduled state with server powered off, issuing PowerOn",
		"Server", server.Name, "ScheduledJobIDs", scheduledJobIDs, "StuckSince", fw.Status.LastProgressTime.Time)
	if err := bmcClient.PowerOn(ctx, server.Spec.SystemURI); err != nil {
		return nil, fmt.Errorf("failed to power on server for stuck scheduled job: %w", err)
	}

	if err := r.Conditions.Update(
		condition,
		conditionutils.UpdateStatus(corev1.ConditionTrue),
		conditionutils.UpdateReason(ReasonScheduledJobStuckPowerOnIssued),
		conditionutils.UpdateMessage(fmt.Sprintf(
			"Job(s) %v remained Scheduled with server powered off for over %v with no progress; issued PowerOn",
			scheduledJobIDs, scheduledJobPowerOnTimeout)),
	); err != nil {
		return nil, fmt.Errorf("failed to update ScheduledJobStuckPowerOnIssued condition: %w", err)
	}
	return condition, nil
}

// ensureServerPoweredOn issues a PowerOn request to the server via BMC if it
// is found powered off before applying the repository update
func (dh *dellHandler) ensureServerPoweredOn(ctx context.Context, bmcClient bmc.BMC, fw *systemv1alpha1.FirmwareUpdate, r *FirmwareUpdateReconciler, server *metalv1alpha1.Server) (bool, error) {
	log := ctrl.LoggerFrom(ctx)
	inPowerOnState, err := utils.IsServerInPowerState(ctx, bmcClient, server, metalv1alpha1.ServerOnPowerState)
	if err != nil {
		return false, fmt.Errorf("failed to check server power state: %w", err)
	}
	if inPowerOnState {
		log.V(1).Info("Server is already powered on", "Server", server.Name)
		return false, nil
	}

	powerOnIssued, err := utils.GetCondition(r.Conditions, fw.Status.Conditions, ConditionRepositoryUpdatePowerOnIssued)
	if err != nil {
		return false, fmt.Errorf("failed to get condition for issued power on of server: %w", err)
	}
	if powerOnIssued.Status != metav1.ConditionTrue {
		log.V(1).Info("Server is powered off, issuing PowerOn request", "Server", server.Name)
		if err := bmcClient.PowerOn(ctx, server.Spec.SystemURI); err != nil {
			return false, fmt.Errorf("failed to power on server: %w", err)
		}
		if err := r.Conditions.Update(
			powerOnIssued,
			conditionutils.UpdateStatus(corev1.ConditionTrue),
			conditionutils.UpdateReason(ReasonServerPowerOnIssued),
			conditionutils.UpdateMessage("Issued PowerOn request to the server via BMC"),
		); err != nil {
			return false, fmt.Errorf("failed to update issued power on condition: %w", err)
		}
		return false, r.patchProgress(ctx, fw, fw.Status.State, powerOnIssued, nil)
	}
	// no watch event to notice the power-on completing in real BMC - poll periodically instead.
	log.V(1).Info("Server in powered off state, retrying", "Server", server.Name)
	return true, nil
}

func (dh *dellHandler) issueRepositoryUpdate(ctx context.Context, updater bmc.FirmwareUpdaterDell, fw *systemv1alpha1.FirmwareUpdate, r *FirmwareUpdateReconciler, server *metalv1alpha1.Server, condition *metav1.Condition) (bool, error) {
	log := ctrl.LoggerFrom(ctx)
	// Snapshot the jobs known to the BMC before issuing the apply call, so
	// newly spawned component jobs can be discovered by diffing against this
	// baseline once the apply job itself completes.
	//
	// The baseline is captured here, immediately before issuing the apply
	// call below, rather than in a separate prior reconcile: if it were
	// persisted a reconcile ahead of the apply call, any job spawned on the
	// BMC in that window (e.g. unrelated background LC-log jobs) would be
	// mistaken for one of our own component jobs by trackComponentJobs.
	var baselineJobIDs []string
	if fw.Status.DellStatus != nil {
		baselineJobIDs = fw.Status.DellStatus.BaselineJobIDs
	}
	if baselineJobIDs == nil {
		jobIDs, err := updater.ListJobs(ctx, "")
		if err != nil {
			log.V(1).Info("Failed to list jobs for baseline snapshot, retrying", "error", err)
			return true, nil
		}
		if jobIDs == nil {
			jobIDs = []string{}
		}
		baselineJobIDs = jobIDs
		log.V(1).Info("Captured baseline job IDs before applying repository update", "Server", server.Name, "BaselineJobCount", len(baselineJobIDs))
	}

	parameters, err := buildRepositoryParameters(ctx, r, fw, true)
	if err != nil {
		return false, fmt.Errorf("failed to build repository parameters: %w", err)
	}

	log.V(1).Info("Calling InstallFirmwareFromRepository (apply)", "Server", server.Name, "Address", parameters.IPAddress, "ShareName", parameters.ShareName, "CatalogFile", parameters.CatalogFile)
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
		// Persist the baseline captured above so a retry doesn't need to
		// re-list jobs; harmless if it does, since apply hasn't succeeded yet.
		return true, r.patchProgress(ctx, fw, fw.Status.State, nil, func(status *systemv1alpha1.FirmwareUpdateStatus) {
			dellStatus(status).BaselineJobIDs = baselineJobIDs
		})
	}

	log.V(1).Info("Repository update job issued", "Server", server.Name, "JobID", jobID)
	if err := r.Conditions.Update(
		condition,
		conditionutils.UpdateStatus(corev1.ConditionTrue),
		conditionutils.UpdateReason(ReasonRepositoryUpdateIssued),
		conditionutils.UpdateMessage(fmt.Sprintf("Issued repository update job %v", jobID)),
	); err != nil {
		return false, fmt.Errorf("failed to update RepositoryUpdateIssued condition: %w", err)
	}

	return false, r.patchProgress(ctx, fw, fw.Status.State, condition, func(status *systemv1alpha1.FirmwareUpdateStatus) {
		ds := dellStatus(status)
		ds.BaselineJobIDs = baselineJobIDs
		ds.UpdateJob = &systemv1alpha1.RepositoryJob{JobID: jobID}
	})
}

func (dh *dellHandler) pollRepositoryUpdate(ctx context.Context, updater bmc.FirmwareUpdaterDell, fw *systemv1alpha1.FirmwareUpdate, r *FirmwareUpdateReconciler, condition *metav1.Condition) (bool, error) {
	log := ctrl.LoggerFrom(ctx)
	if fw.Status.DellStatus == nil || fw.Status.DellStatus.UpdateJob == nil || fw.Status.DellStatus.UpdateJob.JobID == "" {
		return false, fmt.Errorf("missing update job ID while polling repository update")
	}

	job, err := updater.GetJob(ctx, "", fw.Status.DellStatus.UpdateJob.JobID)
	if err != nil {
		log.V(1).Info("Failed to fetch repository update job, retrying", "error", err)
		return true, nil
	}
	repoJob := toRepositoryJob(job)

	if !job.IsTerminal() {
		log.V(1).Info("Repository update job still in progress", "JobID", job.ID, "State", job.State, "PercentComplete", job.PercentComplete, "Message", job.Message)
		return true, r.patchProgress(ctx, fw, fw.Status.State, nil, func(status *systemv1alpha1.FirmwareUpdateStatus) {
			dellStatus(status).UpdateJob = &repoJob
		})
	}

	log.V(1).Info("Repository update job reached terminal state", "JobID", job.ID, "State", job.State, "Message", job.Message)
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
			dellStatus(status).UpdateJob = &repoJob
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
		dellStatus(status).UpdateJob = &repoJob
	})
}

func (dh *dellHandler) trackComponentJobs(ctx context.Context, bmcClient bmc.BMC, updater bmc.FirmwareUpdaterDell, fw *systemv1alpha1.FirmwareUpdate, r *FirmwareUpdateReconciler, server *metalv1alpha1.Server, condition *metav1.Condition) (bool, error) {
	log := ctrl.LoggerFrom(ctx)
	jobIDs, err := updater.ListJobs(ctx, "")
	if err != nil {
		log.V(1).Info("Failed to list jobs for component tracking, retrying", "error", err)
		return true, nil
	}
	//
	// Sort jobs so that an unchanged set of outstanding jobs always produces ComponentJobs
	// in the same order
	slices.Sort(jobIDs)
	log.V(1).Info("Listed BMC jobs for component tracking", "TotalJobsOnBMC", len(jobIDs))

	var baselineJobIDs []string
	var updateJobID string
	if fw.Status.DellStatus != nil {
		baselineJobIDs = fw.Status.DellStatus.BaselineJobIDs
		if fw.Status.DellStatus.UpdateJob != nil {
			updateJobID = fw.Status.DellStatus.UpdateJob.JobID
		}
	}
	known := make(map[string]struct{}, len(baselineJobIDs)+1)
	for _, id := range baselineJobIDs {
		known[id] = struct{}{}
	}
	if updateJobID != "" {
		known[updateJobID] = struct{}{}
	}

	componentJobs := make([]systemv1alpha1.RepositoryJob, 0, len(jobIDs))
	summary := &systemv1alpha1.ComponentJobsSummary{}
	allTerminal := true
	anyFailed := false
	var scheduledJobIDs []string
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
			log.Info("Component firmware job failed", "JobID", job.ID, "Name", job.Name, "State", job.State, "Message", job.Message)
		case job.IsCompleted():
			summary.Completed++
			log.V(1).Info("Component firmware job completed", "JobID", job.ID, "Name", job.Name, "State", job.State)
		default:
			summary.InProgress++
			log.V(1).Info("Component firmware job still in progress", "JobID", job.ID, "Name", job.Name, "State", job.State, "PercentComplete", job.PercentComplete)
			if job.IsScheduled() {
				scheduledJobIDs = append(scheduledJobIDs, job.ID)
			}
		}
		if !job.IsTerminal() {
			allTerminal = false
		}
	}

	if !allTerminal {
		log.V(1).Info("Component firmware jobs still in progress",
			"Total", summary.Total, "Completed", summary.Completed, "Failed", summary.Failed, "InProgress", summary.InProgress)
		powerOnCondition, err := dh.handleStuckScheduledJob(ctx, bmcClient, fw, r, server, scheduledJobIDs)
		if err != nil {
			return false, err
		}
		return true, r.patchProgress(ctx, fw, fw.Status.State, powerOnCondition, func(status *systemv1alpha1.FirmwareUpdateStatus) {
			ds := dellStatus(status)
			ds.ComponentJobs = componentJobs
			ds.ComponentJobsSummary = summary
		})
	}

	if anyFailed {
		log.Info("Component firmware jobs finished with failures", "Total", summary.Total, "Completed", summary.Completed, "Failed", summary.Failed)
		if err := r.Conditions.Update(
			condition,
			conditionutils.UpdateStatus(corev1.ConditionTrue),
			conditionutils.UpdateReason(ReasonComponentJobFailed),
			conditionutils.UpdateMessage("One or more component firmware jobs failed"),
		); err != nil {
			return false, fmt.Errorf("failed to update ComponentJobsCompleted condition: %w", err)
		}
		return false, r.patchProgress(ctx, fw, systemv1alpha1.FirmwareUpdateStateFailed, condition, func(status *systemv1alpha1.FirmwareUpdateStatus) {
			ds := dellStatus(status)
			ds.ComponentJobs = componentJobs
			ds.ComponentJobsSummary = summary
		})
	}

	log.V(1).Info("All component firmware jobs completed successfully", "Total", summary.Total, "Completed", summary.Completed)
	if err := r.Conditions.Update(
		condition,
		conditionutils.UpdateStatus(corev1.ConditionTrue),
		conditionutils.UpdateReason(ReasonComponentJobsCompleted),
		conditionutils.UpdateMessage("All component firmware jobs completed"),
	); err != nil {
		return false, fmt.Errorf("failed to update ComponentJobsCompleted condition: %w", err)
	}
	return false, r.patchProgress(ctx, fw, fw.Status.State, condition, func(status *systemv1alpha1.FirmwareUpdateStatus) {
		ds := dellStatus(status)
		ds.ComponentJobs = componentJobs
		ds.ComponentJobsSummary = summary
	})
}

// buildRepositoryParameters translates the FirmwareUpdate's Repository
// spec (and, if configured, its Secret credentials) into bmc.RepositoryUpdateParameters.
func buildRepositoryParameters(ctx context.Context, r *FirmwareUpdateReconciler, fw *systemv1alpha1.FirmwareUpdate, applyUpdate bool) (*bmc.RepositoryUpdateParameters, error) {
	if fw.Spec.DellRepository == nil {
		return nil, fmt.Errorf("firmware update has no dellRepository configured")
	}
	repo := fw.Spec.DellRepository

	var username, password string
	if repo.CredentialsRef != nil {
		if repo.ShareType == systemv1alpha1.DellShareTypeHTTP {
			return nil, fmt.Errorf("credentialsRef must not be used with HTTP shares: credentials would be sent in cleartext")
		}
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

	var applySameVersions, applyDowngradeVersions bool
	switch ptr.Deref(repo.ApplyVersionPolicy, "") {
	case systemv1alpha1.DellVersionApplyPolicyAllowSameVersion:
		applySameVersions = true
	case systemv1alpha1.DellVersionApplyPolicyAllowDowngradeVersion:
		applyDowngradeVersions = true
	case systemv1alpha1.DellVersionApplyPolicyAllowSameAndDowngradeVersion:
		applySameVersions = true
		applyDowngradeVersions = true
	}

	ignoreCertWarning := ptr.Deref(repo.CertificateVerification, "") == systemv1alpha1.DellCertificateVerificationPolicyIgnore

	return &bmc.RepositoryUpdateParameters{
		ShareType:              string(repo.ShareType),
		IPAddress:              repo.Address,
		ShareName:              repo.ShareName,
		CatalogFile:            catalogFile,
		UserName:               username,
		Password:               password,
		IgnoreCertWarning:      ignoreCertWarning,
		ApplyUpdate:            applyUpdate,
		RebootNeeded:           applyUpdate && repo.RebootNeeded,
		ApplySameVersions:      applySameVersions,
		ApplyDowngradeVersions: applyDowngradeVersions,
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
