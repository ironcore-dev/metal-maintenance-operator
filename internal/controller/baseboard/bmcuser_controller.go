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
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	"github.com/ironcore-dev/metal-operator/bmc"
	"github.com/ironcore-dev/metal-operator/pkg/bmcutils"
	"github.com/stmcginnis/gofish/schemas"
)

const (
	bmcUserFinalizer = "baseboard.metal.ironcore.dev/bmcuser"
	// defaultPasswordLength is used when the BMC's AccountService does not report
	// a MaxPasswordLength (i.e. the field is zero).
	defaultPasswordLength = 20
	// rotationFailureRetryDelay is the minimum time to wait before retrying a
	// failed BMCUserRotation, to avoid tight retry loops on persistent failures.
	rotationFailureRetryDelay = 5 * time.Minute
)

// passwordLength returns maxLen as int, falling back to defaultPasswordLength
// when the BMC firmware omits the field (value is zero).
func passwordLength(maxLen uint) int {
	if maxLen == 0 {
		return defaultPasswordLength
	}
	return int(maxLen)
}

// BMCUserReconciler reconciles a BMCUser object
type BMCUserReconciler struct {
	client.Client
	Scheme             *runtime.Scheme
	ManagerNamespace   string
	DefaultProtocol    metalv1alpha1.ProtocolScheme
	SkipCertValidation bool
	BMCOptions         bmc.Options
}

// +kubebuilder:rbac:groups=baseboard.metal.ironcore.dev,resources=bmcusers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=baseboard.metal.ironcore.dev,resources=bmcusers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=baseboard.metal.ironcore.dev,resources=bmcusers/finalizers,verbs=update
// +kubebuilder:rbac:groups=baseboard.metal.ironcore.dev,resources=bmcusersets,verbs=get;list;watch
// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=bmcs,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=endpoints,verbs=get;list;watch
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
	// credential is known. Use the setup credential from the owning BMCUserSet
	// if available, so we can create the account even before Spec.BMCSecretRef
	// is set or if a prior attempt failed after setting it.
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
		if err = bmcClient.CreateOrUpdateAccount(ctx, user.Spec.UserName, user.Spec.RoleID, password, true); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to create or update BMC account with new password: %w", err)
		}
		if err = r.patchUserStatus(ctx, user, bmcClient, metav1.Now()); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to update User status after creating BMC account: %w", err)
		}
	}
	if user.Status.EffectiveBMCSecretRef != nil &&
		user.Spec.BMCSecretRef.Name != user.Status.EffectiveBMCSecretRef.Name {

		if err := r.handleUpdatedSecretRef(ctx, user, bmcSecret, bmcClient); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to handle updated BMCSecret reference: %w", err)
		}
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
			BMCUserRef:    corev1.LocalObjectReference{Name: user.Name},
			Type:          rotationType,
			TriggeredAt:   metav1.Now(),
			TriggerReason: triggerReason,
		},
	}
	if err := controllerutil.SetControllerReference(user, rotation, r.Scheme); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to set controller reference for BMCUserRotation: %w", err)
	}
	if err := r.Create(ctx, rotation); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to create BMCUserRotation: %w", err)
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
	retryAt := latestFailed.Status.CompletedAt.Add(rotationFailureRetryDelay)
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
	newPassword, err := bmc.GenerateSecurePassword(bmc.Manufacturer(bmcObj.Status.Manufacturer), passwordLength(accountService.MaxPasswordLength))
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

// handleUpdatedSecretRef applies the credentials from a newly referenced
// BMCSecret to the BMC account and records the secret as the effective one.
func (r *BMCUserReconciler) handleUpdatedSecretRef(ctx context.Context, user *baseboardv1alpha1.BMCUser, bmcSecret *metalv1alpha1.BMCSecret, bmcClient bmc.BMC) error {
	log := ctrl.LoggerFrom(ctx)
	log.V(1).Info("BMCSecret credentials have changed, updating BMC user")
	_, password, err := bmcutils.GetBMCCredentialsFromSecret(bmcSecret)
	if err != nil {
		return fmt.Errorf("failed to get credentials from BMCSecret: %w", err)
	}
	if err := bmcClient.CreateOrUpdateAccount(ctx, user.Spec.UserName, user.Spec.RoleID, password, true); err != nil {
		return fmt.Errorf("failed to create or update BMC account with new password: %w", err)
	}
	return nil
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
// BMCUserSet, or nil if the BMCUser has no controller owner of that kind.
// Uses GetControllerOf rather than Status.ManagedBy so it works before the
// status back-reference is populated.
func (r *BMCUserReconciler) lookupSetupCredentialRef(ctx context.Context, user *baseboardv1alpha1.BMCUser) (*corev1.SecretReference, error) {
	owner := metav1.GetControllerOf(user)
	if owner == nil || owner.Kind != "BMCUserSet" {
		return nil, nil
	}
	set := &baseboardv1alpha1.BMCUserSet{}
	if err := r.Get(ctx, client.ObjectKey{Name: owner.Name}, set); err != nil {
		return nil, err
	}
	return &set.Spec.SetupCredentialRef, nil
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

	bmcClient, err := r.getBMCClient(ctx, bmcObj)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to get BMC client: %w", err)
	}
	defer bmcClient.Logout()

	log.V(1).Info("Deleting BMC account for User")
	if err := bmcClient.DeleteAccount(ctx, user.Spec.UserName, user.Status.ID); err != nil {
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

// SetupWithManager sets up the controller with the Manager.
func (r *BMCUserReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&baseboardv1alpha1.BMCUser{}).
		Owns(&metalv1alpha1.BMCSecret{}).
		Owns(&baseboardv1alpha1.BMCUserRotation{}).
		Watches(&metalv1alpha1.BMC{}, handler.EnqueueRequestsFromMapFunc(r.findBMCUsersForBMC)).
		Watches(&baseboardv1alpha1.BMCUserSet{}, handler.EnqueueRequestsFromMapFunc(r.findBMCUsersForBMCUserSet)).
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
		if u.Status.ManagedBy != nil && u.Status.ManagedBy.Name == obj.GetName() {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: u.Name},
			})
		}
	}
	return requests
}
