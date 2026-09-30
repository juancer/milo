package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/juancer/milo/internal/audio"
)

// The "brain" is a small rule-based stand-in for the LLM and the speech-to-text
// API, speaking the same OpenAI-compatible protocol as OpenRouter and Groq. It
// lets the real assistant code path run end to end offline. It is obviously
// not intelligent: it recognises a handful of phrases and asks for the matching
// tool, exactly as a real model would.

type wireCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type wireMsg struct {
	Role       string     `json:"role"`
	Content    *string    `json:"content"`
	ToolCalls  []wireCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type chatReq struct {
	Messages []wireMsg `json:"messages"`
	Tools    []struct {
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	} `json:"tools"`
}

func hasBearer(r *http.Request) bool {
	return strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") && len(r.Header.Get("Authorization")) > 12
}

func (s *sim) chatCompletions(w http.ResponseWriter, r *http.Request) {
	if !hasBearer(r) {
		http.Error(w, `{"error":{"message":"missing API key"}}`, http.StatusUnauthorized)
		return
	}
	var req chatReq
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&req); err != nil || len(req.Messages) == 0 {
		http.Error(w, `{"error":{"message":"bad request"}}`, http.StatusBadRequest)
		return
	}
	tools := map[string]bool{}
	for _, t := range req.Tools {
		tools[t.Function.Name] = true
	}
	msg := decide(req.Messages, tools)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": msg, "finish_reason": "stop"}}})
}

func text(s string) wireMsg { return wireMsg{Role: "assistant", Content: &s} }

func toolCall(name string, args map[string]any) wireMsg {
	b, _ := json.Marshal(args)
	c := wireCall{ID: "call_" + name, Type: "function"}
	c.Function.Name, c.Function.Arguments = name, string(b)
	return wireMsg{Role: "assistant", ToolCalls: []wireCall{c}}
}

var (
	reNumber   = regexp.MustCompile(`\b(\d{1,3})\b`)
	reSendTo   = regexp.MustCompile(`(?i)^(?:please\s+)?(?:send|tell|text|message)\s+(\p{L}+)\s+(?:that\s+|a message\s+(?:saying\s+)?)?(.+)$`)
	reSendTo2  = regexp.MustCompile(`(?i)^(?:please\s+)?send\s+(?:a message\s+)?(.+?)\s+to\s+(\p{L}+)$`)
	reDuration = regexp.MustCompile(`(?i)(\d+)\s*(second|sec|minute|min|hour)`)
	reRemind   = regexp.MustCompile(`(?i)remind me (?:to |about )?(.+?)(?:\s+in\s+(\d+)\s*(second|minute|min|hour)s?)?$`)
	rePlay     = regexp.MustCompile(`(?i)^(?:please\s+)?play\s+(?:the\s+|some\s+)?(.+?)(?:\s+(?:song|music))?$`)
)

func seconds(n int, unit string) int {
	switch strings.ToLower(unit)[0] {
	case 'h':
		return n * 3600
	case 'm':
		return n * 60
	}
	return n
}

// decide returns the next assistant message: a tool call, or words.
func decide(msgs []wireMsg, tools map[string]bool) wireMsg {
	last := msgs[len(msgs)-1]
	if last.Role == "tool" {
		var res struct {
			OK     bool   `json:"ok"`
			Result string `json:"result"`
			Error  string `json:"error"`
		}
		if last.Content != nil {
			json.Unmarshal([]byte(*last.Content), &res)
		}
		if res.OK {
			return text("Done! " + res.Result)
		}
		return text("Sorry, I couldn't do that: " + res.Error + ".")
	}

	utterance := ""
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" && msgs[i].Content != nil {
			utterance = strings.TrimSpace(*msgs[i].Content)
			break
		}
	}
	low := strings.ToLower(utterance)
	need := func(name string, args map[string]any) wireMsg {
		if !tools[name] {
			return text("I can't do that right now.")
		}
		return toolCall(name, args)
	}

	switch {
	case strings.Contains(low, "volume") || strings.Contains(low, "louder") || strings.Contains(low, "quieter"):
		if m := reNumber.FindString(low); m != "" && strings.Contains(low, "volume") {
			n, _ := strconv.Atoi(m)
			if n <= 10 { // "volume 7" is the 0-10 scale
				return need("set_volume", map[string]any{"level_10": n})
			}
			return need("set_volume", map[string]any{"level": min(n, 100)})
		}
		if strings.Contains(low, "quiet") || strings.Contains(low, "down") || strings.Contains(low, "lower") || strings.Contains(low, "soft") {
			return need("set_volume", map[string]any{"change": -15})
		}
		return need("set_volume", map[string]any{"change": 15})
	case reSendTo2.MatchString(utterance):
		m := reSendTo2.FindStringSubmatch(utterance)
		return need("send_message", map[string]any{"chat": m[2], "text": m[1]})
	case reSendTo.MatchString(utterance) && !strings.Contains(low, "timer"):
		m := reSendTo.FindStringSubmatch(utterance)
		return need("send_message", map[string]any{"chat": m[1], "text": m[2]})
	case strings.Contains(low, "how many") && strings.Contains(low, "message"), strings.Contains(low, "any new message"), strings.Contains(low, "any messages"):
		return need("get_pending_messages", map[string]any{})
	case strings.Contains(low, "message") && (strings.Contains(low, "read") || strings.Contains(low, "play")):
		return need("read_messages", map[string]any{})
	case strings.Contains(low, "timer"):
		if m := reDuration.FindStringSubmatch(utterance); m != nil {
			n, _ := strconv.Atoi(m[1])
			return need("set_timer", map[string]any{"seconds": seconds(n, m[2])})
		}
		return text("For how long should I set the timer?")
	case strings.HasPrefix(low, "remind me"):
		if m := reRemind.FindStringSubmatch(utterance); m != nil {
			n, _ := strconv.Atoi(m[2])
			unit := m[3]
			if unit == "" {
				n, unit = 1, "minute"
			}
			return need("create_reminder", map[string]any{"text": m[1], "in_seconds": seconds(n, unit)})
		}
	case strings.Contains(low, "weather"):
		return need("get_weather", map[string]any{})
	case strings.Contains(low, "calendar") || strings.Contains(low, "what's on today") || strings.Contains(low, "what do i have today"):
		return need("get_calendar", map[string]any{})
	case strings.Contains(low, "what time") || strings.Contains(low, "what day"):
		return need("get_time", map[string]any{})
	case strings.Contains(low, "status"):
		return need("get_device_status", map[string]any{})
	case strings.HasPrefix(low, "stop") || strings.Contains(low, "be quiet"):
		if tools["stop_music"] {
			return toolCall("stop_music", map[string]any{})
		}
		return need("stop_audio", map[string]any{})
	case strings.HasPrefix(low, "pause"):
		return need("pause_music", map[string]any{})
	case strings.Contains(low, "next song") || strings.Contains(low, "skip"):
		return need("next_song", map[string]any{})
	case rePlay.MatchString(utterance):
		return need("play_music", map[string]any{"query": rePlay.FindStringSubmatch(utterance)[1]})
	case strings.Contains(low, "joke"):
		return text("Why did the teddy bear say no to dessert? Because she was already stuffed!")
	case strings.Contains(low, "who are you") || strings.Contains(low, "your name"):
		return text("I'm Milo, your friendly helper!")
	case strings.HasPrefix(low, "hello") || strings.HasPrefix(low, "hi") || strings.HasPrefix(low, "hey"):
		return text("Hello! How can I help you today?")
	}
	return text("That's interesting! I'm only a simple demo brain, but I can change the volume, send messages to your family, tell the time and set timers.")
}

// transcribe is the fake speech-to-text endpoint: it reads the words that the
// simulator hides inside its recordings.
func (s *sim) transcribe(w http.ResponseWriter, r *http.Request) {
	if !hasBearer(r) {
		http.Error(w, `{"error":{"message":"missing API key"}}`, http.StatusUnauthorized)
		return
	}
	if err := r.ParseMultipartForm(16 << 20); err != nil {
		http.Error(w, `{"error":{"message":"multipart form expected"}}`, http.StatusBadRequest)
		return
	}
	f, _, err := r.FormFile("file")
	if err != nil {
		http.Error(w, `{"error":{"message":"file is required"}}`, http.StatusBadRequest)
		return
	}
	defer f.Close()
	data, _ := io.ReadAll(io.LimitReader(f, 16<<20))
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"text":%q}`, audio.ExtractText(data))
}
