package wakeword

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"math/rand"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/juancer/milo/internal/actions"
	"github.com/juancer/milo/internal/audio"
	"github.com/juancer/milo/internal/store"
)

type feat map[string]bool

func (f feat) Enabled(_ context.Context, n string) (bool, error) { return f[n], nil }

type busy struct{ v atomic.Bool }

func (b *busy) Busy() bool { return b.v.Load() }

type rig struct {
	svc    *Service
	audio  *audio.Mock
	store  *store.Store
	calls  atomic.Int32
	actors chan actions.Actor
	busy   *busy
	gptErr error
	mu     sync.Mutex
}

func newRig(t *testing.T, simulated bool) *rig {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "n.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	r := &rig{audio: audio.NewMock(), store: st, actors: make(chan actions.Actor, 8), busy: &busy{}}
	eng := actions.NewEngine(feat{"assistant": true}, nil, nil, log)
	eng.MustRegister(actions.Action{
		Name: "gpt", Feature: "assistant", Exposure: actions.ExposeDevice,
		Handler: func(_ context.Context, c actions.Call) (actions.Result, error) {
			r.calls.Add(1)
			r.actors <- c.Actor
			r.mu.Lock()
			defer r.mu.Unlock()
			return actions.Result{Message: "Listening"}, r.gptErr
		},
	})
	r.svc = New(Deps{Store: st, Audio: r.audio, Actions: eng, Assistant: r.busy, Log: log, Simulated: simulated})
	return r
}

func (r *rig) enrollSynthetic(t *testing.T) {
	t.Helper()
	rng := rand.New(rand.NewSource(11))
	for _, v := range []struct{ f0, stretch float64 }{{125, 1.0}, {135, 0.92}, {118, 1.08}} {
		f, err := Enroll(stream(speak(heyMilo, v.f0, v.stretch, 1, 25, rng), rng))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.store.AddWakeTemplate(context.Background(), EncodeFeatures(f), len(f)); err != nil {
			t.Fatal(err)
		}
	}
}

func (r *rig) start(t *testing.T) {
	t.Helper()
	if err := r.svc.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.svc.Stop(context.Background()) })
	for i := 0; !r.audio.Listening(); i++ {
		if i > 400 {
			t.Fatal("service never started listening")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func waitCalls(t *testing.T, r *rig, n int32) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for r.calls.Load() < n {
		if time.Now().After(deadline) {
			t.Fatalf("gpt called %d times, want %d", r.calls.Load(), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestHearingThePhraseStartsTheAssistantThroughTheActionEngine(t *testing.T) {
	r := newRig(t, false)
	r.enrollSynthetic(t)
	r.start(t)
	rng := rand.New(rand.NewSource(12))

	// Other speech and noise: nothing happens.
	for _, w := range others {
		r.audio.Feed(stream(speak(w, 125, 1, 1, 30, rng), rng))
	}
	time.Sleep(100 * time.Millisecond)
	if r.calls.Load() != 0 {
		t.Fatal("the assistant started for something that is not the wake phrase")
	}

	r.audio.Feed(stream(speak(heyMilo, 129, 1.03, 1, 30, rng), rng))
	waitCalls(t, r, 1)
	actor := <-r.actors
	if actor.Kind != actions.ActorDevice || actor.Label != "wake word" {
		t.Fatalf("actor = %+v; the wake word must act as the device through the Action Engine", actor)
	}
	if st := r.svc.Status(context.Background()); !st.Running || st.Engine != "template" || len(st.Scores) == 0 {
		t.Fatalf("status = %+v", st)
	}
}

func TestWakeWordIgnoredWhileAConversationIsRunning(t *testing.T) {
	r := newRig(t, false)
	r.enrollSynthetic(t)
	r.start(t)
	r.busy.v.Store(true)
	rng := rand.New(rand.NewSource(13))
	r.audio.Feed(stream(speak(heyMilo, 127, 1, 1, 30, rng), rng))
	time.Sleep(150 * time.Millisecond)
	if r.calls.Load() != 0 {
		t.Fatal("the wake word interrupted a running conversation")
	}
}

func TestBlockedAssistantGivesAnErrorToneNotSilence(t *testing.T) {
	r := newRig(t, false)
	r.enrollSynthetic(t)
	r.gptErr = actions.ErrScheduleBlocked
	r.start(t)
	rng := rand.New(rand.NewSource(14))
	r.audio.Feed(stream(speak(heyMilo, 127, 1, 1, 30, rng), rng))
	waitCalls(t, r, 1)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, p := range r.audio.Played() {
			if p.Label == "error" {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no error tone after the assistant refused")
}

func TestStartNeedsSamplesOrAValidHelper(t *testing.T) {
	r := newRig(t, false)
	if err := r.svc.Start(context.Background()); err == nil {
		t.Fatal("started with no samples")
	}
	r.store.SetSetting(context.Background(), SettingEngine, "external")
	r.store.SetSetting(context.Background(), SettingExternalPath, "/bin/sh; rm -rf /")
	if err := r.svc.Start(context.Background()); err == nil {
		t.Fatal("started with an unsafe helper path")
	}
}

func TestStopReleasesTheMicrophone(t *testing.T) {
	r := newRig(t, false)
	r.enrollSynthetic(t)
	r.start(t)
	r.svc.Stop(context.Background())
	deadline := time.Now().Add(2 * time.Second)
	for r.audio.Listening() {
		if time.Now().After(deadline) {
			t.Fatal("still listening after Stop")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestSimulatedModeIsTriggeredByHand(t *testing.T) {
	r := newRig(t, true)
	r.start(t)
	if err := r.svc.Trigger(); err != nil {
		t.Fatal(err)
	}
	waitCalls(t, r, 1)
	real := newRig(t, false)
	real.enrollSynthetic(t)
	real.start(t)
	if err := real.svc.Trigger(); err == nil {
		t.Fatal("Trigger must only work in simulated mode")
	}
}

func TestEnrollErrorsAndLimit(t *testing.T) {
	r := newRig(t, false)
	// The mock microphone records silence, so nothing is enrolled.
	if err := r.svc.Enroll(context.Background()); err == nil {
		t.Fatal("silence enrolled as a sample")
	}
	f := EncodeFeatures([][]float64{make([]float64, numCoeffs)})
	for i := 0; i < MaxTemplates; i++ {
		r.store.AddWakeTemplate(context.Background(), f, 1)
	}
	err := r.svc.Enroll(context.Background())
	if err == nil || errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the sample limit", err)
	}
}

func TestLoopReturnsWhenStopClearedTheDetector(t *testing.T) {
	r := newRig(t, false) // never started: det is nil, as after Stop
	done := make(chan struct{})
	go func() { r.svc.loop(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("loop kept running without a detector")
	}
}

func TestEnrollIsSerialised(t *testing.T) {
	r := newRig(t, false)
	f := EncodeFeatures([][]float64{make([]float64, numCoeffs)})
	for i := 0; i < MaxTemplates; i++ {
		r.store.AddWakeTemplate(context.Background(), f, 1)
	}
	r.svc.enrollMu.Lock() // another Enroll is in flight
	done := make(chan error, 1)
	go func() { done <- r.svc.Enroll(context.Background()) }()
	select {
	case <-done:
		t.Fatal("Enroll ran while another Enroll held the lock")
	case <-time.After(100 * time.Millisecond):
	}
	r.svc.enrollMu.Unlock()
	if err := <-done; err == nil {
		t.Fatal("want the sample limit error")
	}
}

type schedStub struct {
	mu     sync.Mutex
	closed map[string]bool
}

func (s *schedStub) Allowed(_ context.Context, feature string, _ time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.closed[feature], nil
}

func (s *schedStub) set(feature string, closed bool) {
	s.mu.Lock()
	s.closed[feature] = closed
	s.mu.Unlock()
}

func TestWakewordScheduleIsHonoured(t *testing.T) {
	r := newRig(t, true)
	sc := &schedStub{closed: map[string]bool{}}
	r.svc.Schedules = sc
	r.start(t)
	sc.set(FeatureName, true) // only the wakeword window is closed
	if err := r.svc.Trigger(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if n := r.calls.Load(); n != 0 {
		t.Fatalf("gpt called %d times outside the wakeword schedule", n)
	}
	sc.set(FeatureName, false)
	r.svc.mu.Lock()
	r.svc.lastFire = time.Time{}
	r.svc.mu.Unlock()
	if err := r.svc.Trigger(); err != nil {
		t.Fatal(err)
	}
	waitCalls(t, r, 1)
}

type panicSchedules struct{}

func (panicSchedules) Allowed(context.Context, string, time.Time) (bool, error) {
	panic("schedule checker blew up")
}

func TestAPanicWhileListeningIsContained(t *testing.T) {
	r := newRig(t, true)
	r.svc.Schedules = panicSchedules{}
	r.start(t)
	r.audio.Feed(nil) // runs the detector callback in this goroutine: a panic would fail the test
	if !r.audio.Listening() {
		t.Fatal("the loop stopped after a panic")
	}
}

func TestMutingReleasesTheMicrophoneAndUnmutingListensAgain(t *testing.T) {
	r := newRig(t, false)
	r.enrollSynthetic(t)
	r.start(t)
	ctx := context.Background()
	if err := r.svc.SetMuted(ctx, true); err != nil {
		t.Fatal(err)
	}
	waitListening := func(want bool) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for r.audio.Listening() != want {
			if time.Now().After(deadline) {
				t.Fatalf("listening = %v, want %v", r.audio.Listening(), want)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	waitListening(false)
	if !r.svc.Status(ctx).Muted {
		t.Fatal("status does not say muted")
	}
	if err := r.svc.SetMuted(ctx, false); err != nil {
		t.Fatal(err)
	}
	waitListening(true)
	if r.svc.Status(ctx).Muted {
		t.Fatal("still muted")
	}
}

func TestMutedStaysMutedAfterARestart(t *testing.T) {
	r := newRig(t, false)
	r.enrollSynthetic(t)
	if err := r.svc.SetMuted(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if err := r.svc.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.svc.Stop(context.Background()) })
	time.Sleep(100 * time.Millisecond)
	if r.audio.Listening() || !r.svc.Muted() {
		t.Fatal("a restarted Milo listens although it was told not to")
	}
}
