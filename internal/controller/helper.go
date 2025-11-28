// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
//
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// helper.go - Utility functions for the controller package.

// shouldIgnoreReconciliation checks if the object should be ignored during reconciliation.
func shouldIgnoreReconciliation(obj client.Object) bool {
	val, found := obj.GetAnnotations()[metalv1alpha1.OperationAnnotation]
	if !found {
		return false
	}
	return val == metalv1alpha1.OperationAnnotationIgnore
}

// shouldRetryReconciliation checks if the object should retry reconciliation from failed state.
func shouldRetryReconciliation(obj client.Object) bool {
	val, found := obj.GetAnnotations()[metalv1alpha1.OperationAnnotation]
	if !found {
		return false
	}
	return val == metalv1alpha1.OperationAnnotationRetry
}
