//go:build !airgap

// This file holds the two vendor HTTP clients. It is excluded from an air gapped build by the
// airgap tag, so such a binary cannot even link a client it is not allowed to use.

package narrate

import (
	"encoding/json"
	"fmt"
	"strings"
)

// newWire picks the vendor client for a backend. Everything else is refused by name.
func newWire(backend string) (wire, error) {
	switch backend {
	case "openai-compat":
		return openAIWire{}, nil
	case "anthropic":
		return anthropicWire{}, nil
	}
	return nil, fmt.Errorf("narrate: unknown backend %q, expected none, openai-compat or anthropic", backend)
}

// openAIWire speaks the OpenAI chat completions shape, which every self hosted gateway and
// most vendors accept. It is the backend an on premise deployment uses.
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

// anthropicWire speaks the Anthropic messages shape.
type anthropicWire struct{}

func (anthropicWire) name() string { return "anthropic" }

func (anthropicWire) url(base string) string { return joinURL(base, "/v1/messages") }

func (anthropicWire) body(model, prompt string) ([]byte, error) {
	return json.Marshal(map[string]any{
		"model":       model,
		"max_tokens":  700,
		"temperature": 0,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
	})
}

func (anthropicWire) headers(key string) map[string]string {
	return map[string]string{
		"content-type":      "application/json",
		"x-api-key":         key,
		"anthropic-version": "2023-06-01",
	}
}

func (anthropicWire) text(raw []byte) (string, error) {
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("the answer was not the expected JSON: %w", err)
	}
	var b strings.Builder
	for _, block := range out.Content {
		if block.Type != "text" {
			continue
		}
		b.WriteString(block.Text)
	}
	answer := strings.TrimSpace(b.String())
	if answer == "" {
		return "", fmt.Errorf("the answer carried no text block")
	}
	return answer, nil
}
