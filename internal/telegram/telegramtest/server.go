// Package telegramtest is an in-memory imitation of the Telegram Bot API. It is
// used by tests and by the development simulator (cmd/milo-sim), so the whole
// Telegram flow can be exercised with no Telegram account and no Internet.
package telegramtest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Button is an inline button in a bubble.
type Button struct {
	Text string `json:"text"`
	Data string `json:"data"`
}

// Bubble is one message in a conversation as a phone would show it.
type Bubble struct {
	ID       int64      `json:"id"`
	From     string     `json:"from"` // "user" or "bot"
	Kind     string     `json:"kind"` // "text" or "voice"
	Text     string     `json:"text,omitempty"`
	Audio    []byte     `json:"-"`
	Caption  string     `json:"caption,omitempty"`
	Seconds  int        `json:"seconds,omitempty"`
	Buttons  [][]Button `json:"buttons,omitempty"`
	At       time.Time  `json:"at"`
	HasAudio bool       `json:"has_audio,omitempty"`
}

// Person is a simulated Telegram account.
type Person struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	Username  string `json:"username"`
}

// Server implements the subset of the Bot API that Milo uses.
type Server struct {
	mu      sync.Mutex
	cond    *sync.Cond
	token   string
	botName string
	nextUp  int64
	nextMsg int64
	queue   []map[string]any // pending updates, each with "update_id"
	people  map[int64]Person
	convo   map[int64][]Bubble
	files   map[string][]byte
	failing bool
	answers []string
}

// New creates a server that accepts only the given bot token.
func New(token, botUsername string) *Server {
	s := &Server{token: token, botName: botUsername, nextUp: 1, nextMsg: 1,
		people: map[int64]Person{}, convo: map[int64][]Bubble{}, files: map[string][]byte{}}
	s.cond = sync.NewCond(&s.mu)
	return s
}

// SetFailing makes every API call return HTTP 500, simulating an outage.
func (s *Server) SetFailing(v bool) {
	s.mu.Lock()
	s.failing = v
	s.mu.Unlock()
}

// AddPerson registers a simulated account.
func (s *Server) AddPerson(p Person) {
	s.mu.Lock()
	s.people[p.ID] = p
	s.mu.Unlock()
}

// People lists simulated accounts.
func (s *Server) People() []Person {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Person, 0, len(s.people))
	for _, p := range s.people {
		out = append(out, p)
	}
	return out
}

// Conversation returns the bubbles exchanged with a person or chat.
func (s *Server) Conversation(chatID int64) []Bubble {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]Bubble(nil), s.convo[chatID]...)
	for i := range out {
		out[i].HasAudio = len(out[i].Audio) > 0
	}
	return out
}

// BubbleAudio returns the audio of a bubble.
func (s *Server) BubbleAudio(chatID, id int64) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, b := range s.convo[chatID] {
		if b.ID == id {
			return b.Audio
		}
	}
	return nil
}

// Answers returns the texts of answerCallbackQuery calls (for tests).
func (s *Server) Answers() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.answers...)
}

func (s *Server) enqueue(update map[string]any) {
	update["update_id"] = s.nextUp
	s.nextUp++
	s.queue = append(s.queue, update)
	s.cond.Broadcast()
}

func (s *Server) user(id int64) map[string]any {
	p := s.people[id]
	if p.FirstName == "" {
		p.FirstName = "User " + strconv.FormatInt(id, 10)
	}
	return map[string]any{"id": id, "is_bot": false, "first_name": p.FirstName, "username": p.Username}
}

func (s *Server) message(id int64, from int64, chatID int64, chatType string) map[string]any {
	return map[string]any{"message_id": id, "from": s.user(from), "chat": map[string]any{"id": chatID, "type": chatType},
		"date": time.Now().Unix()}
}

// Send makes a person send a text message to the bot.
func (s *Server) Send(from int64, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.nextMsg
	s.nextMsg++
	m := s.message(id, from, from, "private")
	m["text"] = text
	s.convo[from] = append(s.convo[from], Bubble{ID: id, From: "user", Kind: "text", Text: text, At: time.Now()})
	s.enqueue(map[string]any{"message": m})
}

// SendGroup makes a person write into a group chat the bot belongs to.
func (s *Server) SendGroup(from, group int64, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.nextMsg
	s.nextMsg++
	m := s.message(id, from, group, "group")
	m["text"] = text
	s.enqueue(map[string]any{"message": m})
}

// SendVoice makes a person send a voice note to the bot.
func (s *Server) SendVoice(from int64, audio []byte, seconds int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.nextMsg
	s.nextMsg++
	fileID := fmt.Sprintf("voice-%d", id)
	s.files[fileID] = audio
	m := s.message(id, from, from, "private")
	m["voice"] = map[string]any{"file_id": fileID, "duration": seconds, "file_size": len(audio)}
	s.convo[from] = append(s.convo[from], Bubble{ID: id, From: "user", Kind: "voice", Audio: audio, Seconds: seconds, At: time.Now()})
	s.enqueue(map[string]any{"message": m})
}

// Press makes a person press an inline button.
func (s *Server) Press(from int64, data string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.nextMsg
	s.nextMsg++
	s.enqueue(map[string]any{"callback_query": map[string]any{
		"id": fmt.Sprintf("cb-%d", id), "from": s.user(from), "data": data,
		"message": s.message(id, 0, from, "private"),
	}})
}

// ServeHTTP handles /bot<token>/<method> and /file/bot<token>/<path>.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	failing := s.failing
	s.mu.Unlock()
	if failing {
		http.Error(w, "simulated outage", http.StatusInternalServerError)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/")
	if rest, ok := strings.CutPrefix(path, "file/bot"+s.token+"/"); ok {
		s.serveFile(w, rest)
		return
	}
	rest, ok := strings.CutPrefix(path, "bot"+s.token+"/")
	if !ok {
		fail(w, 401, "Unauthorized")
		return
	}
	switch rest {
	case "getMe":
		reply(w, map[string]any{"id": 999, "is_bot": true, "first_name": "Milo", "username": s.botName})
	case "getUpdates":
		s.getUpdates(w, r)
	case "sendMessage":
		s.sendMessage(w, r)
	case "sendVoice":
		s.sendVoice(w, r)
	case "editMessageText":
		s.editMessage(w, r)
	case "answerCallbackQuery":
		var p struct {
			Text string `json:"text"`
		}
		json.NewDecoder(r.Body).Decode(&p)
		s.mu.Lock()
		s.answers = append(s.answers, p.Text)
		s.mu.Unlock()
		reply(w, true)
	case "getFile":
		var p struct {
			FileID string `json:"file_id"`
		}
		json.NewDecoder(r.Body).Decode(&p)
		s.mu.Lock()
		data, ok := s.files[p.FileID]
		s.mu.Unlock()
		if !ok {
			fail(w, 400, "Bad Request: invalid file_id")
			return
		}
		reply(w, map[string]any{"file_id": p.FileID, "file_size": len(data), "file_path": "voice/" + p.FileID + ".oga"})
	default:
		fail(w, 404, "Not Found")
	}
}

func (s *Server) serveFile(w http.ResponseWriter, rest string) {
	id := strings.TrimSuffix(strings.TrimPrefix(rest, "voice/"), ".oga")
	s.mu.Lock()
	data, ok := s.files[id]
	s.mu.Unlock()
	if !ok {
		http.NotFound(w, nil)
		return
	}
	w.Header().Set("Content-Type", "audio/ogg")
	w.Write(data)
}

func (s *Server) getUpdates(w http.ResponseWriter, r *http.Request) {
	var p struct {
		Offset  int64 `json:"offset"`
		Timeout int   `json:"timeout"`
	}
	json.NewDecoder(r.Body).Decode(&p)
	deadline := time.Now().Add(time.Duration(min(p.Timeout, 30)) * time.Second)

	stop := make(chan struct{})
	go func() {
		select {
		case <-r.Context().Done():
			s.mu.Lock()
			s.cond.Broadcast()
			s.mu.Unlock()
		case <-stop:
		}
	}()
	defer close(stop)
	go func() { // wake the waiter at the deadline
		time.Sleep(time.Until(deadline))
		s.mu.Lock()
		s.cond.Broadcast()
		s.mu.Unlock()
	}()

	s.mu.Lock()
	defer s.mu.Unlock()
	for {
		// Confirm (drop) everything before offset, then return what is left.
		kept := s.queue[:0]
		for _, u := range s.queue {
			if u["update_id"].(int64) >= p.Offset {
				kept = append(kept, u)
			}
		}
		s.queue = kept
		if len(s.queue) > 0 || time.Now().After(deadline) || r.Context().Err() != nil {
			out := append([]map[string]any{}, s.queue...)
			reply(w, out)
			return
		}
		s.cond.Wait()
	}
}

func (s *Server) sendMessage(w http.ResponseWriter, r *http.Request) {
	var p struct {
		ChatID      int64  `json:"chat_id"`
		Text        string `json:"text"`
		ReplyMarkup struct {
			Inline [][]struct {
				Text string `json:"text"`
				Data string `json:"callback_data"`
			} `json:"inline_keyboard"`
		} `json:"reply_markup"`
	}
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil || p.ChatID == 0 || p.Text == "" {
		fail(w, 400, "Bad Request: chat_id and text are required")
		return
	}
	var kb [][]Button
	for _, row := range p.ReplyMarkup.Inline {
		var out []Button
		for _, b := range row {
			out = append(out, Button{Text: b.Text, Data: b.Data})
		}
		kb = append(kb, out)
	}
	s.mu.Lock()
	id := s.nextMsg
	s.nextMsg++
	s.convo[p.ChatID] = append(s.convo[p.ChatID], Bubble{ID: id, From: "bot", Kind: "text", Text: p.Text, Buttons: kb, At: time.Now()})
	s.mu.Unlock()
	reply(w, map[string]any{"message_id": id, "chat": map[string]any{"id": p.ChatID}})
}

func (s *Server) sendVoice(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(16 << 20); err != nil {
		fail(w, 400, "Bad Request: multipart form expected")
		return
	}
	chatID, _ := strconv.ParseInt(r.FormValue("chat_id"), 10, 64)
	f, _, err := r.FormFile("voice")
	if chatID == 0 || err != nil {
		fail(w, 400, "Bad Request: chat_id and voice are required")
		return
	}
	defer f.Close()
	audio, _ := io.ReadAll(f)
	secs, _ := strconv.Atoi(r.FormValue("duration"))
	s.mu.Lock()
	id := s.nextMsg
	s.nextMsg++
	s.convo[chatID] = append(s.convo[chatID], Bubble{ID: id, From: "bot", Kind: "voice", Audio: audio, Seconds: secs, Caption: r.FormValue("caption"), At: time.Now()})
	s.mu.Unlock()
	reply(w, map[string]any{"message_id": id, "chat": map[string]any{"id": chatID}})
}

// editMessage replaces the text and buttons of an earlier bot bubble, like editMessageText.
func (s *Server) editMessage(w http.ResponseWriter, r *http.Request) {
	var p struct {
		ChatID      int64  `json:"chat_id"`
		MessageID   int64  `json:"message_id"`
		Text        string `json:"text"`
		ReplyMarkup struct {
			Inline [][]struct {
				Text string `json:"text"`
				Data string `json:"callback_data"`
			} `json:"inline_keyboard"`
		} `json:"reply_markup"`
	}
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil || p.ChatID == 0 || p.Text == "" {
		fail(w, 400, "Bad Request: chat_id, message_id and text are required")
		return
	}
	var kb [][]Button
	for _, row := range p.ReplyMarkup.Inline {
		var out []Button
		for _, b := range row {
			out = append(out, Button{Text: b.Text, Data: b.Data})
		}
		kb = append(kb, out)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.convo[p.ChatID] {
		if b := &s.convo[p.ChatID][i]; b.ID == p.MessageID && b.From == "bot" && b.Kind == "text" {
			b.Text, b.Buttons = p.Text, kb
			reply(w, map[string]any{"message_id": p.MessageID, "chat": map[string]any{"id": p.ChatID}})
			return
		}
	}
	fail(w, 400, "Bad Request: message to edit not found")
}

func reply(w http.ResponseWriter, result any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
}

func fail(w http.ResponseWriter, code int, desc string) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": code, "description": desc})
}
