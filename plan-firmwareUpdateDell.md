# Plan: Dell Repository-Based Firmware Upgrade Controller (in maintenance-operator)

## NAMING DECISIONS
- CRD renamed from `FirmwareRepositoryUpdate` to **`FirmwareUpdateDell`** to make the Dell-only
  nature explicit at the CRD level. Kind=`FirmwareUpdateDell`; file names
  `firmwareupdatedell_types.go`/`firmwareupdatedell_controller.go`; finalizer
  `system.metal.ironcore.dev/firmwareupdatedell`; RBAC resource `firmwareupdatedells`;
  shortName `fwud`.
- BMC capability: **NOT** a new `RepositoryFirmwareUpdater` interface — metal-operator's
  `bmc/bmc.go` already defines `type FirmwareUpdater interface` (line ~127, with
  `UpgradeBiosVersion`/`UpgradeBMCVersion`) which is already embedded into the `BMC` union
  interface (`bmc.go` line ~197). Instead: **extend the existing `FirmwareUpdater` interface**
  with the new methods (`InstallFirmwareFromRepository`, `GetRepositoryUpdateList`, `ListJobs`,
  `GetJob`). No new interface, no new embedding into `BMC` needed.

## ARCHITECTURE: split across two repos, two PRs
The CRD/controller for `FirmwareUpdateDell` lives in **metal-maintenance-operator**
(`/Users/I760727/Workspace/maintenance-operator`), NOT in metal-operator. Context:
metal-operator's `BIOSVersion`/`BMCVersion` controllers are in the process of being migrated to
maintenance-operator (current maintenance-operator branch is `feat/bios-bmc-maintenance-controllers`;
see `ironcore-dev/enhancements` PR #20 "Maintenance Pipeline Proposal"). maintenance-operator
already has its own multigroup API (`system`, `baseboard`, `maintenance`, `readiness`,
`vendorconsole` — all under domain `metal.ironcore.dev`) with its own copies of
`BIOSVersion`/`BIOSSettings` (group `system`) and `BMCVersion`/`BMCSettings` (group `baseboard`)
controllers, and its **own** `ServerMaintenance` CRD (group `maintenance.metal.ironcore.dev`,
distinct from metal-operator's built-in `ServerMaintenance`). maintenance-operator imports
metal-operator as a Go module dependency (`github.com/ironcore-dev/metal-operator v0.6.1-...` in
go.mod, no local go.work) and reuses its `bmc.BMC` client interface (via
`pkg/bmcutils.GetBMCClientForServer`/`GetBMCClientFromBMC`) and its `bmc/mock/server` package
directly in its own envtest suites — it does NOT vendor/duplicate the Redfish client code itself.

**Confirmed decisions:**
- New CRD `FirmwareUpdateDell` goes in **group `system`** (`system.metal.ironcore.dev`), alongside
  `BIOSVersion`/`BIOSSettings`, since `InstallFromRepository` acts on
  `Systems/{systemId}/Oem/Dell/...` — the same resource base as BIOS.
- **Delivery is sequenced as two separate PRs, with a temporary local-dev shortcut**:
  1. **PR1 (metal-operator)**: extend the existing `bmc.FirmwareUpdater` interface with the new
     Dell OEM Redfish methods + mock server support — the "BMC client layer" work described
     below, lives entirely in metal-operator's `bmc` package.
  2. **PR2 (maintenance-operator)**: add the new CRD + controller described below, consuming the
     new bmc methods from PR1.
  3. **Local development bridge**: while PR1 is still unmerged, add a temporary `replace`
     directive to maintenance-operator's `go.mod`:
     `replace github.com/ironcore-dev/metal-operator => ../metal-operator`
     This lets PR2 be developed/tested locally against the in-progress metal-operator branch
     without waiting for PR1 to merge/tag. **This `replace` line MUST be removed from PR2 before
     it is opened/merged** — replaced with a real bumped version requirement
     (`github.com/ironcore-dev/metal-operator vX.Y.Z`) once PR1 is merged and tagged upstream.
     Do not let the `replace` directive land in the merged PR2 — it would break other consumers'
     builds (they don't have that local path) and pin maintenance-operator to an unpublished
     commit.
- Per-Server CRD, `RebootNeeded` hardcoded true, dry-run-then-apply need-detection via
  `GetRepoBasedUpdateList`, internal multi-pass loop bounded by a pass counter (no re-request of
  maintenance/BMC-reset each pass).

## PR2 (maintenance-operator) — CRD, controller, wiring, testing
- **Types**: new file `api/system/v1alpha1/firmwareupdatedell_types.go` (package `v1alpha1`,
  group `system`), modeled exactly on
  [api/system/v1alpha1/biosversion_types.go](maintenance-operator/api/system/v1alpha1/biosversion_types.go):
  - Reuse shared types from [api/common_types.go](maintenance-operator/api/common_types.go):
    `api.RetryPolicy` (`api.UpdatePolicy` probably not needed — no per-component version target).
    Note `api.Task`'s `TaskState`/`Health` are **local string types** (deliberately NOT
    `schemas.TaskState`) "so consumers of this API do not need to depend on the gofish module" —
    the new `RepositoryJob`-equivalent status struct MUST follow this same convention (plain
    `string` for Dell `JobState`, not any gofish type).
  - `ServerRef *corev1.LocalObjectReference` (required, immutable via
    `+kubebuilder:validation:XValidation:rule="self == oldSelf"`, same as `BIOSVersionSpec.ServerRef`).
  - `ServerMaintenanceRef *metalv1alpha1.ObjectReference` (note: `metalv1alpha1.ObjectReference`
    type from metal-operator's api package is reused for the ref *shape*, but the referenced
    object is maintenance-operator's own `maintenancev1alpha1.ServerMaintenance`).
  - `ServerMaintenancePolicy *maintenancev1alpha1.ServerMaintenancePolicy`.
  - `FirmwareUpdateDellSpec` (`,inline` template + `ServerMaintenanceRef` + `ServerRef` as above).
  - `FirmwareUpdateDellTemplate`:
    - `Repository RepositorySpec` — `ShareType` (enum NFS/CIFS/HTTP/HTTPS), `Address` (share IP
      or hostname, e.g. `downloads.dell.com`), `ShareName` (optional — not needed for Dell's
      public HTTPS catalog), `CatalogFile` (optional, default `Catalog.xml`), `Workgroup`
      (optional, CIFS), `SecretRef *corev1.SecretReference` (optional share credentials),
      `IgnoreCertWarning *bool` (optional, HTTPS).
    - `ApplySameVersions *bool`, `ApplyDowngradeVersions *bool` (optional, default false).
    - `ServerMaintenancePolicy`, `RetryPolicy *api.RetryPolicy`.
    - No `RebootNeeded` field — hardcoded `true` in the controller.
  - `FirmwareUpdateDellStatus`:
    - `State FirmwareUpdateDellState` (`Pending`/`InProgress`/`Completed`/`Failed`).
    - `CheckJob *RepositoryJob` (dry-run job), `UpdateJob *RepositoryJob` (main apply job),
      `ComponentJobs []RepositoryJob` (spawned per-component jobs from the current pass).
    - `BaselineJobIDs []string` (job IDs present just before issuing the apply call, for diffing).
    - `PassCount int32` (bounds the check→apply→track→recheck loop).
    - `FailedAttempts int32`, `ObservedGeneration int64`, `Conditions []metav1.Condition` (same
      shape as `BIOSVersionStatus`).
    - `RepositoryJob` struct: `JobID`, `Name`, `JobType`, `State string` (raw Dell `JobState`,
      intentionally not `schemas.TaskState`), `Message string`, `PercentComplete int32`.
  - Kubebuilder markers mirroring `BIOSVersion`: `scope=Cluster`, shortName `fwud`, printcolumns
    for State/ServerRef/ServerMaintenanceRef/PassCount.
- **Controller**: new file `internal/controller/system/firmwareupdatedell_controller.go`
  (package `system`), modeled exactly on
  [internal/controller/system/biosversion_controller.go](maintenance-operator/internal/controller/system/biosversion_controller.go):
  - Reuse `utils.GetServerByName`, `utils.IsAnyServerMaintenanceActive`,
    `utils.ShouldProceedWithDeletion` from
    [internal/utils/helper.go](maintenance-operator/internal/utils/helper.go), and shared
    constants from [internal/constants/constants.go](maintenance-operator/internal/constants/constants.go).
  - Get BMC client via `bmcutils.GetBMCClientForServer(ctx, r.Client, server, r.DefaultProtocol,
    r.SkipCertValidation, r.BMCOptions)` (from `github.com/ironcore-dev/metal-operator/pkg/bmcutils`).
  - Create/track maintenance-operator's own `maintenancev1alpha1.ServerMaintenance` (not
    metal-operator's) — same handshake pattern (Pending→InMaintenance wait, policy-based
    approval) as `BIOSVersionReconciler`'s maintenance handling in this repo.
  - `FirmwareUpdateDellReconciler` struct mirrors `BIOSVersionReconciler` fields (`Client`,
    `ManagerNamespace`, `DefaultProtocol`, `SkipCertValidation`, `Scheme`, `BMCOptions`,
    `ResyncInterval`, `Conditions`, `DefaultFailedAutoRetryCount`), plus a new
    `MaxRepositoryPasses int32` field (default e.g. 5) to bound the convergence loop.
  - Finalizer: `"system.metal.ironcore.dev/firmwareupdatedell"` (matches
    `BIOSVersionFinalizer = "system.metal.ironcore.dev/biosversion"` naming convention).
  - RBAC markers: `+kubebuilder:rbac:groups=system.metal.ironcore.dev,resources=firmwareupdatedells,...`
    plus the same `metal.ironcore.dev` (servers), `maintenance.metal.ironcore.dev`
    (servermaintenances), `""` (secrets) groups as `BIOSVersionReconciler`'s markers.
  - State machine (`transitionState`, copy skeleton from `biosversion_controller.go`):
    - **Pending**: same retry-annotation cleanup as BIOSVersion's Pending branch, then move to
      `InProgress` (no "already compliant" short-circuit up front — compliance is only knowable
      via the Dell dry-run, which is the first thing InProgress does).
    - **InProgress** — sub-phases via conditions (same "get-or-create condition, branch on
      Status/Reason, `updateStatus` to persist" style as `processInProgressState`):
      1. `handleServerMaintenance` + `handleBMCReset` — reuse pattern verbatim (own copy, adapted
         receiver/type names).
      2. `ConditionRepositoryCheckIssued`/`ConditionRepositoryCheckCompleted` — issue
         `InstallFirmwareFromRepository` with `ApplyUpdate=false, RebootNeeded=false`; poll
         `GetJob` until terminal; on complete, call `GetRepositoryUpdateList`:
         - not pending → skip directly to Completed (still run `cleanupServerMaintenanceReferences`).
         - pending → continue.
      3. Snapshot `ListJobs` → persist `Status.BaselineJobIDs` (only on first entry into this pass).
      4. `ConditionRepositoryUpdateIssued`/`ConditionRepositoryUpdateCompleted` — issue
         `InstallFirmwareFromRepository` with `ApplyUpdate=true, RebootNeeded=true` (+ spec's
         ApplySameVersions/ApplyDowngradeVersions); poll `GetJob` on the returned job ID until
         terminal (completed/failed), same condition-transition-checkpoint stall detection as
         `checkUpdateBiosUpgradeStatus` in metal-operator's BIOSVersion controller.
      5. `ConditionComponentJobsCompleted` — call `ListJobs` again, diff against
         `BaselineJobIDs ∪ {UpdateJob.ID}` to populate `Status.ComponentJobs`; poll each job's
         `GetJob` until terminal; aggregate failure (any component job failing → overall Failed
         with details of which component/job).
      6. On all component jobs terminal successfully: increment `PassCount`; if `PassCount >=
         MaxRepositoryPasses` → Failed ("exceeded max repository update passes"); else reset the
         check/update/component-jobs conditions (NOT the maintenance/reset ones) and loop back to
         step 2 to re-verify compliance.
      7. When a check pass reports "not pending" → move to Completed +
         `cleanupServerMaintenanceReferences`.
    - **Completed**: same idempotent cleanup pattern as BIOSVersion (verify still compliant via a
      cheap check — reuse the dry-run `GetRepositoryUpdateList` call; if it now reports pending
      packages again, e.g. due to spec change, drop back to `InProgress`).
    - **Failed**: copy `processFailedState` verbatim (retry annotation +
      `RetryPolicy`/`DefaultFailedAutoRetryCount` semantics), adapted to the new types.
  - Deletion (`shouldDelete`/`delete`): same shape as BIOSVersion — block while `InProgress` and
    maintenance active.
  - Watches (`SetupWithManager`): `Owns` maintenance-operator's `ServerMaintenance`,
    `Watches(&metalv1alpha1.Server{}, ...)` using adapted copy of `enqueueBiosVersionByServerRefs`-
    style mapping function.
  - New condition/reason constants (add alongside `BIOSVersionReconciler`'s in this package):
    `ConditionRepositoryCheckIssued`, `ConditionRepositoryCheckCompleted`,
    `ConditionRepositoryUpdateIssued`, `ConditionRepositoryUpdateCompleted`,
    `ConditionComponentJobsCompleted`, plus matching `Reason*` constants (reuse existing
    `ConditionServerMaintenanceWaiting`, `ConditionResetIssued`,
    `ConditionRetryOfFailedResourceIssued` and reason constants as-is).
- **Wiring**:
  - Register `FirmwareUpdateDellReconciler` in `maintenance-operator/cmd/main.go` next to the
    existing `BIOSVersionReconciler` registration; add a new flag for `MaxRepositoryPasses`.
  - Add a `PROJECT` file entry: `group: system`, `kind: FirmwareUpdateDell`,
    `path: github.com/ironcore-dev/metal-maintenance-operator/api/system/v1alpha1`.
  - Run `make generate manifests` in maintenance-operator to produce deepcopy code, CRD YAML
    (`config/crd/bases/...firmwareupdatedells.yaml`), and RBAC role additions.
- **Testing**: extend
  [internal/controller/system/suite_test.go](maintenance-operator/internal/controller/system/suite_test.go)
  (imports `mockserver "github.com/ironcore-dev/metal-operator/bmc/mock/server"` directly — this
  already works today for BIOSVersion/BMCVersion tests) with a new Ginkgo spec file
  `firmwareupdatedell_controller_test.go`, following the same `SetupTest`/Komega pattern;
  relies on PR1's mock server additions being present in the bumped metal-operator dependency.
  Covers: dry-run reports "not pending" → straight to Completed; dry-run pending → apply →
  component jobs → recheck → Completed; a failing component job → Failed with retry per
  `RetryPolicy`; deletion blocked while InProgress+maintenance active.

## PR1 (metal-operator) — BMC client layer

### Goal
Extend metal-operator's existing `bmc.FirmwareUpdater` interface to also drive Dell's
`DellSoftwareInstallationService.InstallFromRepository` Redfish OEM action, bulk-upgrading
firmware (BIOS/NIC/RAID/iDRAC/etc.) from a network repository/catalog, tracking Dell's
proprietary Job resources (not standard Redfish Tasks) to completion.

Reference: Dell's official example script (dell/iDRAC-Redfish-Scripting,
`InstallFromRepositoryREDFISH.py`) is the concrete ground-truth for payload field names and
job-tracking semantics (Dell publishes no separate OpenAPI schema for this OEM action).

### Key reference architecture (existing code to mirror, for both the bmc-layer work here and the
### controller-shape work described in PR2 above)
- State machine skeleton, finalizer, deletion guard, retry/failure handling, condition
  bookkeeping (structural reference — actual controller lives in maintenance-operator, see PR2):
  [internal/controller/biosversion_controller.go](metal-operator/internal/controller/biosversion_controller.go)
  - `reconcileExists`/`shouldDelete`/`delete`, `transitionState` switch on `Status.State`,
    `handleServerMaintenance` + `handleBMCReset`, `processFailedState` (retry annotation +
    `RetryPolicy`/`DefaultFailedAutoRetryCount`), `checkUpdateBiosUpgradeStatus` (condition +
    `conditionutils.FieldsTransition` checkpoint stall detection — Dell Job polling needs new
    logic since Job JSON shape differs from `schemas.Task`), `upgradeBIOSVersion` (image
    credentials + params from `SecretRef`), `enqueueBiosVersionByServerRefs` + `SetupWithManager`.
- BMC interface & vendor factory: [bmc/bmc.go](metal-operator/bmc/bmc.go) — existing
  `FirmwareUpdater` interface (~line 127, to be extended, NOT replaced), `BMC` union interface
  (~line 191, already embeds `FirmwareUpdater`), `DefaultVendors()` factory map.
- Base vendor no-op fallback: `bmc/redfish.go` (`RedfishBaseBMC.UpgradeBiosVersion` returns
  `fmt.Errorf("... not supported for manufacturer %q", r.manufacturer)` — mirror for new methods).
- Dell-specific implementation + shared helpers: [bmc/redfish_dell.go](metal-operator/bmc/redfish_dell.go)
  (`dellBuildRequestBody`, `dellExtractTaskMonitorURI`, `dellParseTaskDetails`,
  `CheckBMCPendingComponentUpgrade`/`dellGetComponentFilters`/`dellMatchesComponentFilter`) and
  [bmc/oem_helpers.go](metal-operator/bmc/oem_helpers.go) (`upgradeVersion`, `getUpgradeTask`,
  `checkPendingComponentUpgrade` — generic HTTP plumbing shared across vendors via injected
  callbacks; **note**: these specific helpers are built around `UpdateService.SimpleUpdate` +
  TaskService and are NOT reusable as-is for the OEM `DellSoftwareInstallationService` action —
  new Dell-only helpers are needed, but should follow the same "raw POST via
  `base.client.GetService().GetClient()`, read `Location` header / body for job URI" style).
- Mock Redfish server for tests: [bmc/mock/server/server.go](metal-operator/bmc/mock/server/server.go)
  — data-driven JSON resources under `bmc/mock/server/data/`, `actionHandlers` dispatch table
  (register new matcher + handler for the Dell OEM action paths, no changes needed to
  `handlePost`), and the `doUpgradeSteps` goroutine pattern (steps-driven async state progression
  + generation counter to cancel stale goroutines) — reuse this exact pattern for simulating Job
  progression. This mock server package is imported directly by maintenance-operator's own
  envtest suites (`mockserver "github.com/ironcore-dev/metal-operator/bmc/mock/server"`), so
  additions here become available there once the dependency is bumped.

### Design

#### 1. Extend the existing `FirmwareUpdater` interface (bmc/ package)
- **`bmc/bmc.go`**: add new methods to the existing `type FirmwareUpdater interface` (do NOT
  create a separate interface — no union-interface change needed, `FirmwareUpdater` is already
  embedded in `BMC`):
  - `InstallFirmwareFromRepository(ctx, systemURI string, params *RepositoryUpdateParameters) (jobID string, isFatal bool, err error)`
    — POSTs to `.../Systems/{systemId}/Oem/Dell/DellSoftwareInstallationService/Actions/DellSoftwareInstallationService.InstallFromRepository`;
    extracts Job ID from the `Location` response header (per reference script:
    `response.headers['Location'].split("/")[-1]`).
  - `GetRepositoryUpdateList(ctx, systemURI string) (hasPendingPackages bool, packageListXML string, err error)`
    — POSTs to `.../DellSoftwareInstallationService.GetRepoBasedUpdateList`; on non-200 with a
    message containing "match catalog" / "not found" → `hasPendingPackages=false, err=nil`; on 200
    → parse `PackageList` (XML string) and report whether it contains device entries.
  - `ListJobs(ctx, UUID string) (jobIDs []string, err error)` — GET
    `.../Managers/iDRAC.Embedded.1/Oem/Dell/Jobs`, extract `Id`/`JID_...` values (used both as the
    pre-apply baseline snapshot and for post-apply diffing to discover spawned component jobs).
  - `GetJob(ctx, UUID string, jobID string) (*RepositoryJob, error)` — GET
    `.../Managers/iDRAC.Embedded.1/Oem/Dell/Jobs/{jobID}`, parse `JobState`, `JobType`, `Message`,
    `PercentComplete`, `Name`.
  - Add `RepositoryUpdateParameters` struct (ShareType, IPAddress, ShareName, CatalogFile,
    UserName, Password, Workgroup, IgnoreCertWarning, ApplyUpdate, RebootNeeded,
    ApplySameVersions, ApplyDowngradeVersions) and `RepositoryJob` struct (ID, Name, JobType,
    State, Message, PercentComplete) as bmc-package types (mirrors how `ComponentType` /
    `ApplyResult` are already package-level types in `bmc.go`).
- **`bmc/redfish.go`**: add `RedfishBaseBMC` default implementations returning
  `bmc.ErrNotSupported` (or the existing "not supported for manufacturer %q" error string,
  matching `UpgradeBiosVersion`'s style) for all four new methods.
- **`bmc/redfish_dell.go`**: real implementation.
  - New Dell-only helper (parallel to `dellBuildRequestBody`/`dellExtractTaskMonitorURI`) to POST
    the OEM action JSON body and pull the Job ID out of the `Location` header.
  - `dellParseJob` to unmarshal the Dell Job JSON body into `bmc.RepositoryJob`.
  - Classify job completion/failure from `JobState`/`Message` strings (Dell doesn't use
    `schemas.TaskState`) — port the reference script's substring checks (`"completed
    successfully"`, `"fail"`, `"invalid"`, `"unable"`, `"cancel"`) into small `isJobCompleted` /
    `isJobFailed` helpers.
  - `GetRepositoryUpdateList`: unmarshal `{"PackageList": "<xml...>"}`; treat any XML response
    containing device entries (e.g. via a minimal `encoding/xml` struct capturing repeated
    `<DICTIONARY>`/`<PACKAGE>` elements, or a pragmatic regex/substring count fallback if the
    schema proves inconsistent — verify actual shape empirically against the mock server /
    real iDRAC during implementation) as "pending".

#### 2. Testing (metal-operator side)
- **Unit tests** (`bmc/redfish_dell_test.go`): request-body construction, Job-ID extraction from
  `Location` header, `GetRepositoryUpdateList` pending/not-pending classification, `JobState`
  completion/failure classification — mirror the existing `dellBuildRequestBody`/
  `dellExtractTaskMonitorURI` test style already in that file.
- **Mock server** (`bmc/mock/server/server.go` + new `data/` fixtures): register two new
  `actionHandlers` entries (matching the `DellSoftwareInstallationService.InstallFromRepository`
  and `.GetRepoBasedUpdateList` action suffixes) plus a plain GET route for
  `Managers/iDRAC.Embedded.1/Oem/Dell/Jobs[/{id}]`; reuse the `doUpgradeSteps` steps-file +
  generation-counter pattern to simulate a Job progressing through Scheduled→Running→Completed
  (and a `.../fail`-triggered failure variant, same convention as `imageURI` containing "fail"
  today).

## Scope boundaries
- **In scope**: Dell-only real implementation; the extended `FirmwareUpdater` interface shape
  stays vendor-neutral (other vendors return `ErrNotSupported`, mirroring existing
  `UpgradeBiosVersion`/`UpgradeBMCVersion`). Per-Server CRD, hardcoded `RebootNeeded=true`,
  dry-run-based need-detection, bounded internal multi-pass loop.
- **Out of scope**: implementing repository-based update for HPE/Lenovo/Supermicro (they have no
  equivalent OEM action currently wired up); parsing full per-component criticality/name detail
  out of `PackageList` XML into status (only presence/absence of pending packages is used for
  control flow — component names surfacing is a nice-to-have, not required); proxy-server support
  for HTTP/HTTPS shares (`ProxyServer`/`ProxyPort`/etc. from the reference script) — can be added
  later if needed.

## Further Considerations
1. `GetRepoBasedUpdateList`'s `PackageList` XML schema isn't fully known from the reference script
   (it only shows regex substring matching, not a full schema) — during implementation, capture a
   real sample (from a dev iDRAC or Dell docs) to write a precise `encoding/xml` struct; until
   then, plan uses a pragmatic "any device entries present" check.
