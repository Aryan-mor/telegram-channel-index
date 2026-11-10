// Package botapi is a small Bot API client that reads updates by long polling.
//
// Polling means the service opens no port and needs no domain or certificate.
package botapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Client talks to the Bot API.
type Client struct {
	token string
	base  string
	http  *http.Client
}

// New builds a client for one bot token.
func New(token string) *Client {
	return &Client{
		token: token,
		base:  "https://api.telegram.org",
		// Longer than the 50 second poll, so a normal poll never times out.
		http: &http.Client{Timeout: 70 * time.Second},
	}
}

// Update is one incoming event.
type Update struct {
	UpdateID int64          `json:"update_id"`
	Message  *Message       `json:"message"`
	Callback *CallbackQuery `json:"callback_query"`
}

// Message is a chat message.
type Message struct {
	MessageID int64    `json:"message_id"`
	From      *User    `json:"from"`
	Chat      Chat     `json:"chat"`
	Text      string   `json:"text"`
	ReplyTo   *Message `json:"reply_to_message"`
}

// User is the sender.
type User struct {
	ID           int64  `json:"id"`
	IsBot        bool   `json:"is_bot"`
	Username     string `json:"username"`
	LanguageCode string `json:"language_code"`
}

// Chat is where the message was sent.
type Chat struct {
	ID int64 `json:"id"`
}

// CallbackQuery is a button press.
type CallbackQuery struct {
	ID      string   `json:"id"`
	From    User     `json:"from"`
	Message *Message `json:"message"`
	Data    string   `json:"data"`
}

// Button is one inline button. Data is the callback payload.
type Button struct {
	Text string `json:"text"`
	Data string `json:"callback_data"`
}

type envelope struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	Description string          `json:"description"`
	Parameters  struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

// call posts a JSON request. The token is cut out of every error text.
func (c *Client) call(ctx context.Context, method string, body any) (json.RawMessage, error) {
	for attempt := 0; ; attempt++ {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/bot"+c.token+"/"+method, bytes.NewReader(raw))
		if err != nil {
			return nil, c.scrub(err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, c.scrub(err)
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if err != nil {
			return nil, c.scrub(err)
		}
		var env envelope
		if err := json.Unmarshal(data, &env); err != nil {
			return nil, fmt.Errorf("%s: bad response (status %d)", method, resp.StatusCode)
		}
		if env.OK {
			return env.Result, nil
		}
		if resp.StatusCode == http.StatusTooManyRequests && env.Parameters.RetryAfter > 0 && attempt == 0 {
			select {
			case <-time.After(time.Duration(env.Parameters.RetryAfter) * time.Second):
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return nil, fmt.Errorf("%s: %s", method, env.Description)
	}
}

func (c *Client) scrub(err error) error {
	return errors.New(strings.ReplaceAll(err.Error(), c.token, "***"))
}

// Updates waits up to timeoutSec seconds for new messages and button presses.
func (c *Client) Updates(ctx context.Context, offset int64, timeoutSec int) ([]Update, error) {
	res, err := c.call(ctx, "getUpdates", map[string]any{
		"offset":          offset,
		"timeout":         timeoutSec,
		"allowed_updates": []string{"message", "callback_query"},
	})
	if err != nil {
		return nil, err
	}
	var out []Update
	return out, json.Unmarshal(res, &out)
}

func markup(rows [][]Button) any {
	if len(rows) == 0 {
		return nil
	}
	return map[string]any{"inline_keyboard": rows}
}

// Send sends a text message and returns its id. Link previews are turned off.
func (c *Client) Send(ctx context.Context, chatID int64, text string, rows [][]Button) (int64, error) {
	body := map[string]any{
		"chat_id":              chatID,
		"text":                 text,
		"link_preview_options": map[string]any{"is_disabled": true},
	}
	if m := markup(rows); m != nil {
		body["reply_markup"] = m
	}
	res, err := c.call(ctx, "sendMessage", body)
	if err != nil {
		return 0, err
	}
	var msg struct {
		MessageID int64 `json:"message_id"`
	}
	if err := json.Unmarshal(res, &msg); err != nil {
		return 0, err
	}
	return msg.MessageID, nil
}

// Edit replaces the text and the buttons of a message the bot sent.
func (c *Client) Edit(ctx context.Context, chatID, messageID int64, text string, rows [][]Button) error {
	body := map[string]any{
		"chat_id":              chatID,
		"message_id":           messageID,
		"text":                 text,
		"link_preview_options": map[string]any{"is_disabled": true},
	}
	if m := markup(rows); m != nil {
		body["reply_markup"] = m
	}
	_, err := c.call(ctx, "editMessageText", body)
	return err
}

// Answer closes a button press with a short note.
func (c *Client) Answer(ctx context.Context, callbackID, text string) error {
	_, err := c.call(ctx, "answerCallbackQuery", map[string]any{
		"callback_query_id": callbackID,
		"text":              text,
	})
	return err
}

// Handler sees one update.
type Handler func(ctx context.Context, u Update)

// Run polls until ctx ends and passes each update to h.
func (c *Client) Run(ctx context.Context, h Handler, log *slog.Logger) error {
	var offset int64
	wait := time.Second
	for ctx.Err() == nil {
		ups, err := c.Updates(ctx, offset, 50)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			log.Warn("poll failed", "err", err, "retry_in", wait)
			sleep(ctx, wait)
			if wait < 30*time.Second {
				wait *= 2
			}
			continue
		}
		wait = time.Second
		for _, u := range ups {
			offset = u.UpdateID + 1
			h(ctx, u)
		}
	}
	return ctx.Err()
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-time.After(d):
	case <-ctx.Done():
	}
}
