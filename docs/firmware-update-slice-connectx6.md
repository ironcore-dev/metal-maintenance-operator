<!--
SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
SPDX-License-Identifier: Apache-2.0
-->

# First slice: ConnectX-6 Dx NIC firmware, all vendors, full hardware loop

**Status:** Scoped implementation plan
**Date:** 2026-06-14
**Companion:** [firmware-update-design.md](firmware-update-design.md) (master design),
[nic-discovery-findings.md](nic-discovery-findings.md),
[oneview-replacement.md](oneview-replacement.md).

This document scopes the **first shippable vertical slice** of the firmware
feature. The master design covers all five component types and the full
architecture; this slice deliberately narrows the *component* to one card while
keeping *vendor coverage* and the *control loop* complete.

---

## 1. Scope

**In scope — exactly one card, all three vendors, the whole loop:**

- **Component:** ConnectX-6 Dx NIC only (selector token `cx6dx`). No other NIC
  family, no Storage/Drive/PSU/BIOS.
- **Vendors:** Dell, HPE, **and** Lenovo. ConnectX-6 Dx is present on all three in
  the QA scan, and all three are updated via the **same `SimpleUpdate` mechanism**
  (§3) — no per-vendor strategy split in this slice.
- **Loop:** the complete control loop on **real hardware** —
  discover → stage (`OnReset`) → manual reboot gate → controller-owned reboot
  (boot-from-disk) → verify version.

**Out of scope (deferred to later slices):**

- Other components (BIOS, StorageController, HardDrive, PSU) — and therefore the
  canonical ordering and the "BIOS failure aborts host" logic. With one component
  type there is no ordering to enforce.
- Other NIC families / ConnectX-6 Lx / ConnectX-5 (the token map will only carry
  `cx6dx` initially).
- **HPE iLO Repository / Install Set** — HPE is updated via `SimpleUpdate` like the
  others (§3). The iLO-Repository path is future work (§8a, oneview-replacement.md).
- The `FirmwareCatalog` / `FirmwareBaseline` compliance layer.
- Production hosts (data is QA-only; see master design Risk #10).

## 2. Definition of done (atomic)

The slice is **atomic**: the milestone counts only when **all three vendors**
complete the full loop on real hardware. A ConnectX-6 Dx firmware update,
expressed in one `FirmwareUpdate` CR, is:

1. discovered correctly on a Dell, an HPE, and a Lenovo host,
2. staged without rebooting (`OnReset` / iLO `UpdatableBy`),
3. held at the reboot gate (slice default `rebootPolicy: OwnerApproval`) until the
   `metal.ironcore.dev/maintenance-approved` label is set on the host's `Server`,
4. activated by a controller-owned reboot that boots **from disk**, and
5. **verified** — the live firmware version equals the target — on a real host of
   **each** vendor.

> Atomic by choice: we do not declare the milestone on Dell/Lenovo alone. HPE's
> iLO-Repository path must reach the same bar for the slice to be "done." This
> means the riskiest vendor path (HPE) can gate the milestone — accepted
> deliberately to keep a single, honest definition of done.

## 3. One mechanism for all three vendors — SimpleUpdate

**All three vendors use `SimpleUpdate` in this slice — including HPE.** We do
**not** use HPE's iLO Repository / Install Set path here.

| Vendor | Mechanism |
|---|---|
| Dell | `SimpleUpdate` + `OnReset` apply-time + Task poll |
| HPE | `SimpleUpdate` + `OnReset` apply-time + Task poll |
| Lenovo | `SimpleUpdate` + `OnReset` apply-time + Task poll |

**Consequence: there is no `UpdateStrategy` split in this slice.** One uniform
update path serves all three vendors, which is a significant simplification —
it drops the entire `.compsig` / multipart-upload / OEM-JSON / repository-capacity
risk class that the iLO-Repository path carries.

> The iLO Repository + Install Set path (master design §8a,
> [oneview-replacement.md](oneview-replacement.md)) remains the *future* route to
> fuller OneView parity, but it is **explicitly out of scope for this slice**.
> HPE firmware here is applied exactly like Dell/Lenovo: `SimpleUpdate`.

## 4. Highest-risk unknowns (retire these first)

All are *hardware/mechanism* unknowns, not code design. (Note: because the slice
uses `SimpleUpdate` for HPE too, the iLO-Repository risk class — `.compsig`,
multipart upload, OEM JSON, repository capacity — **does not apply here.**)

1. **Dell `OnReset` honor.** metal-operator hardwires Dell to `Immediate`; we
   override to `OnReset`. **Does Dell iDRAC actually stage (defer the reboot) for
   a NIC component, so the manual gate works?** If it reboots on issue, the gate
   is bypassed for Dell.
2. **HPE `SimpleUpdate` for an individual component.** Confirm iLO accepts a
   **single ConnectX-6 Dx firmware file** via `SimpleUpdate` (pull from the HTTPS
   repo) with `OnReset` — i.e. that HPE will do per-component `SimpleUpdate` rather
   than *requiring* the iLO Repository path. This is the key HPE question for the
   slice, and it is far smaller than the Install Set path.
3. **Staging per vendor.** Confirm iDRAC / iLO / XCC each honor `OnReset` (stage,
   don't reboot) for the NIC component.
4. **The flash actually takes and the host returns** — the core firmware-operator
   risk, independent of vendor.

## 5. Hardware & artifact prerequisites (the rate-limiter)

"Full loop on hardware" is gated by having these in hand **before** the controller
matters:

- A **rebootable, AD-authenticated BMC per vendor** (note: 2 Lenovo BMCs returned
  401 in the QA scan — need ones we can authenticate and power-cycle).
- A **ConnectX-6 Dx firmware image per vendor** — the per-component firmware file
  that `SimpleUpdate` pulls. (No `.compsig` / SPP packaging needed: this slice uses
  `SimpleUpdate` for HPE too, not the iLO Repository.)
- A **reachable HTTPS firmware repo** the BMCs can pull from (`SimpleUpdate` is
  pull-based).
- A **host per vendor we are allowed to reboot** (not carrying live workload — the
  loop includes the activating reboot).

## 6. Build order (retire risk earliest, not tidy layers)

1. **Manual per-vendor spike (no controller).** By hand via gofish/curl against
   one real host of each vendor: discover the CX6 Dx → `SimpleUpdate`/`OnReset` →
   reboot → verify. This answers §4's make-or-break questions cheaply — especially
   *does each vendor stage on `OnReset`*, and *does HPE accept the individual
   component via `SimpleUpdate`*. **If a vendor's staging fails here, the design
   changes — far cheaper to learn now.** Output: a confirmed, known-good manual
   sequence per vendor.
2. **Discovery library** (`internal/firmware`, pure, fixture-tested). `cx6dx →
   "connectx-6 dx"` against `FirmwareInventory.Name`; guards (over/under-match);
   Dell `Current/Installed/Previous` dedup; `Updateable` gate. Fixtures = the
   `hack/nicprobe` QA captures (Dell triplets, HPE numeric IDs, Lenovo
   `Slot_x.Bundle`).
3. **Update engine** (one, not a strategy split) — `SimpleUpdate` + `OnReset` +
   Task poll, with the small per-vendor body/task-monitor quirks (Dell sets
   `@Redfish.OperationApplyTime`, vendors parse the Task URI differently — master
   design §6). Validated against the spike's known-good steps. Serves Dell, HPE,
   and Lenovo through the one path.
4. **Controller loop** — lean `FirmwareUpdate` CRD (single NIC component),
   one-host-at-a-time rolling, the reboot gate (`rebootPolicy`, slice default
   `OwnerApproval` via the `maintenance-approved` label on `Server`), boot-from-disk,
   status/conditions.
5. **Automated end-to-end on hardware** = the milestone (§2), all three vendors.

## 7. Lean CRD for the slice

Single component, so the CR is minimal (full shape in master design §9):

```yaml
apiVersion: maintenance.metal.ironcore.dev/v1alpha1
kind: FirmwareUpdate
metadata:
  name: bb085-cx6dx
spec:
  buildingBlockSelector:
    matchLabels:
      kubernetes.metal.cloud.sap/bb: bb085
  bmcCredentialSecretRef: { name: bb085-bmc-creds }
  components:
    - type: NIC
      modelSelector: "cx6dx"
      targetVersion: "22.35.10.12"
      image:
        URI: "https://fw-repo.internal/<vendor-path>/cx6dx-22.35.10.12.bin"
        transferProtocol: HTTPS
```

(`image.URI` points to the per-vendor ConnectX-6 Dx component file on the HTTPS
repo. A bb is single-vendor, so one CR targets one vendor's hosts; the controller
applies `SimpleUpdate` regardless of vendor — no strategy selection in this slice.)

## 8. What this slice proves for the broader design

- The **whole control loop** (discover → stage → gate → reboot → verify) works on
  real hardware across all three vendors — the riskiest thing in any firmware
  operator.
- The **`SimpleUpdate` update engine** is validated on Dell/HPE/Lenovo, so adding
  later components (Storage/Drive/PSU) or NIC families (CX6 Lx, CX5) is mostly
  *discovery + token-map* work, not new control-flow.
- The **`OnReset` staging assumption** (master design §7 / Risk #2) is confirmed or
  disproven per vendor on hardware — the single biggest open question in the
  reboot-gate model.
- A clean seam for **future** work: the iLO Repository / Install Set path (§8a,
  oneview-replacement.md) can later be introduced as an alternative HPE engine
  without disturbing this slice's control loop.

## 9. Open questions

1. Which specific QA hosts (per vendor) are designated as the rebootable test
   targets, and are their BMCs AD-authenticated?
2. Source of the ConnectX-6 Dx per-component firmware file per vendor.
3. Is the HTTPS firmware repo already routable from the BMC management network?
4. Does HPE iLO accept an individual component via `SimpleUpdate` + `OnReset`
   (the §4 key HPE question), keyed off `server.Status.Manufacturer`
   (`"Dell Inc."` / `"HPE"` / `"Lenovo"`), consistent with metal-operator's
   dispatch?
