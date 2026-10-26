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

## Workflow

1. The controller requests `ServerMaintenance` for every server managed by the referenced BMC, per
   `spec.serverMaintenancePolicy`.
2. Once all required servers are in maintenance, it issues the firmware upgrade to the BMC using `spec.image`.
3. It polls the BMC-reported task in `status.upgradeTask` until it completes, fails, or times out.
4. On success, `status.state` becomes `Completed`; on unrecoverable failure, `Failed` (subject to
   `spec.retryPolicy`).
5. On deletion, the controller cleans up the `ServerMaintenance` objects it created and removes its finalizer once
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
