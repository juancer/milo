// Package assistant is Nexo's AI assistant: it turns what the child says into
// a short spoken answer, letting the model call only registered, validated
// actions. It knows nothing about GPIO, Telegram or SQL: those are reached
// through interfaces and the Action Engine.
package assistant

import (
	"context"
	"errors"
)

// Message is one entry of a chat with the model.
type Message struct {
	Role       string // "system", "user", "assistant" or "tool"
	Content    string
	ToolCalls  []ToolCall // set on assistant messages that request tools
	ToolCallID string     // set on tool messages
}

// ToolCall is the model asking to run an action.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string // JSON object, untrusted
}

// Tool describes an action to the model.
type Tool struct {
	Name        string
	Description string
	Parameters  map[string]any // JSON schema
}

// ChatRequest is sent to an AIProvider.
type ChatRequest struct {
	Model     string
	Messages  []Message
	Tools     []Tool
	MaxTokens int
}

// AIProvider is a chat-completion backend. The engine depends only on this
// interface, so providers are replaceable.
type AIProvider interface {
	Chat(ctx context.Context, req ChatRequest) (Message, error)
}

// Errors.
var (
	// ErrUnavailable means the provider could not answer (network, quota, outage).
	ErrUnavailable = errors.New("the AI service is not available right now")
	// ErrNotConfigured means credentials or settings are missing.
	ErrNotConfigured = errors.New("the assistant is not configured")
)
