package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

type openAIClient struct{ transport }

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func (c *openAIClient) Stream(ctx context.Context, req Request) (<-chan Delta, error) {
	messages := make([]chatMessage, 0, len(req.Messages)+1)
	if req.System != "" {
		messages = append(messages, chatMessage{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		messages = append(messages, chatMessage{Role: string(m.Role), Content: m.Text})
	}
	body := struct {
		Model       string        `json:"model"`
		Messages    []chatMessage `json:"messages"`
		Stream      bool          `json:"stream"`
		MaxTokens   int           `json:"max_tokens,omitempty"`
		Temperature float64       `json:"temperature"`
	}{req.Model, messages, true, req.MaxTokens, req.Temperature}
	headers := make(http.Header)
	if c.key != "" {
		headers.Set("Authorization", "Bearer "+c.key)
	}
	return c.start(ctx, body, headers, decodeOpenAI)
}

func decodeOpenAI(event sseEvent) (Delta, bool, error) {
	if strings.TrimSpace(event.data) == "[DONE]" {
		return Delta{}, true, nil
	}
	var chunk struct {
		Model   string    `json:"model"`
		Error   *apiError `json:"error"`
		Choices []struct {
			Index int `json:"index"`
			Delta struct {
				Content string `json:"content"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(event.data), &chunk); err != nil {
		return Delta{}, false, fmt.Errorf("invalid OpenAI event: %w", err)
	}
	if chunk.Error != nil {
		return Delta{}, false, fmt.Errorf("%w: %s", chunk.Error.kind(), chunk.Error.Message)
	}
	d := Delta{Model: chunk.Model}
	for _, choice := range chunk.Choices {
		if choice.Index == 0 {
			d.Text = choice.Delta.Content
			break
		}
	}
	return d, false, nil
}
