package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

type anthropicClient struct{ transport }

func (c *anthropicClient) Stream(ctx context.Context, req Request) (<-chan Delta, error) {
	messages := make([]chatMessage, 0, len(req.Messages))
	for _, m := range req.Messages {
		messages = append(messages, chatMessage{Role: string(m.Role), Content: m.Text})
	}
	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = 1024
	}
	body := struct {
		Model       string        `json:"model"`
		System      string        `json:"system,omitempty"`
		Messages    []chatMessage `json:"messages"`
		Stream      bool          `json:"stream"`
		MaxTokens   int           `json:"max_tokens"`
		Temperature float64       `json:"temperature"`
	}{req.Model, req.System, messages, true, maxTokens, req.Temperature}
	headers := make(http.Header)
	headers.Set("anthropic-version", "2023-06-01")
	if c.key != "" {
		headers.Set("x-api-key", c.key)
	}
	return c.start(ctx, body, headers, decodeAnthropic)
}

func decodeAnthropic(event sseEvent) (Delta, bool, error) {
	var chunk struct {
		Type    string    `json:"type"`
		Error   *apiError `json:"error"`
		Message struct {
			Model string `json:"model"`
		} `json:"message"`
		Delta struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
	}
	if err := json.Unmarshal([]byte(event.data), &chunk); err != nil {
		return Delta{}, false, fmt.Errorf("invalid Anthropic event: %w", err)
	}
	if chunk.Error != nil {
		return Delta{}, false, fmt.Errorf("%w: %s", chunk.Error.kind(), chunk.Error.Message)
	}
	kind := chunk.Type
	if kind == "" {
		kind = event.name
	}
	switch kind {
	case "message_start":
		return Delta{Model: chunk.Message.Model}, false, nil
	case "content_block_delta":
		if chunk.Delta.Type == "text_delta" {
			return Delta{Text: chunk.Delta.Text}, false, nil
		}
	case "message_stop":
		return Delta{}, true, nil
	case "error":
		return Delta{}, false, fmt.Errorf("%w: Anthropic stream error", ErrUpstream)
	}
	return Delta{}, false, nil
}
