package botapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSendEditAnswer(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	var bodies []map[string]any
	polls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var req map[string]any
		json.Unmarshal(raw, &req)
		mu.Lock()
		calls = append(calls, r.URL.Path)
		bodies = append(bodies, req)
		mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			allowed, _ := req["allowed_updates"].([]any)
			if len(allowed) != 2 || allowed[0] != "message" || allowed[1] != "callback_query" {
				t.Errorf("allowed_updates %v", allowed)
			}
			mu.Lock()
			polls++
			n := polls
			mu.Unlock()
			if n == 1 {
				io.WriteString(w, `{"ok":true,"result":[
					{"update_id":1,"message":{"message_id":7,"from":{"id":5,"is_bot":false,"username":"ada","language_code":"fa"},"chat":{"id":9},"text":"/tags","reply_to_message":{"message_id":3,"text":"old"}}},
					{"update_id":2,"callback_query":{"id":"cb1","from":{"id":5,"is_bot":false},"message":{"message_id":7,"chat":{"id":9}},"data":"v1:u:1:1"}}
				]}`)
				return
			}
			io.WriteString(w, `{"ok":true,"result":[]}`)
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			io.WriteString(w, `{"ok":true,"result":{"message_id":42}}`)
		default:
			io.WriteString(w, `{"ok":true,"result":true}`)
		}
	}))
	defer srv.Close()

	c := New("123:SECRET")
	c.base = srv.URL
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var got []Update
	go func() {
		for {
			mu.Lock()
			n := len(got)
			mu.Unlock()
			if n >= 2 {
				cancel()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	done := make(chan struct{})
	go func() {
		c.Run(ctx, func(_ context.Context, u Update) {
			mu.Lock()
			got = append(got, u)
			mu.Unlock()
		}, slog.New(slog.NewTextHandler(io.Discard, nil)))
		close(done)
	}()

	id, err := c.Send(context.Background(), 9, "list 1 tag news page 1/1", [][]Button{{{Text: "1", Data: "v1:u:1:1"}}})
	if err != nil || id != 42 {
		t.Fatalf("send id=%d err=%v", id, err)
	}
	if err := c.Edit(context.Background(), 9, 42, "list 1 tag news page 2/3", [][]Button{{{Text: "Next", Data: "v1:p:1:2"}}}); err != nil {
		t.Fatal(err)
	}
	if err := c.Answer(context.Background(), "cb1", "+1"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("poll did not stop")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) < 2 || got[0].Message == nil || got[0].Message.MessageID != 7 || got[0].Message.ReplyTo.MessageID != 3 {
		t.Fatalf("message update %+v", got)
	}
	if got[0].Message.From.Username != "ada" || got[0].Message.From.LanguageCode != "fa" {
		t.Fatalf("from %+v", got[0].Message.From)
	}
	if got[1].Callback == nil || got[1].Callback.Data != "v1:u:1:1" || got[1].Callback.ID != "cb1" {
		t.Fatalf("callback %+v", got[1].Callback)
	}
	var sawSend, sawEdit, sawAnswer bool
	for i, path := range calls {
		switch {
		case strings.HasSuffix(path, "/sendMessage"):
			sawSend = true
			if bodies[i]["chat_id"].(float64) != 9 || bodies[i]["reply_markup"] == nil {
				t.Fatalf("send body %v", bodies[i])
			}
		case strings.HasSuffix(path, "/editMessageText"):
			sawEdit = true
			if bodies[i]["message_id"].(float64) != 42 {
				t.Fatalf("edit body %v", bodies[i])
			}
		case strings.HasSuffix(path, "/answerCallbackQuery"):
			sawAnswer = true
			if bodies[i]["callback_query_id"] != "cb1" || bodies[i]["text"] != "+1" {
				t.Fatalf("answer body %v", bodies[i])
			}
		}
	}
	if !sawSend || !sawEdit || !sawAnswer {
		t.Fatalf("calls %v", calls)
	}
}

func TestRetryAfterAndTokenScrub(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"ok":false,"description":"slow down","parameters":{"retry_after":1}}`)
			return
		}
		io.WriteString(w, `{"ok":true,"result":{"message_id":1}}`)
	}))
	c := New("123:SECRET")
	c.base = srv.URL
	start := time.Now()
	if _, err := c.Send(context.Background(), 1, "x", nil); err != nil || calls != 2 || time.Since(start) < time.Second {
		t.Fatalf("retry: err=%v calls=%d", err, calls)
	}
	srv.Close()
	_, err := c.Send(context.Background(), 1, "x", nil)
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("error leaks the token: %v", err)
	}
}
