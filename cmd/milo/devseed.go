package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/juancer/milo/internal/audio"
	"github.com/juancer/milo/internal/auth"
	"github.com/juancer/milo/internal/secrets"
	"github.com/juancer/milo/internal/store"
	"github.com/juancer/milo/internal/system"
	"github.com/juancer/milo/internal/telegram"
)

// devSeed prepares a demo installation for local development: an admin
// password, the simulator's FAKE bot token, a demo family and enabled
// features. It refuses to run against real hardware and only runs once per
// database, so it can never touch a real device or overwrite parent settings.
func devSeed(ctx context.Context, cfg system.Config) error {
	if cfg.Hardware != "mock" {
		return fmt.Errorf("dev-seed only runs with MILO_HARDWARE=mock; it will never modify a real installation")
	}
	st, err := store.Open(ctx, cfg.DBPath())
	if err != nil {
		return err
	}
	defer st.Close()
	if _, err := st.GetSetting(ctx, "dev.seeded"); err == nil {
		fmt.Println("dev-seed: already done")
		return nil
	}
	key, err := secrets.LoadKey(cfg.KeyPath)
	if err != nil {
		return err
	}
	vault, err := secrets.New(key, st)
	if err != nil {
		return err
	}

	password, err := devPassword()
	if err != nil {
		return err
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	// Demo accounts. Real devices never get these: dev-seed refuses to run unless
	// the hardware is mocked.
	for _, a := range []struct {
		name  string
		owner bool
	}{{"admin", true}, {"mom", false}} {
		if _, err := st.GetAdminByName(ctx, a.name); err != nil {
			if _, err := st.CreateAdmin(ctx, a.name, hash, a.owner, false); err != nil {
				return err
			}
		} else if a.owner {
			owner, _ := st.GetAdminByName(ctx, a.name)
			st.SetAdminPassword(ctx, owner.ID, hash, false) //nolint:errcheck
		}
	}

	// A fake token that only the simulator accepts.
	token := getenv("MILO_SIM_BOT_TOKEN", "123456:DEMO-TOKEN-NOT-A-REAL-BOT-abcdefghijk")
	if err := vault.Put(ctx, telegram.SecretBotToken, []byte(token)); err != nil {
		return err
	}

	// The assistant talks to the simulator's fake OpenAI-compatible endpoints.
	// These keys are placeholders that only the simulator accepts.
	simURLBase := getenv("MILO_SIM_URL", "http://sim:8081")
	simURL := simURLBase + "/v1"
	if err := vault.Put(ctx, "assistant.llm_api_key", []byte("sim-llm-key-not-real")); err != nil {
		return err
	}
	if err := vault.Put(ctx, "assistant.stt_api_key", []byte("sim-stt-key-not-real")); err != nil {
		return err
	}
	g, err := st.GetGPTSettings(ctx)
	if err != nil {
		return err
	}
	g.LLMBaseURL, g.STTBaseURL, g.ChildName, g.Age = simURL, simURL, "Sam", 7
	if err := st.UpdateGPTSettings(ctx, g); err != nil {
		return err
	}

	// Skills: a small music folder of public-domain melodies, and the simulator's
	// fake weather service and calendar.
	musicDir := getenv("MILO_DEV_MUSIC", filepath.Join(cfg.DataDir, "music"))
	if err := writeDemoMusic(musicDir); err != nil {
		return err
	}
	st.SetSetting(ctx, "music.dir", musicDir) //nolint:errcheck
	for k, v := range map[string]string{
		"weather.api_base": simURLBase + "/weather", "weather.geo_base": simURLBase + "/weather",
		"weather.latitude": "40.4168", "weather.longitude": "-3.7038", "weather.place": "Madrid, Demoland",
		"system.timezone": getenv("MILO_DEV_TIMEZONE", "Europe/Madrid"),
	} {
		st.SetSetting(ctx, k, v) //nolint:errcheck
	}
	if err := vault.Put(ctx, "calendar.ics_url", []byte(simURLBase+"/calendar.ics")); err != nil {
		return err
	}

	family := []struct {
		name, role, language string // language "" follows the notification language
		tg                   int64
	}{
		{"Dad", "PARENT", "en", 1001},
		{"Mum", "PARENT", "es", 1002},
		{"Grandma", "FAMILY", "", 1003},
	}
	for _, f := range family {
		uid, err := st.CreateTelegramUser(ctx, f.name, f.role, f.tg, f.name+"_demo")
		if err != nil {
			return err
		}
		if err := st.SetUserLanguage(ctx, uid, f.language); err != nil {
			return err
		}
		if _, err := st.CreateChat(ctx, store.Chat{Name: f.name, Kind: "user", UserID: uid, TelegramChatID: f.tg,
			Favorite: true, Position: int(f.tg - 1000), Enabled: true}); err != nil {
			return err
		}
	}
	for _, name := range devFeatures {
		st.EnsureFeature(ctx, name)           //nolint:errcheck
		st.SetFeatureEnabled(ctx, name, true) //nolint:errcheck
	}
	st.SetSetting(ctx, "dev.seeded", "1") //nolint:errcheck
	// Printed once: it is not stored anywhere in the clear. Set MILO_DEV_PASSWORD to choose it.
	fmt.Printf("dev-seed: sign in as admin or mom with password %q (shown once; set MILO_DEV_PASSWORD to choose your own); demo family Dad/Mum/Grandma; features enabled\n", password)
	return nil
}

// devFeatures are switched on in the demo. Later phases append to this list.
var devFeatures = []string{"telegram", "assistant", "wakeword", "timer", "reminders", "music", "weather", "calendar"}

// devPassword is MILO_DEV_PASSWORD, or a random one when it is unset, so a demo
// installation never has a password that is the same everywhere.
func devPassword() (string, error) {
	if p := os.Getenv("MILO_DEV_PASSWORD"); p != "" {
		return p, nil
	}
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// writeDemoMusic renders a few public-domain nursery melodies as WAV files so
// the music skill has something to play in the demo.
func writeDemoMusic(dir string) error {
	if err := os.MkdirAll(filepath.Join(dir, "Nursery rhymes"), 0o755); err != nil {
		return err
	}
	const (
		c4, d4, e4, f4, g4, a4, b4, c5 = 261.63, 293.66, 329.63, 349.23, 392.00, 440.00, 493.88, 523.25
	)
	type note struct {
		freq float64
		beat float64
	}
	songs := map[string][]note{
		"Twinkle Twinkle Little Star": {{c4, 1}, {c4, 1}, {g4, 1}, {g4, 1}, {a4, 1}, {a4, 1}, {g4, 2}, {f4, 1}, {f4, 1}, {e4, 1}, {e4, 1}, {d4, 1}, {d4, 1}, {c4, 2}},
		"Ode to Joy":                  {{e4, 1}, {e4, 1}, {f4, 1}, {g4, 1}, {g4, 1}, {f4, 1}, {e4, 1}, {d4, 1}, {c4, 1}, {c4, 1}, {d4, 1}, {e4, 1}, {e4, 1.5}, {d4, 0.5}, {d4, 2}},
		"Happy Birthday":              {{g4, 0.75}, {g4, 0.25}, {a4, 1}, {g4, 1}, {c5, 1}, {b4, 2}, {g4, 0.75}, {g4, 0.25}, {a4, 1}, {g4, 1}, {d4 * 2, 1}, {c5, 2}},
		"Frere Jacques":               {{c4, 1}, {d4, 1}, {e4, 1}, {c4, 1}, {c4, 1}, {d4, 1}, {e4, 1}, {c4, 1}, {e4, 1}, {f4, 1}, {g4, 2}, {e4, 1}, {f4, 1}, {g4, 2}},
	}
	for title, melody := range songs {
		var pcm []byte
		for round := 0; round < 2; round++ {
			for _, n := range melody {
				d := time.Duration(n.beat * 380 * float64(time.Millisecond))
				pcm = append(pcm, audio.Tone(n.freq, d-30*time.Millisecond)...)
				pcm = append(pcm, audio.Tone(0, 30*time.Millisecond)...)
			}
		}
		path := filepath.Join(dir, "Nursery rhymes", title+".wav")
		if err := os.WriteFile(path, audio.EncodeWAV(pcm, audio.SampleRate), 0o644); err != nil {
			return err
		}
	}
	return nil
}
