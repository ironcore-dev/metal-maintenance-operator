// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package vendorconsole

import (
	"context"

	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// shouldIgnoreReconciliation returns true when the resource carries the
// metal.ironcore.dev/operation=ignore-reconciliation annotation. Vendor
// controllers should short-circuit their reconcile loop when this returns
// true so operators can pause a stuck flow without deleting the CR.
func shouldIgnoreReconciliation(obj client.Object) bool {
	annotations := obj.GetAnnotations()
	if annotations == nil {
		return false
	}
	return annotations[metalv1alpha1.OperationAnnotation] == metalv1alpha1.OperationAnnotationIgnore
}

// shouldRetryFailed returns true when the resource carries the retry
// annotation, signalling the controller should reset a Failed state back to
// Pending.
func shouldRetryFailed(obj client.Object) bool {
	annotations := obj.GetAnnotations()
	if annotations == nil {
		return false
	}
	return annotations[metalv1alpha1.OperationAnnotation] == metalv1alpha1.OperationAnnotationRetryFailed
}

// clearRetryAnnotation removes the retry annotation via a merge-patch so the
// controller doesn't re-trigger a reset every reconcile.
func clearRetryAnnotation(ctx context.Context, c client.Client, obj client.Object) error {
	base := obj.DeepCopyObject().(client.Object)
	annotations := obj.GetAnnotations()
	if annotations == nil {
		return nil
	}
	delete(annotations, metalv1alpha1.OperationAnnotation)
	obj.SetAnnotations(annotations)
	return c.Patch(ctx, obj, client.MergeFrom(base))
}
