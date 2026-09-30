package web

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/juancer/milo/internal/auth"
	"github.com/juancer/milo/internal/skills"
)

// The Spotify endpoints need a session; changing what Milo runs needs the owner.
func TestSpotifyEndpoints(t *testing.T) {
	e := newEnv(t, true)
	ctx := context.Background()

	if resp := e.do(t, "GET", "/api/spotify", "", nil, nil); resp.StatusCode != 401 {
		t.Fatalf("without a session = %d, want 401", resp.StatusCode)
	}
	owner := e.login(t)
	resp := e.do(t, "GET", "/api/spotify", "", owner, nil)
	var info skills.SpotifyInfo
	if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&info) != nil || info.Binary != skills.DefaultSpotifyBinary || info.VoiceControl {
		t.Fatalf("GET = %d %+v, want 200, the default binary and voice control off", resp.StatusCode, info)
	}

	hash, _ := auth.HashPassword(testPassword)
	if _, err := e.store.CreateAdmin(ctx, "helper", hash, false, false); err != nil {
		t.Fatal(err)
	}
	helper := e.loginAs(t, "helper", testPassword)
	if resp := e.do(t, "PUT", "/api/spotify", `{"voice_control":true}`, helper, nil); resp.StatusCode != 403 {
		t.Fatalf("PUT as a non-owner = %d, want 403", resp.StatusCode)
	}
	if resp := e.do(t, "PUT", "/api/spotify", `{"binary":"relative/path"}`, owner, nil); resp.StatusCode != 400 {
		t.Fatalf("a relative binary path = %d, want 400", resp.StatusCode)
	}
	if resp := e.do(t, "PUT", "/api/spotify", `{"binary":"/opt/go-librespot","voice_control":true}`, owner, nil); resp.StatusCode != 200 {
		t.Fatalf("PUT = %d, want 200", resp.StatusCode)
	}
	if got, _ := e.store.GetSetting(ctx, skills.SettingSpotifyBinary); got != "/opt/go-librespot" {
		t.Fatalf("binary setting = %q", got)
	}
	if got, _ := e.store.GetSetting(ctx, skills.SettingSpotifyVoice); got != "1" {
		t.Fatalf("voice setting = %q", got)
	}
	if resp := e.do(t, "POST", "/api/spotify/signout", "", helper, nil); resp.StatusCode != 200 {
		t.Fatalf("sign out = %d, want 200", resp.StatusCode)
	}
}
