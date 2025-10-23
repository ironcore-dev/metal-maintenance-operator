# API Migration: Standard Kubernetes References

This document shows the migration from custom reference types to standard Kubernetes reference types.

## Before (Custom Types)

```yaml
apiVersion: maintenance.metal.ironcore.dev/v1alpha1
kind: AnsibleJob
metadata:
  name: example-job
spec:
  playbook: site.yml
  playbookRepo: https://github.com/example/playbooks.git
  inventory:
    # Old custom reference type
    configMapRef:
      name: my-inventory
      key: hosts
    # Or
    secretRef:
      name: my-inventory-secret
      key: hosts
```

## After (Standard Kubernetes Types)

```yaml
apiVersion: maintenance.metal.ironcore.dev/v1alpha1
kind: AnsibleJob
metadata:
  name: example-job
spec:
  playbook: site.yml
  playbookRepo: https://github.com/example/playbooks.git
  inventory:
    # Standard Kubernetes reference types
    configMapKeyRef:
      name: my-inventory
      key: hosts
      optional: false  # New optional field available
    # Or
    secretKeyRef:
      name: my-inventory-secret
      key: hosts
      optional: false  # New optional field available
```

## Benefits of Standard Types

1. **Consistency**: Follows established Kubernetes patterns used by Pods, Deployments, etc.
2. **Familiarity**: Developers already know these types from other Kubernetes resources
3. **Additional Features**:
   - `optional` field for graceful handling of missing references
   - Better validation and error messages
4. **Tooling Support**: Better IDE support, validation, and documentation
5. **Maintenance**: Less custom code to maintain

## Migration Impact

This is a **breaking change** that requires updating existing AnsibleJob manifests:

- `configMapRef` → `configMapKeyRef`
- `secretRef` → `secretKeyRef`
- Field structure changes from `{name, key}` to `{name, key, optional}`

The controller now properly uses the `key` field to mount only the specified key as the inventory file, fixing the previous implementation bug where the key field was ignored.
