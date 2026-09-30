package main

import (
	"encoding/json"
	"testing"
)

func user(s string) []wireMsg { return []wireMsg{{Role: "user", Content: &s}} }

func TestBrainPicksTools(t *testing.T) {
	all := map[string]bool{"set_volume": true, "send_message": true, "get_time": true, "set_timer": true, "read_messages": true}
	cases := []struct {
		say, tool string
		key       string
		want      any
	}{
		{"make the volume louder", "set_volume", "change", float64(15)},
		{"turn it down a bit, quieter please", "set_volume", "change", float64(-15)},
		{"set the volume to 40", "set_volume", "level", float64(40)},
		{"tell Dad that I finished my homework", "send_message", "chat", "Dad"},
		{"send Mum a message saying I love you", "send_message", "text", "I love you"},
		{"send I am hungry to Grandma", "send_message", "chat", "Grandma"},
		{"what time is it", "get_time", "", nil},
		{"set a timer for 5 minutes", "set_timer", "seconds", float64(300)},
		{"read my messages", "read_messages", "", nil},
	}
	for _, c := range cases {
		m := decide(user(c.say), all)
		if len(m.ToolCalls) != 1 || m.ToolCalls[0].Function.Name != c.tool {
			t.Errorf("%q -> %+v, want tool %s", c.say, m, c.tool)
			continue
		}
		if c.key != "" {
			var args map[string]any
			json.Unmarshal([]byte(m.ToolCalls[0].Function.Arguments), &args)
			if args[c.key] != c.want {
				t.Errorf("%q: %s = %v, want %v", c.say, c.key, args[c.key], c.want)
			}
		}
	}
}

func TestBrainOnlyUsesOfferedToolsAndSummarisesResults(t *testing.T) {
	m := decide(user("send Dad hello"), map[string]bool{"set_volume": true})
	if len(m.ToolCalls) != 0 || m.Content == nil {
		t.Fatalf("used a tool that was not offered: %+v", m)
	}
	ok := `{"ok":true,"result":"Volume 30 percent"}`
	if m := decide([]wireMsg{{Role: "user", Content: ptr("quieter")}, {Role: "tool", Content: &ok}}, nil); *m.Content != "Done! Volume 30 percent" {
		t.Fatalf("summary = %q", *m.Content)
	}
	bad := `{"ok":false,"error":"that feature is switched off"}`
	if m := decide([]wireMsg{{Role: "tool", Content: &bad}}, nil); *m.Content == "" {
		t.Fatal("no explanation for a failed tool")
	}
}

func ptr(s string) *string { return &s }
