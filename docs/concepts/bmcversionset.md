# BMCVersionSet

`BMCVersionSet` performs a declarative firmware rollout across a label-selected fleet of `BMC` objects, by
creating and managing one child [`BMCVersion`](bmcversion.md) per selected BMC.

## Key Points

- `BMCVersionSet` is cluster-scoped. `spec.bmcSelector` is a label selector identifying the target BMCs.
- `spec.bmcVersionTemplate` (a `BMCVersionTemplate`, the same template embedded in `BMCVersion.spec`) is copied
  into every child `BMCVersion` created for a matching BMC.
- The controller owns and reconciles its children: it creates a child for every newly-matching BMC, and deletes
  children whose BMC no longer matches the selector (once the child is no longer `InProgress`).
- `status` aggregates rollout progress across all children: `fullyLabeledBMCs`, `availableBMCVersion`,
  `pendingBMCVersion`, `inProgressBMCVersion`, `completedBMCVersion`, and `failedBMCVersion`.

## Workflow

1. The controller lists all `BMC` objects matching `spec.bmcSelector`.
2. For every matching BMC without an owned `BMCVersion` child, it creates one from `spec.bmcVersionTemplate`.
3. For every owned child whose BMC no longer matches the selector, the controller deletes the child once it is not
   `InProgress`.
4. Each child `BMCVersion` reconciles independently, following its own [workflow](bmcversion.md#workflow),
   including requesting `ServerMaintenance` for the servers it manages.
5. The controller recomputes `status` counters from the current set of owned children on every reconcile.

## Example

```yaml
apiVersion: baseboard.metal.ironcore.dev/v1alpha1
kind: BMCVersionSet
metadata:
  name: bmc-versionset-sample
spec:
  bmcSelector:
    matchLabels:
      metal.ironcore.dev/vendor: lenovo
  bmcVersionTemplate:
    version: 1.46.455b66-rev1
    serverMaintenancePolicy: Enforced
    image:
      URI: https://example.com/firmware/bmc-1.46.455b66-rev1.bin
```
