// Package openrouter is an AIProvider for OpenRouter. The wire format is the
// OpenAI-compatible chat-completions API, so the same client also works with
// Groq and other compatible services by changing the base URL.
package openrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/juancer/nexo/internal/assistant"
)

// DefaultBaseURL is OpenRouter's API.
const DefaultBaseURL = "https://openrouter.ai/api/v1"

// Client calls a chat-completions endpoint. The API key never appears in errors.
type Client struct {
	base    string
	key     string
	http    *http.Client
	retries int
	backoff time.Duration
}

// New returns a client. The free OpenRouter models are rate limited, so calls
// are retried a couple of times with backoff before giving up gracefully.
func New(baseURL, apiKey string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{base: strings.TrimRight(baseURL, "/"), key: apiKey, http: &http.Client{Timeout: 45 * time.Second},
		retries: 2, backoff: time.Second}
}

// SetRetry changes the retry count and first backoff (used by tests).
func (c *Client) SetRetry(retries int, backoff time.Duration) {
	c.retries, c.backoff = retries, backoff
}

type wireMessage struct {
	Role       string     `json:"role"`
	Content    *string    `json:"content"`
	ToolCalls  []wireCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type wireCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type wireTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
	} `json:"function"`
}

// Chat implements assistant.AIProvider.
func (c *Client) Chat(ctx context.Context, req assistant.ChatRequest) (assistant.Message, error) {
	if c.key == "" {
		return assistant.Message{}, assistant.ErrNotConfigured
	}
	body := map[string]any{"model": req.Model, "messages": toWire(req.Messages), "temperature": 0.6}
	if req.MaxTokens > 0 {
		body["max_tokens"] = req.MaxTokens
	}
	if len(req.Tools) > 0 {
		tools := make([]wireTool, 0, len(req.Tools))
		for _, t := range req.Tools {
			w := wireTool{Type: "function"}
			w.Function.Name, w.Function.Description, w.Function.Parameters = t.Name, t.Description, t.Parameters
			tools = append(tools, w)
		}
		body["tools"] = tools
		body["tool_choice"] = "auto"
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return assistant.Message{}, err
	}

	var lastErr error
	for attempt := 0; attempt <= c.retries; attempt++ {
		if attempt > 0 {
			wait := c.backoff << (attempt - 1)
			var re *retryError
			if errors.As(lastErr, &re) && re.after > 0 {
				wait = min(re.after, 10*time.Second)
			}
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return assistant.Message{}, ctx.Err()
			}
		}
		msg, err := c.once(ctx, payload)
		if err == nil {
			return msg, nil
		}
		lastErr = err
		var re *retryError
		if !errors.As(err, &re) {
			return assistant.Message{}, err // permanent: bad key, bad request...
		}
	}
	return assistant.Message{}, fmt.Errorf("%w: %v", assistant.ErrUnavailable, lastErr)
}

type retryError struct {
	msg   string
	after time.Duration
}

func (e *retryError) Error() string { return e.msg }

func (c *Client) once(ctx context.Context, payload []byte) (assistant.Message, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return assistant.Message{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Title", "Nexo") // optional OpenRouter attribution
	resp, err := c.http.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		if ctx.Err() != nil {
			return assistant.Message{}, ctx.Err()
		}
		return assistant.Message{}, &retryError{msg: "network: " + err.Error()}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))

	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		after, _ := strconv.Atoi(resp.Header.Get("Retry-After"))
		return assistant.Message{}, &retryError{msg: fmt.Sprintf("status %d", resp.StatusCode), after: time.Duration(after) * time.Second}
	}
	if resp.StatusCode != http.StatusOK {
		return assistant.Message{}, fmt.Errorf("AI provider refused the request (status %d): %s", resp.StatusCode, apiMessage(raw))
	}
	var out struct {
		Choices []struct {
			Message wireMessage `json:"message"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
			Code    any    `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return assistant.Message{}, &retryError{msg: "unreadable response"}
	}
	if out.Error != nil && len(out.Choices) == 0 { // some providers report rate limits in a 200 body
		return assistant.Message{}, &retryError{msg: "provider error: " + out.Error.Message}
	}
	if len(out.Choices) == 0 {
		return assistant.Message{}, &retryError{msg: "empty response"}
	}
	return fromWire(out.Choices[0].Message), nil
}

func apiMessage(raw []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &e) == nil && e.Error.Message != "" {
		return e.Error.Message
	}
	return "no details"
}

func toWire(msgs []assistant.Message) []wireMessage {
	out := make([]wireMessage, 0, len(msgs))
	for _, m := range msgs {
		w := wireMessage{Role: m.Role, ToolCallID: m.ToolCallID}
		content := m.Content
		if m.Content != "" || len(m.ToolCalls) == 0 {
			w.Content = &content
		}
		for _, tc := range m.ToolCalls {
			c := wireCall{ID: tc.ID, Type: "function"}
			c.Function.Name, c.Function.Arguments = tc.Name, tc.Arguments
			w.ToolCalls = append(w.ToolCalls, c)
		}
		out = append(out, w)
	}
	return out
}

func fromWire(w wireMessage) assistant.Message {
	m := assistant.Message{Role: "assistant"}
	if w.Content != nil {
		m.Content = *w.Content
	}
	for _, c := range w.ToolCalls {
		m.ToolCalls = append(m.ToolCalls, assistant.ToolCall{ID: c.ID, Name: c.Function.Name, Arguments: c.Function.Arguments})
	}
	return m
}
