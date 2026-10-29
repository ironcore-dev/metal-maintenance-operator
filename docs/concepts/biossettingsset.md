# BIOSSettingsSet

`BIOSSettingsSet` performs a declarative BIOS settings rollout across a label-selected fleet of `Server` objects,
by creating and managing one child [`BIOSSettings`](biossettings.md) per selected server.

## Key Points

- `BIOSSettingsSet` is cluster-scoped. `spec.serverSelector` is a label selector identifying the target servers.
- `spec.biosSettingsTemplate` (a `BIOSSettingsTemplate`, the same template embedded in `BIOSSettings.spec`) is
  copied into every child `BIOSSettings` created for a matching server.
- The controller owns and reconciles its children: it creates a child for every newly-matching server, and deletes
  children whose server no longer matches the selector (once the child is no longer `InProgress`).
- `status` aggregates rollout progress across all children: `fullyLabeledServers`, `availableBIOSSettings`,
  `pendingBIOSSettings`, `inProgressBIOSSettings`, `completedBIOSSettings`, and `failedBIOSSettings`.

## Workflow

1. The controller lists all `Server` objects matching `spec.serverSelector`.
2. For every matching server without an owned `BIOSSettings` child, it creates one from
   `spec.biosSettingsTemplate`.
3. For every owned child whose server no longer matches the selector, the controller deletes the child once it is
   not `InProgress`.
4. Each child `BIOSSettings` reconciles independently, following its own [workflow](biossettings.md#workflow),
   including requesting `ServerMaintenance` for its server.
5. The controller recomputes `status` counters from the current set of owned children on every reconcile.

## Example

```yaml
apiVersion: system.metal.ironcore.dev/v1alpha1
kind: BIOSSettingsSet
metadata:
  name: bios-settingsset-sample
spec:
  serverSelector:
    matchLabels:
      metal.ironcore.dev/vendor: dell
  biosSettingsTemplate:
    version: P79 v1.45 (12/06/2017)
    serverMaintenancePolicy: OwnerApproval
    settingsFlow:
      - name: boot-settings
        priority: 10
        settings:
          bootMode: "Uefi"
```
