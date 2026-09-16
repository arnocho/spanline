//go:build airgap

// This is the air gapped build. The vendor clients are not compiled in at all. What remains is
// the generic OpenAI compatible shape, which is what a model hosted inside the enclave speaks,
// and it still cannot send anything unless its host was named in the allowlist.

package narrate

import (
	"encoding/json"
	"fmt"
	"strings"
)

// newWire refuses every backend a binary built with the airgap tag must not reach. Only "none",
// handled before this call, and an OpenAI compatible endpoint inside the enclave are left.
func newWire(backend string) (wire, error) {
	if backend == "openai-compat" {
		return openAIWire{}, nil
	}
	return nil, fmt.Errorf(
		"narrate: backend %q is not available in an air gapped build, use none or an openai-compat endpoint inside the enclave",
		backend,
	)
}

// openAIWire speaks the OpenAI chat completions shape, the one protocol a self hosted gateway
// can be expected to serve without any vendor code.
type openAIWire struct{}

func (openAIWire) name() string { return "openai-compat" }

func (openAIWire) url(base string) string { return joinURL(base, "/chat/completions") }

func (openAIWire) body(model, prompt string) ([]byte, error) {
	return json.Marshal(map[string]any{
		"model":       model,
		"temperature": 0,
		"max_tokens":  700,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
	})
}

func (openAIWire) headers(key string) map[string]string {
	return map[string]string{
		"content-type":  "application/json",
		"authorization": "Bearer " + key,
	}
}

func (openAIWire) text(raw []byte) (string, error) {
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("the answer was not the expected JSON: %w", err)
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("the answer carried no choice")
	}
	return strings.TrimSpace(out.Choices[0].Message.Content), nil
}
