// Package telegram connects Milo to the Telegram Bot API.
//
// Telegram is only a transport: it identifies people by Telegram user id, never
// authorizes anyone by itself, and every request that changes the device goes
// through the Action Engine.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"time"
)

// DefaultBaseURL is the public Telegram Bot API.
const DefaultBaseURL = "https://api.telegram.org"

// User is a Telegram account.
type User struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	Username  string `json:"username"`
}

// Chat is a Telegram conversation.
type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"` // "private", "group", ...
}

// Voice is a voice note (OGG/Opus).
type Voice struct {
	FileID   string `json:"file_id"`
	Duration int    `json:"duration"`
	FileSize int64  `json:"file_size"`
}

// Message is an incoming message.
type Message struct {
	MessageID int64  `json:"message_id"`
	From      *User  `json:"from"`
	Chat      Chat   `json:"chat"`
	Text      string `json:"text"`
	Voice     *Voice `json:"voice"`
}

// CallbackQuery is a press on an inline button.
type CallbackQuery struct {
	ID      string   `json:"id"`
	From    User     `json:"from"`
	Message *Message `json:"message"`
	Data    string   `json:"data"`
}

// Update is one event from getUpdates.
type Update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *Message       `json:"message"`
	CallbackQuery *CallbackQuery `json:"callback_query"`
}

// InlineButton is a button shown under a message.
type InlineButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}

// Keyboard is a grid of inline buttons.
type Keyboard [][]InlineButton

// APIError is an error reported by Telegram.
type APIError struct {
	Method      string
	Code        int
	Description string
	RetryAfter  int
}

func (e *APIError) Error() string {
	return fmt.Sprintf("telegram %s: %d %s", e.Method, e.Code, e.Description)
}

// Permanent reports whether retrying the same request cannot succeed
// (bad token, blocked bot, unknown chat, malformed request).
func (e *APIError) Permanent() bool {
	switch e.Code {
	case 400, 401, 403, 404:
		return true
	}
	return false
}

// Client talks to the Bot API. The token never appears in errors or logs.
type Client struct {
	base  string
	token string
	http  *http.Client
}

// NewClient returns a client for baseURL (empty for the public API).
func NewClient(baseURL, token string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{base: strings.TrimRight(baseURL, "/"), token: token, http: &http.Client{
		Timeout: 60 * time.Second,
		// The token is in every URL, so a redirect must never carry it elsewhere.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (c *Client) endpoint(method string) string { return c.base + "/bot" + c.token + "/" + method }

// cleanErr strips the request URL (which contains the token) from transport errors.
func cleanErr(method string, err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return fmt.Errorf("telegram %s: %w", method, err)
}

type envelope struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	ErrorCode   int             `json:"error_code"`
	Description string          `json:"description"`
	Parameters  struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

func (c *Client) do(req *http.Request, method string, out any) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return cleanErr(method, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return cleanErr(method, err)
	}
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return &APIError{Method: method, Code: resp.StatusCode, Description: "unexpected response"}
	}
	if !env.OK {
		return &APIError{Method: method, Code: env.ErrorCode, Description: env.Description, RetryAfter: env.Parameters.RetryAfter}
	}
	if out != nil {
		if err := json.Unmarshal(env.Result, out); err != nil {
			return fmt.Errorf("telegram %s: decode result: %w", method, err)
		}
	}
	return nil
}

func (c *Client) call(ctx context.Context, method string, params, out any) error {
	body, err := json.Marshal(params)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(method), bytes.NewReader(body))
	if err != nil {
		return cleanErr(method, err)
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, method, out)
}

// GetMe returns the bot's own account.
func (c *Client) GetMe(ctx context.Context) (User, error) {
	var u User
	err := c.call(ctx, "getMe", struct{}{}, &u)
	return u, err
}

// GetUpdates long-polls for new updates starting at offset.
func (c *Client) GetUpdates(ctx context.Context, offset int64, timeoutSec int) ([]Update, error) {
	var ups []Update
	err := c.call(ctx, "getUpdates", map[string]any{
		"offset": offset, "timeout": timeoutSec, "allowed_updates": []string{"message", "callback_query"},
	}, &ups)
	return ups, err
}

// SendMessage sends text, optionally with an inline keyboard.
func (c *Client) SendMessage(ctx context.Context, chatID int64, text string, kb Keyboard) (int64, error) {
	params := map[string]any{"chat_id": chatID, "text": text}
	if len(kb) > 0 {
		params["reply_markup"] = map[string]any{"inline_keyboard": kb}
	}
	var m Message
	err := c.call(ctx, "sendMessage", params, &m)
	return m.MessageID, err
}

// AnswerCallback acknowledges an inline button press.
func (c *Client) AnswerCallback(ctx context.Context, id, text string) error {
	return c.call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": id, "text": text}, nil)
}

// SendVoice uploads an OGG/Opus voice note.
func (c *Client) SendVoice(ctx context.Context, chatID int64, ogg []byte, durationSec int) (int64, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("chat_id", fmt.Sprint(chatID))
	if durationSec > 0 {
		mw.WriteField("duration", fmt.Sprint(durationSec))
	}
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="voice"; filename="voice.ogg"`)
	h.Set("Content-Type", "audio/ogg")
	part, err := mw.CreatePart(h)
	if err != nil {
		return 0, err
	}
	part.Write(ogg)
	mw.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint("sendVoice"), &buf)
	if err != nil {
		return 0, cleanErr("sendVoice", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	var m Message
	err = c.do(req, "sendVoice", &m)
	return m.MessageID, err
}

// MaxVoiceBytes caps downloaded voice notes (a few minutes of speech).
const MaxVoiceBytes = 5 << 20

// DownloadFile fetches a file by id, refusing anything above MaxVoiceBytes.
func (c *Client) DownloadFile(ctx context.Context, fileID string) ([]byte, error) {
	var f struct {
		FilePath string `json:"file_path"`
		FileSize int64  `json:"file_size"`
	}
	if err := c.call(ctx, "getFile", map[string]any{"file_id": fileID}, &f); err != nil {
		return nil, err
	}
	if f.FileSize > MaxVoiceBytes {
		return nil, fmt.Errorf("telegram: file too large (%d bytes)", f.FileSize)
	}
	segs := strings.Split(f.FilePath, "/")
	for i, seg := range segs {
		if seg == "" || seg == "." || seg == ".." {
			return nil, errors.New("telegram: invalid file path")
		}
		segs[i] = url.PathEscape(seg)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/file/bot"+c.token+"/"+strings.Join(segs, "/"), nil)
	if err != nil {
		return nil, cleanErr("download", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, cleanErr("download", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &APIError{Method: "download", Code: resp.StatusCode, Description: resp.Status}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxVoiceBytes+1))
	if err != nil {
		return nil, cleanErr("download", err)
	}
	if len(data) > MaxVoiceBytes {
		return nil, errors.New("telegram: file too large")
	}
	return data, nil
}
