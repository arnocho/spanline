//go:build airgap

package narrate

import (
	"strings"
	"testing"
)

// This file only compiles under the airgap tag: go test -tags airgap ./internal/narrate/
func TestAirgapBuildRefusesTheHostedBackends(t *testing.T) {
	for _, backend := range []string{"anthropic", "Anthropic", "openai"} {
		_, err := New(Options{Backend: backend, BaseURL: "https://api.example/v1", Model: "m", APIKeyEnv: "K"})
		if err == nil {
			t.Fatalf("an air gapped build accepted backend %q", backend)
		}
		if !strings.Contains(err.Error(), "air gapped") {
			t.Errorf("New(%q) = %v, want an error that says the build is air gapped", backend, err)
		}
	}
	if _, err := New(Options{Backend: "openai-compat", BaseURL: "https://vllm.enclave:8000/v1", Model: "m", APIKeyEnv: "K"}); err != nil {
		t.Errorf("an air gapped build refused the in-enclave OpenAI compatible shape: %v", err)
	}
	n, err := New(Options{Backend: "none"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := n.(noop); !ok {
		t.Errorf("New(none) = %T, want the narrator that sends nothing", n)
	}
}
