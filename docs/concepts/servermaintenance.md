# ServerMaintenance

`ServerMaintenance` represents a maintenance operation for a physical server. It transitions a `Server` from its
current operational state (e.g., Available/Reserved) into a Maintenance state. Each `ServerMaintenance` object tracks
the lifecycle of a maintenance task, ensuring servers are properly taken offline, updated, and restored.

## Key Points

- `ServerMaintenance` is namespaced and may represent various maintenance operations.
- Only one `ServerMaintenance` can be active per `Server` at a time. Others remain pending.
- When the active `ServerMaintenance` completes, the next pending one (if any) starts.
- If no more maintenance tasks are pending, the `Server` returns to its previous operational state.
- `policy` determines how maintenance starts:
    - **OwnerApproval:** Requires the label `metal.ironcore.dev/maintenance-approved: "true"` on the [`ServerClaim`](https://github.com/ironcore-dev/metal-operator/blob/main/docs/concepts/serverclaim.md).
    - **Enforced:** Does not require owner approval.
- `priority` determines which pending maintenance starts first for the same server:
    - Higher value wins.
    - On equal value, older `ServerMaintenance` wins.
    - If omitted, `priority` is treated as `0`.

## Workflow

1. A separate operator (e.g., `metal-maintenance-operator`) or user creates a `ServerMaintenance` resource referencing a
   specific [`Server`](https://github.com/ironcore-dev/metal-operator/blob/main/docs/concepts/server.md).
2. If a [`Server`](https://github.com/ironcore-dev/metal-operator/blob/main/docs/concepts/server.md) is claimed, the label `metal.ironcore.dev/maintenance-needed: "true"` is added to the [`ServerClaim`](https://github.com/ironcore-dev/metal-operator/blob/main/docs/concepts/serverclaim.md).
3. If `policy` is `OwnerApproval` and no `metal.ironcore.dev/maintenance-approved` label is set on the `ServerClaim`, the `ServerMaintenance`
   stays in `Pending`. The `Server` also remains unchanged.
4. If `policy` is `OwnerApproval` and the `metal.ironcore.dev/maintenance-approved: "true"` label is present, or if the policy is `Enforced`, the
   `metal-operator` transitions the `Server` into `Maintenance` and updates the `ServerMaintenance` state accordingly.
5. (optional) If `locatorLED` is set, the `ServerMaintenanceReconciler` sets the `Server`'s locator LED to the
   requested state so the physical server can be identified in the data center.
6. Once the maintenance task is complete, the external operator **deletes** the `ServerMaintenance` resource.
   The controller removes the finalizer, clears the locator LED (if it was set), cleans up labels on the `Server` and
   `ServerClaim`, and releases the `Server`.
7. The `metal-operator` transitions the `Server` back to its prior state. If additional `ServerMaintenance` objects are
   pending, the next one is processed.

## Example

```yaml
apiVersion: maintenance.metal.ironcore.dev/v1alpha1
kind: ServerMaintenance
metadata:
  name: bios-update
  namespace: ops
  annotations:
    metal.ironcore.dev/reason: "BIOS update"
spec:
  priority: 100
  policy: OwnerApproval
  serverRef:
    name: server-foo
  locatorLED: Lit # or Blinking/Off
```

If `policy: OwnerApproval` and no `metal.ironcore.dev/maintenance-approved` label exists on the `ServerClaim`, this
`ServerMaintenance` remains `Pending`, and the `Server` stays as is. Once the label is added, the `metal-operator`
transitions the `Server` to `Maintenance`, and the maintenance operator performs the maintenance task.
