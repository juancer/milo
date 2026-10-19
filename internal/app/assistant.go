package app

import (
	"context"
	"errors"
	"fmt"
	"github.com/juancer/milo/internal/voices"
	"path/filepath"
	"time"

	"github.com/juancer/milo/internal/assistant"
	"github.com/juancer/milo/internal/i18n"
	"github.com/juancer/milo/internal/notifications"
	"github.com/juancer/milo/internal/openrouter"
	"github.com/juancer/milo/internal/permissions"
	"github.com/juancer/milo/internal/secrets"
	"github.com/juancer/milo/internal/speech"
	"github.com/juancer/milo/internal/store"
)

// Secret names for the assistant's providers.
const (
	SecretLLMKey = "assistant.llm_api_key"
	SecretSTTKey = "assistant.stt_api_key"
	SecretTTSKey = "assistant.tts_api_key"
)

// backends builds providers from the current settings and secrets on demand.
type backends struct{ a *App }

func (b backends) key(ctx context.Context, name string) (string, error) {
	v, err := b.a.Vault.Get(ctx, name)
	if errors.Is(err, secrets.ErrNotFound) || (err == nil && len(v) == 0) {
		return "", assistant.ErrNotConfigured
	}
	return string(v), err
}

func (b backends) LLM(ctx context.Context, s store.GPTSettings) (assistant.AIProvider, error) {
	key, err := b.key(ctx, SecretLLMKey)
	if err != nil {
		return nil, err
	}
	return openrouter.New(s.LLMBaseURL, key), nil
}

func (b backends) STT(ctx context.Context, s store.GPTSettings) (speech.STT, error) {
	if s.STTEngine == "whispercpp" {
		return speech.NewWhisperCpp(b.a.Setting(ctx, "stt.whisper_binary", "whisper-cli"), s.STTModelPath)
	}
	key, err := b.key(ctx, SecretSTTKey)
	if err != nil {
		return nil, fmt.Errorf("%w: add the speech-to-text API key under Assistant in the admin", err)
	}
	return speech.NewOpenAICompatSTT(s.STTBaseURL, key, s.STTModel), nil
}

func (b backends) TTS(ctx context.Context, s store.GPTSettings) (speech.TTS, error) {
	// No hardcoded language or voice: everything comes from the settings.
	opts := speech.TTSOptions{Language: s.SpeechLanguage(), Voice: s.TTSVoice, Speed: s.TTSSpeed, Volume: s.TTSVolume}
	if b.a.Cfg.Hardware == "mock" {
		return speech.MockTTS{}, nil
	}
	switch s.TTSEngine {
	case "none":
		return nil, fmt.Errorf("%w: text-to-speech is switched off", assistant.ErrNotConfigured)
	case "cloud":
		key, err := b.key(ctx, SecretTTSKey)
		if err != nil {
			return nil, fmt.Errorf("%w: add the text-to-speech API key under Assistant in the admin", err)
		}
		return speech.NewOpenAICompatTTS(s.TTSBaseURL, key, s.TTSModel, s.TTSVoice).WithOptions(opts), nil
	}
	// An explicit setting wins, then the Piper Milo downloaded, then whatever "piper" is on the PATH.
	bin := b.a.Setting(ctx, "tts.piper_binary", "")
	if bin == "" {
		if bin = b.a.Voices.PiperBinary(); bin == "" {
			bin = "piper"
		}
	}
	byLanguage := b.a.VoiceChoices(ctx)
	if len(byLanguage) == 0 {
		p, err := speech.NewPiper(bin, s.TTSModelPath)
		if err != nil {
			return nil, err
		}
		return p.WithOptions(opts), nil
	}
	// One voice per language: Piper speaks the voice of the language the answer is in.
	p, err := speech.NewPiperVoices(bin, byLanguage, s.TTSModelPath)
	if err != nil {
		return nil, err
	}
	return p.WithOptions(opts), nil
}

// VoiceChoices returns the Piper voice chosen for each language, as language family -> model path.
// Only voices that are still on the device count.
func (a *App) VoiceChoices(ctx context.Context) map[string]string {
	choices := map[string]string{}
	for _, key := range a.Voices.InstalledKeys() {
		family := voices.Family(key)
		if choice := a.Setting(ctx, voices.SettingKey(family), ""); choice != "" && choice == a.Voices.ModelPath(key) {
			choices[family] = choice
		}
	}
	return choices
}

func (a *App) initAssistant() {
	a.Voices = &voices.Manager{Dir: a.Cfg.DataDir}
	if a.Cfg.Hardware != "mock" {
		// Phrases Milo says often are kept as audio, so the second time costs nothing.
		speech.SetCacheDir(filepath.Join(a.Cfg.DataDir, "tts-cache"))
	}
	a.Notifier = notifications.New(a.Audio, a.Store)
	a.Assistant = assistant.NewEngine(a.Store, a.Actions, backends{a}, a.Log)
	a.Assistant.Audio, a.Assistant.Notify = a.Audio, a.Notifier
	a.Assistant.AlertParents = a.alertParents
	a.Assistant.RegisterActions(a.Actions)
}

// alertParents tells the parents (over Telegram) that the child said something
// worrying. It reaches every active OWNER or PARENT with a Telegram account.
func (a *App) alertParents(ctx context.Context, quote string) {
	if len([]rune(quote)) > 200 {
		quote = string([]rune(quote)[:200]) + "…"
	}
	users, err := a.Store.ListTelegramUsers(ctx)
	if err != nil {
		return
	}
	sent := 0
	for _, u := range users {
		if !u.Active || u.TelegramID == 0 || (u.Role != string(permissions.RoleOwner) && u.Role != string(permissions.RoleParent)) {
			continue
		}
		lang := i18n.ForUser(ctx, a.Store, u.Language)
		a.Telegram.SendText(u.TelegramID, i18n.Tf(lang, "assistant.concern_alert", "quote", quote))
		sent++
	}
	a.Audit.Record(ctx, "assistant", "concern.detected", "", map[string]string{"parents_alerted": fmt.Sprint(sent)})
}

// maintenance runs periodic housekeeping.
func (a *App) maintenance(ctx context.Context) {
	for {
		a.maintenanceRound(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Hour):
		}
	}
}

// maintenanceRound runs one pass of housekeeping. A panic in it is logged and the
// next pass still happens, instead of the whole process going down.
func (a *App) maintenanceRound(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			a.Log.Error("panic contained", "where", "maintenance", "panic", fmt.Sprint(r))
		}
	}()
	a.Assistant.PruneHistory(ctx)
	a.Store.PruneSkills(ctx, time.Now().AddDate(0, 0, -30)) //nolint:errcheck
	a.backupIfDue(ctx)
}
