package openrouter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/juancer/nexo/internal/assistant"
)

const key = "sk-or-TEST-KEY-NOT-REAL-0123456789"

func server(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := New(srv.URL, key)
	c.SetRetry(2, 5*time.Millisecond)
	return c
}

func reply(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, msg)
}

func TestChatSendsToolsAndAuth(t *testing.T) {
	var got map[string]any
	var auth string
	c := server(t, func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		json.NewDecoder(r.Body).Decode(&got)
		reply(w, `{"choices":[{"message":{"role":"assistant","content":"Hello!"}}]}`)
	})
	msg, err := c.Chat(context.Background(), assistant.ChatRequest{
		Model:    "openai/gpt-oss-20b:free",
		Messages: []assistant.Message{{Role: "system", Content: "be kind"}, {Role: "user", Content: "hi"}},
		Tools:    []assistant.Tool{{Name: "set_volume", Description: "d", Parameters: map[string]any{"type": "object"}}},
	})
	if err != nil || msg.Content != "Hello!" {
		t.Fatalf("Chat = %+v, %v", msg, err)
	}
	if auth != "Bearer "+key || got["model"] != "openai/gpt-oss-20b:free" {
		t.Fatalf("auth=%q body=%v", auth, got)
	}
	tools := got["tools"].([]any)
	fn := tools[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "set_volume" || got["tool_choice"] != "auto" {
		t.Fatalf("tools = %v", tools)
	}
}

func TestToolCallsAreParsedAndToolResultsSent(t *testing.T) {
	var second map[string]any
	calls := 0
	c := server(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			reply(w, `{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[
				{"id":"call_1","type":"function","function":{"name":"set_volume","arguments":"{\"level\":30}"}}]}}]}`)
			return
		}
		json.NewDecoder(r.Body).Decode(&second)
		reply(w, `{"choices":[{"message":{"role":"assistant","content":"Done"}}]}`)
	})
	first, err := c.Chat(context.Background(), assistant.ChatRequest{Model: "m", Messages: []assistant.Message{{Role: "user", Content: "quieter"}}})
	if err != nil || len(first.ToolCalls) != 1 || first.ToolCalls[0].Name != "set_volume" || first.ToolCalls[0].Arguments != `{"level":30}` {
		t.Fatalf("first = %+v, %v", first, err)
	}
	_, err = c.Chat(context.Background(), assistant.ChatRequest{Model: "m", Messages: []assistant.Message{
		{Role: "user", Content: "quieter"}, first, {Role: "tool", ToolCallID: "call_1", Content: `{"ok":true}`},
	}})
	if err != nil {
		t.Fatal(err)
	}
	msgs := second["messages"].([]any)
	asst := msgs[1].(map[string]any)
	if _, has := asst["content"]; has && asst["content"] != nil {
		t.Fatalf("assistant tool-call message must not carry empty content: %v", asst)
	}
	if asst["tool_calls"].([]any)[0].(map[string]any)["id"] != "call_1" || msgs[2].(map[string]any)["tool_call_id"] != "call_1" {
		t.Fatalf("tool round trip malformed: %v", msgs)
	}
}

func TestRetriesRateLimitThenSucceeds(t *testing.T) {
	var n atomic.Int32
	c := server(t, func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) < 3 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		reply(w, `{"choices":[{"message":{"role":"assistant","content":"finally"}}]}`)
	})
	msg, err := c.Chat(context.Background(), assistant.ChatRequest{Model: "m", Messages: []assistant.Message{{Role: "user", Content: "x"}}})
	if err != nil || msg.Content != "finally" || n.Load() != 3 {
		t.Fatalf("msg=%+v err=%v calls=%d", msg, err, n.Load())
	}
}

func TestGivesUpGracefullyOnPersistentFailure(t *testing.T) {
	for name, h := range map[string]http.HandlerFunc{
		"429":     func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(429) },
		"500":     func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) },
		"garbage": func(w http.ResponseWriter, r *http.Request) { reply(w, "<html>") },
		"error 200": func(w http.ResponseWriter, r *http.Request) {
			reply(w, `{"error":{"message":"Provider returned error"}}`)
		},
		"no choice": func(w http.ResponseWriter, r *http.Request) { reply(w, `{"choices":[]}`) },
	} {
		c := server(t, h)
		_, err := c.Chat(context.Background(), assistant.ChatRequest{Model: "m", Messages: []assistant.Message{{Role: "user", Content: "x"}}})
		if !errors.Is(err, assistant.ErrUnavailable) {
			t.Errorf("%s: err = %v, want ErrUnavailable", name, err)
		}
	}
}

func TestPermanentErrorsAreNotRetriedAndNeverLeakTheKey(t *testing.T) {
	var n atomic.Int32
	c := server(t, func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		reply(w, `{"error":{"message":"invalid api key"}}`)
	})
	_, err := c.Chat(context.Background(), assistant.ChatRequest{Model: "m", Messages: []assistant.Message{{Role: "user", Content: "x"}}})
	if err == nil || errors.Is(err, assistant.ErrUnavailable) || n.Load() != 1 {
		t.Fatalf("err=%v calls=%d", err, n.Load())
	}
	if strings.Contains(err.Error(), key) {
		t.Fatal("API key leaked into the error")
	}
	dead := New("http://127.0.0.1:1", key)
	dead.SetRetry(0, time.Millisecond)
	_, err = dead.Chat(context.Background(), assistant.ChatRequest{Model: "m", Messages: []assistant.Message{{Role: "user", Content: "x"}}})
	if err == nil || strings.Contains(err.Error(), key) {
		t.Fatalf("network error leaks key: %v", err)
	}
}

func TestMissingKeyAndCancellation(t *testing.T) {
	if _, err := New("", "").Chat(context.Background(), assistant.ChatRequest{}); !errors.Is(err, assistant.ErrNotConfigured) {
		t.Fatalf("err = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := server(t, func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body) // lets the server notice the client hanging up
		<-r.Context().Done()
	})
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	start := time.Now()
	_, err := c.Chat(ctx, assistant.ChatRequest{Model: "m", Messages: []assistant.Message{{Role: "user", Content: "x"}}})
	if err == nil || time.Since(start) > 3*time.Second {
		t.Fatalf("cancellation not honoured: %v after %v", err, time.Since(start))
	}
}

func TestProviderErrorTextIsTruncated(t *testing.T) {
	huge := strings.Repeat("A", 1<<20)
	for name, h := range map[string]http.HandlerFunc{
		"refused": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			reply(w, `{"error":{"message":"`+huge+`"}}`)
		},
		"error in 200": func(w http.ResponseWriter, r *http.Request) {
			reply(w, `{"error":{"message":"`+huge+`"}}`)
		},
	} {
		c := server(t, h)
		_, err := c.Chat(context.Background(), assistant.ChatRequest{Model: "m", Messages: []assistant.Message{{Role: "user", Content: "x"}}})
		if err == nil || len(err.Error()) > 1500 {
			t.Errorf("%s: error of %d bytes", name, len(fmt.Sprint(err)))
		}
	}
}

func TestOversizedToolCallArgumentsAreRejected(t *testing.T) {
	c := server(t, func(w http.ResponseWriter, r *http.Request) {
		args, _ := json.Marshal(strings.Repeat("a", 9<<10))
		reply(w, `{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[
			{"id":"c","type":"function","function":{"name":"set_volume","arguments":`+string(args)+`}}]}}]}`)
	})
	if _, err := c.Chat(context.Background(), assistant.ChatRequest{Model: "m", Messages: []assistant.Message{{Role: "user", Content: "x"}}}); err == nil {
		t.Fatal("an 9 KiB tool call was accepted")
	}
}
