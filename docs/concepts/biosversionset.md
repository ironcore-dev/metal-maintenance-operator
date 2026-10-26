# BIOSVersionSet

`BIOSVersionSet` performs a declarative BIOS firmware rollout across a label-selected fleet of `Server` objects,
by creating and managing one child [`BIOSVersion`](biosversion.md) per selected server.

## Key Points

- `BIOSVersionSet` is cluster-scoped. `spec.serverSelector` is a label selector identifying the target servers.
- `spec.biosVersionTemplate` (a `BIOSVersionTemplate`, the same template embedded in `BIOSVersion.spec`) is copied
  into every child `BIOSVersion` created for a matching server.
- The controller owns and reconciles its children: it creates a child for every newly-matching server, and deletes
  children whose server no longer matches the selector (once the child is no longer `InProgress`).
- `status` aggregates rollout progress across all children: `fullyLabeledServers`, `availableBIOSVersion`,
  `pendingBIOSVersion`, `inProgressBIOSVersion`, `completedBIOSVersion`, and `failedBIOSVersion`.

## Workflow

1. The controller lists all `Server` objects matching `spec.serverSelector`.
2. For every matching server without an owned `BIOSVersion` child, it creates one from
   `spec.biosVersionTemplate`.
3. For every owned child whose server no longer matches the selector, the controller deletes the child once it is
   not `InProgress`.
4. Each child `BIOSVersion` reconciles independently, following its own [workflow](biosversion.md#workflow),
   including requesting `ServerMaintenance` for its server.
5. The controller recomputes `status` counters from the current set of owned children on every reconcile.

## Example

```yaml
apiVersion: system.metal.ironcore.dev/v1alpha1
kind: BIOSVersionSet
metadata:
  name: bios-versionset-sample
spec:
  serverSelector:
    matchLabels:
      metal.ironcore.dev/vendor: dell
  biosVersionTemplate:
    version: P79 v1.46 (01/15/2018)
    serverMaintenancePolicy: OwnerApproval
    image:
      URI: https://example.com/firmware/bios-P79-v1.46.bin
```
