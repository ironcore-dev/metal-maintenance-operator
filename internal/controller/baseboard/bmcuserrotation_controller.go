// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package baseboard

import (
	"context"
	"errors"
	"fmt"
	"time"

	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	baseboardv1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/baseboard/v1alpha1"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	"github.com/ironcore-dev/metal-operator/bmc"
	"github.com/ironcore-dev/metal-operator/pkg/bmcutils"
	"github.com/stmcginnis/gofish/schemas"
)

// BMCUserRotationReconciler executes a single BMCUserRotation work unit.
type BMCUserRotationReconciler struct {
	client.Client
	Scheme             *runtime.Scheme
	DefaultProtocol    metalv1alpha1.ProtocolScheme
	SkipCertValidation bool
	BMCOptions         bmc.Options
	ResyncInterval     time.Duration
}

// +kubebuilder:rbac:groups=baseboard.metal.ironcore.dev,resources=bmcuserrotations,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=baseboard.metal.ironcore.dev,resources=bmcuserrotations/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=baseboard.metal.ironcore.dev,resources=bmcusers,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=baseboard.metal.ironcore.dev,resources=bmcusers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=bmcs,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=bmcsecrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=endpoints,verbs=get;list;watch

func (r *BMCUserRotationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	rotation := &baseboardv1alpha1.BMCUserRotation{}
	if err := r.Get(ctx, req.NamespacedName, rotation); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if rotation.Status.Phase == baseboardv1alpha1.BMCUserRotationPhaseSucceeded ||
		rotation.Status.Phase == baseboardv1alpha1.BMCUserRotationPhaseFailed {
		return ctrl.Result{}, nil
	}
	return r.execute(ctx, rotation)
}

func (r *BMCUserRotationReconciler) execute(ctx context.Context, rotation *baseboardv1alpha1.BMCUserRotation) (ctrl.Result, error) {
	user := &baseboardv1alpha1.BMCUser{}
	if err := r.Get(ctx, client.ObjectKey{Name: rotation.Spec.BMCUserRef.Name}, user); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, r.setFailed(ctx, rotation, "BMCUser not found")
		}
		return ctrl.Result{}, err
	}
	if user.Spec.BMCRef == nil {
		return ctrl.Result{}, r.setFailed(ctx, rotation, "BMCUser has no BMCRef")
	}
	bmcObj := &metalv1alpha1.BMC{}
	if err := r.Get(ctx, client.ObjectKey{Name: user.Spec.BMCRef.Name}, bmcObj); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.setPhase(ctx, rotation, baseboardv1alpha1.BMCUserRotationPhaseInProgress, ""); err != nil {
		return ctrl.Result{}, err
	}

	switch rotation.Spec.Type {
	case baseboardv1alpha1.RotationStrategyDualAccount:
		return r.executeDualAccount(ctx, rotation, user, bmcObj)
	case baseboardv1alpha1.RotationStrategySingleAccount:
		return r.executeSingleAccount(ctx, rotation, user, bmcObj)
	default:
		return ctrl.Result{}, r.setFailed(ctx, rotation, fmt.Sprintf("unknown rotation type %q", rotation.Spec.Type))
	}
}

func (r *BMCUserRotationReconciler) executeDualAccount(ctx context.Context, rotation *baseboardv1alpha1.BMCUserRotation, user *baseboardv1alpha1.BMCUser, bmcObj *metalv1alpha1.BMC) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	peer, peerSecret, requeue, err := r.findPeerWithSecret(ctx, user, bmcObj)
	if err != nil {
		return ctrl.Result{}, err
	}
	if requeue {
		log.Info("Peer OperatorAdmin BMCUser has an active rotation in progress, requeueing")
		return ctrl.Result{RequeueAfter: r.ResyncInterval}, nil
	}
	if peer == nil {
		return ctrl.Result{}, r.setFailed(ctx, rotation, "No peer OperatorAdmin BMCUser found — DualAccount rotation requires two OperatorAdmin BMCUser objects for the same BMC")
	}

	protocolScheme := bmcutils.GetProtocolScheme(bmcObj.Spec.Protocol.Scheme, r.DefaultProtocol)
	address, err := bmcutils.GetBMCAddressForBMC(ctx, r.Client, bmcObj)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to get BMC address: %w", err)
	}
	bmcClient, err := bmcutils.CreateBMCClient(ctx, r.Client, protocolScheme, bmcObj.Spec.Protocol.Name, address, bmcObj.Spec.Protocol.Port, peerSecret, r.BMCOptions, r.SkipCertValidation)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to create BMC client using peer credential: %w", err)
	}
	defer bmcClient.Logout()

	log.V(1).Info("DualAccount rotation: using peer credential", "peer", peer.Name)
	return r.rotateWithClient(ctx, rotation, user, bmcObj, bmcClient)
}

func (r *BMCUserRotationReconciler) executeSingleAccount(ctx context.Context, rotation *baseboardv1alpha1.BMCUserRotation, user *baseboardv1alpha1.BMCUser, bmcObj *metalv1alpha1.BMC) (ctrl.Result, error) {
	bmcClient, err := bmcutils.GetBMCClientFromBMC(ctx, r.Client, bmcObj, r.DefaultProtocol, r.SkipCertValidation, r.BMCOptions)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to get BMC client: %w", err)
	}
	defer bmcClient.Logout()
	return r.rotateWithClient(ctx, rotation, user, bmcObj, bmcClient)
}

func (r *BMCUserRotationReconciler) rotateWithClient(ctx context.Context, rotation *baseboardv1alpha1.BMCUserRotation, user *baseboardv1alpha1.BMCUser, bmcObj *metalv1alpha1.BMC, bmcClient bmc.BMC) (ctrl.Result, error) {
	var secret *metalv1alpha1.BMCSecret
	var newPassword string

	// Reuse a previously generated secret on retries so we never have two
	// outstanding passwords after a partial failure: if NewSecretRef is already
	// set the BMC password was already changed in a prior attempt.
	if rotation.Status.NewSecretRef != nil {
		existing := &metalv1alpha1.BMCSecret{}
		if err := r.Get(ctx, client.ObjectKey{Name: rotation.Status.NewSecretRef.Name}, existing); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to get previously created BMCSecret: %w", err)
		}
		secret = existing
		_, pw, err := bmcutils.GetBMCCredentialsFromSecret(secret)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to read password from existing BMCSecret: %w", err)
		}
		newPassword = pw
	} else {
		accountService, err := bmcClient.GetAccountService()
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to get account service: %w", err)
		}
		newPassword, err = bmc.GenerateSecurePassword(bmc.Manufacturer(bmcObj.Status.Manufacturer), passwordLength(accountService.MaxPasswordLength))
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to generate new password: %w", err)
		}

		newSecret := &metalv1alpha1.BMCSecret{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: user.Name,
			},
			Data: map[string][]byte{
				metalv1alpha1.BMCSecretUsernameKeyName: []byte(user.Spec.UserName),
				metalv1alpha1.BMCSecretPasswordKeyName: []byte(newPassword),
			},
			Immutable: new(true),
		}
		if err := controllerutil.SetControllerReference(user, newSecret, r.Scheme); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to set controller reference on BMCSecret: %w", err)
		}
		if err := r.Create(ctx, newSecret); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to create BMCSecret: %w", err)
		}
		secret = newSecret

		// Persist the new secret ref before changing the BMC password so that
		// a retry can reuse it instead of generating yet another credential.
		rotBase := rotation.DeepCopy()
		rotation.Status.NewSecretRef = &v1.LocalObjectReference{Name: secret.Name}
		if err := r.Status().Patch(ctx, rotation, client.MergeFrom(rotBase)); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to persist NewSecretRef: %w", err)
		}
	}

	if err := bmcClient.CreateOrUpdateAccount(ctx, user.Spec.UserName, user.Spec.RoleID, newPassword, true); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to update BMC account password: %w", err)
	}

	invalidCredentials, err := r.bmcConnectionTest(ctx, secret, bmcObj)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to test new credentials: %w", err)
	}
	if invalidCredentials {
		return ctrl.Result{}, r.setFailed(ctx, rotation, "New password rejected by BMC during connection test")
	}

	if err := r.promoteCredential(ctx, user, secret, bmcObj); err != nil {
		return ctrl.Result{}, err
	}

	now := metav1.Now()
	rotBase := rotation.DeepCopy()
	rotation.Status.Phase = baseboardv1alpha1.BMCUserRotationPhaseSucceeded
	rotation.Status.CompletedAt = &now
	rotation.Status.Message = ""
	if err := r.Status().Patch(ctx, rotation, client.MergeFrom(rotBase)); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to set rotation Succeeded: %w", err)
	}
	return ctrl.Result{}, nil
}

// promoteCredential updates BMCUser spec+status and, for OperatorAdmin type, also BMC.Spec.BMCSecretRef.
func (r *BMCUserRotationReconciler) promoteCredential(ctx context.Context, user *baseboardv1alpha1.BMCUser, secret *metalv1alpha1.BMCSecret, bmcObj *metalv1alpha1.BMC) error {
	userBase := user.DeepCopy()
	user.Spec.BMCSecretRef = &v1.LocalObjectReference{Name: secret.Name}
	if err := r.Patch(ctx, user, client.MergeFrom(userBase)); err != nil {
		return fmt.Errorf("failed to patch BMCUser.Spec.BMCSecretRef: %w", err)
	}

	now := metav1.Now()
	userBase = user.DeepCopy()
	user.Status.EffectiveBMCSecretRef = &v1.LocalObjectReference{Name: secret.Name}
	user.Status.LastRotation = &now
	if err := r.Status().Patch(ctx, user, client.MergeFrom(userBase)); err != nil {
		return fmt.Errorf("failed to patch BMCUser status: %w", err)
	}

	if user.Spec.Type == baseboardv1alpha1.BMCUserTypeOperatorAdmin && bmcObj.Spec.BMCSecretRef.Name != secret.Name {
		log := ctrl.LoggerFrom(ctx)
		log.Info("Promoting credential to BMC.Spec.BMCSecretRef", "BMCUser", user.Name, "NewSecret", secret.Name)
		bmcBase := bmcObj.DeepCopy()
		bmcObj.Spec.BMCSecretRef = v1.LocalObjectReference{Name: secret.Name}
		if err := r.Patch(ctx, bmcObj, client.MergeFrom(bmcBase)); err != nil {
			return fmt.Errorf("failed to patch BMC.Spec.BMCSecretRef: %w", err)
		}
	}
	return nil
}

// findPeerWithSecret returns the peer BMCUser and its proven BMCSecret for DualAccount rotation.
// requeue is true when a suitable peer exists but has an active rotation in progress —
// the caller should wait rather than fail.
func (r *BMCUserRotationReconciler) findPeerWithSecret(ctx context.Context, user *baseboardv1alpha1.BMCUser, _ *metalv1alpha1.BMC) (peer *baseboardv1alpha1.BMCUser, peerSecret *metalv1alpha1.BMCSecret, requeue bool, err error) {
	userList := &baseboardv1alpha1.BMCUserList{}
	if err := r.List(ctx, userList); err != nil {
		return nil, nil, false, fmt.Errorf("failed to list BMCUsers: %w", err)
	}
	rotationList := &baseboardv1alpha1.BMCUserRotationList{}
	if err := r.List(ctx, rotationList); err != nil {
		return nil, nil, false, fmt.Errorf("failed to list BMCUserRotations: %w", err)
	}
	// Build a set of BMCUser names that have an active (non-terminal) rotation.
	activePeers := make(map[string]bool)
	for _, rot := range rotationList.Items {
		if rot.Status.Phase != baseboardv1alpha1.BMCUserRotationPhaseSucceeded &&
			rot.Status.Phase != baseboardv1alpha1.BMCUserRotationPhaseFailed {
			activePeers[rot.Spec.BMCUserRef.Name] = true
		}
	}

	for i := range userList.Items {
		candidate := &userList.Items[i]
		if candidate.Name == user.Name {
			continue
		}
		if candidate.Spec.Type != baseboardv1alpha1.BMCUserTypeOperatorAdmin {
			continue
		}
		if candidate.Spec.BMCRef == nil || candidate.Spec.BMCRef.Name != user.Spec.BMCRef.Name {
			continue
		}
		if candidate.Status.EffectiveBMCSecretRef == nil {
			continue
		}
		// If the peer is already rotating, signal a requeue rather than failing.
		if activePeers[candidate.Name] {
			return nil, nil, true, nil
		}
		secret := &metalv1alpha1.BMCSecret{}
		if err := r.Get(ctx, client.ObjectKey{Name: candidate.Status.EffectiveBMCSecretRef.Name}, secret); err != nil {
			continue
		}
		return candidate, secret, false, nil
	}
	return nil, nil, false, nil
}

func (r *BMCUserRotationReconciler) setFailed(ctx context.Context, rotation *baseboardv1alpha1.BMCUserRotation, msg string) error {
	now := metav1.Now()
	base := rotation.DeepCopy()
	rotation.Status.Phase = baseboardv1alpha1.BMCUserRotationPhaseFailed
	rotation.Status.CompletedAt = &now
	rotation.Status.Message = msg
	if err := r.Status().Patch(ctx, rotation, client.MergeFrom(base)); err != nil {
		return fmt.Errorf("failed to set rotation Failed: %w", err)
	}
	return nil
}

func (r *BMCUserRotationReconciler) setPhase(ctx context.Context, rotation *baseboardv1alpha1.BMCUserRotation, phase baseboardv1alpha1.BMCUserRotationPhase, msg string) error {
	base := rotation.DeepCopy()
	rotation.Status.Phase = phase
	rotation.Status.Message = msg
	if err := r.Status().Patch(ctx, rotation, client.MergeFrom(base)); err != nil {
		return fmt.Errorf("failed to set rotation phase %s: %w", phase, err)
	}
	return nil
}

func (r *BMCUserRotationReconciler) bmcConnectionTest(ctx context.Context, secret *metalv1alpha1.BMCSecret, bmcObj *metalv1alpha1.BMC) (bool, error) {
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
			if httpErr, ok := errors.AsType[*schemas.Error](err); ok && (httpErr.HTTPReturnedStatusCode == 401 || httpErr.HTTPReturnedStatusCode == 403) {
				return true, nil
			}
			return false, fmt.Errorf("failed to verify BMC credentials: %w", err)
		}
	}
	return false, nil
}

func (r *BMCUserRotationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&baseboardv1alpha1.BMCUserRotation{}).
		Named("bmcuserrotation").
		Complete(r)
}
