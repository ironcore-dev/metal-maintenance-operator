# Readiness Gates

Readiness gates let independent [`BMCSettings`](bmcsettings.md), [`BMCVersion`](bmcversion.md),
[`BIOSVersion`](biosversion.md), and [`BIOSSettings`](biossettings.md) objects be chained into a deterministic,
manually-defined sequence — e.g. "upgrade the BMC firmware, then apply BMC settings, then upgrade the BIOS, then
apply BIOS settings" — without any of them needing to know about each other directly. Semantics mirror Kubernetes
`Pod.Spec.ReadinessGates`: each object can declare conditions that must be present (with a required status) on its
related `Server`(s) before it starts applying, and/or publish its own completion as a condition on those same
`Server`(s) for downstream objects to gate on.

This is an optional, opt-in mechanism — resources with no `readinessGates`/`completionConditionType` configured
behave exactly as described in their own concept pages, unaffected by any of this.

## Key Points

- Both fields come from the shared `ReadinessGating` struct, inlined into every resource's own template type
  (`BMCSettingsTemplate`, `BMCVersionTemplate`, `BIOSVersionTemplate`, `BIOSSettingsTemplate`) — so the `*Set`
  variants ([`BMCSettingsSet`](bmcsettingsset.md), [`BMCVersionSet`](bmcversionset.md),
  [`BIOSVersionSet`](biosversionset.md), [`BIOSSettingsSet`](biossettingsset.md)) propagate them to every child
  for free, just like any other template field.
- `spec.readinessGates[]` is a list of `{type, requiredStatus}` pairs. `requiredStatus` defaults to `True` if
  omitted. A gate is satisfied only when a condition of the given `type` is present on the related `Server` with
  exactly that status — a missing condition never satisfies a gate, regardless of `requiredStatus`.
- For `BMCSettings`/`BMCVersion` (which can affect every `Server` managed by one `BMC`), gates use AND semantics
  across all related servers: every listed gate must be satisfied on every related `Server` before the object
  proceeds.
- `spec.completionConditionType`, if set, is the condition `Type` this object patches (status `True`) onto its
  related `Server`(s) once it reaches its own terminal success state (`Applied` for `BMCSettings`/`BIOSSettings`,
  `Completed` for `BMCVersion`/`BIOSVersion`). Downstream objects reference this exact string in their own
  `readinessGates[].type` to gate on it. It must be a valid qualified name (optionally `prefix/name`), max 316
  characters.
- There is no built-in ownership/uniqueness check on `completionConditionType` — any object can declare (and
  overwrite) any condition `Type` on a `Server`, including one another resource already uses. Pick a distinct,
  intentional name per logical signal in the chain (e.g. a shared prefix like `e2e-gate/step1-done`) to avoid two
  unrelated resources colliding on the same `Server` condition. This is a non-issue across `*Set` children, since
  each child acts on its own distinct `Server`/`BMC`.
- Each gated object also tracks its own last gate-check outcome via a `ReadinessGatesSatisfied` condition in its
  **own** `status.conditions` (not the `Server`'s) — `False` with reasons while blocked, `True` once satisfied.
  Objects with no `readinessGates` configured never gain this condition.

## How Gating Works

1. On every reconcile while `Pending`, the controller first ensures its own `completionConditionType` (if set)
   exists on the related `Server`(s), initializing it to `False` if absent. This happens *before* checking its
   own gates, so a sibling relying on this condition never races against it simply not existing yet.
2. The controller then evaluates `spec.readinessGates` against the related `Server`(s)' current conditions. If any
   gate is unsatisfied, it records the reasons on its own `ReadinessGatesSatisfied` condition (`False`) and waits;
   it is re-triggered automatically via a `Server` watch once the relevant condition changes, no polling required.
3. Once all gates are satisfied, `ReadinessGatesSatisfied` is marked `True` and the object proceeds with its normal
   workflow (requesting `ServerMaintenance`, applying settings/firmware, etc. — see each resource's own page).
4. Once maintenance is granted and real apply work is about to start, the controller patches its
   `completionConditionType` to `Unknown` — a transitional signal that this resource's completion state can no
   longer be trusted by downstream/frozen gates until it either succeeds or fails.
5. On success, the controller patches `completionConditionType` to `True`. On an already-`Applied`/`Completed`
   object, if a later reconcile detects the live hardware no longer matches spec ("drift"), the condition is reset
   to `False` with `Reason: DriftDetected` while the object re-converges.
6. A condition with `Reason: DriftDetected` never satisfies *any* gate watching it — neither `requiredStatus: True`
   nor `requiredStatus: False` — until it settles back to a stable `True`/`False`. This closes a race in the
   "freeze gate" pattern below, where a transient drift-reset on a downstream condition could otherwise be
   misread as "permanently unsatisfied" by an upstream object waiting for exactly that.

## Example: A Multi-Step Chain

Each step gates on the previous step's `completionConditionType`, all coordinating through the same `Server`:

```yaml
apiVersion: baseboard.metal.ironcore.dev/v1alpha1
kind: BMCVersion
metadata:
  name: step1-bmc-version
spec:
  bmcRef:
    name: endpoint-sample
  version: 1.46.455b66-rev1
  image:
    URI: https://example.com/firmware/bmc-1.46.455b66-rev1.bin
  completionConditionType: rollout/step1-bmc-version-done
---
apiVersion: baseboard.metal.ironcore.dev/v1alpha1
kind: BMCSettings
metadata:
  name: step2-bmc-settings
spec:
  bmcRef:
    name: endpoint-sample
  version: 1.46.455b66-rev1
  settingsFlow:
    - name: boot-settings
      priority: 10
      settings:
        bootMode: "UEFI"
  readinessGates:
    - type: rollout/step1-bmc-version-done
      requiredStatus: "True"
  completionConditionType: rollout/step2-bmc-settings-done
---
apiVersion: system.metal.ironcore.dev/v1alpha1
kind: BIOSVersion
metadata:
  name: step3-bios-version
spec:
  serverRef:
    name: endpoint-sample-system-0
  version: P79 v1.46 (01/15/2018)
  image:
    URI: https://example.com/firmware/bios-P79-v1.46.bin
  readinessGates:
    - type: rollout/step2-bmc-settings-done
      requiredStatus: "True"
  completionConditionType: rollout/step3-bios-version-done
```

`step2-bmc-settings` stays `Pending` (with `ReadinessGatesSatisfied: False`) until `step1-bmc-version` reaches
`Completed` and patches `rollout/step1-bmc-version-done: True` on the `Server`; `step3-bios-version` in turn waits
for `step2-bmc-settings` the same way. Ordering across the whole chain can be confirmed after the fact by
comparing each condition's `lastTransitionTime` on the `Server` object.
