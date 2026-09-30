package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/juancer/nexo/internal/assistant"
	"github.com/juancer/nexo/internal/notifications"
	"github.com/juancer/nexo/internal/openrouter"
	"github.com/juancer/nexo/internal/permissions"
	"github.com/juancer/nexo/internal/secrets"
	"github.com/juancer/nexo/internal/speech"
	"github.com/juancer/nexo/internal/store"
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
		return nil, fmt.Errorf("%w: add the speech-to-text API key in the Assistant tab", err)
	}
	return speech.NewOpenAICompatSTT(s.STTBaseURL, key, s.STTModel), nil
}

func (b backends) TTS(ctx context.Context, s store.GPTSettings) (speech.TTS, error) {
	if b.a.Cfg.Hardware == "mock" {
		return speech.MockTTS{}, nil
	}
	switch s.TTSEngine {
	case "none":
		return nil, fmt.Errorf("%w: text-to-speech is switched off", assistant.ErrNotConfigured)
	case "cloud":
		key, err := b.key(ctx, SecretTTSKey)
		if err != nil {
			return nil, fmt.Errorf("%w: add the text-to-speech API key in the Assistant tab", err)
		}
		return speech.NewOpenAICompatTTS(s.TTSBaseURL, key, s.TTSModel, s.TTSVoice), nil
	}
	return speech.NewPiper(b.a.Setting(ctx, "tts.piper_binary", "piper"), s.TTSModelPath)
}

func (a *App) initAssistant() {
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
	msg := fmt.Sprintf("💛 Nexo noticed something that may need your attention. The child said: “%s”. "+
		"Nexo answered kindly and suggested talking to a trusted adult. You can review conversations in the admin under Assistant.", quote)
	sent := 0
	for _, u := range users {
		if !u.Active || u.TelegramID == 0 || (u.Role != string(permissions.RoleOwner) && u.Role != string(permissions.RoleParent)) {
			continue
		}
		a.Telegram.SendText(u.TelegramID, msg)
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
