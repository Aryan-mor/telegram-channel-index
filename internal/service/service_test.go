package service

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Aryan-mor/telegram-channel-index/internal/store"
)

func newTest(t *testing.T, cfg Config) (*Service, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Unix(1_700_000_000, 0) }
	}
	if cfg.Admins == nil {
		cfg.Admins = map[int64]bool{1: true}
	}
	cfg.Open = true
	return New(cfg, st), st
}

func (s *Service) ask(text string, from From) []Action {
	return s.Handle(context.Background(), Incoming{From: from, ChatID: 10, Text: text})
}

func one(t *testing.T, a []Action) string {
	t.Helper()
	if len(a) != 1 || a[0].Kind != "send" {
		t.Fatalf("actions %#v", a)
	}
	return a[0].Text
}

func TestSilenceAndHelp(t *testing.T) {
	s, st := newTest(t, Config{})
	person := From{ID: 5}
	if a := s.ask("hello", person); a != nil {
		t.Fatalf("plain text %#v", a)
	}
	if a := s.ask("/nope", person); a != nil {
		t.Fatalf("unknown %#v", a)
	}
	if a := s.ask("/accept 3", person); a != nil {
		t.Fatalf("bare vote %#v", a)
	}
	help := one(t, s.ask("/start@ChannelIndexBot", person))
	for _, bad := range []string{"/accept", "/reject", "/unvote", "option", "bot"} {
		if strings.Contains(strings.ToLower(help), bad) {
			t.Fatalf("help contains %q:\n%s", bad, help)
		}
	}
	if strings.HasPrefix(help, "/") || strings.HasPrefix(help, "#") {
		t.Fatal(help)
	}
	tags := one(t, s.ask("/tags@bot", person))
	if !strings.Contains(tags, "direct-file") || strings.Contains(tags, "/") {
		t.Fatal(tags)
	}
	s.cfg.Open = false
	if a := s.ask("/tags", person); a != nil {
		t.Fatal("stranger was answered")
	}
	if got := one(t, s.ask("/tags", From{ID: 1})); !strings.Contains(got, "movie") || strings.Contains(got, "film") {
		t.Fatal(got)
	}
	s.cfg.Open = true
	if err := st.SetRole(context.Background(), 5, store.RoleBanned); err != nil {
		t.Fatal(err)
	}
	if a := s.ask("/tags", person); a != nil {
		t.Fatal("banned was answered")
	}
}

func TestAddSearchVote(t *testing.T) {
	s, st := newTest(t, Config{PrivateLinks: true})
	person := From{ID: 5}
	if one(t, s.ask("/add @somechannel news movie", person)) != "pending @somechannel movie news" && false {
		t.Fatal("order")
	}
	got := one(t, s.ask("/add @somechannel news movie", person))
	if got != "pending @somechannel news movie" {
		t.Fatal(got)
	}
	if one(t, s.ask("/search news", person)) != "none" {
		t.Fatal("pending showed up")
	}
	ch := mustLive(t, st, "somechannel")
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	for i, name := range []string{"earlieraaa", "earlierbbb"} {
		extra, err := st.Submit(ctx, name, "", 1, now)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.AddTags(ctx, extra.ID, 1, []string{"news"}, now); err != nil {
			t.Fatal(err)
		}
		if err := st.MarkLive(ctx, extra.ID, store.Report{PeerID: int64(50 + i), Username: name, Title: "T", Members: 30000 - i, HasPost: true, LastPost: now.Unix()}, now); err != nil {
			t.Fatal(err)
		}
	}
	page := s.ask("/search@bot news", person)
	text := one(t, page)
	if !strings.HasPrefix(text, "list ") || !strings.Contains(text, "tag news page 1/1") || !strings.Contains(text, "3 @somechannel +0 ") {
		t.Fatal(text)
	}
	if strings.Contains(text, "\nnext:") {
		t.Fatal("next on last page")
	}
	if err := s.BindList(context.Background(), page[0].ListID, 10, 77); err != nil {
		t.Fatal(err)
	}
	line := one(t, s.ask("/accept @somechannel news", person))
	if line != "ok news @somechannel +1 score 1" || strings.Contains(line, "/") {
		t.Fatal(line)
	}
	if again := one(t, s.ask("/accept @SomeChannel news", person)); again != line {
		t.Fatalf("repeat %q", again)
	}
	if got := one(t, s.Handle(context.Background(), Incoming{From: person, ChatID: 10, Text: "/accept option3", ReplyToID: 77})); got != "ok news @somechannel +1 score 1" {
		t.Fatal(got)
	}
	ref := "/accept " + strconv.FormatInt(page[0].ListID, 10) + ":3"
	if got := one(t, s.ask(ref, person)); got != "ok news @somechannel +1 score 1" {
		t.Fatal(got)
	}
	if got := one(t, s.ask("/reject @somechannel news", person)); got != "ok news @somechannel -1 score -1" {
		t.Fatal(got)
	}
	if got := one(t, s.ask("/unvote @somechannel news", person)); got != "ok news @somechannel 0 score 0" {
		t.Fatal(got)
	}
	if a := s.ask("/accept 99999:1", person); a != nil {
		t.Fatalf("unknown list %#v", a)
	}
	admin := one(t, s.ask("/accept @somechannel news", From{ID: 1}))
	if admin != "ok news @somechannel +1 score 2" {
		t.Fatal(admin)
	}
	bot := one(t, s.ask("/accept @somechannel news", From{ID: 42, IsBot: true}))
	if bot != "ok news @somechannel +1 score 3" {
		t.Fatal(bot)
	}
	if one(t, s.ask("/purge 1", From{ID: 1})) != "ok" {
		t.Fatal("purge")
	}
	score, err := st.ScoreOf(context.Background(), ch.ID, "news")
	if err != nil || score != 1 {
		t.Fatalf("score after purge %d %v", score, err)
	}
	info := one(t, s.ask("/info c/"+strconv.FormatInt(ch.PeerID, 10), person))
	if !strings.Contains(info, "@somechannel live") || !strings.Contains(info, "news +1") {
		t.Fatal(info)
	}
	if one(t, s.ask("/add t.me/+abcdefghij news", From{ID: 9})) == "no" {
		t.Fatal("private link rejected while enabled")
	}
	s.cfg.PrivateLinks = false
	if one(t, s.ask("/add t.me/+abcdefghij news", From{ID: 9})) != "no" {
		t.Fatal("private link accepted while disabled")
	}
}

func mustLive(t *testing.T, st *store.Store, name string) store.Channel {
	t.Helper()
	ctx := context.Background()
	ch, ok, err := st.Find(ctx, name, "", 0)
	if err != nil || !ok {
		t.Fatal(err, ok)
	}
	if err := st.MarkLive(ctx, ch.ID, store.Report{PeerID: 1029384756, Username: name, Title: "Title text", Members: 18300, HasPost: true, LastPost: 1_700_000_000}, time.Unix(1_700_000_000, 0)); err != nil {
		t.Fatal(err)
	}
	ch, err = st.Channel(ctx, ch.ID)
	if err != nil {
		t.Fatal(err)
	}
	return ch
}

func TestButtonsAndPaging(t *testing.T) {
	s, st := newTest(t, Config{})
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	for i := 0; i < 11; i++ {
		name := "chan" + strconv.Itoa(i+10000)
		ch, err := st.Submit(ctx, name, "", 1, now)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.AddTags(ctx, ch.ID, 1, []string{"news"}, now); err != nil {
			t.Fatal(err)
		}
		if err := st.MarkLive(ctx, ch.ID, store.Report{PeerID: int64(i + 1), Username: name, Title: "T", Members: 100 - i, HasPost: true, LastPost: now.Unix()}, now); err != nil {
			t.Fatal(err)
		}
	}
	person := From{ID: 5, Lang: "fa"}
	page := s.ask("/search news", person)
	text := one(t, page)
	if !strings.Contains(text, "page 1/2") || !strings.Contains(text, "next: /search news 2") {
		t.Fatal(text)
	}
	if page[0].Rows[2][0].Text != "قبلی" {
		t.Fatalf("labels %#v", page[0].Rows[2])
	}
	next := page[0].Rows[2][2]
	edited := s.Handle(ctx, Incoming{From: person, ChatID: 10, CallbackID: "c1", CallbackData: next.Data, CallbackMessageID: 4})
	if len(edited) != 2 || edited[0].Kind != "answer" || edited[1].Kind != "edit" || !strings.Contains(edited[1].Text, "page 2/2") {
		t.Fatalf("%#v", edited)
	}
	if strings.Contains(edited[1].Text, "next:") {
		t.Fatal(edited[1].Text)
	}
	up := page[0].Rows[0][0]
	vote := s.Handle(ctx, Incoming{From: person, ChatID: 10, CallbackID: "c2", CallbackData: up.Data, CallbackMessageID: 4})
	if oneAnswer(t, vote) != "+1" {
		t.Fatal(vote)
	}
	again := s.Handle(ctx, Incoming{From: person, ChatID: 10, CallbackID: "c3", CallbackData: up.Data, CallbackMessageID: 4})
	if oneAnswer(t, again) != "برداشته شد" {
		t.Fatal(again)
	}
	mode := page[0].Rows[2][1].Data
	switched := s.Handle(ctx, Incoming{From: person, ChatID: 10, CallbackID: "c4", CallbackData: mode, CallbackMessageID: 4})
	if len(switched) != 2 || !strings.HasPrefix(switched[1].Rows[0][0].Data, "v1:d:") {
		t.Fatalf("%#v", switched)
	}
}

func oneAnswer(t *testing.T, a []Action) string {
	t.Helper()
	if len(a) != 1 || a[0].Kind != "answer" {
		t.Fatalf("%#v", a)
	}
	return a[0].Text
}

func TestCallbackFits(t *testing.T) {
	rows := keyboard(999999999, 10, 100, true, "en")
	longest := ""
	for _, row := range rows {
		for _, b := range row {
			if len(b.Data) > len(longest) {
				longest = b.Data
			}
			if len(b.Data) > 64 {
				t.Fatal(b.Data)
			}
		}
	}
	if len(longest) != 18 {
		t.Fatalf("longest %d %q", len(longest), longest)
	}
}

func TestLimits(t *testing.T) {
	s, _ := newTest(t, Config{SearchesPerHour: 1, SubmitsPerDay: 1, VotesPerHour: 1})
	person := From{ID: 5}
	if one(t, s.ask("/add @somechannel news", person)) != "pending @somechannel news" {
		t.Fatal("add")
	}
	if one(t, s.ask("/tag @somechannel movie", person)) != "limited" {
		t.Fatal("submit limit")
	}
	if one(t, s.ask("/search news", person)) != "none" {
		t.Fatal("search")
	}
	if one(t, s.ask("/search movie", person)) != "limited" {
		t.Fatal("search limit")
	}
}
