// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package utils

import (
	"context"
	"slices"

	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// GetServerByName returns a Server object by its name.
func GetServerByName(ctx context.Context, c client.Client, serverName string) (*metalv1alpha1.Server, error) {
	server := &metalv1alpha1.Server{}
	if err := c.Get(ctx, client.ObjectKey{Name: serverName}, server); err != nil {
		return nil, err
	}
	return server, nil
}

// ShouldIgnoreReconciliation checks if the object should be ignored during reconciliation
// based on the operation annotation set on it.
func ShouldIgnoreReconciliation(obj client.Object) bool {
	val, found := obj.GetAnnotations()[metalv1alpha1.OperationAnnotation]
	if !found {
		return false
	}
	return slices.Contains([]string{
		metalv1alpha1.OperationAnnotationIgnore,
		metalv1alpha1.OperationAnnotationIgnoreChildAndSelf,
		metalv1alpha1.OperationAnnotationIgnorePropagated,
	}, val)
}
