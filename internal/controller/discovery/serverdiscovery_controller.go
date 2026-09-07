// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package discovery

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/ironcore-dev/metal-maintenance-operator/internal/constants"
	"github.com/ironcore-dev/metal-maintenance-operator/internal/registry"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	discoveryv1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/discovery/v1alpha1"
)

// ServerDiscoveryReconciler discovers Servers tainted with the Undiscovered taint.
type ServerDiscoveryReconciler struct {
	client.Client
	Scheme             *runtime.Scheme
	DiscoveryNamespace string
	ProbeOSImage       string
	IgnitionProvider   func(ctx context.Context, server *metalv1alpha1.Server) ([]byte, error)
	Registry           *registry.RegistryServer
	RegistryDataMaxAge time.Duration
	ResyncInterval     time.Duration
}

// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=servers,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=servers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=serverclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=discovery.metal.ironcore.dev,resources=metadatas,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *ServerDiscoveryReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	server := &metalv1alpha1.Server{}
	if err := r.Get(ctx, req.NamespacedName, server); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	return r.reconcileExists(ctx, server)
}

func (r *ServerDiscoveryReconciler) reconcileExists(ctx context.Context, server *metalv1alpha1.Server) (ctrl.Result, error) {
	if !server.DeletionTimestamp.IsZero() {
		return r.delete(ctx, server)
	}
	return r.reconcile(ctx, server)
}

func (r *ServerDiscoveryReconciler) delete(ctx context.Context, server *metalv1alpha1.Server) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	log.V(1).Info("Deleting Server discovery resources")
	r.Registry.Delete(server.Spec.SystemUUID)
	log.V(1).Info("Deleted Server discovery resources")
	return ctrl.Result{}, nil
}

func (r *ServerDiscoveryReconciler) reconcile(ctx context.Context, server *metalv1alpha1.Server) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	log.V(1).Info("Reconciling Server discovery")

	if !hasUndiscoveredTaint(server) || discoveryConditionStatus(server) == metav1.ConditionTrue {
		log.V(1).Info("Server does not need discovery, cleaning up any leftover discovery resources")
		return ctrl.Result{}, r.cleanupDiscoveryResources(ctx, server)
	}

	if _, err := r.getOrCreateDiscoveryClaim(ctx, server); err != nil {
		return ctrl.Result{}, fmt.Errorf("getting or creating discovery claim: %w", err)
	}

	data, ok := r.Registry.Load(server.Spec.SystemUUID)
	if !ok {
		log.V(1).Info("Server agent did not post discovery data to registry yet")
		return ctrl.Result{RequeueAfter: r.ResyncInterval}, nil
	}
	if data.Timestamp == nil || time.Since(data.Timestamp.Time) >= r.RegistryDataMaxAge {
		log.V(1).Info("Registry data is stale, waiting for fresh update")
		return ctrl.Result{RequeueAfter: r.ResyncInterval}, nil
	}
	log.V(1).Info("Received fresh discovery data from registry")

	if err := r.applyMetadata(ctx, server, data); err != nil {
		return ctrl.Result{}, fmt.Errorf("applying Metadata for server %s: %w", server.Name, err)
	}
	log.V(1).Info("Applied Metadata")

	base := server.DeepCopy()
	if setDiscoveryCondition(server, metav1.Condition{
		Type:    constants.ConditionDiscovered,
		Status:  metav1.ConditionTrue,
		Reason:  "RegistryDataReceived",
		Message: "Server discovery data has been received",
	}) {
		if err := r.Status().Patch(ctx, server, client.MergeFrom(base)); err != nil {
			return ctrl.Result{}, fmt.Errorf("setting server %s discovered condition: %w", server.Name, err)
		}
	}

	// Best-effort: the metalprobe re-posts until powered down, so the entry
	// may reappear until the claim teardown below powers the server off.
	// The registry's sweep reaps any such leftover once it goes stale.
	r.Registry.Delete(server.Spec.SystemUUID)

	if err := r.cleanupDiscoveryResources(ctx, server); err != nil {
		return ctrl.Result{}, fmt.Errorf("cleaning up discovery resources: %w", err)
	}
	log.V(1).Info("Reconciled Server discovery")
	return ctrl.Result{}, nil
}

func (r *ServerDiscoveryReconciler) getOrCreateDiscoveryClaim(
	ctx context.Context,
	server *metalv1alpha1.Server,
) (*metalv1alpha1.ServerClaim, error) {
	log := ctrl.LoggerFrom(ctx)

	discoveryClaim := &metalv1alpha1.ServerClaim{}
	claimKey := client.ObjectKey{Namespace: r.DiscoveryNamespace, Name: server.Name}
	err := r.Get(ctx, claimKey, discoveryClaim)
	if err != nil && !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("getting discovery claim: %w", err)
	}
	if err == nil {
		if !metav1.IsControlledBy(discoveryClaim, server) {
			return nil, fmt.Errorf("discovery claim %s is not owned by server %s", claimKey, server.Name)
		}
		log.V(1).Info("Found matching discovery claim", "MatchingDiscoveryClaim", client.ObjectKeyFromObject(discoveryClaim))
		return discoveryClaim, nil
	}

	var ignitionSecretRef *corev1.LocalObjectReference
	if r.IgnitionProvider != nil {
		ignitionData, err := r.IgnitionProvider(ctx, server)
		if err != nil {
			return nil, fmt.Errorf("getting server ignition data: %w", err)
		}

		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: r.DiscoveryNamespace,
				Name:      server.Name,
				Labels: map[string]string{
					constants.DiscoveryForUIDLabel: string(server.UID),
				},
			},
			Data: map[string][]byte{
				"ignition": ignitionData,
			},
		}
		if err := r.Create(ctx, secret); err != nil && !apierrors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("creating server ignition secret: %w", err)
		}
		ignitionSecretRef = &corev1.LocalObjectReference{Name: secret.Name}
	}

	discoveryClaim = &metalv1alpha1.ServerClaim{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: r.DiscoveryNamespace,
			Name:      server.Name,
			Labels: map[string]string{
				constants.DiscoveryForUIDLabel: string(server.UID),
			},
		},
		Spec: metalv1alpha1.ServerClaimSpec{
			Power:             metalv1alpha1.PowerOn,
			ServerRef:         &corev1.LocalObjectReference{Name: server.Name},
			IgnitionSecretRef: ignitionSecretRef,
			Image:             r.ProbeOSImage,
			Tolerations: []metalv1alpha1.Toleration{{
				Key:      constants.UndiscoveredTaintKey,
				Operator: metalv1alpha1.TolerationOperatorExists,
				Effect:   metalv1alpha1.TaintEffectNoBind,
			}},
		},
	}
	if err := ctrl.SetControllerReference(server, discoveryClaim, r.Scheme); err != nil {
		return nil, fmt.Errorf("setting server as owner of discovery claim: %w", err)
	}
	if err := r.Create(ctx, discoveryClaim); err != nil {
		return nil, fmt.Errorf("creating discovery claim: %w", err)
	}
	if ignitionSecretRef != nil {
		secret := &corev1.Secret{}
		if err := r.Get(ctx, client.ObjectKey{Namespace: r.DiscoveryNamespace, Name: ignitionSecretRef.Name}, secret); err != nil {
			return nil, fmt.Errorf("getting ignition secret: %w", err)
		}
		base := secret.DeepCopy()
		if err := ctrl.SetControllerReference(discoveryClaim, secret, r.Scheme); err != nil {
			return nil, fmt.Errorf("setting discovery claim as owner of ignition secret: %w", err)
		}
		if err := r.Patch(ctx, secret, client.MergeFrom(base)); err != nil {
			return nil, fmt.Errorf("patching ignition secret owner: %w", err)
		}
	}
	log.V(1).Info("Created discovery claim", "DiscoveryClaim", client.ObjectKeyFromObject(discoveryClaim))
	return discoveryClaim, nil
}

func (r *ServerDiscoveryReconciler) applyMetadata(
	ctx context.Context,
	server *metalv1alpha1.Server,
	data discoveryv1alpha1.Metadata,
) error {
	metadata := &discoveryv1alpha1.Metadata{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: r.DiscoveryNamespace,
			Name:      server.Name,
		},
	}
	opResult, err := controllerutil.CreateOrPatch(ctx, r.Client, metadata, func() error {
		metadata.Timestamp = data.Timestamp
		metadata.SystemInfo = data.SystemInfo
		metadata.CPU = data.CPU
		metadata.NetworkInterfaces = data.NetworkInterfaces
		metadata.LLDP = data.LLDP
		metadata.Storage = data.Storage
		metadata.Memory = data.Memory
		metadata.NICs = data.NICs
		metadata.PCIDevices = data.PCIDevices
		return ctrl.SetControllerReference(server, metadata, r.Scheme)
	})
	if err != nil {
		return err
	}
	ctrl.LoggerFrom(ctx).V(1).Info("Created or patched Metadata",
		"Metadata", client.ObjectKeyFromObject(metadata), "Operation", opResult)
	return nil
}

func (r *ServerDiscoveryReconciler) cleanupDiscoveryResources(ctx context.Context, server *metalv1alpha1.Server) error {
	var (
		cleanupTypes = []client.Object{&metalv1alpha1.ServerClaim{}, &corev1.Secret{}}
		errs         []error
	)
	for _, cleanupType := range cleanupTypes {
		if err := r.DeleteAllOf(ctx, cleanupType,
			client.InNamespace(r.DiscoveryNamespace),
			client.MatchingLabels{constants.DiscoveryForUIDLabel: string(server.UID)},
		); err != nil {
			errs = append(errs, fmt.Errorf("cleaning up discovery resource type %T: %w", cleanupType, err))
		}
	}
	return errors.Join(errs...)
}

func hasUndiscoveredTaint(server *metalv1alpha1.Server) bool {
	return slices.ContainsFunc(server.Spec.Taints, func(t metalv1alpha1.Taint) bool {
		return t.Key == constants.UndiscoveredTaintKey && t.Effect == metalv1alpha1.TaintEffectNoBind
	})
}

func discoveryConditionStatus(server *metalv1alpha1.Server) metav1.ConditionStatus {
	for _, cond := range server.Status.Conditions {
		if cond.Type == constants.ConditionDiscovered {
			return cond.Status
		}
	}
	return metav1.ConditionUnknown
}

func setDiscoveryCondition(server *metalv1alpha1.Server, cond metav1.Condition) (modified bool) {
	idx := slices.IndexFunc(server.Status.Conditions, func(c metav1.Condition) bool {
		return c.Type == cond.Type
	})
	if idx >= 0 {
		actual := server.Status.Conditions[idx]
		if actual.Status == cond.Status && actual.Reason == cond.Reason && actual.Message == cond.Message {
			return false
		}
	}
	cond.LastTransitionTime = metav1.Now()
	if idx < 0 {
		server.Status.Conditions = append(server.Status.Conditions, cond)
	} else {
		server.Status.Conditions[idx] = cond
	}
	return true
}

func (r *ServerDiscoveryReconciler) serverPredicate() predicate.Predicate {
	return predicate.NewPredicateFuncs(func(obj client.Object) bool {
		server := obj.(*metalv1alpha1.Server)
		return hasUndiscoveredTaint(server) || discoveryConditionStatus(server) != metav1.ConditionTrue
	})
}

func (r *ServerDiscoveryReconciler) serverClaimPredicate() predicate.Predicate {
	return predicate.NewPredicateFuncs(func(obj client.Object) bool {
		_, ok := obj.GetLabels()[constants.DiscoveryForUIDLabel]
		return ok
	})
}

// SetupWithManager sets up the controller with the Manager.
func (r *ServerDiscoveryReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("serverdiscovery").
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
