# BMCVersion

`BMCVersion` upgrades the firmware of exactly one `BMC` object to a desired version, using an upgrade image served
from an external location. Because a BMC upgrade can affect every `Server` it manages, the controller requests
[`ServerMaintenance`](servermaintenance.md) for those servers before starting the upgrade.

## Key Points

- `BMCVersion` is cluster-scoped and immutably bound to one BMC via `spec.bmcRef`.
- `spec.version` is the desired BMC firmware version; `spec.image` supplies the upgrade image URI (and optional
  transfer protocol / credentials via `spec.image.secretRef`).
- `spec.updatePolicy: Force` instructs the BMC's upgrade service to bypass vendor update policies (e.g. downgrade
  protection), when supported.
- `spec.serverMaintenancePolicy` controls how maintenance is requested for the affected servers (`Enforced` or
  `OwnerApproval`), same semantics as [`BMCSettings`](bmcsettings.md).
- `spec.serverMaintenanceRefs[]` and `status.upgradeTask` are managed by the controller; the latter tracks the
  BMC-reported upgrade task (URI, state, status, percent complete).
- `status.state` reflects the overall lifecycle: `Pending`, `InProgress`, `Completed`, or `Failed`.
- `spec.retryPolicy.maxAttempts` bounds automatic retries after a transient failure.
- `spec.readinessGates`/`spec.completionConditionType` (optional) let this object participate in a manual
  cross-resource sequencing chain — see [Readiness Gates](readiness-gates.md).

## Workflow

1. If `spec.readinessGates` is set, the controller waits until those conditions are satisfied on every `Server`
   managed by the BMC before proceeding (see [Readiness Gates](readiness-gates.md)).
2. The controller requests `ServerMaintenance` for every server managed by the referenced BMC, per
   `spec.serverMaintenancePolicy`.
3. Once all required servers are in maintenance, it issues the firmware upgrade to the BMC using `spec.image`.
4. It polls the BMC-reported task in `status.upgradeTask` until it completes, fails, or times out.
5. On success, `status.state` becomes `Completed` (patching `spec.completionConditionType` `True` on every related
   `Server`, if set); on unrecoverable failure, `Failed` (subject to `spec.retryPolicy`).
6. On deletion, the controller cleans up the `ServerMaintenance` objects it created and removes its finalizer once
   the upgrade is no longer in progress.

## Example

```yaml
apiVersion: baseboard.metal.ironcore.dev/v1alpha1
kind: BMCVersion
metadata:
  name: bmc-version-sample
spec:
  bmcRef:
    name: endpoint-sample
  version: 1.46.455b66-rev1
  serverMaintenancePolicy: Enforced
  image:
    URI: https://example.com/firmware/bmc-1.46.455b66-rev1.bin
```
