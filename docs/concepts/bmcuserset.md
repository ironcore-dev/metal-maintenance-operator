# BMCUserSet — BMC credential onboarding and rotation

A `BMCUserSet` selects a group of `BMC` objects and ensures every matched BMC has a set of
operator-managed accounts with regularly rotated passwords.

## Onboarding operator contract

The metal-maintenance-operator (mmo) does not discover new BMCs itself.
A separate onboarding operator performs that role. Its contract with mmo:

1. It creates a `BMCSecret` carrying the BMC's factory credentials
   (username + password; may be per-device, e.g. Dell pull-tag passwords).
   It labels this secret with:
   ```
   baseboard.metal.ironcore.dev/credential-role: factory
   ```
2. It creates the `BMC` object with:
   - `spec.access` (inline endpoint — `endpointRef` is deprecated)
   - `spec.bmcSecretRef` pointing at the factory `BMCSecret` created in step 1
3. It **never** modifies `BMC.spec.bmcSecretRef` or the factory secret again after creation.

mmo takes over from that point.

## Bootstrap flow

The following describes the DualAccount strategy (recommended for operator admin accounts).

```
BMC created by onboarding operator
       │
       ▼
BMCUserSet reconciles → creates two BMCUser objects per BMC:
    <set>-<bmc>-a  (OperatorAdmin)
    <set>-<bmc>-b  (OperatorAdmin)

Before creating each user, the Set controller stamps:
    BMCUser.Status.BootstrapSecretRef = BMC.Spec.BMCSecretRef
    (the factory secret name, recorded before -a can promote a new one)

       │
       ▼
BMCUserReconciler bootstraps -a:
  1. No setupCredentialRef configured → uses BootstrapSecretRef to authenticate
  2. Generates a random password, creates a BMCSecret owned by -a
  3. Creates/updates the BMC account for -a
  4. Runs bmcConnectionTest with the new BMCSecret
  5. On success: promotes to EffectiveBMCSecretRef AND BMC.Spec.BMCSecretRef

       │
       ▼
BMCUserReconciler bootstraps -b:
  1. setupCredentialRef absent → falls back to BootstrapSecretRef
     (still pointing at the factory secret, even though BMC.Spec.BMCSecretRef
      now points at -a's secret — safe because BootstrapSecretRef is immutable)
  2. Same steps as -a; promotes its EffectiveBMCSecretRef but does NOT
     overwrite BMC.Spec.BMCSecretRef (only the active admin does that)

       │
       ▼
BMCUserSetReconciler.handleFactoryAccount (when FactoryAccount is configured):
  Gate: both -a and -b have EffectiveBMCSecretRef
        AND BMC.Spec.BMCSecretRef = one of those EffectiveBMCSecretRefs
        AND FactoryAccountHandled == false

  Action (Rotate):
  1. Determines factory username from FactoryAccount.UserNames or BootstrapSecretRef
  2. Creates a BMCUserRotation with TriggerReason=FactoryBootstrap owned by the active admin
  3. BMCUserRotationReconciler runs executeSingleAccount using BMC.Spec.BMCSecretRef
     (operator admin credential) to authenticate and rotate the factory account
  4. On Succeeded:
     - If RetainCredential=true: labels NewSecretRef BMCSecret as break-glass
     - If RetainCredential=false: deletes NewSecretRef BMCSecret
     - Deletes the original factory BMCSecret (if no longer referenced)
     - Sets BMCUser.Status.FactoryAccountHandled = true
     - Sets BMCUser.Status.Conditions[CredentialsBootstrapped]=True

       │
       ▼
BMCUserSet.Status.BootstrappedBMCs is incremented (when FactoryAccount is nil,
counted on EffectiveBMCSecretRef alone; when configured, also requires FactoryAccountHandled)
```

## Gating on CredentialsBootstrapped

The `CredentialsBootstrapped` condition (`True`/`False`) on `BMCUser` indicates that:
- The user has a proven `EffectiveBMCSecretRef`, AND
- For `OperatorAdmin` users: the factory account has been handled (or no factory handling is configured)

Other mmo controllers (BMCSettings, BMCVersion, FirmwareUpdate) and metal-operator
`ServerReadinessRule` objects can gate on this condition via label selectors or field selectors
against the `BMCUser` resource, or by checking `BMCUserSet.Status.BootstrappedBMCs`.

Example `ServerReadinessRule` gate (pseudocode):
```yaml
apiVersion: metal.ironcore.dev/v1alpha1
kind: ServerReadinessRule
spec:
  requirements:
    - resource: bmcusers.baseboard.metal.ironcore.dev
      selector:
        matchLabels:
          baseboard.metal.ironcore.dev/bmcuserset-name: maintenance-operator-admin
      conditions:
        - type: CredentialsBootstrapped
          status: "True"
```

## API reference summary

| Field | Description |
|---|---|
| `BMCUserSet.Spec.SetupCredentialRef` | Optional global setup credential. When omitted, per-BMC factory credential is used. |
| `BMCUserSet.Spec.FactoryAccount` | Optional factory account handling config (action, ignoreVendors, retainCredential). |
| `BMCUserSet.Spec.Template.DeletionPolicy` | `Retain` (default for OperatorAdmin) or `Delete`. |
| `BMCUserSet.Status.BootstrappedBMCs` | BMCs with all accounts proven and factory account handled. |
| `BMCUser.Status.BootstrapSecretRef` | Immutable: the factory BMCSecret used for first contact. |
| `BMCUser.Status.EffectiveBMCSecretRef` | The credential currently proven to work. |
| `BMCUser.Status.FactoryAccountHandled` | True once factory rotation is complete. |
| `BMCUser.Status.Conditions[CredentialsBootstrapped]` | Gating signal for downstream controllers. |

## Security invariants

- `promoteCredential` (in `BMCUserRotationReconciler`) only runs after `bmcConnectionTest`
  succeeds — the operator never promotes credentials it has not verified.
- `BMCUser.delete` with `DeletionPolicy=Retain` refuses to remove the finalizer if the user's
  credential is still live in `BMC.Spec.BMCSecretRef`, preventing accidental lockout.
- `deleteOrphanedBMCUsers` applies the same guard: a relabelled BMC does not cause the
  operator to delete the live admin account.
- No password or credential data is ever written to logs, events, or status conditions.
