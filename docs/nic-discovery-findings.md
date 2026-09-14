<!--
SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
SPDX-License-Identifier: Apache-2.0
-->

# Findings: NIC discovery field consistency (Dell / HPE / Lenovo)

**Date:** 2026-06-12
**Method:** Read-only Redfish probe (`hack/nicprobe`) against one live BMC per
server model — 6 Dell PowerEdge, 6 HPE ProLiant, 5 Lenovo ThinkSystem. For each
host it dumped `Chassis.NetworkAdapters()` (`Model`, `Manufacturer`, `PartNumber`,
`SKU`) and `UpdateService.FirmwareInventory()` (`Name`, `Version`, `Updateable`).
15/17 returned data; 2 Lenovo BMCs returned 401 (not AD-authenticated) and are
excluded.

## TL;DR

- **`NetworkAdapter.Model` / `Manufacturer` is NOT a viable cross-vendor selector
  key.** The strings differ by vendor, differ *within* a vendor by SKU, are
  sometimes empty, and place the identifier in different fields.
- **`FirmwareInventory.Name` is the better matcher** — it consistently embeds the
  silicon family name (e.g. `ConnectX-6 Dx`, `BCM57508`), so keyword matching
  actually works on it. **Our design switches the NIC selector to match
  `FirmwareInventory.Name`** (see `firmware-update-design.md` §5).
- **`Updateable: false` is real and common** — the discovery gate is necessary,
  not theoretical.
- **Dell exposes Current/Installed/Previous firmware triplets** — discovery must
  dedup to the active entry and ignore `Previous-`.

## Evidence 1 — the same card, described three different ways

Mellanox ConnectX-6 Dx (essentially the same silicon family) as seen in
`NetworkAdapter`:

| Server | `Model` | `Manufacturer` |
|---|---|---|
| Dell R740XD | `ConnectX-6 DX 2-port 100GbE QSFP56 PCIe Adapter` | `Mellanox Technologies` |
| HPE DL345 Gen11 | `P25962-001 / Mellanox MCX623106AS-CDAT Eth 100Gb 2p QSFP56` | `MLNX` |
| Lenovo SR650 | `CX623106AN-CDAT` | `Mellanox Technologies` |

A selector like `modelSelector: "ConnectX-6 DX"` matched against
`NetworkAdapter.Model` would hit the Dell, **miss** HPE (`MCX623106AS-CDAT`) and
**miss** Lenovo (`CX623106AN-CDAT`).

It is not even consistent within one vendor: Dell shows Mellanox ConnectX-5 as
`MLNX 25GbE 2P ConnectX5 Adpt` (R640) vs `MLNX 100GbE 2P ConnectX5 Adpt` (R840).
Broadcom appears as `Broadcom Inc. and subsidiaries` (Dell), `Broadcom` (HPE),
`Broadcom Limited` (Lenovo). Some rows are blank entirely (Lenovo SR950 `slot-9`,
HPE DL320 `DA000000` have empty `Manufacturer`/`Model`).

## Evidence 2 — each vendor uses a different field convention

| Vendor | `Model` holds | `Manufacturer` | `PartNumber` | `SKU` |
|---|---|---|---|---|
| **Dell** | marketing name (`ConnectX-6 DX...`) | silicon vendor | Dell part (`08P2T2`) | always empty |
| **HPE** | `HPE-SKU / vendor-name` blob | abbreviated (`MLNX`) | HPE option SKU (`P25962-001`) | HPE option SKU |
| **Lenovo** | bare vendor part code (`CX623106AN-CDAT`, `BCM57508`) | silicon vendor | Lenovo serial (`SN37A28327`) | Lenovo SKU (`01PE649`) |

There is no single field that means "the model" across all three vendors.

## Evidence 3 — `FirmwareInventory.Name` is more consistent

Same Mellanox ConnectX-6 Dx, as seen in `FirmwareInventory`:

| Vendor | `FirmwareInventory.Name` |
|---|---|
| Dell R740XD | `Mellanox ConnectX-6 Dx Dual Port 100 GbE QSFP56 Adapter - <MAC>` |
| HPE (several) | `ConnectX-6 Dx 100GE 2P NIC` |
| Lenovo SR650 | `Firmware:DEVICE-Mellanox ConnectX-6 Dx 100GbE QSFP56 2-port ...` |

Not identical, but **every one contains the case-folded substring
`ConnectX-6 Dx`**. The `NetworkAdapter.Model` strings do not share this property
(Lenovo's `CX623106AN-CDAT` has no "ConnectX" in it at all). This is why a keyword
filter works against firmware names but fails against adapter models — and it is
the basis for switching our NIC selector to `FirmwareInventory.Name`.

## Evidence 4 — `Updateable: false` occurs in practice

Real `updateable=false` entries observed:

- HPE DL360 Gen10 — `HP Ethernet 1Gb 4-port 366FLR Adapter`
- HPE DL560 Gen10 — `HP Ethernet 1Gb 4-port 331FLR Adapter`
- **HPE DL560 Gen11 — both `ConnectX-6 Dx 100GE 2P NIC` entries (`22.45.10.20`)**
- HPE DL320 Gen11 — `BCM 5720 1GbE 2p BASE-T LOM Adptr`
- Lenovo SR650 / SR650 v3 / SR950 — `Marvell Firmware` / `Marvell UEFI NVMe Driver`

A discovery that matches by name but does **not** check `Updateable` would attempt
a `SimpleUpdate` against a reporting-only entry (e.g. the DL560 Gen11 ConnectX-6)
and fail after entering maintenance. The `Updateable` gate is required.

## Evidence 5 — Dell Current/Installed/Previous triplets

Every Dell NIC firmware entry appears **three times** with `Current-`,
`Installed-`, and `Previous-` ID prefixes (the iDRAC rollback slots), at two
different versions:

```
Current-109587-22.35.10.12__NIC.Slot.4-1-1   ver=22.35.10.12
Installed-109587-22.35.10.12__NIC.Slot.4-1-1 ver=22.35.10.12
Previous-109587-22.31.10.14__NIC.Slot.4-1-1  ver=22.31.10.14   <- older, rollback
```

HPE and Lenovo use single IDs (`13`, `Slot_4.Bundle`) without this. Discovery must
**dedup to the active entry** (`Installed-`/`Current-`) and ignore `Previous-`,
otherwise it counts each component three times at conflicting versions. This is a
Dell-specific quirk.

## Implications

1. **Selector key:** match `FirmwareInventory.Name` (substring, case-insensitive),
   not `NetworkAdapter.Model`. Updated in design §5.
2. **Updateable gate:** mandatory — exclude `Updateable: false` from `Targets`.
3. **Dell dedup:** collapse Current/Installed/Previous to the active component;
   ignore `Previous-`.
4. **Building-block scoping still matters:** even firmware names are not byte-identical
   across vendors, so a per-bb selector (single vendor/model at a time, design R3)
   is what keeps a keyword match reliable.
5. **Variant granularity:** the selector token must carry the variant (`cx6dx`, not
   `cx6`) — ConnectX-6 ships as Lx (≤50GbE) and Dx (≤100GbE), and the fleet already
   has both `ConnectX-5 EN` (25GbE SFP28) and `ConnectX-5 Ex` (100GbE QSFP). Match
   `connectx-6 dx`, not `connectx-6`. See design §5.
6. **ConnectX-5 `EN`/`Ex` are orthogonal axes, not a speed axis.** Per NVIDIA, `EN`
   = Ethernet-only (vs `VPI` = Eth+InfiniBand) and `Ex` = enhanced/PCIe Gen4 — and
   `Ex` can apply to an EN card. In QA they map cleanly to distinct cards (EN→25G,
   Ex→100G) but that is **fleet-specific**: production could have an EN card at
   100GbE. Tokens must be validated against per-fleet captures, not assumed.
7. **This scan is QA-only.** Production may add unseen CX5 (and other) variants /
   name formats / BMC firmware levels. The token map and fixtures must be extended
   with a production `nicprobe` capture before production rollout (design Risk #10).

## Why not match on the OPN / part number (e.g. `CDAT`)

A tempting shortcut: the adapter `Model`/part number carries an OPN suffix, and
`-CDAT` reliably indicates a ConnectX-6 **Dx** card. Verified against NVIDIA's OPN
catalog, the suffix encodes **speed + PCIe + bracket**, not the Dx/Lx generation:
`C`=100GbE, `A`=25GbE, `G`=50GbE, `V`=200GbE. `CDAT` appears only on the Dx
(MCX623) family — Lx (MCX631) uses `ADAT`/`GDAT` and never `CDAT`, because Lx tops
out at 50GbE and cannot carry the `C` (100GbE) code.

So `CDAT ⟹ Dx` is **true**, but we still do **not** key the selector on it:

1. **It under-matches.** `Dx ⟹ CDAT` is **false** — Dx also ships as `VDAT`
   (200GbE). A `CDAT` selector would silently skip a 200GbE Dx card. A missed NIC
   in a firmware campaign is an invisible failure, worse than a visible error.
2. **Wrong field.** `CDAT` lives only in `NetworkAdapter.Model` — the field this
   document shows is unreliable. `FirmwareInventory.Name` (our match field) states
   `ConnectX-6 Dx` directly, on all three vendors (verified).
3. **No inference needed.** The firmware name already says `Dx` in plain text;
   decoding an OPN suffix to re-derive it adds a fragile layer for no gain.

`CDAT` is kept as a documented sanity fact only. If a future vendor's firmware
`Name` ever lacks a `Dx`/`Lx` token, the robust fallback is
`SoftwareInventory.RelatedItem` correlation to the device — not OPN substring
matching.

## Broadcom & Intel — the stable token is the chip number, not the family name

Unlike Mellanox (where the family+variant `ConnectX-6 Dx` is consistent),
Broadcom and Intel firmware names vary more, and the portable substring is the
**chip model number**:

**Broadcom** — same BCM57508 across vendors:

| Vendor | `FirmwareInventory.Name` | `57508` present? |
|---|---|---|
| Dell | `Broadcom BCM57508 2x100G QSFP PCIE` | yes |
| Lenovo | `Firmware:DEVICE-Broadcom 57508 100GbE QSFP56 2-port ...` | yes |

So the token is the bare digits (`bcm57508 → 57508`), which survives the
`BCM`-prefix vs no-prefix difference. `BCM5720` (`5720`) behaves the same.

**Exception — chip-less marketing names (under-match risk):** on the Dell R660,
the 25G card is named `Broadcom Adv. Dual 25Gb Ethernet` and the R860 has
`Broadcom P225p NetXtreme-E Dual-port 10Gb/25Gb Ethernet PCIe Adapter` — **neither
contains a chip number** (`57414` etc.). A chip-number token matches **zero**
entries on those hosts. This is a *silent* failure mode (no match ≠ error), which
is why the design adds an **under-match guard**: zero matches on a NIC-bearing
host is surfaced as `Blocked`/warning, not treated as success. For such a bb the
token maps to the marketing substring (e.g. `adv. dual 25gb`) instead.

> So "can we match Broadcom on Dell?" — **yes for BCM57508 and BCM5720** (chip
> number is in the name); **the 25G cards with marketing-only names need a
> name-based token**, and the under-match guard ensures they are never silently
> skipped.

**Intel** — the I350 firmware name (`Intel(R) Gigabit 4P I350-t rNDC`) contains
`I350` on all hosts, even where the *adapter* `Model` is an opaque OEM SKU (Lenovo
`J31979`, `N21486`). Token = `i350`. **Do not use `intel`** — it would match HPE's
non-NIC `Intelligent Provisioning` and `Intelligent Platform Abstraction Data`
firmware entries. Always key on the chip model.

## Relevance to the upstream `NICVersion` PR

`feature/nic-firmware-update` already discovers via `FirmwareInventory` name
filters — **this data validates that choice** over a `NetworkAdapter`-based
approach. Two gaps the data exposes in that PR:

- **No `Updateable` check** — would fail on the HPE DL560 Gen11 ConnectX-6
  (`updateable=false`).
- **No Dell triplet dedup** — its name filter would match `Current-`/`Installed-`/
  `Previous-` as three separate hits. (The PR is HPE-only today, so this is latent
  rather than active, but it surfaces as soon as Dell support is added.)
