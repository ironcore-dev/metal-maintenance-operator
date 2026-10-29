# BIOSSettings

`BIOSSettings` applies an ordered sequence of BIOS configuration changes to exactly one `Server`. It is intended
for granular, per-server Day-2 operations that need deterministic sequencing and safety gates such as version
checks, maintenance, and post-apply verification.

## Key Points

- `BIOSSettings` is cluster-scoped and immutably bound to one server via `spec.serverRef`.
- Settings are only applied once the server's current BIOS version matches `spec.version`; otherwise the object
  waits in `Pending`.
- `spec.settingsFlow[]` is an ordered list of named settings batches (`name`, `priority`, `settings`), applied in
  ascending `priority` order. If empty, the object is immediately marked `Applied`.
- `spec.serverMaintenancePolicy` controls how maintenance is requested for the server (`Enforced` or
  `OwnerApproval`), same semantics as [`ServerMaintenance`](servermaintenance.md).
- `spec.serverMaintenanceRef` is managed by the controller (or may be pre-populated to reuse an existing
  `ServerMaintenance`).
- `status.state` reflects the overall lifecycle: `Pending`, `InProgress`, `Applied`, or `Failed`. Each entry in
  `status.flowState[]` mirrors this per settings-flow step, so a stuck rollout can be pinpointed to the exact step.
- `spec.retryPolicy.maxAttempts` bounds automatic retries after a transient failure.

## Workflow

1. The controller validates `spec.settingsFlow` for duplicate step names and duplicate setting keys across steps.
2. It waits until the server's BIOS version matches `spec.version`, then requests (or reuses) `ServerMaintenance`
   for the server per `spec.serverMaintenancePolicy`.
3. Once the server is in maintenance, the controller applies each `settingsFlow` step in priority order, powering
   the server off/on when a reboot is required to make settings take effect.
4. After each step is applied, the controller verifies it against the live BIOS settings before advancing to the
   next step, recording progress in `status.flowState[]`.
5. Once all steps are verified, `status.state` becomes `Applied`; on unrecoverable failure, `Failed` (subject to
   `spec.retryPolicy`).
6. On deletion, the controller cleans up the `ServerMaintenance` it requested and removes its finalizer once no
   maintenance for this object is still in progress.

## Example

```yaml
apiVersion: system.metal.ironcore.dev/v1alpha1
kind: BIOSSettings
metadata:
  name: bios-settings-sample
spec:
  serverRef:
    name: endpoint-sample-system-0
  version: P79 v1.45 (12/06/2017)
  serverMaintenancePolicy: OwnerApproval
  settingsFlow:
    - name: boot-settings
      priority: 10
      settings:
        bootMode: "Uefi"
```
