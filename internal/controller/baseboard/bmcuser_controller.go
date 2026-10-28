// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package baseboard

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/ironcore-dev/controller-utils/clientutils"
	baseboardv1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/baseboard/v1alpha1"
	utils "github.com/ironcore-dev/metal-maintenance-operator/internal/utils"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	"github.com/ironcore-dev/metal-operator/bmc"
	"github.com/ironcore-dev/metal-operator/pkg/bmcutils"
	"github.com/stmcginnis/gofish/schemas"
)

const (
	bmcUserFinalizer = "baseboard.metal.ironcore.dev/bmcuser"

	// conditionTypeCredentialsBootstrapped is True once the operator admin account has a
	// proven EffectiveBMCSecretRef and (when applicable) the factory account is handled.
	// Other controllers and ServerReadinessRules can gate on this condition.
	conditionTypeCredentialsBootstrapped = "CredentialsBootstrapped"
	reasonCredentialsBootstrapped        = "CredentialsBootstrapped"
	reasonCredentialsPending             = "CredentialsPending"

	// conditionTypePasswordValid is False when a user-supplied password in the
	// referenced BMCSecret violates the BMC's reported length limits. The controller
	// skips applying such a password (the BMC would silently reject it) and surfaces
	// the reason here instead of looping.
	conditionTypePasswordValid = "PasswordValid"
	reasonPasswordValid        = "PasswordValid"
	reasonPasswordInvalid      = "PasswordInvalid"
)

// rotationFailureRetryDelay returns the configured back-off delay, defaulting to 5 minutes.
func (r *BMCUserReconciler) rotationFailureDelay() time.Duration {
	if r.RotationFailureRetryDelay > 0 {
		return r.RotationFailureRetryDelay
	}
	return 5 * time.Minute
}

// BMCUserReconciler reconciles a BMCUser object
type BMCUserReconciler struct {
	client.Client
	Scheme             *runtime.Scheme
	ManagerNamespace   string
	DefaultProtocol    metalv1alpha1.ProtocolScheme
	SkipCertValidation bool
	BMCOptions         bmc.Options
	// DefaultPasswordLength is the minimum password length used when the BMC
	// AccountService does not report a MaxPasswordLength. Defaults to 20.
	DefaultPasswordLength int
	// RotationFailureRetryDelay is the minimum back-off before retrying after a
	// failed rotation. Defaults to 5 minutes.
	RotationFailureRetryDelay time.Duration
}

// +kubebuilder:rbac:groups=baseboard.metal.ironcore.dev,resources=bmcusers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=baseboard.metal.ironcore.dev,resources=bmcusers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=baseboard.metal.ironcore.dev,resources=bmcusers/finalizers,verbs=update
// +kubebuilder:rbac:groups=baseboard.metal.ironcore.dev,resources=bmcusersets,verbs=get;list;watch
// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=bmcs,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=bmcsecrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *BMCUserReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	user := &baseboardv1alpha1.BMCUser{}
	if err := r.Get(ctx, req.NamespacedName, user); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	return r.reconcileExists(ctx, user)
}

// reconcileExists routes the BMCUser to deletion handling when it is being
// deleted, or to normal reconciliation otherwise.
func (r *BMCUserReconciler) reconcileExists(ctx context.Context, user *baseboardv1alpha1.BMCUser) (ctrl.Result, error) {
	if !user.DeletionTimestamp.IsZero() {
		return r.delete(ctx, user)
	}
	return r.reconcile(ctx, user)
}

// reconcile ensures the BMC account for the BMCUser exists on the referenced
// BMC, keeps the associated BMCSecret and effective secret reference up to date,
// manages the finalizer, and drives password rotation.
func (r *BMCUserReconciler) reconcile(ctx context.Context, user *baseboardv1alpha1.BMCUser) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	if user.Spec.BMCRef == nil {
		log.V(1).Info("No BMC reference set for User, skipping reconciliation")
		return ctrl.Result{}, nil
	}
	bmcObj := &metalv1alpha1.BMC{}
	if err := r.Get(ctx, client.ObjectKey{Name: user.Spec.BMCRef.Name}, bmcObj); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.updateEffectiveSecret(ctx, user, bmcObj); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to update effective BMCSecret: %w", err)
	}
	if modified, err := clientutils.PatchEnsureFinalizer(ctx, r.Client, user, bmcUserFinalizer); err != nil || modified {
		return ctrl.Result{}, err
	}

	// Bootstrap: account doesn't exist on the BMC yet (no ID) and no working
	// credential is known. Credential resolution order:
	//  1. set.Spec.SetupCredentialRef (global setup secret, if configured)
	//  2. user.Status.BootstrapSecretRef (per-BMC factory secret recorded by the Set
	//     controller before -a might promote a new credential)
	//  3. getBMCClient (current BMC.Spec.BMCSecretRef — fallback for standalone users)
	needsBootstrap := user.Status.ID == "" && user.Status.EffectiveBMCSecretRef == nil
	var bmcClient bmc.BMC
	var clientErr error
	if needsBootstrap {
		setupRef, err := r.lookupSetupCredentialRef(ctx, user)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to look up setup credential: %w", err)
		}
		if setupRef != nil {
			bmcClient, clientErr = r.getSetupBMCClient(ctx, bmcObj, *setupRef)
		} else if user.Status.BootstrapSecretRef != nil {
			bmcClient, clientErr = r.getBootstrapBMCClient(ctx, bmcObj, user.Status.BootstrapSecretRef.Name)
		} else {
			bmcClient, clientErr = r.getBMCClient(ctx, bmcObj)
		}
	} else {
		bmcClient, clientErr = r.getBMCClient(ctx, bmcObj)
	}
	if clientErr != nil {
		return ctrl.Result{}, fmt.Errorf("failed to get BMC client: %w", clientErr)
	}
	defer bmcClient.Logout()
	if err := r.patchUserStatus(ctx, user, bmcClient, metav1.Time{}); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to update User status: %w", err)
	}

	if user.Spec.BMCSecretRef == nil {
		log.V(1).Info("No BMCSecret reference set for User, creating a new one")
		if err := r.ensureBMCSecretForUser(ctx, bmcClient, user, bmcObj); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to handle missing BMCSecret reference: %w", err)
		}
	}
	bmcSecret := &metalv1alpha1.BMCSecret{}
	if err := r.Get(ctx, client.ObjectKey{Name: user.Spec.BMCSecretRef.Name}, bmcSecret); err != nil {
		return ctrl.Result{}, err
	}

	if user.Status.ID == "" {
		log.V(1).Info("No BMC account ID set in User status, creating or updating BMC account")
		_, password, err := bmcutils.GetBMCCredentialsFromSecret(bmcSecret)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to get credentials from BMCSecret: %w", err)
		}
		accountService, err := bmcClient.GetAccountService()
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to get account service: %w", err)
		}
		if err := utils.ValidatePasswordLength(password, accountService.MaxPasswordLength, accountService.MinPasswordLength, bmc.Manufacturer(bmcObj.Status.Manufacturer)); err != nil {
			log.Info("Password in referenced BMCSecret violates BMC length limits; not applying", "BMCSecret", bmcSecret.Name, "error", err.Error())
			if condErr := r.setPasswordValidCondition(ctx, user, false, err.Error()); condErr != nil {
				return ctrl.Result{}, condErr
			}
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
		if err := r.setPasswordValidCondition(ctx, user, true, ""); err != nil {
			return ctrl.Result{}, err
		}
		if err = bmcClient.CreateOrUpdateAccount(ctx, user.Spec.UserName, user.Spec.RoleID, password, true); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to create or update BMC account with new password: %w", err)
		}
		if err = r.patchUserStatus(ctx, user, bmcClient, metav1.Now()); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to update User status after creating BMC account: %w", err)
		}
	}
	if err := r.reconcileSecretRef(ctx, user, bmcSecret, bmcClient, bmcObj); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to reconcile BMCSecret reference: %w", err)
	}
	if err := r.pruneOldSecrets(ctx, user, bmcObj); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to prune old BMCSecrets: %w", err)
	}
	if err := r.updateCredentialsBootstrappedCondition(ctx, user); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to update CredentialsBootstrapped condition: %w", err)
	}
	return r.ensureBMCUserRotation(ctx, user)
}

// patchUserStatus looks up the BMC account matching the user's UserName and
// patches the BMCUser status with the account ID, optional last-rotation time,
// and password expiration reported by the BMC.
func (r *BMCUserReconciler) patchUserStatus(ctx context.Context, user *baseboardv1alpha1.BMCUser, bmcClient bmc.BMC, lastRotation metav1.Time) error {
	log := ctrl.LoggerFrom(ctx)
	accounts, err := bmcClient.GetAccounts()
	if err != nil {
		return fmt.Errorf("failed to get BMC accounts: %w", err)
	}
	for _, account := range accounts {
		if account.UserName == user.Spec.UserName {
			log.V(1).Info("BMC account already exists", "ID", account.ID)
			userBase := user.DeepCopy()
			user.Status.ID = account.ID
			if !lastRotation.IsZero() {
				user.Status.LastRotation = &lastRotation
			}
			if account.PasswordExpiration != "" {
				exp, err := time.Parse(time.RFC3339, account.PasswordExpiration)
				if err == nil {
					user.Status.PasswordExpiration = &metav1.Time{Time: exp}
				} else {
					log.Error(err, "Failed to parse password expiration time from BMC account", "Expiration", account.PasswordExpiration)
				}
			}
			if err := r.Status().Patch(ctx, user, client.MergeFrom(userBase)); err != nil {
				return fmt.Errorf("failed to patch User status with BMC account ID: %w", err)
			}
			log.V(1).Info("Updated User status with BMC account ID", "AccountID", account.ID)
			return nil
		}
	}
	return nil
}

// ensureBMCUserRotation creates a BMCUserRotation object when rotation is due,
// unless one is already in flight. The BMCUserRotationReconciler executes the
// actual rotation work and updates BMCUser status on completion.
func (r *BMCUserReconciler) ensureBMCUserRotation(ctx context.Context, user *baseboardv1alpha1.BMCUser) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	forceRotation := false
	var triggerReason baseboardv1alpha1.BMCUserRotationTrigger

	if user.GetAnnotations() != nil && user.GetAnnotations()[metalv1alpha1.OperationAnnotation] == metalv1alpha1.OperationAnnotationRotateCredentials {
		log.V(1).Info("User has rotation annotation set, triggering password rotation")
		forceRotation = true
		triggerReason = baseboardv1alpha1.BMCUserRotationTriggerAnnotation
		userBase := user.DeepCopy()
		delete(user.Annotations, metalv1alpha1.OperationAnnotation)
		if err := r.Patch(ctx, user, client.MergeFrom(userBase)); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to remove rotation annotation from User: %w", err)
		}
	}
	if user.Status.PasswordExpiration != nil && user.Status.PasswordExpiration.Before(&metav1.Time{Time: time.Now()}) {
		log.V(1).Info("BMC user password has expired, rotating password")
		forceRotation = true
		triggerReason = baseboardv1alpha1.BMCUserRotationTriggerExpiry
	}
	if user.Spec.RotationPeriod == nil && !forceRotation {
		log.V(1).Info("No rotation period set for BMC user, skipping password rotation")
		return ctrl.Result{}, nil
	}
	if user.Spec.RotationPeriod != nil && user.Status.LastRotation != nil &&
		user.Status.LastRotation.Add(user.Spec.RotationPeriod.Duration).After(time.Now()) && !forceRotation {
		log.V(1).Info("BMC user password rotation is not needed yet")
		remaining := time.Until(user.Status.LastRotation.Add(user.Spec.RotationPeriod.Duration))
		return ctrl.Result{RequeueAfter: remaining}, nil
	}
	if triggerReason == "" {
		triggerReason = baseboardv1alpha1.BMCUserRotationTriggerPeriod
	}

	// Check for an existing in-flight rotation to avoid duplicates.
	rotationList := &baseboardv1alpha1.BMCUserRotationList{}
	if err := r.List(ctx, rotationList); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to list BMCUserRotations: %w", err)
	}
	for _, rot := range rotationList.Items {
		if rot.Spec.BMCUserRef.Name == user.Name &&
			rot.Status.Phase != baseboardv1alpha1.BMCUserRotationPhaseSucceeded &&
			rot.Status.Phase != baseboardv1alpha1.BMCUserRotationPhaseFailed {
			log.V(1).Info("Rotation already in flight, skipping", "BMCUserRotation", rot.Name)
			return ctrl.Result{}, nil
		}
	}

	// For DualAccount (OperatorAdmin), prevent both peers from creating a rotation
	// simultaneously to avoid a deadlock. See hasPeerRotationInFlight.
	if user.Spec.Type == baseboardv1alpha1.BMCUserTypeOperatorAdmin && user.Spec.BMCRef != nil {
		defer_, err := r.hasPeerRotationInFlight(ctx, user, rotationList.Items)
		if err != nil {
			return ctrl.Result{}, err
		}
		if defer_ {
			log.V(1).Info("Peer OperatorAdmin BMCUser already has an in-flight rotation; deferring")
			return ctrl.Result{}, nil
		}
	}

	// Back off before retrying after a recent failure to avoid tight retry loops.
	// Annotation-triggered rotations are an explicit operator override and bypass
	// the delay; expiry and period triggers still honour it.
	if triggerReason != baseboardv1alpha1.BMCUserRotationTriggerAnnotation {
		if result, backoff := r.checkRotationFailureBackoff(ctx, user.Name, rotationList.Items); backoff {
			return result, nil
		}
	}

	rotationType := baseboardv1alpha1.RotationStrategySingleAccount
	if user.Spec.Type == baseboardv1alpha1.BMCUserTypeOperatorAdmin {
		rotationType = baseboardv1alpha1.RotationStrategyDualAccount
	}

	rotation := &baseboardv1alpha1.BMCUserRotation{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: user.Name + "-",
		},
		Spec: baseboardv1alpha1.BMCUserRotationSpec{
			BMCUserRef: corev1.LocalObjectReference{Name: user.Name},
			Type:       rotationType,
		},
	}
	if err := controllerutil.SetControllerReference(user, rotation, r.Scheme); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to set controller reference for BMCUserRotation: %w", err)
	}
	if err := r.Create(ctx, rotation); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to create BMCUserRotation: %w", err)
	}
	// TriggeredAt/TriggerReason are observed facts recorded by the controller, so
	// they live in status and must be written after Create (status is a subresource).
	rotBase := rotation.DeepCopy()
	rotation.Status.TriggeredAt = &metav1.Time{Time: time.Now()}
	rotation.Status.TriggerReason = triggerReason
	if err := r.Status().Patch(ctx, rotation, client.MergeFrom(rotBase)); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to record rotation trigger in status: %w", err)
	}
	log.Info("Created BMCUserRotation", "BMCUserRotation", rotation.Name, "Type", rotationType, "Trigger", triggerReason)
	if err := r.pruneRotationHistory(ctx, user, rotationList.Items); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to prune rotation history: %w", err)
	}
	return ctrl.Result{}, nil
}

// checkRotationFailureBackoff returns a RequeueAfter result and backoff=true when
// the most recent failed rotation for userName completed recently enough that we
// should wait before retrying.
func (r *BMCUserReconciler) checkRotationFailureBackoff(ctx context.Context, userName string, rotations []baseboardv1alpha1.BMCUserRotation) (ctrl.Result, bool) {
	log := ctrl.LoggerFrom(ctx)
	var latestFailed *baseboardv1alpha1.BMCUserRotation
	for i := range rotations {
		rot := &rotations[i]
		if rot.Spec.BMCUserRef.Name != userName ||
			rot.Status.Phase != baseboardv1alpha1.BMCUserRotationPhaseFailed ||
			rot.Status.CompletedAt == nil {
			continue
		}
		if latestFailed == nil || rot.Status.CompletedAt.After(latestFailed.Status.CompletedAt.Time) {
			latestFailed = rot
		}
	}
	if latestFailed == nil {
		return ctrl.Result{}, false
	}
	retryAt := latestFailed.Status.CompletedAt.Add(r.rotationFailureDelay())
	if time.Now().Before(retryAt) {
		remaining := time.Until(retryAt)
		log.V(1).Info("Last rotation failed recently, backing off", "BMCUserRotation", latestFailed.Name, "retryAfter", remaining)
		return ctrl.Result{RequeueAfter: remaining}, true
	}
	return ctrl.Result{}, false
}

// hasPeerRotationInFlight returns true when another OperatorAdmin BMCUser on
// the same BMC already has a non-terminal rotation, meaning this user should
// defer creating its own rotation to avoid the mutual-wait deadlock where both
// peers block on each other inside findPeerWithSecret.
func (r *BMCUserReconciler) hasPeerRotationInFlight(ctx context.Context, user *baseboardv1alpha1.BMCUser, rotations []baseboardv1alpha1.BMCUserRotation) (bool, error) {
	inFlight := make(map[string]bool, len(rotations))
	for _, rot := range rotations {
		if rot.Status.Phase != baseboardv1alpha1.BMCUserRotationPhaseSucceeded &&
			rot.Status.Phase != baseboardv1alpha1.BMCUserRotationPhaseFailed {
			inFlight[rot.Spec.BMCUserRef.Name] = true
		}
	}
	peerList := &baseboardv1alpha1.BMCUserList{}
	if err := r.List(ctx, peerList); err != nil {
		return false, fmt.Errorf("failed to list BMCUsers: %w", err)
	}
	for i := range peerList.Items {
		peer := &peerList.Items[i]
		if peer.Name == user.Name ||
			peer.Spec.Type != baseboardv1alpha1.BMCUserTypeOperatorAdmin ||
			peer.Spec.BMCRef == nil ||
			peer.Spec.BMCRef.Name != user.Spec.BMCRef.Name {
			continue
		}
		if inFlight[peer.Name] {
			return true, nil
		}
	}
	return false, nil
}

// ensureBMCSecretForUser generates a password, creates a BMCSecret to hold the
// credentials, and links it to the BMCUser via its BMCSecret reference when the
// user does not yet have one.
func (r *BMCUserReconciler) ensureBMCSecretForUser(ctx context.Context, bmcClient bmc.BMC, user *baseboardv1alpha1.BMCUser, bmcObj *metalv1alpha1.BMC) error {
	log := ctrl.LoggerFrom(ctx)
	log.Info("No BMCSecret reference set for User, creating a new one")
	accountService, err := bmcClient.GetAccountService()
	if err != nil {
		return fmt.Errorf("failed to get account service: %w", err)
	}
	fallback := r.DefaultPasswordLength
	if fallback <= 0 {
		fallback = 20
	}
	newPassword, err := utils.GenerateHTMLSafePassword(bmc.Manufacturer(bmcObj.Status.Manufacturer), accountService.MaxPasswordLength, accountService.MinPasswordLength, fallback)
	if err != nil {
		return fmt.Errorf("failed to generate new password for BMC account %s: %w", user.Name, err)
	}
	secret, err := r.createBMCSecretForUser(ctx, user, newPassword)
	if err != nil {
		return fmt.Errorf("failed to create BMCSecret: %w", err)
	}
	if err := r.setBMCUserSecretRef(ctx, user, secret); err != nil {
		return fmt.Errorf("failed to set BMCSecret reference for User: %w", err)
	}
	return nil
}

// reconcileSecretRef handles the desired-vs-effective BMCSecret reconciliation.
// When the desired secret (Spec.BMCSecretRef) differs from the effective one, it
// delegates to handleUpdatedSecretRef. When they are the same and the effective
// secret is already known, it clears any stale PasswordValid=False condition.
func (r *BMCUserReconciler) reconcileSecretRef(ctx context.Context, user *baseboardv1alpha1.BMCUser, bmcSecret *metalv1alpha1.BMCSecret, bmcClient bmc.BMC, bmcObj *metalv1alpha1.BMC) error {
	if user.Status.EffectiveBMCSecretRef == nil {
		return nil
	}
	if user.Spec.BMCSecretRef.Name != user.Status.EffectiveBMCSecretRef.Name {
		return r.handleUpdatedSecretRef(ctx, user, bmcSecret, bmcClient, bmcObj)
	}
	// Spec.BMCSecretRef matches the effective secret — clear any stale PasswordValid=False.
	return r.setPasswordValidCondition(ctx, user, true, "")
}

// handleUpdatedSecretRef applies the credentials from a newly referenced
// BMCSecret to the BMC account and records the secret as the effective one.
// It validates the new password against the BMC's reported length limits first;
// on violation it records PasswordValid=False and returns without touching the
// account, so an over-length password is surfaced instead of being silently
// rejected by the BMC (see the create path in reconcile).
func (r *BMCUserReconciler) handleUpdatedSecretRef(ctx context.Context, user *baseboardv1alpha1.BMCUser, bmcSecret *metalv1alpha1.BMCSecret, bmcClient bmc.BMC, bmcObj *metalv1alpha1.BMC) error {
	log := ctrl.LoggerFrom(ctx)
	log.V(1).Info("BMCSecret credentials have changed, updating BMC user")
	_, password, err := bmcutils.GetBMCCredentialsFromSecret(bmcSecret)
	if err != nil {
		return fmt.Errorf("failed to get credentials from BMCSecret: %w", err)
	}
	accountService, err := bmcClient.GetAccountService()
	if err != nil {
		return fmt.Errorf("failed to get account service: %w", err)
	}
	if err := utils.ValidatePasswordLength(password, accountService.MaxPasswordLength, accountService.MinPasswordLength, bmc.Manufacturer(bmcObj.Status.Manufacturer)); err != nil {
		log.Info("Updated password in referenced BMCSecret violates BMC length limits; not applying", "BMCSecret", bmcSecret.Name, "error", err.Error())
		return r.setPasswordValidCondition(ctx, user, false, err.Error())
	}
	if err := bmcClient.CreateOrUpdateAccount(ctx, user.Spec.UserName, user.Spec.RoleID, password, true); err != nil {
		return fmt.Errorf("failed to create or update BMC account with new password: %w", err)
	}
	return r.setPasswordValidCondition(ctx, user, true, "")
}

// createBMCSecretForUser builds a BMCSecret (owned by the BMCUser) populated
// with the user's name and the given password.
func (r *BMCUserReconciler) createBMCSecretForUser(ctx context.Context, user *baseboardv1alpha1.BMCUser, password string) (*metalv1alpha1.BMCSecret, error) {
	log := ctrl.LoggerFrom(ctx)
	log.V(1).Info("Creating BMCSecret for User")
	secret := &metalv1alpha1.BMCSecret{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: user.Name,
		},
		Data: map[string][]byte{
			metalv1alpha1.BMCSecretUsernameKeyName: []byte(user.Spec.UserName),
			metalv1alpha1.BMCSecretPasswordKeyName: []byte(password),
		},
		Immutable: new(true),
	}
	if err := controllerutil.SetControllerReference(user, secret, r.Scheme); err != nil {
		return nil, fmt.Errorf("failed to set controller reference for BMCSecret: %w", err)
	}
	if err := r.Create(ctx, secret); err != nil {
		return nil, fmt.Errorf("failed to create BMCSecret: %w", err)
	}
	log.V(1).Info("BMCSecret created", "BMCSecret", secret.Name)
	return secret, nil
}

// setBMCUserSecretRef patches the BMCUser to reference the given BMCSecret as
// its desired credentials secret.
func (r *BMCUserReconciler) setBMCUserSecretRef(ctx context.Context, user *baseboardv1alpha1.BMCUser, secret *metalv1alpha1.BMCSecret) error {
	log := ctrl.LoggerFrom(ctx)
	log.V(1).Info("Setting BMCSecret reference for User", "User", user.Name)
	userBase := user.DeepCopy()
	user.Spec.BMCSecretRef = &corev1.LocalObjectReference{Name: secret.Name}
	if err := r.Patch(ctx, user, client.MergeFrom(userBase)); err != nil {
		return fmt.Errorf("failed to patch User with BMCSecretRef: %w", err)
	}
	return nil
}

// setEffectiveSecretRef records in the BMCUser status which BMCSecret currently
// holds the credentials that are known to work against the BMC. When the BMCUser
// is of OperatorAdmin type, it also propagates the secret to BMC.Spec.BMCSecretRef
// so all operator controllers pick up the new credential.
func (r *BMCUserReconciler) setEffectiveSecretRef(ctx context.Context, user *baseboardv1alpha1.BMCUser, secret *metalv1alpha1.BMCSecret, bmcObj *metalv1alpha1.BMC) error {
	log := ctrl.LoggerFrom(ctx)
	log.V(1).Info("Setting effective BMCSecret")
	userBase := user.DeepCopy()
	user.Status.EffectiveBMCSecretRef = &corev1.LocalObjectReference{Name: secret.Name}
	if err := r.Status().Patch(ctx, user, client.MergeFrom(userBase)); err != nil {
		return fmt.Errorf("failed to patch User status with effective BMCSecretRef: %w", err)
	}
	if user.Spec.Type == baseboardv1alpha1.BMCUserTypeOperatorAdmin {
		if err := r.syncBMCSecretRefToBMC(ctx, user, secret, bmcObj); err != nil {
			return fmt.Errorf("failed to sync effective secret to BMC: %w", err)
		}
	}
	return nil
}

// syncBMCSecretRefToBMC patches BMC.Spec.BMCSecretRef to the given secret when
// it differs from the current value. This is only called for BMCUsers of
// OperatorAdmin type, after a live auth test has confirmed the secret works.
func (r *BMCUserReconciler) syncBMCSecretRefToBMC(ctx context.Context, user *baseboardv1alpha1.BMCUser, secret *metalv1alpha1.BMCSecret, bmcObj *metalv1alpha1.BMC) error {
	if bmcObj.Spec.BMCSecretRef.Name == secret.Name {
		return nil
	}
	log := ctrl.LoggerFrom(ctx)
	log.Info("Promoting effective BMCUser secret to BMC.Spec.BMCSecretRef",
		"BMCUser", user.Name, "NewSecret", secret.Name, "OldSecret", bmcObj.Spec.BMCSecretRef.Name)
	bmcBase := bmcObj.DeepCopy()
	bmcObj.Spec.BMCSecretRef = corev1.LocalObjectReference{Name: secret.Name}
	if err := r.Patch(ctx, bmcObj, client.MergeFrom(bmcBase)); err != nil {
		return fmt.Errorf("failed to patch BMC.Spec.BMCSecretRef: %w", err)
	}
	return nil
}

// getBMCClient builds an authenticated BMC client for the given BMC using the
// reconciler's default protocol, certificate validation, and BMC options.
func (r *BMCUserReconciler) getBMCClient(ctx context.Context, bmcObj *metalv1alpha1.BMC) (bmc.BMC, error) {
	bmcClient, err := bmcutils.GetBMCClientFromBMC(ctx, r.Client, bmcObj, r.DefaultProtocol, r.SkipCertValidation, r.BMCOptions)
	if err != nil {
		return bmcClient, fmt.Errorf("failed to create BMC client: %w", err)
	}
	return bmcClient, nil
}

// lookupSetupCredentialRef returns the SetupCredentialRef from the owning
// BMCUserSet, or nil if the BMCUser has no controller owner of that kind or
// the SetupCredentialRef is not configured.
func (r *BMCUserReconciler) lookupSetupCredentialRef(ctx context.Context, user *baseboardv1alpha1.BMCUser) (*corev1.SecretReference, error) {
	owner := metav1.GetControllerOf(user)
	if owner == nil || owner.Kind != "BMCUserSet" {
		return nil, nil
	}
	set := &baseboardv1alpha1.BMCUserSet{}
	if err := r.Get(ctx, client.ObjectKey{Name: owner.Name}, set); err != nil {
		return nil, err
	}
	if set.Spec.SetupCredentialRef == nil || set.Spec.SetupCredentialRef.Name == "" {
		return nil, nil
	}
	return set.Spec.SetupCredentialRef, nil
}

// getSetupBMCClient builds a BMC client authenticated with the operator-wide
// setup credential. Used only during bootstrap (before a dedicated operator
// account exists on the BMC).
func (r *BMCUserReconciler) getSetupBMCClient(ctx context.Context, bmcObj *metalv1alpha1.BMC, setupRef corev1.SecretReference) (bmc.BMC, error) {
	ns := setupRef.Namespace
	if ns == "" {
		ns = r.ManagerNamespace
	}
	setupSecret := &corev1.Secret{}
	if err := r.Get(ctx, client.ObjectKey{Name: setupRef.Name, Namespace: ns}, setupSecret); err != nil {
		return nil, fmt.Errorf("failed to get setup credential secret %s/%s: %w", ns, setupRef.Name, err)
	}
	syntheticBMCSecret := &metalv1alpha1.BMCSecret{
		Data: map[string][]byte{
			metalv1alpha1.BMCSecretUsernameKeyName: setupSecret.Data["username"],
			metalv1alpha1.BMCSecretPasswordKeyName: setupSecret.Data["password"],
		},
	}
	protocolScheme := bmcutils.GetProtocolScheme(bmcObj.Spec.Protocol.Scheme, r.DefaultProtocol)
	address, err := bmcutils.GetBMCAddressForBMC(ctx, r.Client, bmcObj)
	if err != nil {
		return nil, fmt.Errorf("failed to get BMC address: %w", err)
	}
	return bmcutils.CreateBMCClient(ctx, r.Client, protocolScheme, bmcObj.Spec.Protocol.Name,
		address, bmcObj.Spec.Protocol.Port, syntheticBMCSecret, r.BMCOptions, r.SkipCertValidation)
}

// getBootstrapBMCClient builds a BMC client authenticated with a specific
// BMCSecret identified by name. Used during bootstrap when the per-BMC
// factory credential was recorded in BMCUser.Status.BootstrapSecretRef.
func (r *BMCUserReconciler) getBootstrapBMCClient(ctx context.Context, bmcObj *metalv1alpha1.BMC, secretName string) (bmc.BMC, error) {
	secret := &metalv1alpha1.BMCSecret{}
	if err := r.Get(ctx, client.ObjectKey{Name: secretName}, secret); err != nil {
		return nil, fmt.Errorf("failed to get bootstrap BMCSecret %s: %w", secretName, err)
	}
	protocolScheme := bmcutils.GetProtocolScheme(bmcObj.Spec.Protocol.Scheme, r.DefaultProtocol)
	address, err := bmcutils.GetBMCAddressForBMC(ctx, r.Client, bmcObj)
	if err != nil {
		return nil, fmt.Errorf("failed to get BMC address: %w", err)
	}
	return bmcutils.CreateBMCClient(ctx, r.Client, protocolScheme, bmcObj.Spec.Protocol.Name,
		address, bmcObj.Spec.Protocol.Port, secret, r.BMCOptions, r.SkipCertValidation)
}

// updateEffectiveSecret validates the credentials in the user's referenced
// BMCSecret against the BMC and promotes it to the effective secret when it
// works, so the status always points at credentials that authenticate.
func (r *BMCUserReconciler) updateEffectiveSecret(ctx context.Context, user *baseboardv1alpha1.BMCUser, bmcObj *metalv1alpha1.BMC) error {
	log := ctrl.LoggerFrom(ctx)
	if user.Spec.BMCSecretRef == nil || user.Status.ID == "" {
		return nil
	}
	secret := &metalv1alpha1.BMCSecret{}
	if err := r.Get(ctx, client.ObjectKey{
		Name: user.Spec.BMCSecretRef.Name,
	}, secret); err != nil {
		return fmt.Errorf("failed to get BMCSecret %s: %w", user.Spec.BMCSecretRef.Name, err)
	}

	invalidCredentials, err := r.bmcConnectionTest(ctx, secret, bmcObj)
	if err != nil {
		return fmt.Errorf("failed to test BMC connection with BMCSecret %s: %w", secret.Name, err)
	}
	if invalidCredentials {
		log.V(1).Info("New BMCSecret is invalid, will not update effective BMCSecret", "NewBMCSecret", secret.Name)
		return nil
	}
	if user.Status.EffectiveBMCSecretRef == nil {
		if err := r.setEffectiveSecretRef(ctx, user, secret, bmcObj); err != nil {
			return fmt.Errorf("failed to update effective BMCSecret: %w", err)
		}
		log.V(1).Info("Set effective BMCSecret for User")
		return nil
	}

	effSecret := &metalv1alpha1.BMCSecret{}
	if err := r.Get(ctx, client.ObjectKey{
		Name: user.Status.EffectiveBMCSecretRef.Name,
	}, effSecret); err != nil {
		return fmt.Errorf("failed to get effective BMCSecret %s: %w", user.Status.EffectiveBMCSecretRef.Name, err)
	}

	invalidCredentials, err = r.bmcConnectionTest(ctx, effSecret, bmcObj)
	if err != nil {
		return fmt.Errorf("failed to test BMC connection with effectiveSecret %s: %w", effSecret.Name, err)
	}
	if invalidCredentials {
		if err := r.setEffectiveSecretRef(ctx, user, secret, bmcObj); err != nil {
			return fmt.Errorf("failed to update effective BMCSecret: %w", err)
		}
		log.V(1).Info("Updated effective BMCSecret for User")
	}
	return nil
}

// bmcConnectionTest reports whether the credentials in the given BMCSecret are
// rejected by the BMC. It returns true when authentication fails (HTTP 401/403)
// and false when the credentials are accepted.
func (r *BMCUserReconciler) bmcConnectionTest(ctx context.Context, secret *metalv1alpha1.BMCSecret, bmcObj *metalv1alpha1.BMC) (bool, error) {
	protocolScheme := bmcutils.GetProtocolScheme(bmcObj.Spec.Protocol.Scheme, r.DefaultProtocol)
	address, err := bmcutils.GetBMCAddressForBMC(ctx, r.Client, bmcObj)
	if err != nil {
		return false, fmt.Errorf("failed to get BMC address: %w", err)
	}
	bmcClient, err := bmcutils.CreateBMCClient(ctx, r.Client, protocolScheme, bmcObj.Spec.Protocol.Name, address, bmcObj.Spec.Protocol.Port, secret, r.BMCOptions, r.SkipCertValidation)
	if err != nil {
		if httpErr, ok := errors.AsType[*schemas.Error](err); ok {
			if httpErr.HTTPReturnedStatusCode == 401 || httpErr.HTTPReturnedStatusCode == 403 {
				return true, nil
			}
		}
		return false, fmt.Errorf("failed to create BMC client: %w", err)
	}
	defer bmcClient.Logout()
	if r.BMCOptions.BasicAuth {
		if _, err := bmcClient.GetAccountService(); err != nil {
			var httpErr *schemas.Error
			if errors.As(err, &httpErr) && (httpErr.HTTPReturnedStatusCode == 401 || httpErr.HTTPReturnedStatusCode == 403) {
				return true, nil
			}
			return false, fmt.Errorf("failed to verify BMC credentials: %w", err)
		}
	}
	return false, nil
}

// isLiveCredential returns true if any BMCSecret owned by the given user is
// currently referenced by BMC.Spec.BMCSecretRef. Used to prevent lockout when
// deleting an OperatorAdmin BMCUser.
func (r *BMCUserReconciler) isLiveCredential(ctx context.Context, user *baseboardv1alpha1.BMCUser, bmcObj *metalv1alpha1.BMC) (bool, error) {
	secretList := &metalv1alpha1.BMCSecretList{}
	if err := r.List(ctx, secretList); err != nil {
		return false, fmt.Errorf("failed to list BMCSecrets: %w", err)
	}
	for _, secret := range secretList.Items {
		if !metav1.IsControlledBy(&secret, user) {
			continue
		}
		if secret.Name == bmcObj.Spec.BMCSecretRef.Name {
			return true, nil
		}
	}
	return false, nil
}

// delete removes the BMC account for the BMCUser from its referenced BMC (if
// still present) and clears the finalizer so the object can be garbage
// collected.
func (r *BMCUserReconciler) delete(ctx context.Context, user *baseboardv1alpha1.BMCUser) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	if user.Spec.BMCRef == nil {
		log.V(1).Info("No BMC reference set for User, removing finalizer")
		if modified, err := clientutils.PatchEnsureNoFinalizer(ctx, r.Client, user, bmcUserFinalizer); err != nil || modified {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	bmcObj := &metalv1alpha1.BMC{}
	if err := r.Get(ctx, client.ObjectKey{Name: user.Spec.BMCRef.Name}, bmcObj); err != nil {
		if client.IgnoreNotFound(err) == nil {
			log.V(1).Info("BMC not found, removing finalizer")
			if modified, err := clientutils.PatchEnsureNoFinalizer(ctx, r.Client, user, bmcUserFinalizer); err != nil || modified {
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Determine effective deletion policy: explicit spec value, or default by type.
	deletionPolicy := user.Spec.DeletionPolicy
	if deletionPolicy == "" {
		if user.Spec.Type == baseboardv1alpha1.BMCUserTypeOperatorAdmin {
			deletionPolicy = "Retain"
		} else {
			deletionPolicy = "Delete"
		}
	}

	if deletionPolicy == "Retain" {
		// Never delete an account whose credential is still live in BMC.Spec.BMCSecretRef.
		live, err := r.isLiveCredential(ctx, user, bmcObj)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to check live credential: %w", err)
		}
		if live {
			log.Info("Refusing to delete BMC account: its credential is referenced by BMC.Spec.BMCSecretRef; waiting for credential rotation before removing")
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
		// Retain policy: keep the BMC account, just remove the finalizer.
		log.V(1).Info("DeletionPolicy=Retain: removing finalizer without deleting BMC account")
		if modified, err := clientutils.PatchEnsureNoFinalizer(ctx, r.Client, user, bmcUserFinalizer); err != nil || modified {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	bmcClient, err := r.getBMCClient(ctx, bmcObj)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to get BMC client: %w", err)
	}
	defer bmcClient.Logout()

	log.V(1).Info("Deleting BMC account for User")
	accountID := user.Status.ID
	accountFound := accountID != ""
	if !accountFound {
		// Status.ID may be empty if patchUserStatus failed after CreateOrUpdateAccount
		// succeeded. Fall back to a lookup by username to avoid orphaning the account.
		accounts, err := bmcClient.GetAccounts()
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to get BMC accounts: %w", err)
		}
		for _, account := range accounts {
			if account.UserName == user.Spec.UserName {
				accountID = account.ID
				accountFound = true
				break
			}
		}
	}
	if !accountFound {
		// The account was never created on the BMC (e.g. the password was rejected
		// before it could be applied), so there is nothing to delete.
		log.V(1).Info("No BMC account recorded for User, skipping BMC account deletion")
	} else if err := bmcClient.DeleteAccount(ctx, user.Spec.UserName, accountID); err != nil {
		var httpErr *schemas.Error
		if errors.As(err, &httpErr) && httpErr.HTTPReturnedStatusCode == 404 {
			log.V(1).Info("BMC account not found, continuing finalizer removal")
		} else {
			return ctrl.Result{}, fmt.Errorf("failed to delete BMC account: %w", err)
		}
	}
	if modified, err := clientutils.PatchEnsureNoFinalizer(ctx, r.Client, user, bmcUserFinalizer); err != nil || modified {
		return ctrl.Result{}, err
	}
	log.V(1).Info("Successfully deleted BMC account and removed finalizer for User")
	return ctrl.Result{}, nil
}

// pruneRotationHistory deletes the oldest completed BMCUserRotation objects
// (Succeeded or Failed) for this user when the count exceeds RotationHistoryLimit.
func (r *BMCUserReconciler) pruneRotationHistory(ctx context.Context, user *baseboardv1alpha1.BMCUser, allRotations []baseboardv1alpha1.BMCUserRotation) error {
	limit := int32(10)
	if user.Spec.RotationHistoryLimit != nil {
		limit = *user.Spec.RotationHistoryLimit
	}

	var completed []baseboardv1alpha1.BMCUserRotation
	for _, rot := range allRotations {
		if rot.Spec.BMCUserRef.Name != user.Name {
			continue
		}
		if rot.Status.Phase == baseboardv1alpha1.BMCUserRotationPhaseSucceeded ||
			rot.Status.Phase == baseboardv1alpha1.BMCUserRotationPhaseFailed {
			completed = append(completed, rot)
		}
	}
	if int32(len(completed)) <= limit {
		return nil
	}
	// Sort oldest first (by creation timestamp) and delete the excess.
	slices.SortFunc(completed, func(a, b baseboardv1alpha1.BMCUserRotation) int {
		return a.CreationTimestamp.Compare(b.CreationTimestamp.Time)
	})
	toDelete := completed[:int32(len(completed))-limit]
	for i := range toDelete {
		if err := r.Delete(ctx, &toDelete[i]); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("failed to delete old BMCUserRotation %s: %w", toDelete[i].Name, err)
		}
	}
	return nil
}

// pruneOldSecrets deletes owned BMCSecret objects that are no longer needed.
// The following secrets are always retained:
//   - Spec.BMCSecretRef (desired credential)
//   - Status.EffectiveBMCSecretRef (current working credential)
//   - Status.BootstrapSecretRef (factory credential for peer bootstrap)
//   - BMC.Spec.BMCSecretRef (live credential used by all operator controllers)
//   - Any in-flight BMCUserRotation's Status.NewSecretRef
//   - Secrets labelled baseboard.metal.ironcore.dev/bmcuserrotation-name=<name>
//     for an in-flight BMCUserRotation (protects a freshly created rotation
//     credential before its NewSecretRef is persisted)
//   - Secrets labelled baseboard.metal.ironcore.dev/credential-role=break-glass
func (r *BMCUserReconciler) pruneOldSecrets(ctx context.Context, user *baseboardv1alpha1.BMCUser, bmcObj *metalv1alpha1.BMC) error {
	// Build the protected set.
	protected := make(map[string]bool)
	if user.Spec.BMCSecretRef != nil {
		protected[user.Spec.BMCSecretRef.Name] = true
	}
	if user.Status.EffectiveBMCSecretRef != nil {
		protected[user.Status.EffectiveBMCSecretRef.Name] = true
	}
	if user.Status.BootstrapSecretRef != nil {
		protected[user.Status.BootstrapSecretRef.Name] = true
	}
	if bmcObj != nil {
		protected[bmcObj.Spec.BMCSecretRef.Name] = true
	}

	// Add any in-flight rotation's NewSecretRef, and track the names of in-flight
	// rotations so their freshly created secrets are protected during the window
	// between secret creation and NewSecretRef being persisted.
	rotationList := &baseboardv1alpha1.BMCUserRotationList{}
	if err := r.List(ctx, rotationList); err != nil {
		return fmt.Errorf("failed to list BMCUserRotations for secret pruning: %w", err)
	}
	inFlightRotations := make(map[string]bool)
	for _, rot := range rotationList.Items {
		if rot.Spec.BMCUserRef.Name != user.Name {
			continue
		}
		if rot.Status.Phase == baseboardv1alpha1.BMCUserRotationPhaseSucceeded ||
			rot.Status.Phase == baseboardv1alpha1.BMCUserRotationPhaseFailed {
			continue
		}
		inFlightRotations[rot.Name] = true
		if rot.Status.NewSecretRef != nil {
			protected[rot.Status.NewSecretRef.Name] = true
		}
	}

	secretList := &metalv1alpha1.BMCSecretList{}
	if err := r.List(ctx, secretList); err != nil {
		return fmt.Errorf("failed to list BMCSecrets for pruning: %w", err)
	}
	var errs []error
	for i := range secretList.Items {
		secret := &secretList.Items[i]
		if !metav1.IsControlledBy(secret, user) {
			continue
		}
		if protected[secret.Name] {
			continue
		}
		if secret.Labels[credentialRoleLabel] == credentialRoleBreakGlass {
			continue
		}
		if rotationName := secret.Labels[rotationNameLabel]; rotationName != "" && inFlightRotations[rotationName] {
			continue
		}
		if err := r.Delete(ctx, secret); client.IgnoreNotFound(err) != nil {
			errs = append(errs, fmt.Errorf("failed to delete old BMCSecret %s: %w", secret.Name, err))
		}
	}
	return errors.Join(errs...)
}

// setPasswordValidCondition sets the PasswordValid condition on the BMCUser. message is
// ignored when valid is true.
func (r *BMCUserReconciler) setPasswordValidCondition(ctx context.Context, user *baseboardv1alpha1.BMCUser, valid bool, message string) error {
	cond := metav1.Condition{
		Type:               conditionTypePasswordValid,
		Status:             metav1.ConditionTrue,
		Reason:             reasonPasswordValid,
		Message:            "Password meets BMC length requirements",
		ObservedGeneration: user.Generation,
	}
	if !valid {
		cond.Status = metav1.ConditionFalse
		cond.Reason = reasonPasswordInvalid
		cond.Message = message
	}

	userBase := user.DeepCopy()
	if apimeta.SetStatusCondition(&user.Status.Conditions, cond) {
		if err := r.Status().Patch(ctx, user, client.MergeFrom(userBase)); err != nil {
			return fmt.Errorf("failed to patch PasswordValid condition: %w", err)
		}
	}
	return nil
}

// updateCredentialsBootstrappedCondition sets the CredentialsBootstrapped condition
// on the BMCUser. True when the user has a proven EffectiveBMCSecretRef and (for
// OperatorAdmin users) the factory account has been handled (or no factory handling
// was configured for the owning set).
func (r *BMCUserReconciler) updateCredentialsBootstrappedCondition(ctx context.Context, user *baseboardv1alpha1.BMCUser) error {
	bootstrapped := user.Status.EffectiveBMCSecretRef != nil
	if bootstrapped && user.Spec.Type == baseboardv1alpha1.BMCUserTypeOperatorAdmin {
		// Check whether the owning set has FactoryAccount configured.
		owner := metav1.GetControllerOf(user)
		if owner != nil && owner.Kind == "BMCUserSet" {
			set := &baseboardv1alpha1.BMCUserSet{}
			if err := r.Get(ctx, client.ObjectKey{Name: owner.Name}, set); err == nil {
				if set.Spec.FactoryAccount != nil && !user.Status.FactoryAccountHandled {
					bootstrapped = false
				}
			}
		}
	}

	var cond metav1.Condition
	if bootstrapped {
		cond = metav1.Condition{
			Type:               conditionTypeCredentialsBootstrapped,
			Status:             metav1.ConditionTrue,
			Reason:             reasonCredentialsBootstrapped,
			Message:            "Operator credentials bootstrapped and factory account handled",
			ObservedGeneration: user.Generation,
		}
	} else {
		cond = metav1.Condition{
			Type:               conditionTypeCredentialsBootstrapped,
			Status:             metav1.ConditionFalse,
			Reason:             reasonCredentialsPending,
			Message:            "Waiting for credential bootstrap to complete",
			ObservedGeneration: user.Generation,
		}
	}

	userBase := user.DeepCopy()
	apimeta.SetStatusCondition(&user.Status.Conditions, cond)
	if err := r.Status().Patch(ctx, user, client.MergeFrom(userBase)); err != nil {
		return fmt.Errorf("failed to patch CredentialsBootstrapped condition: %w", err)
	}
	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *BMCUserReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&baseboardv1alpha1.BMCUser{}).
		Owns(&metalv1alpha1.BMCSecret{}).
		Owns(&baseboardv1alpha1.BMCUserRotation{}).
		Watches(&metalv1alpha1.BMC{}, handler.EnqueueRequestsFromMapFunc(r.findBMCUsersForBMC)).
		Watches(&baseboardv1alpha1.BMCUserSet{}, handler.EnqueueRequestsFromMapFunc(r.findBMCUsersForBMCUserSet)).
		Watches(&metalv1alpha1.BMCSecret{}, handler.EnqueueRequestsFromMapFunc(r.findBMCUsersForBMCSecret)).
		Named("bmcuser").
		Complete(r)
}

// findBMCUsersForBMC enqueues all BMCUser objects whose Spec.BMCRef.Name matches
// the name of the changed BMC, so address or protocol changes trigger reconciliation.
func (r *BMCUserReconciler) findBMCUsersForBMC(ctx context.Context, obj client.Object) []reconcile.Request {
	userList := &baseboardv1alpha1.BMCUserList{}
	if err := r.List(ctx, userList); err != nil {
		return nil
	}
	var requests []reconcile.Request
	for _, u := range userList.Items {
		if u.Spec.BMCRef != nil && u.Spec.BMCRef.Name == obj.GetName() {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: u.Name},
			})
		}
	}
	return requests
}

// findBMCUsersForBMCUserSet enqueues all BMCUser objects managed by the changed
// BMCUserSet, so SetupCredentialRef updates reach unbootstrapped users.
func (r *BMCUserReconciler) findBMCUsersForBMCUserSet(ctx context.Context, obj client.Object) []reconcile.Request {
	userList := &baseboardv1alpha1.BMCUserList{}
	if err := r.List(ctx, userList); err != nil {
		return nil
	}
	var requests []reconcile.Request
	for _, u := range userList.Items {
		ownerRef := metav1.GetControllerOf(&u)
		if ownerRef != nil && ownerRef.Kind == "BMCUserSet" && ownerRef.Name == obj.GetName() {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: u.Name},
			})
		}
	}
	return requests
}

// findBMCUsersForBMCSecret enqueues all BMCUser objects whose Spec.BMCSecretRef.Name
// matches the changed BMCSecret. This ensures that when an operator corrects a
// user-supplied BMCSecret that was previously rejected (PasswordValid=False), the
// BMCUser is reconciled without waiting for a periodic requeue.
func (r *BMCUserReconciler) findBMCUsersForBMCSecret(ctx context.Context, obj client.Object) []reconcile.Request {
	userList := &baseboardv1alpha1.BMCUserList{}
	if err := r.List(ctx, userList); err != nil {
		return nil
	}
	var requests []reconcile.Request
	for _, u := range userList.Items {
		if u.Spec.BMCSecretRef != nil && u.Spec.BMCSecretRef.Name == obj.GetName() {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: u.Name},
			})
		}
	}
	return requests
}
