// Command nexo-sim is the development simulator: fake family phones talking to a
// fake Telegram Bot API (and, from Phase 5, a fake LLM and speech-to-text).
// It lets the whole product be seen working with no Telegram account, no API
// keys and no Raspberry Pi. It is never installed on a device.
package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/juancer/nexo/internal/audio"
	"github.com/juancer/nexo/internal/telegram/telegramtest"
)

//go:embed ui
var uiFiles embed.FS

// DemoToken is the fake bot token shared with `nexo dev-seed`. It is not a
// real credential.
const DemoToken = "123456:DEMO-TOKEN-NOT-A-REAL-BOT-abcdefghijk"

type sim struct {
	tg  *telegramtest.Server
	bot string
}

func main() {
	addr := env("NEXO_SIM_ADDR", ":8081")
	token := env("NEXO_SIM_BOT_TOKEN", DemoToken)
	bot := env("NEXO_SIM_BOT_USERNAME", "nexo_demo_bot")

	s := &sim{tg: telegramtest.New(token, bot), bot: bot}
	for _, p := range []telegramtest.Person{
		{ID: 1001, FirstName: "Dad", Username: "dad_demo"},
		{ID: 1002, FirstName: "Mum", Username: "mum_demo"},
		{ID: 1003, FirstName: "Grandma", Username: "grandma_demo"},
		{ID: 9001, FirstName: "Stranger", Username: "stranger_demo"},
	} {
		s.tg.AddPerson(p)
	}

	ui, _ := fs.Sub(uiFiles, "ui")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /sim/api/state", s.state)
	mux.HandleFunc("GET /sim/api/chat/{id}", s.chat)
	mux.HandleFunc("POST /sim/api/chat/{id}/text", s.sendText)
	mux.HandleFunc("POST /sim/api/chat/{id}/voice", s.sendVoice)
	mux.HandleFunc("POST /sim/api/chat/{id}/press", s.press)
	mux.HandleFunc("GET /sim/api/chat/{id}/audio/{bubble}", s.audioFile)
	mux.HandleFunc("POST /sim/api/people", s.addPerson)
	mux.HandleFunc("POST /sim/api/outage", s.outage)
	mux.HandleFunc("POST /v1/chat/completions", s.chatCompletions)
	mux.HandleFunc("POST /v1/audio/transcriptions", s.transcribe)
	mux.Handle("/bot"+token+"/", s.tg)
	mux.Handle("/file/", s.tg)
	mux.Handle("/", http.FileServerFS(ui))

	log.Printf("nexo-sim listening on %s (bot @%s)", addr, bot)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func id(r *http.Request) int64 {
	n, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return n
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func (s *sim) state(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{"people": s.tg.People(), "bot": s.bot})
}

func (s *sim) chat(w http.ResponseWriter, r *http.Request) {
	type bubble struct {
		telegramtest.Bubble
		Transcript string `json:"transcript,omitempty"`
	}
	var out []bubble
	for _, b := range s.tg.Conversation(id(r)) {
		nb := bubble{Bubble: b}
		if b.Kind == "voice" {
			nb.Transcript = audio.ExtractText(b.Audio)
		}
		out = append(out, nb)
	}
	writeJSON(w, out)
}

func (s *sim) sendText(w http.ResponseWriter, r *http.Request) {
	var b struct{ Text string }
	json.NewDecoder(r.Body).Decode(&b)
	text := strings.TrimSpace(b.Text)
	// Pasting an invitation link behaves like tapping it in Telegram.
	if i := strings.Index(text, "?start="); strings.HasPrefix(text, "https://t.me/") && i > 0 {
		text = "/start " + text[i+len("?start="):]
	}
	if text != "" {
		s.tg.Send(id(r), text)
	}
	w.WriteHeader(http.StatusNoContent)
}

// sendVoice records a "voice note": silence carrying the words as a hidden
// transcript, which the development speech-to-text can read back.
func (s *sim) sendVoice(w http.ResponseWriter, r *http.Request) {
	var b struct{ Text string }
	json.NewDecoder(r.Body).Decode(&b)
	secs := max(1, min(60, len(strings.Fields(b.Text))/2+1))
	wav := audio.EmbedText(audio.EncodeWAV(audio.Silence(time.Duration(secs)*time.Second), audio.SampleRate), b.Text)
	s.tg.SendVoice(id(r), wav, secs)
	w.WriteHeader(http.StatusNoContent)
}

func (s *sim) press(w http.ResponseWriter, r *http.Request) {
	var b struct{ Data string }
	json.NewDecoder(r.Body).Decode(&b)
	s.tg.Press(id(r), b.Data)
	w.WriteHeader(http.StatusNoContent)
}

func (s *sim) audioFile(w http.ResponseWriter, r *http.Request) {
	bid, _ := strconv.ParseInt(r.PathValue("bubble"), 10, 64)
	data := s.tg.BubbleAudio(id(r), bid)
	if data == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "audio/wav")
	w.Write(data)
}

func (s *sim) addPerson(w http.ResponseWriter, r *http.Request) {
	var p telegramtest.Person
	json.NewDecoder(r.Body).Decode(&p)
	if p.ID == 0 || p.FirstName == "" {
		http.Error(w, "id and first_name are required", http.StatusBadRequest)
		return
	}
	s.tg.AddPerson(p)
	w.WriteHeader(http.StatusNoContent)
}

func (s *sim) outage(w http.ResponseWriter, r *http.Request) {
	var b struct{ Failing bool }
	json.NewDecoder(r.Body).Decode(&b)
	s.tg.SetFailing(b.Failing)
	fmt.Fprintf(w, "ok")
}
