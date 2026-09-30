package web

import (
	"context"
	"encoding/json"
	"testing"
)

func TestChangingProviderHostClearsItsKey(t *testing.T) {
	e := newEnv(t, true)
	cookie := e.login(t)
	get := decode[struct {
		Settings assistantDTO `json:"settings"`
	}](t, e.do(t, "GET", "/api/assistant", "", cookie, nil))
	d := get.Settings
	if resp := e.do(t, "PUT", "/api/assistant/keys", `{"llm_api_key":"sk-or-v1-SOME-LONG-KEY","stt_api_key":"sk-or-v1-OTHER-LONG-KEY"}`, cookie, nil); resp.StatusCode != 200 {
		t.Fatalf("keys = %d", resp.StatusCode)
	}
	put := func(x assistantDTO) map[string]any {
		b, _ := json.Marshal(x)
		return decode[map[string]any](t, e.do(t, "PUT", "/api/assistant", string(b), cookie, nil))
	}
	// Same host, different path: keys stay.
	same := d
	same.LLMBaseURL = d.LLMBaseURL + "/v2"
	if out := put(same); out["keys_cleared"] != nil || out["llm_key_configured"] != true {
		t.Fatalf("same host cleared the key: %v", out)
	}
	// New host: only that key goes, and the admin is told.
	moved := same
	moved.LLMBaseURL = "https://other.example/v1"
	out := put(moved)
	if out["llm_key_configured"] != false || out["stt_key_configured"] != true || out["keys_cleared"] == nil || out["notice"] == nil {
		t.Fatalf("after moving the host: %v", out)
	}
	if v, _ := e.store.GetSecret(context.Background(), "assistant.llm_api_key"); len(v) != 0 {
		t.Fatal("old key still stored")
	}
}
