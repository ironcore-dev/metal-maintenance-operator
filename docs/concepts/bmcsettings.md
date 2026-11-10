# BMCSettings

`BMCSettings` applies a desired set of BMC (baseboard management controller) manager settings to exactly one
`BMC` object. Because a single BMC can manage multiple `Server` objects, the controller requests
[`ServerMaintenance`](servermaintenance.md) for every server managed by the referenced BMC before making changes.

## Key Points

- `BMCSettings` is cluster-scoped and immutably bound to one BMC via `spec.bmcRef`.
- Settings are only applied once the BMC's current firmware version matches `spec.version`; otherwise the object
  waits in `Pending`.
- `spec.settingsFlow` holds the BMC settings to apply. Values may reference `spec.variables` using `$(VarName)` syntax,
  resolved against the `BMCSettings` object itself, the referenced `BMC`, `ConfigMap`s, or `Secret`s. A `ConfigMap` or
  `Secret` can only be read if it is opted in — see [Variable Sources](#variable-sources).
- `spec.serverMaintenancePolicy` controls how maintenance is requested for the affected servers:
  - **Enforced:** maintenance starts immediately, without owner approval.
  - **OwnerApproval:** requires the `metal.ironcore.dev/maintenance-approved: "true"` label on the servers'
    [`ServerClaim`](https://github.com/ironcore-dev/metal-operator/blob/main/docs/concepts/serverclaim.md).
- `spec.serverMaintenanceRefs[]` is populated and managed by the controller; it tracks one `ServerMaintenance` per
  server that needs to be in maintenance for the settings change to be applied safely.
- `status.state` reflects the overall lifecycle: `Pending`, `InProgress`, `Applied`, or `Failed`.
- `spec.retryPolicy.maxAttempts` bounds automatic retries after a transient failure; if unset, the
  operator-level default (`--default-failed-auto-retry-count`) is used.
- `spec.readinessGates`/`spec.completionConditionType` (optional) let this object participate in a manual
  cross-resource sequencing chain — see [Readiness Gates](readiness-gates.md).
- `spec.settingsFlow` (optional) applies settings in discrete, ordered priority groups instead of as one flat map —
  see [Settings Flow](#settings-flow) below.

## Workflow

1. The controller resolves the referenced `BMC` and waits until its firmware version matches `spec.version`.
2. If `spec.readinessGates` is set, it waits until those conditions are satisfied on every `Server` managed by the
   BMC before proceeding (see [Readiness Gates](readiness-gates.md)).
3. It fetches every `Server` managed by the BMC and requests `ServerMaintenance` for each one that is not already
   in maintenance, according to `spec.serverMaintenancePolicy`.
4. Once all required servers are in maintenance, the controller resolves `spec.variables` and diffs the desired
   settings against the BMC's current manager settings. Groups from `spec.settingsFlow` are applied one at a time
   in priority order (see [Settings Flow](#settings-flow)).
5. Any drifted settings are issued to the BMC. If the BMC requires a reset to apply them, the controller powers the
   BMC off/on as needed and waits for it to come back.
6. The controller verifies the settings converged and marks `status.state` as `Applied` (patching
   `spec.completionConditionType` `True` on every related `Server`, if set); on unrecoverable failure, `Failed`
   (subject to `spec.retryPolicy`).
7. On deletion, the controller cleans up the `ServerMaintenance` objects it created and removes its finalizer once
   no maintenance for this object is still in progress.

## Settings Example

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
  settingsFlow:
    - name: main
      priority: 1
      settings:
        bootMode: "UEFI"
        hyperThreading: "Enabled"
```

## Variable Sources

`spec.variables[].valueFrom.secretKeyRef` and `configMapKeyRef` name an object by namespace and name. `BMCSettings` is
cluster-scoped and the operator can read objects in any namespace its cache covers, so the reference alone does not
grant access. Two controls apply:

- **Label (always):** the controller reads a `Secret` or `ConfigMap` only if it carries the label
  `metal.ironcore.dev/bmcsettings-variable-source: "true"`. A missing object and an unlabelled one produce the same
  error, so a reference cannot be used to probe for objects. Only label objects that are meant to be written to
  BMCs, and keep them in a namespace that holds nothing else.
- **Namespaces (opt-in):** `--secret-cache-namespaces` and `--configmap-cache-namespaces` limit the namespaces the
  operator's cache covers, so an object outside them cannot be read at all. `--secret-cache-namespaces` also applies
  to every other Secret the operator reads (credential and image references, sanitization), so list every
  namespace those use. `--manager-namespace` and `--sanitization-namespace` are added automatically. If the flag is
  empty, the Secret cache is not restricted.

Resolved values are not stored in `status` (only a keyed hash of applied values is). When the BMC rejects a setting
during validation, the `SettingsValidationFailed` condition lists the setting name and the reason only, not the
rejected value.

**Example** — a syslog server read from an opted-in `ConfigMap`:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: bmc-settings-config
  namespace: kube-system
  labels:
    metal.ironcore.dev/bmcsettings-variable-source: "true"
data:
  syslogServer: remotesyslog.example.com
---
apiVersion: baseboard.metal.ironcore.dev/v1alpha1
kind: BMCSettings
metadata:
  name: bmc-settings-syslog
spec:
  bmcRef:
    name: endpoint-sample
  version: 1.45.455b66-rev4
  serverMaintenancePolicy: Enforced
  variables:
    - key: SyslogServer
      valueFrom:
        configMapKeyRef:
          name: bmc-settings-config
          namespace: kube-system
          key: syslogServer
  settingsFlow:
    - name: main
      priority: 1
      settings:
        SysLog.1.Server1: "$(SyslogServer)"
```

**Upgrading:** add the label to every `Secret` and `ConfigMap` your `BMCSettings` and `BMCSettingsSet` variables
already reference *before* rolling out a version with this check. Without it, resolution fails with
`... not found or not labelled metal.ironcore.dev/bmcsettings-variable-source=true` and the affected `BMCSettings`
stop progressing.

## Settings Flow

`spec.settingsFlow` provides ordered, staged application of BMC settings. Each entry is a named priority group with
its own `settings` map; groups are applied in ascending `priority` order (lower number = applied first), one
maintenance cycle at a time.

**Why use multiple groups?**  Some BMC firmware requires certain settings (e.g. `SecureBootMode`) to be applied and
verified before dependent settings (e.g. `BootMode`) can take effect. Multiple `settingsFlow` entries let you encode
that sequencing explicitly. A single-entry `settingsFlow` applies all settings in one maintenance cycle.

**How it works:**

1. Before entering maintenance, the controller checks whether the BMC already matches the full merged settings of all
   groups. If it does, every unapplied group is marked `Applied` immediately — no maintenance is requested.
2. Otherwise, the controller finds the first group (in priority order) whose desired settings are not yet reflected on
   the BMC and requests maintenance to apply that group only.
3. After each maintenance cycle the group is marked `Applied` in `status.flowState`; the controller then moves on to
   the next pending group in the next reconcile.
4. Once every group is `Applied` and the BMC matches the fully merged settings, `status.state` becomes `Applied`.

**Key fields:**

| Field | Description |
|---|---|
| `spec.settingsFlow[].name` | Unique label for the group (used in `status.flowState`). |
| `spec.settingsFlow[].priority` | Ascending application order; lower numbers are applied first. |
| `spec.settingsFlow[].settings` | Flat map of BMC settings for this group. Supports `$(VarName)` variable interpolation. |
| `status.flowState[].state` | Per-group state: `Pending`, `InProgress`, `Applied`, or `Failed`. |

**Example — apply SecureBoot settings before boot-mode settings:**

```yaml
apiVersion: baseboard.metal.ironcore.dev/v1alpha1
kind: BMCSettings
metadata:
  name: bmc-settings-staged
spec:
  bmcRef:
    name: endpoint-sample
  version: 1.45.455b66-rev4
  serverMaintenancePolicy: Enforced
  settingsFlow:
    - name: secure-boot
      priority: 1
      settings:
        SecureBootMode: "UserMode"
    - name: boot-config
      priority: 2
      settings:
        BootMode: "UEFI"
        HttpBootUri: "https://boot.example.com/ipxe.efi"
```
