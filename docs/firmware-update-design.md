<!--
SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
SPDX-License-Identifier: Apache-2.0
-->

# Design: GitOps-driven Firmware Updates

**Status:** Draft / Proposal
**Date:** 2026-06-12
**Scope:** Extend `metal-maintenance-operator` to perform firmware upgrades of
BIOS, NIC, StorageController, HardDrive and PSU across a heterogeneous
(Dell / HPE / Lenovo) bare-metal fleet, driven entirely by GitOps.

---

## 1. Goal

Let an operations team upgrade firmware for an entire **building block** (a
vSphere cluster, abbreviated **bb**) by editing a single Kubernetes custom
resource in Git. ArgoCD syncs the CR to the API server; this operator picks it
up and remediates every host in the building block to the declared firmware
versions — **without anyone ever specifying a Redfish device path**.

### Requirements

| # | Requirement |
|---|---|
| R1 | Upgrade firmware for BIOS, NIC, StorageController, HardDrive and PSU. |
| R2 | Work across a heterogeneous fleet: Dell, HPE and Lenovo, with differing components. |
| R3 | Exploit homogeneity *within* a building block — every host in a bb has identical hardware in identical slots. |
| R4 | True GitOps: Ops edits one CR per building block declaring target versions, model selectors and firmware bundle paths; ArgoCD + this controller do the rest. |
| R5 | Discover each component and its Redfish endpoint at runtime (query Redfish). Never ask Ops to type a Redfish URI; discover, then pass it as `Targets` to the Redfish `SimpleUpdate` action. |
| R6 | NetBox is the source of truth. The `argora-operator` pulls inventory and labels the resources. Upgrades are expressed at building-block level, **not** per host. |

---

## 2. Confirmed execution model

All five component types require a host restart to activate, so they are managed
together in **one CRD per building block**. Within a host the controller works
**one component at a time, in a fixed canonical order** (see §2b):

```
FirmwareUpdate (one per building block)
  for each host (BMC) in the bb            # strictly one host at a time (rolling)
    pre-flight: force boot-source = Disk (clear any boot override)   # never PXE — see §7a
    # ---- STAGE phase (automatic, non-disruptive: OnReset means nothing reboots) ----
    for each component in CANONICAL order: BIOS, StorageController, HardDrive, NIC, PSU:
        1. discover Targets via the component's model selector
           (BIOS/BMC need no selector — empty Targets; BMC infers from the image)
        2. skip if already at target version (idempotency via firmware inventory)
        3. issue ONE SimpleUpdate POST { ImageURI, TransferProtocol, Targets, OnReset }
        4. poll the returned Task until STAGED/pending-reboot, then proceed
        5. if BIOS staging fails -> ABORT this host (mark Failed, skip the rest)
           (other component failures are recorded but staging CONTINUES — §2b)
    # ---- GATE (rebootPolicy: OwnerApproval waits; Enforced proceeds) ----
    mark host AwaitingReboot; if OwnerApproval, WAIT for maintenance-approved label on host's Server (§2a)
    # ---- ACTIVATE phase (on approval / Enforced) ----
    re-assert boot-source = Disk  ->  power-cycle (owned here, not metal-operator)
    verify versions  ->  clear the approval signal  ->  proceed to the next host
```

Confirmed decisions:

- **One CRD** holds BIOS + NIC + StorageController + HardDrive + PSU. **No
  per-component CRDs and no `*Set` CRDs** — this keeps the Git repo clean and
  operational overhead low.
- **Components are staged in a fixed canonical order**
  (`BIOS → StorageController → HardDrive → NIC → PSU`, see §2b), enforced by the
  controller regardless of CR list order. **Only BIOS staging failure aborts the
  host**; other component failures are recorded and staging continues.
- **One host at a time** across the building block (strict rolling update) to
  protect the live vSphere cluster's capacity.
- **One `SimpleUpdate` POST per component**, processed sequentially with
  `OnReset` apply-time, so staging never reboots the host.
- **The reboot is gated per host by `rebootPolicy`** (see §2a), mirroring
  metal-operator's `ServerMaintenancePolicy`: **`OwnerApproval`** (default) waits
  for an approval signal before rebooting; **`Enforced`** reboots automatically
  once staged. Either way it is `rebootStrategy: SingleAtEnd` — one reboot
  activates all staged firmware.
- **This controller owns the reboot and the boot source.** We do **not** put the
  host into `metal-operator`'s `Maintenance` state, because that state forces a
  persistent **PXE / network-boot override** — fatal for a live ESXi host
  (see §7a). We power-cycle directly and always boot **from disk**.

### 2a. The reboot gate (`rebootPolicy`)

The operator splits work into a **safe, automatic staging phase** and a
**disruptive activation (reboot) phase**. Whether the reboot needs human/external
approval is governed by a per-CR **`rebootPolicy`**, mirroring metal-operator's
own `ServerMaintenancePolicy`:

| `rebootPolicy` | Behavior |
|---|---|
| **`OwnerApproval`** (default) | Controller stages, sets host `AwaitingReboot`, and **waits for an approval signal** before rebooting that host. |
| **`Enforced`** | Controller reboots **automatically** once the host is staged — no human in the loop. |

**Why a policy, not a hardcoded gate:** this is exactly how metal-operator's
`ServerMaintenance` works — `Enforced` proceeds automatically, `OwnerApproval`
waits for a `metal.ironcore.dev/maintenance-approved` label/annotation. We adopt
the same pattern (and the same approval-key convention) so behavior is familiar
to anyone who knows metal-operator, and so **automatic reboot is a first-class
option**, not an afterthought.

The phases:

- **Stage (automatic, both policies):** discover and `SimpleUpdate` every
  component with `OnReset` apply-time. Firmware lands but stays dormant — nothing
  reboots. Poll each Task until staged; then set host `AwaitingReboot`.
- **Gate:**
  - `Enforced` → proceed straight to Activate.
  - `OwnerApproval` → wait until the **host's `Server` CR** carries the label
    **`metal.ironcore.dev/maintenance-approved`** (presence check; value `true` by
    convention). This is exactly metal-operator's approval key and semantics — it
    approves on the per-host `ServerClaim`; we use the same label on the host's
    `Server` (we do not use `ServerClaim`/`ServerMaintenance`, §7a):

    ```sh
    # approve the reboot of one host — set by a human OR an automated
    # drain-orchestrator once the ESXi host is evacuated:
    kubectl label server node001-bb085 metal.ironcore.dev/maintenance-approved=true
    ```

- **Activate:** re-assert boot-from-disk, power-cycle **that one host** (controller
  owns it — §7a). On boot the staged firmware activates.
- **Verify & complete:** read the live firmware version back from Redfish; when it
  equals the target, the host is `Completed`. The controller **removes the
  `maintenance-approved` label** from that host's `Server` (one-shot) and advances
  to the next host (staged and gated in turn). The host is left **powered on,
  booted from disk** (production OS), at the target version.

> **Scope boundary — the operator does not restore the host to the cluster.** It
> guarantees "firmware verified, host booted from disk," and nothing more. Taking
> the ESXi host **out of maintenance mode / un-cordoning / rescheduling VMs is
> *not* this operator's job** — whoever drained the host before approving the
> reboot (vSphere automation / drain-orchestrator) owns restoring it afterward.
> The firmware operator owns *firmware + reboot + boot-source*; the vSphere layer
> owns *drain + restore*. (Symmetric to who sets the approval label, §2a.)

#### Design notes

- **Approval is a label on the host's `Server` CR — metal-operator's exact UX.**
  Same key (`metal.ironcore.dev/maintenance-approved`), same **presence check**
  (value ignored, `true` by convention). metal-operator labels the per-host
  `ServerClaim`; we label the per-host `Server` because we don't use
  `ServerClaim`/`ServerMaintenance`. Set with a plain `kubectl label` (by a human
  *or* an automation); tolerated by ArgoCD (unmanaged metadata is not reverted).
- **Per host by construction.** The label lives on the **individual host's
  `Server`**, so approving one host's reboot can never reboot another. This is what
  lets the **CR stay building-block-level while the reboot is gated per host**.
- **Set by human or machine — `rebootPolicy` decouples the two.** `Enforced` =
  fully automatic (no approval needed). `OwnerApproval` = wait for the label, which
  a human *or* an external drain/evacuation orchestrator may set. The controller
  only checks for the label; it does not care who set it.
- **Cleared on use — surgically.** The controller removes the label after the host
  verifies, so it is one-shot. It **deletes only the `maintenance-approved` key**,
  never replacing the `Server`'s label map — argora stamps NetBox-derived labels
  (`kubernetes.metal.cloud.sap/bb`, …) on the same object, and those must be left
  untouched. *Spike/impl caveat:* confirm this out-of-band key-delete is not
  reverted by ArgoCD/argora re-reconciling the `Server`.
- **We do not use metal-operator's `ServerMaintenance` object itself** — it forces
  a persistent PXE boot override (§7a). We mirror the *pattern* (policy +
  approval-label) but **own the reboot ourselves**, always booting from disk.
- **Vendor staging is assumed for all of Dell/HPE/Lenovo.** This whole model
  depends on `OnReset` staging actually deferring the reboot. The spike **must
  verify this per vendor** (metal-operator wires Dell to `Immediate`; we send
  `OnReset` ourselves via option (c), §8). **Fail-safe:** if a host cannot stage
  (the update would reboot on issue), the controller marks it `Blocked` and does
  **not** proceed — it never reboots a host without staging succeeding.

### 2b. Component ordering (fixed and canonical)

The controller stages components in a **fixed order**, independent of how they
appear in the CR's `components` list:

```
BIOS  →  StorageController  →  HardDrive  →  NIC  →  PSU
```

Rationale:

- **BIOS first** — it underpins the platform, and a bad BIOS should abort the host
  before anything else is touched.
- **StorageController before HardDrive** — the controller (RAID/HBA) sits between
  the host and the drives; controller firmware can change drive
  compatibility/support, while a drive is a leaf that cannot invalidate the
  controller. So drives should land into the controller firmware they will
  actually run under. (This is the safe default; vendor release notes can in rare
  cases invert it — but the order is intentionally **not** Ops-overridable to keep
  behavior predictable. A documented vendor inversion would be handled in code.)
- **NIC / PSU after the storage chain** — largely independent; PSU last as the
  most physical-layer component.

**Abort semantics (per decision):** only a **BIOS** staging failure aborts the
host. A `StorageController` (or any non-BIOS) staging failure is **recorded but
staging continues**. The consequence is deliberate and must be made visible: a
host can reach the gate with drives staged against a controller that failed to
stage. The controller therefore marks the failed component `Failed` **and** sets
a host-level condition (e.g. `PartialStaging`) so the operator sees the mixed
state at the `AwaitingReboot` gate — **before** approving the reboot, which is the
natural inspection point (§2a).

---

## 3. Feasibility — Yes (verified against source)

The hardest parts already exist in `metal-operator`'s `bmc` package (this repo
already depends on `metal-operator v0.5.0`; `gofish` comes transitively):

1. **Cross-vendor Redfish + the `SimpleUpdate` POST mechanics** are implemented in
   `bmc/oem_helpers.go::upgradeVersion()` — shared by Dell/HPE/Lenovo, with
   vendor request-body and task-monitor callbacks. (Supermicro has no firmware
   path and returns "not supported".)
2. **Task polling** is implemented (`getUpgradeTask()` → `GET <taskURI>` →
   `schemas.Task{TaskState, TaskStatus, PercentComplete}`).
3. **Power-cycle + boot-source control** are available on the `bmc.BMC`
   interface (`Reset`, `PowerOn`/`PowerOff`, `SetBootOverride`,
   `ClearBootOverride`). We drive these **directly** — see the caveat below about
   *not* using `ServerMaintenance`.
4. **Component discovery** is fully supported by `gofish` (see §5).

> **Caveat — do not reuse `metal-operator`'s `ServerMaintenance` for reboot.**
> While a `Server` sits in `ServerState == Maintenance`, `metal-operator`'s
> `ServerReconciler` re-asserts a **persistent PXE / network-boot override on
> every reconcile** (`SetBootOverride(persistent=true)`), by design, to keep a
> machine it is provisioning off the production OS. For a live ESXi host that is
> exactly wrong — a reboot would land in a network installer. We therefore own
> the reboot ourselves and force boot-from-disk (see §8).

### Net-new work (our scope)

- **Per-component discovery → `Targets` URI** (R5) for NIC / StorageController /
  HardDrive / PSU. The existing code never populates `Targets`.
- **Sequential multi-component orchestration** within one host (canonical order
  §2b, abort-on-BIOS-failure only, gated single reboot).
- **The `FirmwareUpdate` CRD + controller** that drives all of the above at
  building-block scope.
- **Apply-time control** for single-reboot staging (see §7) — not exposed by
  `metal-operator`'s current exported methods.

---

## 4. Inputs from NetBox / argora (verified)

`argora-operator` (`internal/controller/ironcore_controller.go`) stamps a fixed
label set onto the **`BMC`** and **`BMCSecret`** CRs it creates from NetBox.
Verified keys (relevant subset):

| Label | NetBox source | Relevance |
|---|---|---|
| `kubernetes.metal.cloud.sap/cluster` | `cluster.Name` | the vSphere cluster |
| **`kubernetes.metal.cloud.sap/bb`** | name after `-` | **building block id** |
| `kubernetes.metal.cloud.sap/type` | `device.DeviceType.Slug` | hardware model |
| `topology.kubernetes.io/region` | region | — |

Device naming is `<nodename>-<bb>`. **Selection target:** these labels are on
**`BMC`** CRs (which also carry the Redfish endpoint and `BMCSecretRef`), so the
`FirmwareUpdate` reconciler selects `BMC` resources by the bb label and talks
Redfish to each.

> **No overlap with argora's own `Update` CRD** — that is a NetBox-sync concern
> only (it never touches Redfish/firmware). We use the distinct kind name
> `FirmwareUpdate` and a Kubernetes-native `LabelSelector` (no NetBox calls from
> this operator).

---

## 5. Discovery: model selector → `Targets` (R5)

Ops declares a **model selector** per component; the controller matches it against
the **`UpdateService.FirmwareInventory()`** collection and collects the matching
entries' `@odata.id` into `Targets`.

> **Why FirmwareInventory, not the device tree.** An earlier draft matched
> `Chassis.NetworkAdapters().Model`. A live probe across 15 Dell/HPE/Lenovo
> servers (`hack/nicprobe`, see [nic-discovery-findings.md](nic-discovery-findings.md))
> showed `NetworkAdapter.Model`/`Manufacturer` is **not** a usable cross-vendor
> key: the *same* Mellanox ConnectX-6 Dx reports as
> `ConnectX-6 DX 2-port 100GbE QSFP56 PCIe Adapter` (Dell),
> `P25962-001 / Mellanox MCX623106AS-CDAT ...` (HPE), and `CX623106AN-CDAT`
> (Lenovo) — and is sometimes empty. The **FirmwareInventory `Name`**, by
> contrast, consistently embeds the silicon family (`ConnectX-6 Dx` appears in all
> three), so a keyword match actually works. This also matches what the upstream
> `NICVersion` PR does.

### Discovery sources

| Component | Selector | Discovery source | Match field | → `Targets[]` |
|---|---|---|---|---|
| BIOS | _(none)_ | — | — | **empty** (BMC infers from image) |
| NIC | `modelSelector` | `UpdateService.FirmwareInventory()` | `SoftwareInventory.Name` (substring, case-insensitive) | entry `@odata.id` |
| StorageController | `modelSelector` | `UpdateService.FirmwareInventory()` | `SoftwareInventory.Name` | entry `@odata.id` |
| HardDrive | `modelSelector` | `UpdateService.FirmwareInventory()` | `SoftwareInventory.Name` | entry `@odata.id` |
| PSU | `modelSelector` | `UpdateService.FirmwareInventory()` | `SoftwareInventory.Name` | entry `@odata.id` |

The `Target` is the **FirmwareInventory entry's own `@odata.id`** (e.g.
`/redfish/v1/UpdateService/FirmwareInventory/15/`), which is what `SimpleUpdate`
expects — not a device-tree URI.

> The device tree (`Chassis.NetworkAdapters()`, `System.Storage().Drives()`,
> `Chassis.Power().PowerSupplies`) remains available and may be used as an
> *optional* precision aid (e.g. correlating via `SoftwareInventory.RelatedItem`
> links, or restricting to a physical slot), but it is **not** the primary match
> key.

### Selector tokens — Ops types a short, variant-specific token

Ops types a **short, variant-specific token** in the CR (e.g.
`modelSelector: "cx6dx"`); the controller resolves it to the real distinguishing
string in `FirmwareInventory.Name`, then matches case-insensitive substring.

**The token must carry the variant suffix — a family token is unsafe.** A NIC
*family* is not a single firmware target: ConnectX-6 ships as **CX6 Lx** (≤50GbE)
and **CX6 Dx** (≤100GbE) — distinct cards with distinct firmware. A token of
`cx6 → connectx-6` would match *both* `ConnectX-6 Lx` and `ConnectX-6 Dx`, risking
the wrong firmware on the wrong card. The probe confirms this is live, not
hypothetical: the fleet already carries **both** `ConnectX-5 EN` and
`ConnectX-5 Ex` (distinct cards). So the token granularity must equal the
granularity the firmware name distinguishes — **the controller cannot be more
specific than the data.**

This resolution step is also **required, not cosmetic** for cross-vendor reasons:
the contiguous string `CX6` appears only on the *adapter* `Model` field (Lenovo
`CX623106`, HPE `MCX623106`), whereas `FirmwareInventory.Name` says `ConnectX-6 Dx`
on all three vendors (Dell uppercase `DX`). So `cx6dx` must map to
`connectx-6 dx`, matched case-insensitively.

The mapping is a **small, hardcoded, variant-specific token table** in the
controller — deliberately not a ConfigMap/CRD field, to keep the CR simple:

```go
// canonical Ops token  →  distinguishing substring in FirmwareInventory.Name
var selectorTokens = map[string]string{
    "cx6dx":    "connectx-6 dx",   // ConnectX-6 Dx (≤100GbE)
    "cx6lx":    "connectx-6 lx",   // ConnectX-6 Lx (≤50GbE)
    "cx5en":    "connectx-5 en",   // ConnectX-5 EN  (see CX5 caveat below)
    "cx5ex":    "connectx-5 ex",   // ConnectX-5 Ex  (see CX5 caveat below)
    "bcm57508": "bcm57508",
    // ... one entry per distinct card variant (rare; reviewed like any code)
}
```

> **ConnectX-5 variant caveat — `EN`/`Ex` are not a clean speed axis.** Per
> NVIDIA's product line, ConnectX-5 splits along *orthogonal* axes:
> **`EN`** = Ethernet-only vs **`VPI`** = Ethernet+InfiniBand (protocol), and
> **`Ex`** = enhanced / PCIe Gen 4.0 (performance tier) — and `Ex` *can apply to an
> EN card*. So `ConnectX-5 Ex` does **not** by itself imply a speed, and
> `ConnectX-5 EN` does not imply a PCIe generation. In the **QA** fleet the two
> happen to map cleanly to distinct cards — `ConnectX-5 EN 25GbE SFP28` and
> `ConnectX-5 Ex 100 GbE QSFP` — so `cx5en`/`cx5ex` separate them correctly *there*.
> **But that mapping is fleet-specific, not universal:** production could carry,
> e.g., a `ConnectX-5 EN` at 100GbE, where `cx5en` would resolve to a different
> physical card than it does in QA. The token resolves a *substring*; what physical
> card that substring denotes is a property of the fleet, not of the token. The
> over-match guard (below) is what keeps this safe — a token that matches two
> distinct names on a host blocks rather than guesses. **Tokens are validated
> against captured fixtures per fleet, not assumed to be universal.**

Flow: `nicSelector: "cx6dx"` → `selectorTokens["cx6dx"] = "connectx-6 dx"` →
case-insensitive substring against `FirmwareInventory.Name` → matches
`"ConnectX-6 DX ..."` (Dell) and `"ConnectX-6 Dx ..."` (HPE/Lenovo) → collect each
matched entry's `@odata.id` into `Targets`.

**Over-match guard.** Even with variant-specific tokens, if a resolved substring
matches **two or more distinct model names** on a host, the controller **does not
proceed**: the host goes `Blocked` with a condition listing every distinct name
that matched, and Ops must supply a more specific token. A `SimpleUpdate` is never
issued against an ambiguous match. (Distinctness is judged on the firmware `Name`
after stripping per-port MAC suffixes — see §5 dedup.)

**Under-match guard.** Conversely, if a selector matches **zero** firmware entries
on a host that *does* expose NIC firmware, the controller treats it as a
`Blocked`/warning condition — **not** a silent success. This is essential because
some firmware names are pure marketing strings with no stable identifier (see
Broadcom below): a chip-number token can legitimately match nothing, and a silent
zero-match would skip a NIC that should have been updated — an invisible failure.
Surfacing it prompts Ops to pick a token that matches that bb's actual name.

### Token conventions differ by silicon vendor

The token always resolves to *whatever contiguous substring is stable in
`FirmwareInventory.Name`* — but **which** substring that is depends on the vendor:

- **Mellanox → family + variant** (`connectx-6 dx`, `connectx-5 en`). The family
  name is consistently present across Dell/HPE/Lenovo.
- **Broadcom → bare chip number** (`57508`, `5720`). The chip number is portable:
  Dell writes `Broadcom BCM57508 ...`, Lenovo writes `Broadcom 57508 ...` — both
  contain `57508`. **Exception:** some Dell 25G cards are named by *marketing*
  (`Broadcom Adv. Dual 25Gb Ethernet`, `Broadcom P225p NetXtreme-E ...`) with **no
  chip number** — a `57414` token misses these (under-match guard fires). For such
  a bb, the token maps to the marketing substring instead (e.g. `adv. dual 25gb`).
- **Intel → chip model** (`i350`). Note the *adapter* `Model` may be an opaque OEM
  SKU (Lenovo `J31979`/`N21486`) while the *firmware Name* still says `I350` — a
  further reason to match `FirmwareInventory.Name`, not the adapter model. **Do not
  use `intel` as a token**: it would match HPE's non-NIC `Intelligent Provisioning`
  / `Intelligent Platform Abstraction Data` entries. Always key on the chip model.

```go
var selectorTokens = map[string]string{
    // Mellanox — family + variant
    "cx6dx":    "connectx-6 dx",
    "cx5en":    "connectx-5 en",
    "cx5ex":    "connectx-5 ex",
    // Broadcom — bare chip number (portable across "BCM57508"/"Broadcom 57508")
    "bcm57508": "57508",
    "bcm5720":  "5720",
    "bcm57414": "57414",        // NOTE: misses Dell's "Broadcom Adv. Dual 25Gb" name
    // Intel — chip model (NOT "intel": that matches HPE non-NIC entries)
    "i350":     "i350",
}
```

> **Numeric-token caution.** Bare chip numbers (`5720`, `57508`) are substrings;
> implementation should match on a word boundary so `5720` does not match a
> hypothetical `57208`. The over-match guard is the backstop if it does.

Notes:
- **Case-insensitivity is mandatory** — Dell writes `DX`, HPE/Lenovo write `Dx`.
- **Trade-off:** a new card variant means a one-line addition to `selectorTokens`
  and a controller rebuild. Accepted — new variants are rare and this keeps fleet
  vocabulary out of the CR. An **unknown token is rejected with a clear error**,
  never matched as an empty/loose string.
- Because a building block is single-vendor/single-model (R3), one CR only ever
  faces one vendor's naming; the token map mainly gives Ops a **shared vocabulary
  across CRs** (`cx6dx` means the same thing everywhere).

### The matching gate (per inventory entry, in order)

`FirmwareInventory()` returns `[]*SoftwareInventory{ID, Name, Version, Updateable,
Staged, RelatedItem, @odata.id}`. For each entry whose `Name` matches the
selector:

1. **Dedup vendor rollback slots.** Some vendors (Dell) expose the *same*
   component three times with `Current-` / `Installed-` / `Previous-` ID prefixes
   at different versions. Collapse to the **active** entry (`Installed-`/`Current-`)
   and **ignore `Previous-`**, so a component is counted once. (HPE/Lenovo use
   single IDs and are unaffected.)
2. **`Updateable == true`?** Redfish marks read-only / reporting-only firmware as
   `Updateable: false` ("*the service cannot update this software and the software
   is for reporting purposes only*"). Such an entry is **excluded from `Targets`**
   and surfaced distinctly in status as "matched but not updatable" — never
   silently dropped, never sent to `SimpleUpdate`. (Observed live: an HPE DL560
   Gen11 reports its ConnectX-6 NICs as `Updateable: false`.)
3. **`Version != targetVersion`?** Skip (idempotent) if already at target.

Because a bb is homogeneous (R3), the matched set is uniform across hosts; we
discover per host (defensive) and may cache the bb's shape. Even firmware `Name`
strings are not byte-identical across vendors, so per-bb scoping (single
vendor/model at a time) is what keeps a keyword match reliable.

### Payload construction — one `SimpleUpdate` per card/slot

Once discovery has produced the surviving set (deduped, `Updateable`,
model-matched), the controller issues **one `SimpleUpdate` per matched card/slot** —
not a single batched payload for the whole host. (`Targets` is a Redfish array and
*can* hold many URIs, but we deliberately use a single-target call per card for
per-slot failure isolation and status.)

- **One call per matched card/slot.** If a host has two ConnectX-6 Dx cards
  (`NIC.Slot.4`, `NIC.Slot.8`), the controller issues a `SimpleUpdate` for each,
  with that card's `@odata.id`(s) in `Targets`. (`Targets` is the full Redfish
  path, `/redfish/v1/UpdateService/FirmwareInventory/<id>`, i.e. gofish
  `fw.ODataID` — not the bare member id.)
- **Each call returns its own Task → per-slot tracking.** Status and
  success/failure are reported per card, so a bad card does not obscure the others.
- **Same `ImageURI` across the calls** — the model selector resolves to one card
  model, so every call for that component uses the same firmware image (the
  over-match guard guarantees a single model).
- Calls are issued **sequentially within the host's NIC step**; all are staged
  (`OnReset`) and activate together at the single gated reboot (§2a) — so multiple
  per-slot calls still mean *one* reboot per host.

```jsonc
// issued once per matched card/slot (here: Slot.4, then Slot.8)
POST <UpdateService SimpleUpdate action target>
{
  "ImageURI": "https://fw-repo/.../cx6dx-22.35.10.12.bin",
  "TransferProtocol": "HTTPS",
  "@Redfish.OperationApplyTime": "OnReset",     // Dell sets this; HPE/Lenovo per their builders
  "Targets": [
    "/redfish/v1/UpdateService/FirmwareInventory/Installed-109587-...__NIC.Slot.4-1-1",
    "/redfish/v1/UpdateService/FirmwareInventory/Installed-109587-...__NIC.Slot.4-2-1"
  ]
}
```

**Trade-off (accepted):** more POSTs and Tasks per host than a single batched
payload, in exchange for **per-slot failure isolation and visibility** — the
controller knows exactly which card succeeded or failed. **Spike item:** confirm
the per-call `Targets` granularity each vendor's BMC expects — both port-entries
of a card (as shown) versus a single card-level entry per call.

---

## 6. How metal-operator issues `UpdateService.SimpleUpdate` (verified)

Reference for the mechanics we build on. In `bmc/oem_helpers.go::upgradeVersion()`:

1. `service.UpdateService()` — GET `/redfish/v1/UpdateService`.
2. **Discover the action URI** by unmarshalling `updateService.RawData` and
   reading `Actions."#UpdateService.SimpleUpdate".Target` (not hardcoded).
3. Build the vendor body via the injected `requestBodyFn`.
4. `updateService.PostWithResponse(target, &body)` — gofish's generic
   authenticated POST (not the typed `SimpleUpdate()` helper), so OEM fields can
   be attached.
5. **`202 Accepted` is the only success**; anything else returns `isFatal=true`
   (the request may have partially applied — never blindly re-issue).
6. Extract the Task monitor URI via the injected `taskMonitorURIFn`.

Body = `SimpleUpdateRequestBody { embeds schemas.UpdateServiceSimpleUpdateParameters;
+ RedfishOperationApplyTime "@Redfish.OperationApplyTime" }`. The embedded params
carry `ImageURI`, `TransferProtocol`, `Targets[]`, `Username`, `Password`,
`ForceUpdate`, `Stage`. All three vendor builders copy `body.Targets =
parameters.Targets` verbatim, so a populated `Targets` reaches the wire
unmodified.

Vendor variance is small: **Dell** hardcodes `@Redfish.OperationApplyTime:
Immediate` and reads the Task URI from the `Location` header; **HPE/Lenovo** set
no apply-time and parse the Task URI differently. Vendor dispatch keys on the
`Manufacturer` string — **`"Dell Inc."`**, `"Lenovo"`, `"HPE"`.

---

## 7. Reboot strategy and the apply-time problem (verified)

Single-reboot-at-end requires **staging**: each `SimpleUpdate` lands firmware in
a pending state and a later reboot activates it. In Redfish this is the
`OperationApplyTime` field, which supports `Immediate`, **`OnReset`**,
`AtMaintenanceWindowStart`, `InMaintenanceWindowOnReset`, `OnStartUpdateRequest`
(and `SoftwareInventory` has an `Activate(targets)` action).

**Constraint discovered in `metal-operator`:** its exported `UpgradeBiosVersion` /
`UpgradeBMCVersion` do **not** let us control apply-time —
`dellBuildRequestBody` hardcodes `Immediate`, and HPE/Lenovo send no apply-time
at all. So through the existing exported API we **cannot reliably stage** for a
single end-of-host reboot.

Implications:

- **Single reboot is the goal, not a guarantee.** It is achievable only where the
  vendor/component honors `OnReset` (or maintenance-window) apply-time.
- Where a component applies immediately (e.g. Dell as currently wired), the host
  reboots at that point regardless — the fallback is effectively per-component
  reboot.
- Achieving controllable apply-time pushes us toward **integration option (c)**
  (see §8), where *we* set the apply-time, rather than option (a).
- **Phase 0 must empirically determine per-vendor staging support** before we
  promise single-reboot behavior in the API contract.

### 7a. Boot source — always boot from disk, never PXE

The reboot itself is **owned by this controller**, and every power-on must boot
the host **from disk** so a live ESXi host returns to its production OS.

This is a deliberate divergence from `metal-operator`. Its `ServerReconciler`,
while a `Server` is in `ServerState == Maintenance`, re-asserts a **persistent
network-boot (PXE) override on every reconcile**
(`bmcClient.SetBootOverride(systemURI, true)`) — by design, so a machine it is
re-provisioning never falls through to the production OS. Their own code
comment notes this also guards against *self-initiated* reboots from a vendor
firmware-upgrade task. For our use case the intent is inverted: the host **is**
running production ESXi and must come back on it.

Mitigations baked into the flow:

1. **Do not enter `metal-operator`'s `Maintenance` state.** That state is the
   trigger for the persistent PXE override. The firmware controller manages the
   host lifecycle itself.
2. **Force boot-from-disk before every power-on.** Pre-flight per host, and again
   immediately before each reboot, set the boot source to `Disk` /
   `ClearBootOverride`, so neither our reboot nor a BMC-self-reboot during a
   staged update can land on PXE.
3. **Re-assert on requeue.** Because a vendor task may reboot the host on its own,
   the controller re-asserts boot-from-disk on each reconcile while an update is
   in flight (the mirror image of metal-operator's behavior).

> If draining (cordon/evict ESXi workloads) is wanted later, it must be done
> **without** putting the Server into metal-operator's `Maintenance` state —
> e.g. via vSphere maintenance mode driven externally — so the PXE override is
> never engaged.

---

## 8. `bmc` integration options

The `SimpleUpdate` flow (`upgradeVersion`, `SimpleUpdateRequestBody`, vendor
builders) is **unexported** in `metal-operator`. Only `UpgradeBiosVersion` /
`UpgradeBMCVersion` are exported, and they fix BIOS/BMC semantics + apply-time.

| Option | What | Pros | Cons |
|---|---|---|---|
| **(a)** Reuse exported `UpgradeBiosVersion` with a populated `Targets` | Works today; builders pass `Targets` through | Zero upstream change | Semantically "the BIOS method"; **cannot set apply-time** → no reliable single-reboot; BIOS/BMC only by name |
| **(b)** Add a generic `UpgradeComponent(ctx, manufacturer, params)` upstream | Component-agnostic entrypoint, apply-time as a parameter | Cleanest; quirks live with vendor code; enables staging | Couples to upstream release cadence |
| **(c)** Replicate the ~40-line `upgradeVersion` POST locally via gofish | Full control of body + apply-time + Task parsing | No upstream dependency; immediate | Duplicates vendor quirks; must track upstream drift |

**Decision:** implement **(c)** — replicate the `SimpleUpdate` POST flow locally
in this repo, so we control the request body (including `OperationApplyTime`),
the Task parsing, and the vendor quirks, without depending on an upstream
release. `rebootStrategy: SingleAtEnd` is the **default**.

## 9. Proposed CRD: `FirmwareUpdate` (per building block)

Scaffolded via `kubebuilder create api` (per `AGENTS.md` — never hand-create).

```yaml
apiVersion: maintenance.metal.ironcore.dev/v1alpha1
kind: FirmwareUpdate
metadata:
  name: bb24-firmware                      # one per building block
spec:
  buildingBlockSelector:
    matchLabels:
      kubernetes.metal.cloud.sap/bb: bb24  # argora label; or .../cluster
  bmcCredentialSecretRef:
    name: bb24-bmc-creds

  policy:
    maxConcurrentHosts: 1                  # strict rolling (default 1)
    rebootStrategy: SingleAtEnd            # one reboot activates all staged firmware
    rebootPolicy: OwnerApproval            # OwnerApproval (default, gated) | Enforced (auto) — §2a
    forceUpdate: false

  # List order is NOT significant — the controller stages in a fixed canonical
  # order: BIOS → StorageController → HardDrive → NIC → PSU (§2b).
  # Each entry => one SimpleUpdate POST (OnReset), staged before the gate.
  components:
    - type: BIOS                           # no modelSelector; empty Targets
      targetVersion: "2.19.0"
      image:
        URI: "https://fw-repo.internal/bb24/bios-2.19.0.exe"
        transferProtocol: HTTPS
    - type: NIC
      modelSelector: "cx6dx"               # variant-specific token → "connectx-6 dx", matched vs FirmwareInventory.Name (§5)
      targetVersion: "22.35.1014"
      image: { URI: "https://fw-repo.internal/bb24/cx6dx-22.35.1014.bin", transferProtocol: HTTPS }
    - type: StorageController
      modelSelector: "PERC H750"
      targetVersion: "52.16.1-4405"
      image: { URI: "https://fw-repo.internal/bb24/perc-h750.bin", transferProtocol: HTTPS }
    - type: HardDrive
      modelSelector: "MZ7LH960"
      targetVersion: "HXT7904Q"
      image: { URI: "https://fw-repo.internal/bb24/ssd-hxt7904q.bin", transferProtocol: HTTPS }
    - type: PSU
      modelSelector: "PWR-2000W"
      targetVersion: "1.10"
      image: { URI: "https://fw-repo.internal/bb24/psu-1.10.bin", transferProtocol: HTTPS }

# With rebootPolicy: OwnerApproval, a human OR an automated drain-orchestrator
# approves the reboot of one host by labeling that host's Server CR (§2a):
#   kubectl label server node001-bb085 metal.ironcore.dev/maintenance-approved=true
# (With rebootPolicy: Enforced, no approval needed — the controller reboots once staged.)

status:
  observedGeneration: 2
  phase: InProgress                        # Pending | InProgress | Completed | Failed
  hosts:
    - bmcName: node001-bb24
      phase: AwaitingReboot                # Staging | AwaitingReboot | Rebooting | Verifying | Completed | Failed | Blocked
      message: "All components staged; awaiting reboot approval"
      # if a non-BIOS component had failed to stage, phase is still AwaitingReboot
      # but a host condition PartialStaging=True flags the mixed state (§2b)
      components:
        - type: BIOS
          state: Staged
          currentVersion: "2.18.0"
          targetVersion: "2.19.0"
        - type: NIC
          modelSelector: "cx6dx"
          discoveredTargets:
            - /redfish/v1/UpdateService/FirmwareInventory/Installed-109587-22.35.10.12__NIC.Slot.4-1-1
          currentVersion: "22.31.1014"
          targetVersion: "22.35.1014"
          taskURI: /redfish/v1/TaskService/Tasks/JID_12345
          state: Staged
  conditions:
    - type: Ready
      status: "False"
      reason: AwaitingRebootApproval
```

Notes:

- `components` list order is **not** significant; the controller stages in the
  fixed canonical order `BIOS → StorageController → HardDrive → NIC → PSU` (§2b).
- `modelSelector` is empty/absent for BIOS (and BMC, if added later).
- Per-component `state` flows `Pending → Staged` (after `SimpleUpdate`/`OnReset`)
  `→ Completed` (after the approved reboot verifies the new version).
- Host `phase: AwaitingReboot` is the **gate** — the operator holds here until a
  host's `Server` carries the `maintenance-approved` label, or `rebootPolicy:
  Enforced` (§2a). `Blocked` means the host could not stage
  (fail-safe; never rebooted without approval).
- Status mirrors `metal-operator`'s `Task` shape for per-component progress and
  uses `metav1.Condition` per `AGENTS.md`.

---

## 10. Architecture

```
NetBox → argora-operator → labels BMC / BMCSecret CRs (kubernetes.metal.cloud.sap/bb, …)

Ops edits FirmwareUpdate CR (Git) ──► ArgoCD sync ──► kube-apiserver
                                                          │
                                                          ▼
                                    FirmwareUpdateReconciler  (NEW, this repo)
  1. Resolve bb → set of BMCs via spec.buildingBlockSelector
  2. Pick the next host (respect maxConcurrentHosts = 1)
  3. Force boot-source = Disk (NOT metal-operator Maintenance → no PXE)  ← §7a
  4. STAGE — per component, canonical order BIOS→StorageController→HardDrive→NIC→PSU (§2b):
       discover Targets via modelSelector  (NIC/Storage/Drive/PSU)   ← NET-NEW (R5)
       skip if currentVersion == targetVersion
       one SimpleUpdate POST (ImageURI, TransferProtocol, Targets, OnReset)
       poll Task until Staged; BIOS staging failure -> abort host
       (non-BIOS failure -> record Failed + host PartialStaging, continue)
  5. GATE — set host phase=AwaitingReboot; if rebootPolicy=OwnerApproval WAIT for the
       maintenance-approved label on <host>'s Server; if Enforced proceed   ← §2a
       │  (label set by a human OR an automated drain-orchestrator)
  6. ACTIVATE — re-assert boot-from-disk → power-cycle (owned here)
  7. Verify versions; clear the approval signal; advance to next host
  8. Aggregate per-host status; update conditions/phase
```

---

## 11. Implementation plan — feature by feature

Each feature below is an **independently mergeable PR** that compiles and leaves
`main` working. The hard parts (discovery, the `SimpleUpdate` POST) are pure,
fixture-tested libraries that land *before* the controller wires them in, so
reviewers verify them in isolation — without hardware or a cluster.

### Feature 0 — Spike (throwaway, not merged)
De-risk the unknowns. **(a) Discovery field consistency — DONE:** `hack/nicprobe`
probed 15 live Dell/HPE/Lenovo BMCs; results in
[nic-discovery-findings.md](nic-discovery-findings.md) established that the NIC
selector matches `FirmwareInventory.Name` (not `NetworkAdapter.Model`), and
surfaced the Dell triplet-dedup and `Updateable:false` requirements (§5). Those
captured JSON trees become the Feature 2 fixtures. **Still to prove:** (b) a NIC
`SimpleUpdate` with a discovered `Targets` returns a Task against the
emulator/a real BMC; (c) **per-vendor `OnReset` apply-time / staging support** —
does "stage all, reboot once" hold on Dell/HPE/Lenovo? No production code.

### Feature 1 — `FirmwareUpdate` CRD (API only, no behavior)
`kubebuilder create api --group maintenance --version v1alpha1 --kind FirmwareUpdate`.
Define spec (buildingBlockSelector, bmcCredentialSecretRef, policy{maxConcurrentHosts,
rebootStrategy=`SingleAtEnd` default, serverMaintenancePolicy, forceUpdate},
ordered `components[]`) and status (phase, per-host/per-component state,
`metav1.Condition`, Task mirror). Enums + validation markers (CEL: `modelSelector`
required unless BIOS). `make manifests generate`. No-op reconciler stub.
**Reviewable as:** "the contract Ops edits in Git."

### Feature 2 — Redfish discovery library (`internal/firmware`, pure, unit-tested)
Solves R5 in isolation. `Resolve(componentType, modelSelector) → []targetURI`
first resolves the Ops token via the hardcoded variant-specific `selectorTokens`
map (`cx6dx → connectx-6 dx`; unknown token → error), then enumerates
`UpdateService.FirmwareInventory()` and matches `SoftwareInventory.Name` by
case-insensitive substring (§5), returning each matched entry's `@odata.id`.
Applies the gate in order: **(1)** dedup vendor rollback slots — collapse Dell
`Current-`/`Installed-`/`Previous-` triplets to the active entry, ignore
`Previous-`; **(2)** **over-match guard** — 2+ distinct model names → ambiguity
error (host blocked); **(3)** **under-match guard** — zero matches on a
NIC-bearing host → warning/blocked, never a silent skip (catches chip-less
marketing names like Dell's `Broadcom Adv. Dual 25Gb Ethernet`); **(4)**
**`Updateable` gate** — exclude `Updateable: false` entries from `Targets`,
report as "matched but not updatable", never sent to `SimpleUpdate`; **(5)**
version compare for idempotency. Token conventions are vendor-specific (Mellanox
family+variant, Broadcom chip number, Intel chip model — §5). Tested entirely
against
**recorded Redfish JSON fixtures captured by `hack/nicprobe`, one set per vendor**
(Dell triplets, HPE numeric IDs + `Updateable:false`, Lenovo `Slot_x.Bundle`) —
this is where R2 heterogeneity is pinned down. **Reviewable as:** "given this
Redfish tree, this selector resolves to these URIs."

### Feature 3 — Redfish update engine (`internal/firmware`, the option-(c) POST)
Replicate `metal-operator`'s `upgradeVersion()` locally so we control the body +
apply-time. GET UpdateService → read action target from raw JSON → POST
`{ImageURI, TransferProtocol, Targets, ForceUpdate, @Redfish.OperationApplyTime}`
→ 202-or-fatal → return Task URI. Vendor quirk table keyed on `Manufacturer`
(`"Dell Inc."`/`"HPE"`/`"Lenovo"`): apply-time default, Task-URI extraction.
`PollTask(uri) → {state, status, percent}`. Unit-tested with an HTTP test server
returning canned vendor responses. **Reviewable as:** "exactly how we call the
BMC, and how each vendor differs" — the riskiest code, fully isolated.

### Feature 4 — Host selection + gated, self-owned reboot / boot-source control
Wire the controller to the fleet and own the power/boot lifecycle, without firing
updates. Resolve `buildingBlockSelector` → `BMC` CRs (argora
`kubernetes.metal.cloud.sap/bb` label). Rolling host picker honoring
`maxConcurrentHosts: 1`. **The `rebootPolicy` gate (§2a):** implement
`OwnerApproval` (default — wait for the `metal.ironcore.dev/maintenance-approved`
label on the host's `Server`) and `Enforced` (auto-reboot once staged), mirroring
metal-operator's `ServerMaintenancePolicy`; act only on that host and **remove the
label** after rebooting (one-shot). **Boot-source control via the `bmc` client**: force
boot-from-disk (`SetBootOverride`/`ClearBootOverride`) and power-cycle
(`Reset`/`PowerOn`/`PowerOff`) **directly** — explicitly **not** via
`metal-operator`'s `Maintenance` state, which would engage a persistent PXE
override (§7a). RBAC markers (incl. who may set the `maintenance-approved` label on `Server`).
**Reviewable as:** "reboots exactly one host at a time, gated by `rebootPolicy`,
and always boots it from disk, never PXE."

### Feature 5 — Orchestration loop (ties 2 + 3 + 4 together)
The real per-host, **stage → gate → activate** remediation. **Stage:** each
component in canonical order `BIOS → StorageController → HardDrive → NIC → PSU`
(§2b) → discover (F2) → skip if at version → one `SimpleUpdate` with `OnReset`
(F3) → poll until `Staged` → next. **Abort-on-BIOS-failure only**: BIOS staging
fails → host `Failed`, skip rest; a non-BIOS failure is recorded (`Failed` +
host `PartialStaging`) and staging continues. **Gate:**
set `AwaitingReboot`; if `rebootPolicy: OwnerApproval` wait for the
`maintenance-approved` label on the host's `Server` (F4, §2a), if `Enforced`
proceed — never reboot unstaged; if
a host cannot stage, mark `Blocked`. **Activate:** one self-owned power-cycle with
boot-from-disk re-asserted (F4, §7a) → verify versions → clear the approval signal
→ next host. Idempotent, requeue-driven; full status/conditions. **Reviewable as:**
"the complete behavior, observable in status, with the reboot gated by `rebootPolicy`."

### Feature 6 — Hardening
Retry/backoff on transient BMC errors; operation timeouts; stalled-Task
detection; clear surfacing of model-selector ambiguity (0 / unexpected match
counts); finalizer so a mid-upgrade host isn't abandoned; events.
**Reviewable as:** "edge cases and failure modes handled."

### Feature 7 — GitOps wiring, samples & E2E
Sample `FirmwareUpdate` CR (both `rebootPolicy` values); ArgoCD `Application`
example; README/docs section documenting the `maintenance-approved` label step
(§2a) and the RBAC for who may set it on `Server`; Kind-based e2e against the
emulator (isolated cluster per `AGENTS.md`). E2E **asserts the gate**: with
`OwnerApproval` the host reaches `AwaitingReboot` and does **not** reboot until the
`maintenance-approved` label is set, and the boot source is `Disk` throughout.
**Reviewable as:**
"how an operator uses it, with a test that proves the gated flow."

### Dependency stack

```
F0 spike ─► informs ─► F1 CRD
                          │
              ┌───────────┼───────────┐
              ▼           ▼           ▼
         F2 discovery  F3 update   F4 host/reboot+boot   (depend only on F1 types)
              └───────────┴───────────┘
                          ▼
                  F5 orchestration loop
                          ▼
                     F6 hardening
                          ▼
                  F7 gitops + e2e
```

**PR packaging:** F1 lands first and alone (dedicated API review). F2 and F3 are
each their own small, pure, heavily-unit-tested PR (easy approvals). F4+F5 may be
one PR (the host loop is hard to demo without orchestration). F6 and F7 follow.

---

## 12. Risks & open questions

1. **PXE-on-reboot if `ServerMaintenance` is reused (§7a).** `metal-operator`
   forces a persistent network-boot override while a `Server` is in `Maintenance`
   state — a live ESXi host would reboot into a network installer. **Mitigation:**
   this controller owns the reboot, never enters that state, and re-asserts
   boot-from-disk before every power-on. This must be covered by an e2e assertion
   (F7) that the host's boot source is `Disk` across the whole flow.
2. **The reboot gate depends on `OnReset` staging working on every vendor (§2a).**
   The design assumes Dell/HPE/Lenovo all stage firmware without rebooting on
   `SimpleUpdate`. If a vendor applies immediately, sending the update *is* the
   reboot — bypassing the gate. **Mitigation:** Phase 0 verifies staging per
   vendor (we send `OnReset` via option (c), §8, even though metal-operator wires
   Dell to `Immediate`); a host that cannot stage is marked `Blocked` and is never
   rebooted without approval. E2E (F7) asserts the host holds at `AwaitingReboot`.
3. **Pull-based image hosting.** `SimpleUpdate` is pull-style: the BMC must reach
   `image.URI`. Confirm an HTTPS firmware repo routable from the BMC network;
   air-gapped BMCs may need a push (`MultipartUpload`) path.
4. **Supermicro has no firmware path** in `metal-operator` today — a known gap if
   the fleet includes it.
5. **`bmc` extension strategy** — decided: option (c), self-contained in this
   repo (§8). Trade-off: we must track upstream `metal-operator` drift in the
   vendor quirks we replicate.
6. **HardDrive blast radius.** Drive updates can be numerous/slow; per-component
   sequencing plus one-host-at-a-time must keep cluster redundancy safe
   throughout.
7. **Model-selector ambiguity.** If a `modelSelector` matches zero or unexpectedly
   many devices on a host, the controller must surface this clearly (status +
   condition) rather than silently no-op.
8. **Non-updatable matches.** A device can match a selector but be
   `Updateable: false` in firmware inventory (reporting-only). These are excluded
   from `Targets` and reported, never sent to `SimpleUpdate` (§5).
9. **Stale approvals.** The `maintenance-approved` label lives on exactly one host's
   `Server` and is **removed on use**, so it can never trigger an unintended reboot
   on a later host or re-run. The controller treats it as one-shot (§2a). With
   `rebootPolicy: Enforced` there is no label — the gate is successful staging itself.
10. **Discovery data is QA-only.** The `nicprobe` capture
    ([nic-discovery-findings.md](nic-discovery-findings.md)) and the derived
    `selectorTokens` / fixtures came from **QA hosts**. Production may carry NIC
    variants, firmware-name formats, or BMC firmware levels not seen in QA —
    notably additional ConnectX-5 SKUs (the `EN`/`Ex`/speed mapping is
    fleet-specific, §5). **Mitigation:** the design fails *safe* on the unknown —
    an unmapped token is rejected, and an ambiguous match blocks the host rather
    than flashing. **Action:** run `nicprobe` against a representative production
    sample (one host per distinct model/vendor) and fold the captures into the
    token map and Feature 2 fixtures before production rollout.

---

## 13. References

- NIC discovery field-consistency findings (live probe): [nic-discovery-findings.md](nic-discovery-findings.md)
  (tool: `hack/nicprobe`)
- DMTF Redfish Firmware Update White Paper (DSP2062):
  <https://www.dmtf.org/sites/default/files/standards/documents/DSP2062_1.0.0.pdf>
- gofish: <https://github.com/stmcginnis/gofish> · <https://pkg.go.dev/github.com/stmcginnis/gofish>
- Redfish forum — `UpdateService.SimpleUpdate`: <https://redfishforum.com/thread/112/updateservice-simpleupdate>
- argora-operator: <https://github.com/sapcc/argora>
- metal-operator: <https://github.com/ironcore-dev/metal-operator>
