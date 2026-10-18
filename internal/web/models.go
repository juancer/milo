package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/juancer/milo/internal/app"
	"github.com/juancer/milo/internal/secrets"
)

// listProviderModels asks a provider for the models it offers (the OpenAI-compatible GET
// /models), so the admin can pick one instead of typing its name. It only ever talks to the
// address that is already saved for that provider, with the key saved for it: a stored key must
// not be sent to an address typed into a form and not yet saved.
func (s *server) listProviderModels(w http.ResponseWriter, r *http.Request) {
	kind := r.URL.Query().Get("kind")
	g, err := s.app.Store.GetGPTSettings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "web.err.internal_error")
		return
	}
	var base, secret string
	switch kind {
	case "llm":
		base, secret = g.LLMBaseURL, app.SecretLLMKey
	case "stt":
		base, secret = g.STTBaseURL, app.SecretSTTKey
	case "tts":
		base, secret = g.TTSBaseURL, app.SecretTTSKey
	default:
		writeError(w, http.StatusBadRequest, "web.err.internal_error")
		return
	}
	if !s.validAPIURL(base) {
		writeError(w, http.StatusUnprocessableEntity, "web.err.save_the_provider_address_first")
		return
	}
	key, err := s.app.Vault.Get(r.Context(), secret)
	if errors.Is(err, secrets.ErrNotFound) || (err == nil && len(key) == 0) {
		writeError(w, http.StatusUnprocessableEntity, "web.err.save_the_provider_key_first")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "web.err.internal_error")
		return
	}
	ids, err := fetchModelIDs(r.Context(), base, string(key))
	if err != nil {
		writeError(w, http.StatusBadGateway, "web.err.the_provider_did_not_list_its_models")
		return
	}
	writeJSON(w, http.StatusOK, filterModels(kind, ids))
}

func fetchModelIDs(ctx context.Context, base, key string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }} // a redirect could carry the key elsewhere
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("provider answered " + resp.Status)
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&body); err != nil {
		return nil, err
	}
	var ids []string
	for _, m := range body.Data {
		if id := strings.TrimSpace(m.ID); id != "" && len(id) <= 100 && modelRe.MatchString(id) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

// filterModels keeps the models that fit the job: providers list chat, speech and voice models
// together. When the filter would leave nothing, everything is returned.
func filterModels(kind string, ids []string) []string {
	has := func(id string, words ...string) bool {
		id = strings.ToLower(id)
		for _, w := range words {
			if strings.Contains(id, w) {
				return true
			}
		}
		return false
	}
	var out []string
	for _, id := range ids {
		var keep bool
		switch kind {
		case "stt":
			keep = has(id, "whisper", "transcribe", "stt", "speech-to-text")
		case "tts":
			keep = has(id, "tts", "speech")
		default:
			keep = !has(id, "whisper", "tts", "embed", "guard", "moderation", "transcribe", "speech", "rerank", "playai", "image", "dall-e")
		}
		if keep {
			out = append(out, id)
		}
	}
	if len(out) == 0 {
		return ids
	}
	return out
}
