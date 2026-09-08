package recommendation

import (
	"encoding/json"
	"reflect"
	"testing"
)

// An account with no image_scan attribute must behave exactly as before the
// setting existed: scanning on, only the two built-in namespaces excluded.
func TestImageScanSettings_DefaultsToEnabled(t *testing.T) {
	var s imageScanSettings
	if !s.IsEnabled() {
		t.Error("IsEnabled() = false for empty settings; want true (absent config scans everything)")
	}
	if got, want := s.namespaceExclusions(), []string{"kube-system", "nudgebee-agent"}; !reflect.DeepEqual(got, want) {
		t.Errorf("namespaceExclusions() = %v; want %v", got, want)
	}
}

// The drawer writes {"enabled":false} to turn scanning off for one account.
func TestImageScanSettings_ExplicitlyDisabled(t *testing.T) {
	var s imageScanSettings
	if err := json.Unmarshal([]byte(`{"enabled":false,"excluded_namespaces":[]}`), &s); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if s.IsEnabled() {
		t.Error("IsEnabled() = true for {\"enabled\":false}; want false")
	}
}

// Configured namespaces merge with the built-ins; blanks and duplicates (the
// shape a comma-separated UI field produces) must not reach the query.
func TestImageScanSettings_NamespaceExclusionsMergeAndClean(t *testing.T) {
	var s imageScanSettings
	if err := json.Unmarshal([]byte(`{"excluded_namespaces":[" staging ","kube-system","","staging","prod"]}`), &s); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	got := s.namespaceExclusions()
	want := []string{"kube-system", "nudgebee-agent", "prod", "staging"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("namespaceExclusions() = %v; want %v", got, want)
	}
	if !s.isNamespaceExcluded("staging") || !s.isNamespaceExcluded("kube-system") {
		t.Error("isNamespaceExcluded missed a configured or built-in exclusion")
	}
	if s.isNamespaceExcluded("default") {
		t.Error("isNamespaceExcluded(\"default\") = true; want false (not configured)")
	}
}
