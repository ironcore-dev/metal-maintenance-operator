// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package criticalevent_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/go-logr/logr"
	"github.com/ironcore-dev/metal-maintenance-operator/internal/telemetry/criticalevent"
	"github.com/ironcore-dev/metal-maintenance-operator/internal/telemetry/sink"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const testServerName = "srv-1"

// newClient builds a fake controller-runtime client with the field indexer
// registered on Server.spec.bmcRef.name — same indexer the production
// runtime sets up. Tests use it to verify the handler's List call works.
func newClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := metalv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&metalv1alpha1.Server{}).
		WithIndex(&metalv1alpha1.Server{}, criticalevent.BMCRefField, func(obj client.Object) []string {
			s := obj.(*metalv1alpha1.Server)
			if s.Spec.BMCRef == nil {
				return nil
			}
			return []string{s.Spec.BMCRef.Name}
		}).
		Build()
}

func server(name, bmcName string) *metalv1alpha1.Server {
	s := &metalv1alpha1.Server{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       metalv1alpha1.ServerSpec{SystemUUID: "uuid-" + name},
	}
	if bmcName != "" {
		s.Spec.BMCRef = &corev1.LocalObjectReference{Name: bmcName}
	}
	return s
}

func criticalEvent(eventID string) sink.Event {
	return sink.Event{
		EventID:           eventID,
		Severity:          "Critical",
		Message:           "PSU failure",
		OriginOfCondition: "/redfish/v1/Chassis/1/PowerSupplies/1",
	}
}

func findCondition(server *metalv1alpha1.Server) *metav1.Condition {
	for i := range server.Status.Conditions {
		if server.Status.Conditions[i].Type == criticalevent.CriticalEventConditionType {
			return &server.Status.Conditions[i]
		}
	}
	return nil
}

// -- tests --

func TestHandleCritical_SingleMatchingServer_SetsCondition(t *testing.T) {
	s := server(testServerName, "bmc-1")
	c := newClient(t, s)
	h := &criticalevent.ConditionHandler{Client: c, Log: logr.Discard()}

	if err := h.HandleCritical(context.Background(), "bmc-1", criticalEvent("E1")); err != nil {
		t.Fatalf("HandleCritical: %v", err)
	}
	got := &metalv1alpha1.Server{}
	if err := c.Get(context.Background(), client.ObjectKey{Name: testServerName}, got); err != nil {
		t.Fatalf("Get: %v", err)
	}
	cond := findCondition(got)
	if cond == nil {
		t.Fatal("condition missing")
	}
	if cond.Status != metav1.ConditionTrue {
		t.Errorf("status: got %v, want True", cond.Status)
	}
	if !strings.HasPrefix(cond.Reason, "CriticalEvent") {
		t.Errorf("reason: got %q, want CriticalEvent*", cond.Reason)
	}
	if cond.Reason != "CriticalEventE1" {
		t.Errorf("reason: got %q, want CriticalEventE1", cond.Reason)
	}
	if !strings.Contains(cond.Message, "PSU failure") {
		t.Errorf("message missing event Message: %q", cond.Message)
	}
}

func TestHandleCritical_MultipleMatchingServers_SetsConditionOnAll(t *testing.T) {
	s1 := server(testServerName, "bmc-1")
	s2 := server("srv-2", "bmc-1") // same BMC
	other := server("srv-3", "bmc-other")
	c := newClient(t, s1, s2, other)
	h := &criticalevent.ConditionHandler{Client: c, Log: logr.Discard()}

	if err := h.HandleCritical(context.Background(), "bmc-1", criticalEvent("E1")); err != nil {
		t.Fatalf("HandleCritical: %v", err)
	}
	for _, name := range []string{testServerName, "srv-2"} {
		got := &metalv1alpha1.Server{}
		_ = c.Get(context.Background(), client.ObjectKey{Name: name}, got)
		if findCondition(got) == nil {
			t.Errorf("%s: condition missing", name)
		}
	}
	// srv-3 (different BMC) must NOT have the condition.
	got := &metalv1alpha1.Server{}
	_ = c.Get(context.Background(), client.ObjectKey{Name: "srv-3"}, got)
	if findCondition(got) != nil {
		t.Error("srv-3 (different BMC) got the condition incorrectly")
	}
}

func TestHandleCritical_NoMatchingServers_ReturnsNil(t *testing.T) {
	c := newClient(t) // empty
	h := &criticalevent.ConditionHandler{Client: c, Log: logr.Discard()}

	if err := h.HandleCritical(context.Background(), "bmc-nobody", criticalEvent("E1")); err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
}

func TestHandleCritical_EmptyBMCName_IsNoop(t *testing.T) {
	// A Server with empty BMCRef shouldn't be picked up. The handler
	// short-circuits on empty bmcName regardless.
	s := server(testServerName, "")
	c := newClient(t, s)
	h := &criticalevent.ConditionHandler{Client: c, Log: logr.Discard()}

	if err := h.HandleCritical(context.Background(), "", criticalEvent("E1")); err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
	got := &metalv1alpha1.Server{}
	_ = c.Get(context.Background(), client.ObjectKey{Name: testServerName}, got)
	if findCondition(got) != nil {
		t.Error("server with empty BMCRef got the condition incorrectly")
	}
}

func TestHandleCritical_IdempotentReplay(t *testing.T) {
	s := server(testServerName, "bmc-1")
	c := newClient(t, s)
	h := &criticalevent.ConditionHandler{Client: c, Log: logr.Discard()}

	ev := criticalEvent("E1")
	if err := h.HandleCritical(context.Background(), "bmc-1", ev); err != nil {
		t.Fatal(err)
	}
	got := &metalv1alpha1.Server{}
	_ = c.Get(context.Background(), client.ObjectKey{Name: testServerName}, got)
	firstTransitionTime := findCondition(got).LastTransitionTime

	// Replay the identical event. apimeta.SetStatusCondition should
	// short-circuit (no change in status/reason/message), so the
	// LastTransitionTime stays the same.
	if err := h.HandleCritical(context.Background(), "bmc-1", ev); err != nil {
		t.Fatal(err)
	}
	_ = c.Get(context.Background(), client.ObjectKey{Name: testServerName}, got)
	if got := findCondition(got); got == nil {
		t.Fatal("condition vanished after replay")
	} else if !got.LastTransitionTime.Equal(&firstTransitionTime) {
		t.Errorf("LastTransitionTime changed on idempotent replay: %v -> %v",
			firstTransitionTime, got.LastTransitionTime)
	}
}

func TestHandleCritical_NewEventID_UpdatesReason(t *testing.T) {
	// Different EventID → Reason differs → SetStatusCondition does patch.
	s := server(testServerName, "bmc-1")
	c := newClient(t, s)
	h := &criticalevent.ConditionHandler{Client: c, Log: logr.Discard()}

	if err := h.HandleCritical(context.Background(), "bmc-1", criticalEvent("E1")); err != nil {
		t.Fatal(err)
	}
	if err := h.HandleCritical(context.Background(), "bmc-1", criticalEvent("E2")); err != nil {
		t.Fatal(err)
	}
	got := &metalv1alpha1.Server{}
	_ = c.Get(context.Background(), client.ObjectKey{Name: testServerName}, got)
	cond := findCondition(got)
	if cond == nil {
		t.Fatal("condition missing")
	}
	if cond.Reason != "CriticalEventE2" {
		t.Errorf("reason: got %q, want CriticalEventE2 (latest event wins)", cond.Reason)
	}
}

func TestHandleCritical_SanitisesAwkwardEventID(t *testing.T) {
	s := server(testServerName, "bmc-1")
	c := newClient(t, s)
	h := &criticalevent.ConditionHandler{Client: c, Log: logr.Discard()}

	// Redfish IDs sometimes contain "-" or "." which are illegal in
	// Condition.Reason. sanitizeEventID strips them.
	if err := h.HandleCritical(context.Background(), "bmc-1", sink.Event{
		EventID:  "sel-12.345/9",
		Severity: "Critical",
		Message:  "x",
	}); err != nil {
		t.Fatal(err)
	}
	got := &metalv1alpha1.Server{}
	_ = c.Get(context.Background(), client.ObjectKey{Name: testServerName}, got)
	cond := findCondition(got)
	if cond == nil {
		t.Fatal("condition missing")
	}
	// "sel-12.345/9" should sanitize to "sel123459".
	if cond.Reason != "CriticalEventsel123459" {
		t.Errorf("reason: got %q, want CriticalEventsel123459", cond.Reason)
	}
}

func TestHandleCritical_PropagatesListError(t *testing.T) {
	// Wrap a fake client whose List always errors.
	inner := newClient(t)
	errClient := &erroringClient{Client: inner, listErr: errors.New("apiserver down")}
	h := &criticalevent.ConditionHandler{Client: errClient, Log: logr.Discard()}

	err := h.HandleCritical(context.Background(), "bmc-1", criticalEvent("E1"))
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "apiserver down") {
		t.Errorf("error doesn't wrap underlying cause: %v", err)
	}
}

// erroringClient is a client.Client that injects a controllable List error.
// Patch failures are recorded but not injected here (a separate test could
// exercise that — kept minimal for now).
type erroringClient struct {
	client.Client
	mu      sync.Mutex
	listErr error
}

func (c *erroringClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	c.mu.Lock()
	listErr := c.listErr
	c.mu.Unlock()
	if listErr != nil {
		return listErr
	}
	return c.Client.List(ctx, list, opts...)
}
