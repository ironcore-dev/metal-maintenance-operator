# BIOSVersion

`BIOSVersion` upgrades the BIOS firmware of exactly one `Server` to a desired version, using an upgrade image
served from an external location.

## Key Points

- `BIOSVersion` is cluster-scoped and immutably bound to one server via `spec.serverRef`.
- `spec.version` is the desired BIOS firmware version; `spec.image` supplies the upgrade image URI (and optional
  transfer protocol / credentials via `spec.image.secretRef`).
- `spec.updatePolicy: Force` instructs the server's upgrade service to bypass vendor update policies, when
  supported.
- `spec.serverMaintenancePolicy` controls how maintenance is requested for the server (`Enforced` or
  `OwnerApproval`), same semantics as [`ServerMaintenance`](servermaintenance.md).
- `spec.serverMaintenanceRef` and `status.upgradeTask` are managed by the controller; the latter tracks the
  BMC-reported upgrade task (state, status, percent complete).
- `status.state` reflects the overall lifecycle: `Pending`, `InProgress`, `Completed`, or `Failed`.
- `spec.retryPolicy.maxAttempts` bounds automatic retries after a transient failure.

## Workflow

1. The controller requests (or reuses) `ServerMaintenance` for the server per `spec.serverMaintenancePolicy`.
2. Once the server is in maintenance, it issues the firmware upgrade using `spec.image`, then powers the server
   off/on as needed to complete the upgrade.
3. It polls the BMC-reported task in `status.upgradeTask` until it completes, fails, or times out
   (`spec.rebootTimeoutExpiry` bounds the reboot wait, configured at the operator level).
4. On success, `status.state` becomes `Completed`; on unrecoverable failure, `Failed` (subject to
   `spec.retryPolicy`).
5. On deletion, the controller cleans up the `ServerMaintenance` it requested and removes its finalizer once the
   upgrade is no longer in progress.

## Example

```yaml
apiVersion: system.metal.ironcore.dev/v1alpha1
kind: BIOSVersion
metadata:
  name: bios-version-sample
spec:
  serverRef:
    name: endpoint-sample-system-0
  version: P79 v1.46 (01/15/2018)
  serverMaintenancePolicy: OwnerApproval
  image:
    URI: https://example.com/firmware/bios-P79-v1.46.bin
```
