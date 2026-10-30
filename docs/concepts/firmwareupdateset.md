# FirmwareUpdateSet

`FirmwareUpdateSet` performs a declarative firmware update rollout across a label-selected fleet of
`Server` objects, by creating and managing one child [`FirmwareUpdate`](firmwareupdate.md) per
selected server.

## Key Points

- `FirmwareUpdateSet` is namespaced. `spec.serverSelector` is a label selector identifying the
  target servers.
- `spec.firmwareUpdateTemplate` (a `FirmwareUpdateTemplate`, the same template embedded in
  `FirmwareUpdate.spec`) is copied into every child `FirmwareUpdate` created for a matching server,
  including `dellRepository`, `serverMaintenancePolicy`, `retryPolicy`, and
  `progressDeadlineSeconds`.
- `spec.firmwareUpdateTemplate.ttlSecondsAfterFinished` is the one exception: it is **not** copied
  onto owned children (an individually self-deleting child would just be recreated for its still-
  selected server on the next reconcile). Instead, the `FirmwareUpdateSet` itself reads this value
  directly to delete itself that many seconds after all of its owned `FirmwareUpdate`s have reached
  `Completed`, using the most recent `status.lastProgressTime` among them as the completion-time
  proxy (the same proxy each standalone `FirmwareUpdate` uses for its own TTL).
- The controller owns and reconciles its children through the shared generic child-management
  helper (`utils.ChildManager`): it creates a child for every newly-matching server, and deletes
  children whose server no longer matches the selector.
- Only a child actively `InProgress` can hold an active `ServerMaintenance` window; deleting a
  `Pending`, `Completed`, or `Failed` child is always safe, so the orphan-deletion check only waits
  on `InProgress` children (their owned `ServerMaintenance` is cascade-deleted and releases/unparks
  the server on its own, tolerating the server already being gone).
- `status` aggregates rollout progress across all children: `fullyLabeledServers`,
  `availableFirmwareUpdate`, `pendingFirmwareUpdate`, `inProgressFirmwareUpdate`,
  `completedFirmwareUpdate`, and `failedFirmwareUpdate`.

## Workflow

1. The controller lists all `Server` objects matching `spec.serverSelector`.
2. For every matching server without an owned `FirmwareUpdate` child, it creates one from
   `spec.firmwareUpdateTemplate`.
3. For every owned child whose server no longer matches the selector, the controller deletes the
   child once it is not `InProgress`.
4. Each child `FirmwareUpdate` reconciles independently, following its own
   [workflow](firmwareupdate.md#workflow-dell), including requesting `ServerMaintenance` for its
   server.
5. The controller recomputes `status` counters from the current set of owned children on every
   reconcile.
6. Once `status.fullyLabeledServers` equals `status.completedFirmwareUpdate` (all owned
   `FirmwareUpdate`s are `Completed`) and `spec.firmwareUpdateTemplate.ttlSecondsAfterFinished` is
   set, the controller deletes the `FirmwareUpdateSet` itself once that many seconds have elapsed
   since the last child completed.

## Example

```yaml
apiVersion: system.metal.ironcore.dev/v1alpha1
kind: FirmwareUpdateSet
metadata:
  name: firmware-updateset-sample
  namespace: default
spec:
  serverSelector:
    matchLabels:
      metal.ironcore.dev/vendor: dell
  firmwareUpdateTemplate:
    dellRepository:
      shareType: HTTPS
      address: example.repository.com
      shareName: /hw-firmware/firmware_80u3_04_2026
      catalogFile: Catalog.xml
      rebootNeeded: true
    serverMaintenancePolicy: OwnerApproval
    progressDeadlineSeconds: 3600
    ttlSecondsAfterFinished: 86400
```
