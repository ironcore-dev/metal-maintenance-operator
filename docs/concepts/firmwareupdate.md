# FirmwareUpdate

`FirmwareUpdate` updates the firmware of one or more components on exactly one `Server`, driving a
vendor-specific BMC workflow (e.g. Dell's repository-based install) under a requested
`ServerMaintenance` window.

## Key Points

- `FirmwareUpdate` is namespaced and immutably bound to one server via `spec.serverRef`.
- The actual update mechanism is vendor-specific, selected by the server's discovered
  `Status.Manufacturer`:
  - **Dell Inc.**: `spec.dellRepository` describes a network share (NFS/CIFS/HTTP/HTTPS) hosting an
    update repository/catalog, consumed via iDRAC's `InstallFromRepository` job.
  - **HPE / Lenovo**: routed to a handler that is not yet implemented; the `FirmwareUpdate`
    immediately fails.
  - Any other/undiscovered manufacturer fails with an `UnsupportedVendor` condition (or requeues if
    the manufacturer hasn't been discovered yet).
- `spec.serverMaintenancePolicy` controls how maintenance is requested (`Enforced` or
  `OwnerApproval`), same semantics as [`ServerMaintenance`](servermaintenance.md).
- `status.state` reflects the overall lifecycle: `Pending`, `InProgress`, `Completed`, or `Failed`.
- `status.serverMaintenanceRef` and `status.dellStatus` (vendor-specific job tracking: check/update/
  component jobs, pass count) are managed by the controller.
- `spec.progressDeadlineSeconds` (default 3600s) fails the update if no observable forward progress
  (`status.lastProgressTime`) is made within the deadline — guards against a stuck BMC job leaving
  the server parked in maintenance indefinitely.
- `spec.retryPolicy.maxAttempts` bounds automatic retries after a transient failure.
- `spec.ttlSecondsAfterFinished`, if set, deletes the `FirmwareUpdate` that many seconds after it
  reaches `Completed` (failed objects are retained for inspection).

## Workflow (Dell)

1. **Pending**: the controller issues a dry-run (`ApplyUpdate=false`) repository check job against
   the configured `spec.dellRepository`. If the check finds packages pending installation, it
   records a baseline and transitions to `InProgress`; if nothing is pending, it stays `Pending`
   (nothing to do) until the spec changes.
2. **InProgress**: the controller requests (or reuses) `ServerMaintenance` for the server. Once the
   server is in maintenance, it issues the real apply job (`ApplyUpdate=true`), diffs newly spawned
   per-component jobs against the baseline, and tracks them in `status.dellStatus.componentJobs`
   until they all reach a terminal state. If an apply job gets stuck `Scheduled` with the server
   powered off, the operator-level `ScheduledJobPowerOnTimeout` triggers a recovery power-on.
3. Once the pass completes, control hands back to **Completed** for re-verification: another
   dry-run check confirms no further packages are pending. If more are found, the controller
   re-enters `InProgress` for another pass (`status.passCount`), bounded by the operator-level
   `MaxRepositoryPasses` before failing instead.
4. On success, `status.state` is `Completed`; on unrecoverable failure (including exceeding
   `progressDeadlineSeconds` or `MaxRepositoryPasses`), `Failed` (subject to `spec.retryPolicy`).
5. On deletion, the controller removes its finalizer once the update is no longer `InProgress`
   (cascade-deleting its `ServerMaintenance`, if any, is handled by that object's own controller).

## Example

```yaml
apiVersion: system.metal.ironcore.dev/v1alpha1
kind: FirmwareUpdate
metadata:
  name: sample-fw-update
  namespace: default
spec:
  serverRef:
    name: sample-system-0
  dellRepository:
    shareType: HTTPS
    address: example.repository.com
    shareName: /hw-firmware/firmware_80u3_04_2026
    catalogFile: Catalog.xml
    rebootNeeded: true
    # applyVersionPolicy omitted -> only genuine upgrades are applied
  serverMaintenancePolicy: OwnerApproval
```
