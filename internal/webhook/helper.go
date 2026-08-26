// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"github.com/ironcore-dev/metal-maintenance-operator/internal/constants"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ShouldAllowForceUpdateInProgress checks if the object should force allow update.
func ShouldAllowForceUpdateInProgress(obj client.Object) bool {
	val, found := obj.GetAnnotations()[constants.OperationAnnotation]
	if !found {
		return false
	}
	return val == constants.OperationAnnotationForceUpdateInProgress || val == constants.OperationAnnotationForceUpdateOrDeleteInProgress
}

// ShouldAllowForceDeleteInProgress checks if the object be allowed to be force deleted.
func ShouldAllowForceDeleteInProgress(obj client.Object) bool {
	val, found := obj.GetAnnotations()[constants.OperationAnnotation]
	if !found {
		return false
	}
	return val == constants.OperationAnnotationForceUpdateOrDeleteInProgress
}
