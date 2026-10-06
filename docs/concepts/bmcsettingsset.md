# BMCSettingsSet

`BMCSettingsSet` performs a declarative rollout of the same BMC settings across a label-selected fleet of `BMC`
objects, by creating and managing one child [`BMCSettings`](bmcsettings.md) per selected BMC.

## Key Points

- `BMCSettingsSet` is cluster-scoped. `spec.bmcSelector` is a label selector identifying the target BMCs.
- `spec.bmcSettingsTemplate` (a `BMCSettingsTemplate`, the same template embedded in `BMCSettings.spec`) is copied
  into every child `BMCSettings` created for a matching BMC.
- The controller owns and reconciles its children: it creates a child for every newly-matching BMC, and deletes
  children whose BMC no longer matches the selector (once the child is no longer `InProgress`).
- `status` aggregates rollout progress across all children: `fullyLabeledBMCs`, `availableBMCSettings`,
  `pendingBMCSettings`, `inProgressBMCSettings`, `completedBMCSettings`, and `failedBMCSettings`.
- `spec.bmcSettingsTemplate.readinessGates`/`completionConditionType` (optional) are propagated as-is to every
  child, so the whole fleet can participate in a manual [Readiness Gates](readiness-gates.md) chain; since each
  child targets a distinct `BMC`, using the same `completionConditionType` across all children is safe (no
  collision, as each is patched onto that child's own servers).

## Workflow

1. The controller lists all `BMC` objects matching `spec.bmcSelector`.
2. For every matching BMC without an owned `BMCSettings` child, it creates one from `spec.bmcSettingsTemplate`.
3. For every owned child whose BMC no longer matches the selector, the controller deletes the child once it is not
   `InProgress`.
4. Each child `BMCSettings` reconciles independently, following its own [workflow](bmcsettings.md#workflow),
   including requesting `ServerMaintenance` for the servers it manages.
5. The controller recomputes `status` counters from the current set of owned children on every reconcile.

## Example

```yaml
apiVersion: baseboard.metal.ironcore.dev/v1alpha1
kind: BMCSettingsSet
metadata:
  name: bmc-settingsset-sample
spec:
  bmcSelector:
    matchLabels:
      metal.ironcore.dev/vendor: lenovo
  bmcSettingsTemplate:
    version: 1.45.455b66-rev4
    serverMaintenancePolicy: Enforced
    settings:
      bootMode: "UEFI"
      hyperThreading: "Enabled"
```
