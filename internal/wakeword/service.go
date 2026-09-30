package wakeword

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/juancer/milo/internal/actions"
	"github.com/juancer/milo/internal/audio"
	"github.com/juancer/milo/internal/store"
)

// FeatureName is the feature under which the wake word is registered.
const FeatureName = "wakeword"

// Setting keys.
const (
	SettingEngine       = "wakeword.engine"        // "template" (default) or "external"
	SettingPhrase       = "wakeword.phrase"        // label shown in the UI; the sound comes from the samples
	SettingSensitivity  = "wakeword.sensitivity"   // 1-10
	SettingExternalPath = "wakeword.external_path" // helper for the "external" engine
	MaxTemplates        = 5
)

// Busy tells whether a conversation is already running.
type Busy interface{ Busy() bool }

// Deps are the wake-word service's collaborators.
type Deps struct {
	Store     *store.Store
	Audio     audio.Audio
	Actions   *actions.Engine
	Assistant Busy
	// Schedules is the usage-schedule checker for the wakeword feature itself
	// (may be nil: no schedules configured).
	Schedules actions.ScheduleChecker
	Log       *slog.Logger
	// Simulated selects the hand-triggered detector (development, no microphone).
	Simulated bool
}

// Score is one candidate phrase and how close it was to the wake phrase.
type Score struct {
	At       time.Time `json:"at"`
	Distance float64   `json:"distance"`
}

// Status is what the admin UI shows.
type Status struct {
	Running       bool      `json:"running"`
	Engine        string    `json:"engine"`
	Threshold     float64   `json:"threshold"`
	LastDetection time.Time `json:"last_detection,omitempty"`
	Scores        []Score   `json:"scores"`
}

// Service is the wake-word feature. It never sends audio anywhere: detection
// happens on the device, and only after a detection does a normal (cloud or
// local) assistant conversation start.
type Service struct {
	Deps
	mu       sync.Mutex
	enrollMu sync.Mutex // makes Enroll's sample-count check and insert atomic
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	running  bool
	det      Detector
	engine   string
	thresh   float64
	scores   []Score
	lastFire time.Time
	now      func() time.Time
}

// New returns the (not yet running) service.
func New(d Deps) *Service { return &Service{Deps: d, now: time.Now} }

// Name implements features.Feature.
func (s *Service) Name() string { return FeatureName }

func (s *Service) setting(ctx context.Context, key, def string) string {
	if v, err := s.Store.GetSetting(ctx, key); err == nil && v != "" {
		return v
	}
	return def
}

// buildDetector creates the detector for the current settings.
func (s *Service) buildDetector(ctx context.Context) (Detector, string, error) {
	sens, _ := strconv.Atoi(s.setting(ctx, SettingSensitivity, "5"))
	s.mu.Lock()
	s.thresh = ThresholdFor(sens)
	s.mu.Unlock()
	if s.Simulated {
		return &Mock{}, "simulated", nil
	}
	switch engine := s.setting(ctx, SettingEngine, "template"); engine {
	case "external":
		d, err := NewExternal(context.Background(), s.setting(ctx, SettingExternalPath, ""))
		return d, engine, err
	default:
		rows, err := s.Store.ListWakeTemplates(ctx)
		if err != nil {
			return nil, "", err
		}
		var templates [][][]float64
		for _, r := range rows {
			if f, err := DecodeFeatures(r.Features); err == nil {
				templates = append(templates, f)
			}
		}
		if len(templates) == 0 {
			return nil, "", errors.New("no wake-word samples yet: record the phrase a few times in the Wake word tab")
		}
		d := NewTemplateDetector(templates, s.thresh)
		d.OnScore = s.recordScore
		return d, "template", nil
	}
}

func (s *Service) recordScore(dist float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scores = append(s.scores, Score{At: s.now(), Distance: dist})
	if len(s.scores) > 10 {
		s.scores = s.scores[len(s.scores)-10:]
	}
}

// Start implements features.Feature.
func (s *Service) Start(ctx context.Context) error {
	det, engine, err := s.buildDetector(ctx)
	if err != nil {
		return err
	}
	runCtx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.det, s.engine, s.cancel, s.running = det, engine, cancel, true
	s.mu.Unlock()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer s.recoverPanic("wake word loop")
		s.loop(runCtx)
	}()
	return nil
}

// Stop implements features.Feature.
func (s *Service) Stop(ctx context.Context) error {
	s.mu.Lock()
	cancel, det := s.cancel, s.det
	// Cancel before clearing det, both under the lock: the loop installs a rebuilt
	// helper only while holding s.mu with its context still live, so none can land
	// after this point and go unclosed.
	if cancel != nil {
		cancel()
	}
	s.running, s.det = false, nil
	s.mu.Unlock()
	if det != nil {
		det.Close()
	}
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
	return nil
}

// recoverPanic contains a panic in one of the service's goroutines: it is logged
// and the goroutine ends instead of taking the whole process down.
func (s *Service) recoverPanic(what string) {
	if r := recover(); r != nil {
		s.Log.Error("panic contained", "where", what, "panic", fmt.Sprint(r))
	}
}

// loop feeds microphone audio to the detector until stopped, retrying if the
// microphone or the helper goes away.
func (s *Service) loop(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		s.mu.Lock()
		det := s.det
		s.mu.Unlock()
		if det == nil || ctx.Err() != nil {
			return // Stop ran
		}
		if ext, ok := det.(*External); ok && !ext.Alive() {
			if d, _, err := s.buildDetector(ctx); err == nil {
				s.mu.Lock()
				if ctx.Err() != nil {
					s.mu.Unlock()
					d.Close() // stopped meanwhile: do not leave a helper running
					return
				}
				s.det, det = d, d
				s.mu.Unlock()
				ext.Close()
			}
		}
		err := s.Audio.Listen(ctx, func(pcm []byte) {
			defer s.recoverPanic("wake word detector")
			if !s.scheduleOpen(ctx) {
				return // outside the wakeword usage schedule: the detector idles
			}
			if det.Process(pcm) {
				s.fire(ctx)
			}
		})
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			s.Log.Warn("wake word cannot listen; will retry", "error", err, "retry_in", backoff)
		}
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

// scheduleOpen tells whether the wakeword feature's own usage schedule allows
// listening now. Like the Action Engine it fails closed on a checker error.
func (s *Service) scheduleOpen(ctx context.Context) bool {
	if s.Schedules == nil {
		return true
	}
	ok, err := s.Schedules.Allowed(ctx, FeatureName, s.now())
	return err == nil && ok
}

// fire starts a conversation through the Action Engine, so the assistant's
// feature switch and usage schedule apply to the wake word exactly as they do
// to the GPT button.
func (s *Service) fire(ctx context.Context) {
	if !s.scheduleOpen(ctx) {
		return
	}
	if s.Assistant != nil && s.Assistant.Busy() {
		return
	}
	s.mu.Lock()
	if s.now().Sub(s.lastFire) < 2*time.Second {
		s.mu.Unlock()
		return
	}
	s.lastFire = s.now()
	s.mu.Unlock()
	s.Log.Info("wake word heard")
	go func() {
		defer s.recoverPanic("wake word fire")
		actor := actions.Actor{Kind: actions.ActorDevice, Label: "wake word"}
		if _, err := s.Actions.Execute(ctx, actor, "gpt", nil); err != nil && ctx.Err() == nil {
			s.Log.Info("wake word could not start the assistant", "reason", err.Error())
			s.Audio.PlayNotification(ctx, audio.NotifyError, 100) //nolint:errcheck
		}
	}()
}

// Trigger simulates hearing the wake word (development mode only).
func (s *Service) Trigger() error {
	s.mu.Lock()
	det := s.det
	s.mu.Unlock()
	m, ok := det.(*Mock)
	if !ok {
		return errors.New("the wake word is not running in simulated mode")
	}
	m.Trigger()
	// The simulated microphone produces no audio, so nudge the detector directly.
	if m.Process(nil) {
		s.fire(context.Background())
	}
	return nil
}

// Status reports the current state.
func (s *Service) Status(ctx context.Context) Status {
	sens, _ := strconv.Atoi(s.setting(ctx, SettingSensitivity, "5"))
	s.mu.Lock()
	defer s.mu.Unlock()
	st := Status{Running: s.running, Engine: s.engine, Threshold: ThresholdFor(sens),
		LastDetection: s.lastFire, Scores: append([]Score{}, s.scores...)}
	return st
}

// Enroll records the wake phrase from the microphone and stores it as a
// template. Call it while the parent stands near the device and says the phrase.
func (s *Service) Enroll(ctx context.Context) error {
	s.enrollMu.Lock()
	defer s.enrollMu.Unlock()
	rows, err := s.Store.ListWakeTemplates(ctx)
	if err != nil {
		return err
	}
	if len(rows) >= MaxTemplates {
		return fmt.Errorf("there are already %d samples: delete one first", MaxTemplates)
	}
	rctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	wav, err := s.Audio.Record(rctx, audio.RecordOptions{MaxDuration: 4 * time.Second, StopOnSilence: true, SilenceFor: 700 * time.Millisecond})
	if err != nil {
		return err
	}
	pcm, _, err := audio.DecodeWAV(wav)
	if err != nil {
		return err
	}
	feats, err := Enroll(pcm)
	if err != nil {
		return err
	}
	_, err = s.Store.AddWakeTemplate(ctx, EncodeFeatures(feats), len(feats))
	return err
}
