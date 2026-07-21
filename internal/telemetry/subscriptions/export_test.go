// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package subscriptions

// InitForTest performs the same field validation + parsed-URL setup
// that SetupWithManager runs, without registering with a
// controller-runtime manager. Available to package-external tests via
// the subscriptions_test package; not part of the public API.
//
// Use in unit tests where wiring a real ctrl.Manager (and envtest
// binary) would be overkill for exercising Reconcile in isolation.
func InitForTest(r *BMCReconciler) error {
	return r.init()
}
