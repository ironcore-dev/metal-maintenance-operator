// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package utils

import (
	"context"
	"errors"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// MockStatusWriter is a mock implementation of client.StatusWriter that can simulate failures
type MockStatusWriter struct {
	client.StatusWriter
	ShouldFail bool
	FailError  error
}

// Update implements client.StatusWriter and can simulate failure scenarios
func (m *MockStatusWriter) Update(
	ctx context.Context,
	obj client.Object,
	opts ...client.SubResourceUpdateOption,
) error {
	if m.ShouldFail {
		return m.FailError
	}
	return nil
}

// Patch implements client.StatusWriter and can simulate failure scenarios
func (m *MockStatusWriter) Patch(
	ctx context.Context,
	obj client.Object,
	patch client.Patch,
	opts ...client.SubResourcePatchOption,
) error {
	if m.ShouldFail {
		return m.FailError
	}
	return nil
}

// MockClient wraps a fake client with a controllable status writer
type MockClient struct {
	client.Client
	StatusWriter *MockStatusWriter
}

// Status returns the controllable mock status writer
func (m *MockClient) Status() client.StatusWriter {
	return m.StatusWriter
}

// MockFailingClient is a mock client that can simulate Get failures
type MockFailingClient struct {
	client.Client
	ShouldFail bool
	FailError  error
}

// Get implements client.Client and can simulate Get failures
func (m *MockFailingClient) Get(
	ctx context.Context,
	key client.ObjectKey,
	obj client.Object,
	opts ...client.GetOption,
) error {
	if m.ShouldFail {
		return m.FailError
	}
	return m.Client.Get(ctx, key, obj, opts...)
}

// MockJobGetFailingClient is a mock client that can simulate Job Get failures
type MockJobGetFailingClient struct {
	client.Client
	ShouldFail bool
	FailError  error
}

// Get implements client.Client and can simulate Job Get failures
func (m *MockJobGetFailingClient) Get(
	ctx context.Context,
	key client.ObjectKey,
	obj client.Object,
	opts ...client.GetOption,
) error {
	// Only fail for Job objects
	if _, isJob := obj.(*batchv1.Job); isJob && m.ShouldFail {
		return m.FailError
	}
	return m.Client.Get(ctx, key, obj, opts...)
}

// MockJobCreateFailingClient is a mock client that can simulate Job Create failures
type MockJobCreateFailingClient struct {
	client.Client
	ShouldFail bool
	FailError  error
}

// Create implements client.Client and can simulate Job Create failures
func (m *MockJobCreateFailingClient) Create(
	ctx context.Context,
	obj client.Object,
	opts ...client.CreateOption,
) error {
	// Only fail for Job objects
	if _, isJob := obj.(*batchv1.Job); isJob && m.ShouldFail {
		return m.FailError
	}
	return m.Client.Create(ctx, obj, opts...)
}

// MockConfigMapGetFailingClient is a mock client that can simulate ConfigMap Get failures
type MockConfigMapGetFailingClient struct {
	client.Client
	ShouldFail bool
	FailError  error
}

// Get implements client.Client and can simulate ConfigMap Get failures
func (m *MockConfigMapGetFailingClient) Get(
	ctx context.Context,
	key client.ObjectKey,
	obj client.Object,
	opts ...client.GetOption,
) error {
	// Only fail for ConfigMap objects
	if _, isConfigMap := obj.(*corev1.ConfigMap); isConfigMap && m.ShouldFail {
		return m.FailError
	}
	return m.Client.Get(ctx, key, obj, opts...)
}

// MockConfigMapCreateFailingClient is a mock client that can simulate ConfigMap Create failures
type MockConfigMapCreateFailingClient struct {
	client.Client
	ShouldFail bool
	FailError  error
}

// Create implements client.Client and can simulate ConfigMap Create failures
func (m *MockConfigMapCreateFailingClient) Create(
	ctx context.Context,
	obj client.Object,
	opts ...client.CreateOption,
) error {
	// Only fail for ConfigMap objects
	if _, isConfigMap := obj.(*corev1.ConfigMap); isConfigMap && m.ShouldFail {
		return m.FailError
	}
	return m.Client.Create(ctx, obj, opts...)
}

// MockSetControllerRefFailingClient simulates SetControllerReference failure by failing Create with specific error
type MockSetControllerRefFailingClient struct {
	client.Client
	ShouldFailControllerRef bool
}

// Create implements client.Client and simulates SetControllerReference failure
func (m *MockSetControllerRefFailingClient) Create(
	ctx context.Context,
	obj client.Object,
	opts ...client.CreateOption,
) error {
	if m.ShouldFailControllerRef {
		// Simulate SetControllerReference type error
		return errors.New("failed to set controller reference: owner must have non-empty UID")
	}
	return m.Client.Create(ctx, obj, opts...)
}

// MockJobClient is a mock client that can return pre-configured Job objects
type MockJobClient struct {
	client.Client
	Jobs map[client.ObjectKey]*batchv1.Job
}

// Get implements client.Client and can return pre-configured Job objects
func (m *MockJobClient) Get(
	ctx context.Context,
	key client.ObjectKey,
	obj client.Object,
	opts ...client.GetOption,
) error {
	if job, ok := obj.(*batchv1.Job); ok {
		if mockJob, exists := m.Jobs[key]; exists {
			*job = *mockJob
			return nil
		}
		return errors.New("job not found")
	}
	return m.Client.Get(ctx, key, obj, opts...)
}

// FindCondition finds a condition by type in the conditions slice
func FindCondition(conditions []metav1.Condition, conditionType string) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == conditionType {
			return &conditions[i]
		}
	}
	return nil
}
