package v1alpha1_test

import (
	"encoding/json"
	"testing"

	authv1alpha1 "github.com/appthrust/ocm-managed-serviceaccount-replicaset-controller/api/v1alpha1"
)

// TestSummaryFieldOmittedWhenNil verifies that ManagedServiceAccountReplicaSetStatus
// drops the "summary" key from its JSON encoding when Summary is nil.
//
// Regression guard for K8S-10: the field was previously a struct value with
// `omitempty`, which Go's encoding/json never omits for non-pointer structs,
// so the zero-value Summary leaked through every status write. Switching to
// `*Summary` is the fix; this test ensures pointer-ness keeps the field
// correctly omitted.
func TestSummaryFieldOmittedWhenNil(t *testing.T) {
	status := authv1alpha1.ManagedServiceAccountReplicaSetStatus{
		ObservedGeneration:   1,
		SelectedClusterCount: 0,
	}
	data, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := raw["summary"]; ok {
		t.Fatalf("summary key must be omitted when Summary is nil; got JSON: %s", data)
	}
}

// TestPlacementSummaryFieldOmittedWhenNil verifies that PlacementStatus drops
// the "summary" key from its JSON encoding when Summary is nil.
//
// Regression guard for K8S-10: matches the parent struct fix and protects the
// per-placement aggregation path from re-introducing the same bug.
func TestPlacementSummaryFieldOmittedWhenNil(t *testing.T) {
	placement := authv1alpha1.PlacementStatus{
		Name: "p",
	}
	data, err := json.Marshal(placement)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := raw["summary"]; ok {
		t.Fatalf("summary key must be omitted when Summary is nil; got JSON: %s", data)
	}
}

// TestSummaryFieldPresentWhenSet ensures that, after the pointer change, the
// "summary" key still appears when an explicit Summary value is assigned.
// This guards against accidentally over-omitting (e.g. via a wrong tag) and
// keeps the contract symmetric with the nil-omission guards above.
func TestSummaryFieldPresentWhenSet(t *testing.T) {
	status := authv1alpha1.ManagedServiceAccountReplicaSetStatus{
		Summary: &authv1alpha1.Summary{DesiredTotal: 2, Total: 1},
	}
	data, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	summary, ok := raw["summary"].(map[string]any)
	if !ok {
		t.Fatalf("summary key must be present and be an object; got JSON: %s", data)
	}
	if got, _ := summary["desiredTotal"].(float64); got != 2 {
		t.Fatalf("summary.desiredTotal = %v, want 2; JSON: %s", summary["desiredTotal"], data)
	}
	if got, _ := summary["total"].(float64); got != 1 {
		t.Fatalf("summary.total = %v, want 1; JSON: %s", summary["total"], data)
	}
}
