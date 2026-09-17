//go:build !airgap

package narrate

import "testing"

// The default build links both wire shapes; the airgap build has its own test file.
func TestHostedBuildAcceptsBothWireShapes(t *testing.T) {
	for _, backend := range []string{"openai-compat", "anthropic"} {
		if _, err := New(Options{Backend: backend, BaseURL: "https://api.example/v1", Model: "m", APIKeyEnv: "K"}); err != nil {
			t.Errorf("New(%q) = %v, want a narrator", backend, err)
		}
	}
	if _, err := New(Options{Backend: "openai", BaseURL: "https://api.example/v1", Model: "m", APIKeyEnv: "K"}); err == nil {
		t.Error("New accepted a backend name that is not on the list")
	}
}
