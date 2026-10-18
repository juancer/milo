package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestFilterModelsKeepsWhatFitsTheJob(t *testing.T) {
	ids := []string{"openai/gpt-oss-20b", "whisper-large-v3-turbo", "playai-tts", "llama-3.3-70b-versatile", "meta-llama/llama-guard-4-12b", "text-embedding-3"}
	if got := filterModels("llm", ids); !reflect.DeepEqual(got, []string{"openai/gpt-oss-20b", "llama-3.3-70b-versatile"}) {
		t.Errorf("llm = %v", got)
	}
	if got := filterModels("stt", ids); !reflect.DeepEqual(got, []string{"whisper-large-v3-turbo"}) {
		t.Errorf("stt = %v", got)
	}
	if got := filterModels("tts", ids); !reflect.DeepEqual(got, []string{"playai-tts"}) {
		t.Errorf("tts = %v", got)
	}
	if got := filterModels("stt", []string{"a", "b"}); len(got) != 2 {
		t.Errorf("nothing matched, want everything back, got %v", got)
	}
}

func TestFetchModelIDsSendsTheKeyAndDoesNotFollowRedirects(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			auth = r.Header.Get("Authorization")
			w.Write([]byte(`{"data":[{"id":"b-model"},{"id":"a-model"},{"id":"bad id with spaces"},{"id":""}]}`))
		case "/redirect/models":
			http.Redirect(w, r, "http://127.0.0.1:1/steal", http.StatusFound)
		}
	}))
	defer srv.Close()
	ids, err := fetchModelIDs(context.Background(), srv.URL+"/v1/", "gsk_secret_key")
	if err != nil || !reflect.DeepEqual(ids, []string{"a-model", "b-model"}) {
		t.Fatalf("ids = %v, %v", ids, err)
	}
	if auth != "Bearer gsk_secret_key" {
		t.Errorf("Authorization = %q", auth)
	}
	if _, err := fetchModelIDs(context.Background(), srv.URL+"/redirect", "gsk_secret_key"); err == nil {
		t.Error("a redirect was followed or accepted")
	}
}
