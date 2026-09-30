package telegram

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/juancer/milo/internal/telegram/telegramtest"
)

const testToken = "123456:TEST-TOKEN-NOT-A-REAL-ONE-abcdefghij"

func newFake(t *testing.T) (*telegramtest.Server, *Client) {
	t.Helper()
	fake := telegramtest.New(testToken, "family_test_bot")
	fake.AddPerson(telegramtest.Person{ID: 10, FirstName: "Dad", Username: "dad"})
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	return fake, NewClient(srv.URL, testToken)
}

func TestGetMeAndUpdatesRoundTrip(t *testing.T) {
	ctx := context.Background()
	fake, c := newFake(t)
	me, err := c.GetMe(ctx)
	if err != nil || me.Username != "family_test_bot" || !me.IsBot {
		t.Fatalf("GetMe = %+v, %v", me, err)
	}
	fake.Send(10, "hello")
	ups, err := c.GetUpdates(ctx, 0, 1)
	if err != nil || len(ups) != 1 || ups[0].Message.Text != "hello" || ups[0].Message.From.ID != 10 || ups[0].Message.Chat.Type != "private" {
		t.Fatalf("updates = %+v, %v", ups, err)
	}
	// Confirming the offset drops the update.
	ups, _ = c.GetUpdates(ctx, ups[0].UpdateID+1, 0)
	if len(ups) != 0 {
		t.Fatalf("update delivered twice: %+v", ups)
	}
}

func TestLongPollWakesOnNewUpdate(t *testing.T) {
	ctx := context.Background()
	fake, c := newFake(t)
	go func() { time.Sleep(100 * time.Millisecond); fake.Send(10, "late") }()
	start := time.Now()
	ups, err := c.GetUpdates(ctx, 0, 10)
	if err != nil || len(ups) != 1 || time.Since(start) > 3*time.Second {
		t.Fatalf("ups=%+v err=%v after %v", ups, err, time.Since(start))
	}
}

func TestSendMessageKeyboardAndVoiceAndDownload(t *testing.T) {
	ctx := context.Background()
	fake, c := newFake(t)
	kb := Keyboard{{{Text: "Yes", CallbackData: "y"}, {Text: "No", CallbackData: "n"}}}
	if _, err := c.SendMessage(ctx, 10, "sure?", kb); err != nil {
		t.Fatal(err)
	}
	b := fake.Conversation(10)
	if len(b) != 1 || b[0].From != "bot" || len(b[0].Buttons[0]) != 2 || b[0].Buttons[0][1].Data != "n" {
		t.Fatalf("bubbles = %+v", b)
	}
	if _, err := c.SendVoice(ctx, 10, []byte("OggS-data"), 3); err != nil {
		t.Fatal(err)
	}
	if b = fake.Conversation(10); len(b) != 2 || b[1].Kind != "voice" || string(b[1].Audio) != "OggS-data" || b[1].Seconds != 3 {
		t.Fatalf("voice bubble = %+v", b)
	}

	fake.SendVoice(10, []byte("incoming-voice"), 2)
	ups, _ := c.GetUpdates(ctx, 0, 0)
	data, err := c.DownloadFile(ctx, ups[0].Message.Voice.FileID)
	if err != nil || string(data) != "incoming-voice" {
		t.Fatalf("download = %q, %v", data, err)
	}
	if _, err := c.DownloadFile(ctx, "nope"); err == nil {
		t.Fatal("unknown file downloaded")
	}
}

func TestErrorsNeverContainTheToken(t *testing.T) {
	ctx := context.Background()
	fake, c := newFake(t)

	bad := NewClient(strings.Replace(c.base, "http://", "http://", 1), "999:wrong-token-value-aaaaaaaaaaaaaaaaaa")
	_, err := bad.GetMe(ctx)
	var api *APIError
	if !errors.As(err, &api) || api.Code != 401 || !api.Permanent() {
		t.Fatalf("wrong token err = %v", err)
	}

	// Transport failure: the URL (which embeds the token) must not leak.
	dead := NewClient("http://127.0.0.1:1", testToken)
	_, err = dead.GetMe(ctx)
	if err == nil || strings.Contains(err.Error(), testToken) {
		t.Fatalf("transport error leaks token: %v", err)
	}

	fake.SetFailing(true)
	if _, err := c.GetMe(ctx); err == nil || strings.Contains(err.Error(), testToken) {
		t.Fatalf("outage err = %v", err)
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Permanent() {
		t.Fatal("an outage must not be treated as permanent")
	}
}
