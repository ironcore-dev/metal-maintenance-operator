// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package baseboard

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"text/template"
	"time"

	"github.com/ironcore-dev/controller-utils/clientutils"
	baseboardv1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/baseboard/v1alpha1"
	utils "github.com/ironcore-dev/metal-maintenance-operator/internal/utils"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	"github.com/ironcore-dev/metal-operator/bmc"
	"github.com/ironcore-dev/metal-operator/pkg/bmcutils"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	bmcUserSetFinalizer = "baseboard.metal.ironcore.dev/bmcuserset"

	// bmcUserSetNameLabel is stamped on every BMCUser owned by a BMCUserSet so
	// the list call in getOwnedBMCUsers can be scoped to this set's children
	// rather than scanning all BMCUser objects cluster-wide.
	bmcUserSetNameLabel = "baseboard.metal.ironcore.dev/bmcuserset-name"

	conditionTypeOperatorCredentialReady = "OperatorCredentialReady"
	conditionTypeBootstrapPending        = "BootstrapPending"

	reasonAllProven        = "AllAccountsProven"
	reasonBootstrapPending = "BootstrapPending"

	// credentialRoleLabel is the label key stamped on BMCSecrets to identify their role.
	credentialRoleLabel = "baseboard.metal.ironcore.dev/credential-role"
	// credentialRoleBreakGlass is the label value for a retained factory credential.
	credentialRoleBreakGlass = "break-glass"
)

// BMCUserSetReconciler reconciles a BMCUserSet object.
type BMCUserSetReconciler struct {
	client.Client
	Scheme             *runtime.Scheme
	ManagerNamespace   string
	ResyncInterval     time.Duration
	DefaultProtocol    metalv1alpha1.ProtocolScheme
	SkipCertValidation bool
	BMCOptions         bmc.Options
}

// +kubebuilder:rbac:groups=baseboard.metal.ironcore.dev,resources=bmcusersets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=baseboard.metal.ironcore.dev,resources=bmcusersets/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=baseboard.metal.ironcore.dev,resources=bmcusersets/finalizers,verbs=update
// +kubebuilder:rbac:groups=baseboard.metal.ironcore.dev,resources=bmcusers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=baseboard.metal.ironcore.dev,resources=bmcusers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=bmcs,verbs=get;list;watch
// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=servers,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=servers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=endpoints,verbs=get;list;watch
// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=bmcsecrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch

func (r *BMCUserSetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	set := &baseboardv1alpha1.BMCUserSet{}
	if err := r.Get(ctx, req.NamespacedName, set); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	return r.reconcileExists(ctx, set)
}

func (r *BMCUserSetReconciler) reconcileExists(ctx context.Context, set *baseboardv1alpha1.BMCUserSet) (ctrl.Result, error) {
	if !set.DeletionTimestamp.IsZero() {
		return r.delete(ctx, set)
	}
	return r.reconcile(ctx, set)
}

func (r *BMCUserSetReconciler) reconcile(ctx context.Context, set *baseboardv1alpha1.BMCUserSet) (ctrl.Result, error) {
	if utils.ShouldIgnoreReconciliation(set) {
		return ctrl.Result{}, nil
	}
	if modified, err := clientutils.PatchEnsureFinalizer(ctx, r.Client, set, bmcUserSetFinalizer); err != nil || modified {
		return ctrl.Result{}, err
	}

	bmcList, err := r.getBMCsBySelector(ctx, set)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to list BMCs by selector: %w", err)
	}

	ownedUsers, err := r.getOwnedBMCUsers(ctx, set)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to list owned BMCUsers: %w", err)
	}

	if err := r.ensureBMCUsers(ctx, set, bmcList.Items, ownedUsers.Items); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to ensure BMCUsers: %w", err)
	}

	// Re-fetch after ensureBMCUsers so newly-created BMCUsers are included in
	// the status calculation and stable-secret/condition passes below.
	ownedUsers, err = r.getOwnedBMCUsers(ctx, set)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to re-list owned BMCUsers after ensure: %w", err)
	}

	if err := r.handleStableSecrets(ctx, set, bmcList.Items, ownedUsers.Items); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to handle stable secrets: %w", err)
	}

	if err := r.handleOperatorCredentialReady(ctx, set, bmcList.Items, ownedUsers.Items); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to update OperatorCredentialReady condition: %w", err)
	}

	if err := r.handleFactoryAccount(ctx, set, bmcList.Items, ownedUsers.Items); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to handle factory account: %w", err)
	}

	if err := r.deleteOrphanedBMCUsers(ctx, bmcList.Items, ownedUsers.Items); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to delete orphaned BMCUsers: %w", err)
	}

	if err := r.updateSetStatus(ctx, set, bmcList.Items, ownedUsers.Items); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to update BMCUserSet status: %w", err)
	}

	return ctrl.Result{RequeueAfter: r.ResyncInterval}, nil
}

func (r *BMCUserSetReconciler) delete(ctx context.Context, set *baseboardv1alpha1.BMCUserSet) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(set, bmcUserSetFinalizer) {
		return ctrl.Result{}, nil
	}
	ownedUsers, err := r.getOwnedBMCUsers(ctx, set)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to list owned BMCUsers: %w", err)
	}
	if len(ownedUsers.Items) > 0 {
		var errs []error
		for i := range ownedUsers.Items {
			if err := r.Delete(ctx, &ownedUsers.Items[i]); client.IgnoreNotFound(err) != nil {
				errs = append(errs, err)
			}
		}
		if err := errors.Join(errs...); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: r.ResyncInterval}, nil
	}
	if modified, err := clientutils.PatchEnsureNoFinalizer(ctx, r.Client, set, bmcUserSetFinalizer); err != nil || modified {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *BMCUserSetReconciler) getBMCsBySelector(ctx context.Context, set *baseboardv1alpha1.BMCUserSet) (*metalv1alpha1.BMCList, error) {
	selector, err := metav1.LabelSelectorAsSelector(&set.Spec.BMCSelector)
	if err != nil {
		return nil, err
	}
	bmcList := &metalv1alpha1.BMCList{}
	if err := r.List(ctx, bmcList, client.MatchingLabelsSelector{Selector: selector}); err != nil {
		return nil, err
	}
	return bmcList, nil
}

func (r *BMCUserSetReconciler) getOwnedBMCUsers(ctx context.Context, set *baseboardv1alpha1.BMCUserSet) (*baseboardv1alpha1.BMCUserList, error) {
	userList := &baseboardv1alpha1.BMCUserList{}
	if err := clientutils.ListAndFilterControlledBy(ctx, r.Client, set, userList,
		client.MatchingLabels{bmcUserSetNameLabel: set.Name},
	); err != nil {
		return nil, err
	}
	return userList, nil
}

func (r *BMCUserSetReconciler) ensureBMCUsers(
	ctx context.Context,
	set *baseboardv1alpha1.BMCUserSet,
	bmcs []metalv1alpha1.BMC,
	ownedUsers []baseboardv1alpha1.BMCUser,
) error {
	log := ctrl.LoggerFrom(ctx)
	existingByName := make(map[string]struct{}, len(ownedUsers))
	for _, u := range ownedUsers {
		existingByName[u.Name] = struct{}{}
	}

	userType := baseboardv1alpha1.BMCUserTypeServiceAccount
	if set.Spec.RotationStrategy == baseboardv1alpha1.RotationStrategyDualAccount {
		userType = baseboardv1alpha1.BMCUserTypeOperatorAdmin
	}

	var errs []error
	for _, bmcObj := range bmcs {
		var desired []struct {
			name     string
			userName string
		}

		base := utils.VersionSetChildName(set.Name, bmcObj.Name)
		if set.Spec.RotationStrategy == baseboardv1alpha1.RotationStrategySingleAccount {
			desired = []struct {
				name     string
				userName string
			}{
				{name: base, userName: set.Spec.Template.UserName},
			}
		} else {
			desired = []struct {
				name     string
				userName string
			}{
				{name: base + "-a", userName: set.Spec.Template.UserName + "-a"},
				{name: base + "-b", userName: set.Spec.Template.UserName + "-b"},
			}
		}

		for _, d := range desired {
			user := &baseboardv1alpha1.BMCUser{
				ObjectMeta: metav1.ObjectMeta{Name: d.name},
			}
			opResult, err := controllerutil.CreateOrPatch(ctx, r.Client, user, func() error {
				if user.Labels == nil {
					user.Labels = make(map[string]string)
				}
				user.Labels[bmcUserSetNameLabel] = set.Name
				user.Spec.UserName = d.userName
				user.Spec.RoleID = set.Spec.Template.RoleID
				user.Spec.RotationPeriod = set.Spec.Template.RotationPeriod
				user.Spec.RotationHistoryLimit = set.Spec.Template.RotationHistoryLimit
				user.Spec.Type = userType
				user.Spec.BMCRef = &corev1.LocalObjectReference{Name: bmcObj.Name}
				// Stamp DeletionPolicy from the template. When the template is
				// empty, default to Retain for OperatorAdmin, Delete otherwise.
				if set.Spec.Template.DeletionPolicy != "" {
					user.Spec.DeletionPolicy = set.Spec.Template.DeletionPolicy
				} else if userType == baseboardv1alpha1.BMCUserTypeOperatorAdmin {
					user.Spec.DeletionPolicy = "Retain"
				} else {
					user.Spec.DeletionPolicy = "Delete"
				}
				return controllerutil.SetControllerReference(set, user, r.Scheme)
			})
			if err != nil {
				errs = append(errs, fmt.Errorf("failed to create/patch BMCUser %s: %w", d.name, err))
				continue
			}
			if opResult != controllerutil.OperationResultNone {
				log.Info("Reconciled BMCUser", "BMCUser", d.name, "Operation", opResult)
			}
			if user.Status.ManagedBy == nil {
				userBase := user.DeepCopy()
				user.Status.ManagedBy = &corev1.LocalObjectReference{Name: set.Name}
				if err := r.Status().Patch(ctx, user, client.MergeFrom(userBase)); err != nil {
					errs = append(errs, fmt.Errorf("failed to set ManagedBy on BMCUser %s: %w", d.name, err))
					continue
				}
			}
			// Record the factory secret once, before -a can promote a new credential
			// into BMC.Spec.BMCSecretRef. The peer (-b) needs this to bootstrap.
			if user.Status.BootstrapSecretRef == nil && bmcObj.Spec.BMCSecretRef.Name != "" {
				userBase := user.DeepCopy()
				user.Status.BootstrapSecretRef = &corev1.LocalObjectReference{Name: bmcObj.Spec.BMCSecretRef.Name}
				if err := r.Status().Patch(ctx, user, client.MergeFrom(userBase)); err != nil {
					errs = append(errs, fmt.Errorf("failed to set BootstrapSecretRef on BMCUser %s: %w", d.name, err))
					continue
				}
			}
		}
	}
	return errors.Join(errs...)
}

func (r *BMCUserSetReconciler) handleStableSecrets(
	ctx context.Context,
	set *baseboardv1alpha1.BMCUserSet,
	bmcs []metalv1alpha1.BMC,
	ownedUsers []baseboardv1alpha1.BMCUser,
) error {
	if set.Spec.Template.CredentialSecretNameTemplate == "" {
		return nil
	}
	ns := set.Spec.Template.CredentialSecretNamespace
	if ns == "" {
		ns = r.ManagerNamespace
	}

	tmpl, err := template.New("secretName").Parse(set.Spec.Template.CredentialSecretNameTemplate)
	if err != nil {
		return fmt.Errorf("failed to parse CredentialSecretNameTemplate: %w", err)
	}

	// Build a map of BMC name → the secret name currently in BMC.Spec.BMCSecretRef.
	// Used below to skip OperatorAdmin users that are not currently the active credential.
	activeBMCSecret := make(map[string]string, len(bmcs))
	for _, b := range bmcs {
		activeBMCSecret[b.Name] = b.Spec.BMCSecretRef.Name
	}

	log := ctrl.LoggerFrom(ctx)
	// For DualAccount, track which stable secret has already been written for a
	// given BMC in this reconcile pass — only write once per BMC (from whichever
	// user is currently effective).
	writtenForBMC := make(map[string]bool)

	var errs []error
	for _, user := range ownedUsers {
		if user.Status.EffectiveBMCSecretRef == nil || user.Spec.BMCRef == nil {
			continue
		}
		bmcName := user.Spec.BMCRef.Name
		if writtenForBMC[bmcName] {
			continue
		}

		// For OperatorAdmin users (DualAccount), only the account whose
		// EffectiveBMCSecretRef matches the BMC's current Spec.BMCSecretRef is the
		// live credential. Skip the inactive peer to avoid overwriting the stable
		// secret with stale credentials.
		if user.Spec.Type == baseboardv1alpha1.BMCUserTypeOperatorAdmin {
			if activeBMCSecret[bmcName] != user.Status.EffectiveBMCSecretRef.Name {
				continue
			}
		}

		var nameBuf bytes.Buffer
		if err := tmpl.Execute(&nameBuf, struct{ BMCName string }{BMCName: bmcName}); err != nil {
			errs = append(errs, fmt.Errorf("failed to render secret name for BMC %s: %w", bmcName, err))
			continue
		}
		secretName := nameBuf.String()

		bmcSecret := &metalv1alpha1.BMCSecret{}
		if err := r.Get(ctx, client.ObjectKey{Name: user.Status.EffectiveBMCSecretRef.Name}, bmcSecret); err != nil {
			errs = append(errs, fmt.Errorf("failed to get BMCSecret %s: %w", user.Status.EffectiveBMCSecretRef.Name, err))
			continue
		}

		stableSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: ns},
		}
		opResult, err := controllerutil.CreateOrPatch(ctx, r.Client, stableSecret, func() error {
			if stableSecret.Data == nil {
				stableSecret.Data = make(map[string][]byte)
			}
			stableSecret.Data["username"] = bmcSecret.Data[metalv1alpha1.BMCSecretUsernameKeyName]
			stableSecret.Data["password"] = bmcSecret.Data[metalv1alpha1.BMCSecretPasswordKeyName]
			return nil
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("failed to create/update stable secret %s/%s: %w", ns, secretName, err))
			continue
		}
		if opResult != controllerutil.OperationResultNone {
			log.Info("Updated stable credential secret", "Secret", secretName, "Namespace", ns, "Operation", opResult)
		}
		writtenForBMC[bmcName] = true
	}
	return errors.Join(errs...)
}

func (r *BMCUserSetReconciler) handleOperatorCredentialReady(
	ctx context.Context,
	set *baseboardv1alpha1.BMCUserSet,
	bmcs []metalv1alpha1.BMC,
	ownedUsers []baseboardv1alpha1.BMCUser,
) error {
	usersByBMC := make(map[string][]baseboardv1alpha1.BMCUser)
	for _, u := range ownedUsers {
		if u.Spec.BMCRef != nil {
			usersByBMC[u.Spec.BMCRef.Name] = append(usersByBMC[u.Spec.BMCRef.Name], u)
		}
	}

	allReady := len(bmcs) > 0
	for _, bmcObj := range bmcs {
		users := usersByBMC[bmcObj.Name]
		if len(users) == 0 {
			allReady = false
			break
		}
		for _, u := range users {
			if u.Status.EffectiveBMCSecretRef == nil {
				allReady = false
				break
			}
		}
		if !allReady {
			break
		}
	}

	setBase := set.DeepCopy()
	if allReady {
		apimeta.SetStatusCondition(&set.Status.Conditions, metav1.Condition{
			Type:               conditionTypeOperatorCredentialReady,
			Status:             metav1.ConditionTrue,
			Reason:             reasonAllProven,
			Message:            "All operator BMC accounts have proven credentials",
			ObservedGeneration: set.Generation,
		})
		apimeta.RemoveStatusCondition(&set.Status.Conditions, conditionTypeBootstrapPending)
	} else {
		apimeta.SetStatusCondition(&set.Status.Conditions, metav1.Condition{
			Type:               conditionTypeOperatorCredentialReady,
			Status:             metav1.ConditionFalse,
			Reason:             reasonBootstrapPending,
			Message:            "Waiting for all operator BMC accounts to be bootstrapped",
			ObservedGeneration: set.Generation,
		})
		apimeta.SetStatusCondition(&set.Status.Conditions, metav1.Condition{
			Type:               conditionTypeBootstrapPending,
			Status:             metav1.ConditionTrue,
			Reason:             reasonBootstrapPending,
			Message:            "Bootstrap pending: operator accounts not yet proven on all BMCs",
			ObservedGeneration: set.Generation,
		})
	}
	if err := r.Status().Patch(ctx, set, client.MergeFrom(setBase)); err != nil {
		return fmt.Errorf("failed to patch BMCUserSet conditions: %w", err)
	}
	return nil
}

func (r *BMCUserSetReconciler) deleteOrphanedBMCUsers(
	ctx context.Context,
	bmcs []metalv1alpha1.BMC,
	ownedUsers []baseboardv1alpha1.BMCUser,
) error {
	log := ctrl.LoggerFrom(ctx)
	bmcNames := make(map[string]struct{}, len(bmcs))
	for _, b := range bmcs {
		bmcNames[b.Name] = struct{}{}
	}
	// Build a map of BMC name → BMC for live-credential check.
	bmcByName := make(map[string]*metalv1alpha1.BMC, len(bmcs))
	for i := range bmcs {
		bmcByName[bmcs[i].Name] = &bmcs[i]
	}
	var errs []error
	for i := range ownedUsers {
		u := &ownedUsers[i]
		if u.Spec.BMCRef == nil {
			continue
		}
		if _, ok := bmcNames[u.Spec.BMCRef.Name]; !ok {
			// Guard: do not delete if this user's credential is still live.
			if bmcObj, exists := bmcByName[u.Spec.BMCRef.Name]; exists {
				secretList := &metalv1alpha1.BMCSecretList{}
				if err := r.List(ctx, secretList); err == nil {
					for _, secret := range secretList.Items {
						if metav1.IsControlledBy(&secret, u) && secret.Name == bmcObj.Spec.BMCSecretRef.Name {
							log.Info("Skipping deletion of orphaned BMCUser: its credential is still referenced by BMC.Spec.BMCSecretRef",
								"BMCUser", u.Name, "BMC", u.Spec.BMCRef.Name)
							goto skipDelete
						}
					}
				}
			}
			if err := r.Delete(ctx, u); client.IgnoreNotFound(err) != nil {
				errs = append(errs, fmt.Errorf("failed to delete orphaned BMCUser %s: %w", u.Name, err))
			}
		skipDelete:
		}
	}
	return errors.Join(errs...)
}

func (r *BMCUserSetReconciler) updateSetStatus(
	ctx context.Context,
	set *baseboardv1alpha1.BMCUserSet,
	bmcs []metalv1alpha1.BMC,
	ownedUsers []baseboardv1alpha1.BMCUser,
) error {
	usersByBMC := make(map[string][]baseboardv1alpha1.BMCUser)
	for _, u := range ownedUsers {
		if u.Spec.BMCRef != nil {
			usersByBMC[u.Spec.BMCRef.Name] = append(usersByBMC[u.Spec.BMCRef.Name], u)
		}
	}

	var bootstrapped, pending int32
	for _, bmcObj := range bmcs {
		users := usersByBMC[bmcObj.Name]
		allProven := len(users) > 0
		for _, u := range users {
			if u.Status.EffectiveBMCSecretRef == nil {
				allProven = false
				break
			}
		}
		// When FactoryAccount is configured, also require the factory account to have
		// been handled before counting the BMC as fully bootstrapped.
		if allProven && set.Spec.FactoryAccount != nil {
			factoryHandled := false
			for _, u := range users {
				if u.Spec.Type == baseboardv1alpha1.BMCUserTypeOperatorAdmin && u.Status.FactoryAccountHandled {
					factoryHandled = true
					break
				}
			}
			if !factoryHandled {
				allProven = false
			}
		}
		if allProven {
			bootstrapped++
		} else {
			pending++
		}
	}

	setBase := set.DeepCopy()
	set.Status.TotalBMCs = int32(len(bmcs))
	set.Status.BootstrappedBMCs = bootstrapped
	set.Status.PendingBMCs = pending
	if err := r.Status().Patch(ctx, set, client.MergeFrom(setBase)); err != nil {
		return fmt.Errorf("failed to patch BMCUserSet status: %w", err)
	}
	return nil
}

func (r *BMCUserSetReconciler) enqueueByBMC(ctx context.Context, obj client.Object) []reconcile.Request {
	log := ctrl.LoggerFrom(ctx)
	bmcObj := obj.(*metalv1alpha1.BMC)

	setList := &baseboardv1alpha1.BMCUserSetList{}
	if err := r.List(ctx, setList); err != nil {
		log.Error(err, "Failed to list BMCUserSets")
		return nil
	}
	var reqs []reconcile.Request
	for _, set := range setList.Items {
		selector, err := metav1.LabelSelectorAsSelector(&set.Spec.BMCSelector)
		if err != nil {
			continue
		}
		if selector.Matches(labels.Set(bmcObj.GetLabels())) {
			reqs = append(reqs, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: set.Name},
			})
		}
	}
	return reqs
}

func (r *BMCUserSetReconciler) enqueueByBMCUser(ctx context.Context, obj client.Object) []reconcile.Request {
	for _, ref := range obj.GetOwnerReferences() {
		if ref.Kind == "BMCUserSet" {
			return []reconcile.Request{
				{NamespacedName: types.NamespacedName{Name: ref.Name}},
			}
		}
	}
	return nil
}

// enqueueByBMCUserRotation re-enqueues the owning BMCUserSet when a
// FactoryBootstrap BMCUserRotation changes phase (so the set controller can
// label/delete the resulting BMCSecret and mark FactoryAccountHandled).
func (r *BMCUserSetReconciler) enqueueByBMCUserRotation(ctx context.Context, obj client.Object) []reconcile.Request {
	rot, ok := obj.(*baseboardv1alpha1.BMCUserRotation)
	if !ok || rot.Spec.TriggerReason != baseboardv1alpha1.BMCUserRotationTriggerFactoryBootstrap {
		return nil
	}
	// The rotation is owned by the active BMCUser; find the BMCUserSet above it.
	for _, ref := range rot.GetOwnerReferences() {
		if ref.Kind != "BMCUser" {
			continue
		}
		user := &baseboardv1alpha1.BMCUser{}
		if err := r.Get(ctx, client.ObjectKey{Name: ref.Name}, user); err != nil {
			return nil
		}
		if user.Status.ManagedBy != nil {
			return []reconcile.Request{
				{NamespacedName: types.NamespacedName{Name: user.Status.ManagedBy.Name}},
			}
		}
	}
	return nil
}

// handleFactoryAccount handles the factory/default account for each BMC in the set,
// once both OperatorAdmin users are proven and BMC.Spec.BMCSecretRef is operator-owned.
// It creates a FactoryBootstrap BMCUserRotation, then on its success labels/deletes the
// resulting BMCSecret and marks the active OperatorAdmin user's FactoryAccountHandled=true.
func (r *BMCUserSetReconciler) handleFactoryAccount(
	ctx context.Context,
	set *baseboardv1alpha1.BMCUserSet,
	bmcs []metalv1alpha1.BMC,
	ownedUsers []baseboardv1alpha1.BMCUser,
) error {
	if set.Spec.FactoryAccount == nil {
		return nil
	}
	log := ctrl.LoggerFrom(ctx)

	usersByBMC := make(map[string][]baseboardv1alpha1.BMCUser)
	for _, u := range ownedUsers {
		if u.Spec.BMCRef != nil {
			usersByBMC[u.Spec.BMCRef.Name] = append(usersByBMC[u.Spec.BMCRef.Name], u)
		}
	}

	var errs []error
	for _, bmcObj := range bmcs {
		// Skip vendors in the ignore list.
		ignored := false
		for _, v := range set.Spec.FactoryAccount.IgnoreVendors {
			if strings.EqualFold(v, bmcObj.Status.Manufacturer) {
				ignored = true
				break
			}
		}
		if ignored {
			continue
		}

		users := usersByBMC[bmcObj.Name]

		// Find the active OperatorAdmin user: EffectiveBMCSecretRef == BMC.Spec.BMCSecretRef.
		var activeAdmin *baseboardv1alpha1.BMCUser
		for i := range users {
			u := &users[i]
			if u.Spec.Type != baseboardv1alpha1.BMCUserTypeOperatorAdmin {
				continue
			}
			if u.Status.EffectiveBMCSecretRef == nil {
				continue
			}
			if u.Status.EffectiveBMCSecretRef.Name == bmcObj.Spec.BMCSecretRef.Name {
				activeAdmin = u
				break
			}
		}
		if activeAdmin == nil {
			// Not all admins proven or BMC not yet promoted — wait.
			continue
		}

		// Gate: all OperatorAdmin users for this BMC must have EffectiveBMCSecretRef.
		allProven := true
		for _, u := range users {
			if u.Spec.Type == baseboardv1alpha1.BMCUserTypeOperatorAdmin && u.Status.EffectiveBMCSecretRef == nil {
				allProven = false
				break
			}
		}
		if !allProven {
			continue
		}

		// Already handled.
		if activeAdmin.Status.FactoryAccountHandled {
			continue
		}

		// Check for an existing in-flight or completed FactoryBootstrap rotation.
		rotationList := &baseboardv1alpha1.BMCUserRotationList{}
		if err := r.List(ctx, rotationList); err != nil {
			errs = append(errs, fmt.Errorf("BMC %s: failed to list BMCUserRotations: %w", bmcObj.Name, err))
			continue
		}
		var factoryRotation *baseboardv1alpha1.BMCUserRotation
		for i := range rotationList.Items {
			rot := &rotationList.Items[i]
			if rot.Spec.BMCUserRef.Name == activeAdmin.Name &&
				rot.Spec.TriggerReason == baseboardv1alpha1.BMCUserRotationTriggerFactoryBootstrap {
				factoryRotation = rot
				break
			}
		}

		if factoryRotation == nil {
			// Determine factory username(s).
			factoryUserNames := set.Spec.FactoryAccount.UserNames
			if len(factoryUserNames) == 0 && activeAdmin.Status.BootstrapSecretRef != nil {
				bootstrapSecret := &metalv1alpha1.BMCSecret{}
				if err := r.Get(ctx, client.ObjectKey{Name: activeAdmin.Status.BootstrapSecretRef.Name}, bootstrapSecret); err != nil {
					errs = append(errs, fmt.Errorf("BMC %s: failed to get bootstrap BMCSecret: %w", bmcObj.Name, err))
					continue
				}
				username, _, err := bmcutils.GetBMCCredentialsFromSecret(bootstrapSecret)
				if err != nil {
					errs = append(errs, fmt.Errorf("BMC %s: failed to read bootstrap credentials: %w", bmcObj.Name, err))
					continue
				}
				factoryUserNames = []string{username}
			}
			// Misconfiguration guard: skip if factory username matches any operator username.
			operatorNames := make(map[string]bool, len(users))
			for _, u := range users {
				operatorNames[u.Spec.UserName] = true
			}
			overlap := false
			for _, fu := range factoryUserNames {
				if operatorNames[fu] {
					log.Info("Factory username matches an operator username; skipping factory rotation",
						"BMC", bmcObj.Name, "FactoryUser", fu)
					overlap = true
				}
			}
			if overlap {
				continue
			}
			rotation := &baseboardv1alpha1.BMCUserRotation{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: activeAdmin.Name + "-factory-",
				},
				Spec: baseboardv1alpha1.BMCUserRotationSpec{
					BMCUserRef:    corev1.LocalObjectReference{Name: activeAdmin.Name},
					Type:          baseboardv1alpha1.RotationStrategySingleAccount,
					TriggeredAt:   metav1.Now(),
					TriggerReason: baseboardv1alpha1.BMCUserRotationTriggerFactoryBootstrap,
				},
			}
			if err := controllerutil.SetControllerReference(activeAdmin, rotation, r.Scheme); err != nil {
				errs = append(errs, fmt.Errorf("BMC %s: failed to set controller reference on factory rotation: %w", bmcObj.Name, err))
				continue
			}
			if err := r.Create(ctx, rotation); err != nil {
				errs = append(errs, fmt.Errorf("BMC %s: failed to create factory BMCUserRotation: %w", bmcObj.Name, err))
				continue
			}
			log.Info("Created FactoryBootstrap BMCUserRotation", "BMC", bmcObj.Name, "Rotation", rotation.Name)
			continue
		}

		// Rotation exists — check its phase.
		if factoryRotation.Status.Phase != baseboardv1alpha1.BMCUserRotationPhaseSucceeded {
			// Still in progress or failed — wait.
			continue
		}

		// Rotation succeeded. Handle credential retention.
		if factoryRotation.Status.NewSecretRef != nil {
			newSecret := &metalv1alpha1.BMCSecret{}
			if err := r.Get(ctx, client.ObjectKey{Name: factoryRotation.Status.NewSecretRef.Name}, newSecret); err == nil {
				retainCredential := set.Spec.FactoryAccount.RetainCredential == nil || *set.Spec.FactoryAccount.RetainCredential
				if retainCredential {
					if newSecret.Labels == nil || newSecret.Labels[credentialRoleLabel] != credentialRoleBreakGlass {
						secretBase := newSecret.DeepCopy()
						if newSecret.Labels == nil {
							newSecret.Labels = make(map[string]string)
						}
						newSecret.Labels[credentialRoleLabel] = credentialRoleBreakGlass
						if err := r.Patch(ctx, newSecret, client.MergeFrom(secretBase)); err != nil {
							errs = append(errs, fmt.Errorf("BMC %s: failed to label break-glass BMCSecret: %w", bmcObj.Name, err))
							continue
						}
					}
				} else {
					if err := r.Delete(ctx, newSecret); client.IgnoreNotFound(err) != nil {
						errs = append(errs, fmt.Errorf("BMC %s: failed to delete factory rotation BMCSecret: %w", bmcObj.Name, err))
						continue
					}
				}
			}
		}

		// Delete the original factory BMCSecret if it is no longer referenced.
		if activeAdmin.Status.BootstrapSecretRef != nil {
			bootstrapSecretName := activeAdmin.Status.BootstrapSecretRef.Name
			inUse := bmcObj.Spec.BMCSecretRef.Name == bootstrapSecretName
			if !inUse {
				for i := range rotationList.Items {
					rot := &rotationList.Items[i]
					if rot.Status.NewSecretRef != nil && rot.Status.NewSecretRef.Name == bootstrapSecretName {
						inUse = true
						break
					}
				}
			}
			if !inUse {
				origSecret := &metalv1alpha1.BMCSecret{}
				if err := r.Get(ctx, client.ObjectKey{Name: bootstrapSecretName}, origSecret); err == nil {
					if err := r.Delete(ctx, origSecret); client.IgnoreNotFound(err) != nil {
						errs = append(errs, fmt.Errorf("BMC %s: failed to delete original factory BMCSecret: %w", bmcObj.Name, err))
						continue
					}
					log.Info("Deleted original factory BMCSecret", "BMC", bmcObj.Name, "Secret", bootstrapSecretName)
				}
			}
		}

		// Mark factory account as handled on the active admin user.
		userBase := activeAdmin.DeepCopy()
		activeAdmin.Status.FactoryAccountHandled = true
		if err := r.Status().Patch(ctx, activeAdmin, client.MergeFrom(userBase)); err != nil {
			errs = append(errs, fmt.Errorf("BMC %s: failed to set FactoryAccountHandled on BMCUser: %w", bmcObj.Name, err))
		}
	}
	return errors.Join(errs...)
}

// SetupWithManager sets up the controller with the Manager.
func (r *BMCUserSetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&baseboardv1alpha1.BMCUserSet{}).
		Owns(&baseboardv1alpha1.BMCUser{}).
		Watches(
			&metalv1alpha1.BMC{},
			handler.EnqueueRequestsFromMapFunc(r.enqueueByBMC),
			builder.WithPredicates(predicate.Funcs{
				UpdateFunc: func(e event.UpdateEvent) bool {
					return !labels.Equals(
						labels.Set(e.ObjectOld.GetLabels()),
						labels.Set(e.ObjectNew.GetLabels()),
					)
				},
			}),
		).
		Watches(
			&baseboardv1alpha1.BMCUser{},
			handler.EnqueueRequestsFromMapFunc(r.enqueueByBMCUser),
		).
		Watches(
			&baseboardv1alpha1.BMCUserRotation{},
			handler.EnqueueRequestsFromMapFunc(r.enqueueByBMCUserRotation),
		).
		Named("bmcuserset").
		Complete(r)
}
