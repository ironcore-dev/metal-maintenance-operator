// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"github.com/ironcore-dev/metal-maintenance-operator/internal/constants"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

const (
	conditionTypeSanitized = "maintenance.metal.ironcore.dev/sanitized"

	sanitizationForUIDLabel = "maintenance.metal.ironcore.dev/sanitization-for-uid"
)

type ServerSanitizationReconciler struct {
	client.Client
	Scheme                       *runtime.Scheme
	SanitizationNamespace        string
	SanitizationImage            string
	SanitizationTolerations      []metalv1alpha1.Toleration
	SanitizationIgnitionProvider func(
		ctx context.Context,
		server *metalv1alpha1.Server,
		sanitizationUID string,
	) ([]byte, error)
}

func (r *ServerSanitizationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	server := &metalv1alpha1.Server{}
	if err := r.Get(ctx, req.NamespacedName, server); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !server.DeletionTimestamp.IsZero() {
		log.V(1).Info("Server is deleting")
		return ctrl.Result{}, nil
	}
	switch server.Status.State {
	case metalv1alpha1.ServerStateReleased:
		return r.reconcileReleased(ctx, server)
	case metalv1alpha1.ServerStateAvailable:
		return r.reconcileAvailable(ctx, server)
	default:
		return ctrl.Result{}, nil
	}
}

func (r *ServerSanitizationReconciler) reconcileReleased(ctx context.Context, server *metalv1alpha1.Server) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	base := server.DeepCopy()
	if addCondition(server, metav1.Condition{
		Type:    conditionTypeSanitized,
		Status:  metav1.ConditionFalse,
		Reason:  "SanitizationRequired",
		Message: "Server needs sanitization",
	}) {
		if err := r.Status().Patch(ctx, server, client.MergeFrom(base)); err != nil {
			return ctrl.Result{}, fmt.Errorf("setting server %s sanitized condition: %w", server.Name, err)
		}
		return ctrl.Result{}, nil
	}

	log.V(1).Info("Sanitized condition set, releasing claim ref")
	server.Spec.ServerClaimRef = nil
	if err := r.Patch(ctx, server, client.MergeFrom(base)); err != nil {
		return ctrl.Result{}, fmt.Errorf("releasing server %s claim ref: %w", server.Name, err)
	}
	return ctrl.Result{}, nil
}

func (r *ServerSanitizationReconciler) reconcileAvailable(ctx context.Context, server *metalv1alpha1.Server) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	if getConditionStatus(server, conditionTypeSanitized) == metav1.ConditionTrue {
		log.V(1).Info("Server is sanitized, deleting any leftover sanitization resources")
		var (
			cleanupTypes = []client.Object{&metalv1alpha1.ServerClaim{}, &corev1.Secret{}}
			errs         []error
		)
		for _, cleanupType := range cleanupTypes {
			if err := r.DeleteAllOf(ctx, cleanupType,
				client.InNamespace(r.SanitizationNamespace),
				client.MatchingLabels{sanitizationForUIDLabel: string(server.UID)},
			); err != nil {
				errs = append(errs, fmt.Errorf("cleaning up sanitization resource type %T: %w", cleanupType, err))
			}
		}
		return ctrl.Result{}, errors.Join(errs...)
	}

	sanitizationClaim, err := r.getOrCreateSanitizationClaim(ctx, server)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting or creating sanitization claim: %w", err)
	}

	_, ok := sanitizationClaim.Labels[constants.SanitizedLabel]
	if !ok {
		log.V(1).Info("Server not yet sanitized")
		return ctrl.Result{}, nil
	}

	log.V(1).Info("Claim reports sanitized, setting sanitized condition")
	base := server.DeepCopy()
	addCondition(server, metav1.Condition{
		Type:    conditionTypeSanitized,
		Status:  metav1.ConditionTrue,
		Reason:  "Sanitized",
		Message: "Server has been sanitized",
	})
	if err := r.Status().Patch(ctx, server, client.MergeFrom(base)); err != nil {
		return ctrl.Result{}, fmt.Errorf("setting server %s sanitized condition: %w", server.Name, err)
	}

	log.V(1).Info("Deleting sanitization claim")
	if err := r.Delete(ctx, sanitizationClaim); client.IgnoreNotFound(err) != nil {
		return ctrl.Result{}, fmt.Errorf("deleting sanitization claim: %w", err)
	}
	return ctrl.Result{}, nil
}

func (r *ServerSanitizationReconciler) getOrCreateSanitizationClaim(ctx context.Context, server *metalv1alpha1.Server) (*metalv1alpha1.ServerClaim, error) {
	log := ctrl.LoggerFrom(ctx)

	serverClaimList := &metalv1alpha1.ServerClaimList{}
	if err := r.List(ctx, serverClaimList,
		client.InNamespace(r.SanitizationNamespace),
		client.MatchingLabels{sanitizationForUIDLabel: string(server.UID)},
	); err != nil {
		return nil, fmt.Errorf("listing sanitization server claims: %w", err)
	}

	var matchingClaims []metalv1alpha1.ServerClaim
	for _, claim := range serverClaimList.Items {
		if !metav1.IsControlledBy(&claim, server) {
			continue
		}

		matchingClaims = append(matchingClaims, claim)
	}
	// Sort matching claims so oldest one wins
	slices.SortFunc(matchingClaims, func(c1, c2 metalv1alpha1.ServerClaim) int {
		return c1.CreationTimestamp.Compare(c2.CreationTimestamp.Time)
	})
	if len(matchingClaims) > 0 {
		matchingClaim := &matchingClaims[0]
		sanitizationUID := matchingClaim.Name
		if len(matchingClaims) > 1 {
			go func() {
				if err := r.cleanupOutdatedSanitizationResources(ctx, server, sanitizationUID); err != nil {
					log.Error(err, "Cleaning outdated sanitization resources")
				}
			}()
		}
		log.V(1).Info("Found matching sanitization claim", "MatchingSanitizationClaim", klog.KObj(matchingClaim))
		return matchingClaim, nil
	}

	sanitizationUID := uuid.NewString()
	log = log.WithValues("SanitizationUID", sanitizationUID)
	log.V(1).Info("No matching sanitization claim found, creating new claim")
	var (
		ignitionSecret    *corev1.Secret
		ignitionSecretRef *corev1.LocalObjectReference
	)
	if r.SanitizationIgnitionProvider != nil {
		ignitionData, err := r.SanitizationIgnitionProvider(ctx, server, sanitizationUID)
		if err != nil {
			return nil, fmt.Errorf("getting server ignition data: %w", err)
		}

		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: r.SanitizationNamespace,
				Name:      sanitizationUID,
				Labels: map[string]string{
					sanitizationForUIDLabel: string(server.UID),
				},
			},
			Data: map[string][]byte{
				"ignition": ignitionData,
			},
		}
		if err := r.Create(ctx, secret); err != nil {
			return nil, fmt.Errorf("creating server ignition secret: %w", err)
		}
		ignitionSecret = secret
		ignitionSecretRef = &corev1.LocalObjectReference{
			Name: ignitionSecret.Name,
		}
	}

	sanitizationClaim := &metalv1alpha1.ServerClaim{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: r.SanitizationNamespace,
			Name:      sanitizationUID,
			Labels: map[string]string{
				sanitizationForUIDLabel: string(server.UID),
			},
		},
		Spec: metalv1alpha1.ServerClaimSpec{
			Power:             metalv1alpha1.PowerOn,
			ServerRef:         &corev1.LocalObjectReference{Name: server.Name},
			IgnitionSecretRef: ignitionSecretRef,
			Image:             r.SanitizationImage,
			Tolerations:       r.SanitizationTolerations,
		},
	}
	_ = ctrl.SetControllerReference(server, sanitizationClaim, r.Scheme)
	if err := r.Create(ctx, sanitizationClaim); err != nil {
		return nil, fmt.Errorf("creating sanitization claim: %w", err)
	}
	if ignitionSecret != nil {
		baseIgnitionSecret := ignitionSecret.DeepCopy()
		if err := ctrl.SetControllerReference(sanitizationClaim, ignitionSecret, r.Scheme); err != nil {
			return nil, fmt.Errorf("setting server ignition secret: %w", err)
		}
		if err := r.Patch(ctx, ignitionSecret, client.MergeFrom(baseIgnitionSecret)); err != nil {
			return nil, fmt.Errorf("setting ignition to be owned by claim: %w", err)
		}
	}
	return sanitizationClaim, nil
}

func (r *ServerSanitizationReconciler) cleanupOutdatedSanitizationResources(
	ctx context.Context,
	server *metalv1alpha1.Server,
	sanitizationUID string,
) error {
	var (
		cleanupTypes = []client.Object{&metalv1alpha1.ServerClaim{}, &corev1.Secret{}}
		errs         []error
	)
	for _, cleanupType := range cleanupTypes {
		if err := r.DeleteAllOf(ctx, cleanupType,
			client.InNamespace(r.SanitizationNamespace),
			client.MatchingLabels{sanitizationForUIDLabel: string(server.UID)},
			client.MatchingFieldsSelector{
				Selector: fields.OneTermEqualSelector("metadata.name", sanitizationUID),
			},
		); err != nil {
			errs = append(errs, fmt.Errorf("cleaning up outdated sanitization resource type %T: %w",
				cleanupType, err))
		}
	}
	return errors.Join(errs...)
}

func (r *ServerSanitizationReconciler) serverPredicate() predicate.Predicate {
	return predicate.NewPredicateFuncs(func(obj client.Object) bool {
		server := obj.(*metalv1alpha1.Server)
		if state := server.Status.State; state != metalv1alpha1.ServerStateAvailable &&
			state != metalv1alpha1.ServerStateReleased {
			return false
		}

		return getConditionStatus(server, conditionTypeSanitized) != metav1.ConditionTrue
	})
}

func (r *ServerSanitizationReconciler) serverClaimPredicate() predicate.Predicate {
	return predicate.NewPredicateFuncs(func(obj client.Object) bool {
		serverClaim := obj.(*metalv1alpha1.ServerClaim)
		_, ok := serverClaim.GetLabels()[sanitizationForUIDLabel]
		return ok
	})
}

func (r *ServerSanitizationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("serversanitization").
		For(
			&metalv1alpha1.Server{},
			builder.WithPredicates(r.serverPredicate()),
		).
		Owns(
			&metalv1alpha1.ServerClaim{},
			builder.WithPredicates(r.serverClaimPredicate()),
		).
		Complete(r)
}

func getConditionStatus(server *metalv1alpha1.Server, conditionType string) metav1.ConditionStatus {
	for _, condition := range server.Status.Conditions {
		if condition.Type == conditionType {
			return condition.Status
		}
	}
	return metav1.ConditionUnknown
}

func addCondition(server *metalv1alpha1.Server, conditionsToAdd ...metav1.Condition) (modified bool) {
	for _, condToAdd := range conditionsToAdd {
		idx := slices.IndexFunc(server.Status.Conditions, func(cond metav1.Condition) bool {
			return cond.Type == condToAdd.Type
		})
		if idx < 0 {
			condToAdd.LastTransitionTime = metav1.Now()
			server.Status.Conditions = append(server.Status.Conditions, condToAdd)
			modified = true
			continue
		}

		actualCond := server.Status.Conditions[idx]
		if actualCond.Reason == condToAdd.Reason &&
			actualCond.Status == condToAdd.Status &&
			actualCond.Message == condToAdd.Message {
			continue
		}

		condToAdd.LastTransitionTime = metav1.Now()
		server.Status.Conditions[idx] = condToAdd
		modified = true
	}
	return modified
}
