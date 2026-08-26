# BMCSettings

`BMCSettings` applies a desired set of BMC (baseboard management controller) manager settings to exactly one
`BMC` object. Because a single BMC can manage multiple `Server` objects, the controller requests
[`ServerMaintenance`](servermaintenance.md) for every server managed by the referenced BMC before making changes.

## Key Points

- `BMCSettings` is cluster-scoped and immutably bound to one BMC via `spec.bmcRef`.
- Settings are only applied once the BMC's current firmware version matches `spec.version`; otherwise the object
  waits in `Pending`.
- `spec.settings` is a flat map of BMC manager settings. Values may reference `spec.variables` using `$(VarName)`
  syntax, resolved against the `BMCSettings` object itself, the referenced `BMC`, `ConfigMap`s, or `Secret`s.
- `spec.serverMaintenancePolicy` controls how maintenance is requested for the affected servers:
  - **Enforced:** maintenance starts immediately, without owner approval.
  - **OwnerApproval:** requires the `metal.ironcore.dev/maintenance-approved: "true"` label on the servers'
    [`ServerClaim`](https://github.com/ironcore-dev/metal-operator/blob/main/docs/concepts/serverclaim.md).
- `spec.serverMaintenanceRefs[]` is populated and managed by the controller; it tracks one `ServerMaintenance` per
  server that needs to be in maintenance for the settings change to be applied safely.
- `status.state` reflects the overall lifecycle: `Pending`, `InProgress`, `Applied`, or `Failed`.
- `spec.retryPolicy.maxAttempts` bounds automatic retries after a transient failure; if unset, the
  operator-level default (`--default-failed-auto-retry-count`) is used.

## Workflow

1. The controller resolves the referenced `BMC` and waits until its firmware version matches `spec.version`.
2. It fetches every `Server` managed by the BMC and requests `ServerMaintenance` for each one that is not already
   in maintenance, according to `spec.serverMaintenancePolicy`.
3. Once all required servers are in maintenance, the controller resolves `spec.variables` and diffs the desired
   `spec.settings` against the BMC's current manager settings.
4. Any drifted settings are issued to the BMC. If the BMC requires a reset to apply them, the controller powers the
   BMC off/on as needed and waits for it to come back.
5. The controller verifies the settings converged and marks `status.state` as `Applied`; on unrecoverable failure,
   `Failed` (subject to `spec.retryPolicy`).
6. On deletion, the controller cleans up the `ServerMaintenance` objects it created and removes its finalizer once
   no maintenance for this object is still in progress.

## Example

```yaml
apiVersion: baseboard.metal.ironcore.dev/v1alpha1
kind: BMCSettings
metadata:
  name: bmc-settings-sample
spec:
  bmcRef:
    name: endpoint-sample
  version: 1.45.455b66-rev4
  serverMaintenancePolicy: Enforced
  settings:
    bootMode: "UEFI"
    hyperThreading: "Enabled"
```
