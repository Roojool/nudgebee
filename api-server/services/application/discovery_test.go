package application

import (
	"nudgebee/services/internal/testenv"
	"nudgebee/services/security"
	"os"
	"testing"
)

func TestDiscoverAndUpdateFrameworkAndDashboardAttributes(t *testing.T) {
	testenv.RequireEnv(t, testenv.User, testenv.Tenant, testenv.Account)
	ctxt := security.NewRequestContextForUserTenant(os.Getenv("TEST_USER"), os.Getenv("TEST_TENANT"), nil, nil, nil)

	err := discoverAndUpdateFrameworkAndDashboardAttributes(ctxt, os.Getenv("TEST_USER"), os.Getenv("TEST_ACCOUNT"))

	if err != nil {
		println(err)
		t.Errorf("Test case 1 failed")
	}
}

func TestDiscoverAndUpdateExternalApps(t *testing.T) {
	testenv.RequireEnv(t, testenv.User, testenv.Tenant, testenv.Account)
	ctxt := security.NewRequestContextForUserTenant(os.Getenv("TEST_USER"), os.Getenv("TEST_TENANT"), nil, nil, nil)

	err := discoverAndUpdateExternalApps(ctxt, os.Getenv("TEST_USER"), os.Getenv("TEST_ACCOUNT"))

	if err != nil {
		println(err)
		t.Errorf("Test case 1 failed")
	}
}

func TestDiscoverAndUpdateExternalVMs(t *testing.T) {
	testenv.RequireEnv(t, testenv.User, testenv.Tenant, testenv.Account)
	ctxt := security.NewRequestContextForUserTenant(os.Getenv("TEST_USER"), os.Getenv("TEST_TENANT"), nil, nil, nil)

	err := discoverAndUpdateExternalVMs(ctxt, os.Getenv("TEST_TENANT"), os.Getenv("TEST_ACCOUNT"))

	if err != nil {
		println(err)
		t.Errorf("Test case 1 failed")
	}
}

func TestApplicationNameFromPodName(t *testing.T) {
	cache := map[string]string{
		"default/workspace-3f2504e0-4f89-11d3-9a0c-0305e82c3301": "workspace-controller",
	}

	tests := []struct {
		name     string
		podName  string
		expected string
	}{
		{"cache wins over pattern matching", "workspace-3f2504e0-4f89-11d3-9a0c-0305e82c3301", "workspace-controller"},
		{"uuid suffix", "workspace-8a1f0c2b-4f89-11d3-9a0c-0305e82c3301", "workspace"},
		{"uuid suffix with all digit last group", "workspace-8a1f0c2b-4f89-11d3-9a0c-030582123301", "workspace"},
		{"uuid suffix with dashes in app name", "my-workspace-8a1f0c2b-4f89-11d3-9a0c-0305e82c3301", "my-workspace"},
		{"pod named as a bare uuid is not mistaken for a suffix", "8a1f0c2b-4f89-11d3-9a0c-0305e82c3301", ""},
		{"statefulset ordinal", "kafka-0", "kafka"},
		{"deployment replica set hash", "nginx-7896994969-m5g7c", "nginx"},
		{"deployment replica set hash with dashes in app name", "my-app-6c57d8c464-txp74", "my-app"},
		{"standalone pod with no controller", "workspace", ""},
		{"pod name matching no pattern", "node-exporter-abcde", ""},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actual := applicationNameFromPodName("default", test.podName, cache)
			if actual != test.expected {
				t.Errorf("applicationNameFromPodName(%q) = %q, want %q", test.podName, actual, test.expected)
			}
		})
	}
}
