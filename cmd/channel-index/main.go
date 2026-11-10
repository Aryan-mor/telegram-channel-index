// Command channel-index is a Telegram bot that keeps channels under a fixed set of tags.
//
//	channel-index login   sign the checker's user account in once (needs a terminal)
//	channel-index serve   run the bot and the checker (default)
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Aryan-mor/telegram-channel-index/internal/botapi"
	"github.com/Aryan-mor/telegram-channel-index/internal/service"
	"github.com/Aryan-mor/telegram-channel-index/internal/store"
	"github.com/Aryan-mor/telegram-channel-index/internal/userauth"
	"github.com/Aryan-mor/telegram-channel-index/internal/verify"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch cmd {
	case "login":
		err = login(ctx)
	case "say":
		err = say(ctx)
	case "serve":
		err = serve(ctx, log)
	default:
		err = fmt.Errorf("unknown command %q (use login or serve)", cmd)
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Error("stopped", "err", err)
		os.Exit(1)
	}
}

// say sends the arguments, after the bot name, to that bot from the checker account and prints
// the answers. Stop the service first so two programs do not share one login.
func say(ctx context.Context) error {
	if len(os.Args) < 4 {
		return errors.New("usage: say <botusername> <text>...")
	}
	id, hash, err := telegramKeys()
	if err != nil {
		return err
	}
	return userauth.Say(ctx, id, hash, dataDir(), os.Args[2], os.Args[3:], os.Stdout)
}

func dataDir() string { return env("DATA_DIR", "data") }

func telegramKeys() (int, string, error) {
	id, err := strconv.Atoi(os.Getenv("TG_API_ID"))
	hash := os.Getenv("TG_API_HASH")
	if err != nil || hash == "" {
		return 0, "", errors.New("set TG_API_ID and TG_API_HASH (from my.telegram.org)")
	}
	return id, hash, nil
}

func login(ctx context.Context) error {
	id, hash, err := telegramKeys()
	if err != nil {
		return err
	}
	return userauth.Login(ctx, id, hash, dataDir(), os.Stdin, os.Stdout)
}

func serve(ctx context.Context, log *slog.Logger) error {
	token := os.Getenv("BOT_TOKEN")
	if token == "" {
		return errors.New("set BOT_TOKEN")
	}
	admins, err := parseIDs(os.Getenv("ADMIN_IDS"))
	if err != nil || len(admins) == 0 {
		return errors.New("set ADMIN_IDS to a comma separated list of Telegram user ids")
	}
	dir := dataDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	st, err := store.Open(filepath.Join(dir, "index.db"))
	if err != nil {
		return err
	}
	defer st.Close()

	svc := service.New(service.Config{
		Admins:          admins,
		Open:            os.Getenv("OPEN") != "false",
		PrivateLinks:    os.Getenv("PRIVATE_LINKS") == "true",
		VotesPerHour:    envInt("VOTES_PER_HOUR", 30),
		SubmitsPerDay:   envInt("SUBMITS_PER_DAY", 10),
		SearchesPerHour: envInt("SEARCHES_PER_HOUR", 60),
	}, st)

	go runVerifier(ctx, st, log)

	bot := botapi.New(token)
	log.Info("bot running")
	return bot.Run(ctx, func(ctx context.Context, u botapi.Update) {
		in, ok := incoming(u)
		if !ok {
			return
		}
		for _, a := range svc.Handle(ctx, in) {
			if err := apply(ctx, bot, svc, a); err != nil {
				log.Warn("send failed", "err", err)
			}
		}
	}, log)
}

func incoming(u botapi.Update) (service.Incoming, bool) {
	if u.Callback != nil {
		msgID := int64(0)
		chat := int64(0)
		if u.Callback.Message != nil {
			msgID = u.Callback.Message.MessageID
			chat = u.Callback.Message.Chat.ID
		}
		return service.Incoming{
			From:              service.From{ID: u.Callback.From.ID, IsBot: u.Callback.From.IsBot, Lang: u.Callback.From.LanguageCode},
			ChatID:            chat,
			CallbackID:        u.Callback.ID,
			CallbackData:      u.Callback.Data,
			CallbackMessageID: msgID,
		}, true
	}
	m := u.Message
	if m == nil || m.From == nil || strings.TrimSpace(m.Text) == "" {
		return service.Incoming{}, false
	}
	in := service.Incoming{
		From:   service.From{ID: m.From.ID, IsBot: m.From.IsBot, Lang: m.From.LanguageCode},
		ChatID: m.Chat.ID,
		Text:   m.Text,
	}
	if m.ReplyTo != nil {
		in.ReplyToID = m.ReplyTo.MessageID
	}
	return in, true
}

func apply(ctx context.Context, bot *botapi.Client, svc *service.Service, a service.Action) error {
	rows := make([][]botapi.Button, len(a.Rows))
	for i, row := range a.Rows {
		rows[i] = make([]botapi.Button, len(row))
		for j, b := range row {
			rows[i][j] = botapi.Button{Text: b.Text, Data: b.Data}
		}
	}
	switch a.Kind {
	case "answer":
		return bot.Answer(ctx, a.CallbackID, a.Text)
	case "edit":
		return bot.Edit(ctx, a.ChatID, a.MessageID, a.Text, rows)
	default:
		id, err := bot.Send(ctx, a.ChatID, a.Text, rows)
		if err != nil {
			return err
		}
		if a.ListID != 0 {
			return svc.BindList(ctx, a.ListID, a.ChatID, id)
		}
		return nil
	}
}

// runVerifier keeps the user account connected and checks pending channels. Channels stay
// pending, and the bot keeps working, while no session exists.
func runVerifier(ctx context.Context, st *store.Store, log *slog.Logger) {
	id, hash, err := telegramKeys()
	if err != nil {
		log.Warn("verifier off", "reason", err)
		return
	}
	wait := 5 * time.Second
	for ctx.Err() == nil {
		client := userauth.NewClient(id, hash, dataDir())
		err := client.Run(ctx, func(ctx context.Context) error {
			if err := userauth.Authorized(ctx, client); err != nil {
				return err
			}
			log.Info("verifier running")
			wait = 5 * time.Second
			(&verify.Worker{
				Store:  st,
				Check:  verify.NewTelegram(client.API()),
				PerMin: envInt("VERIFY_PER_MIN", 20),
				Log:    log,
			}).Run(ctx)
			return ctx.Err()
		})
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, userauth.ErrNotLoggedIn) {
			wait = 5 * time.Minute
		}
		log.Warn("verifier stopped", "err", err, "retry_in", wait)
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return
		}
		if wait < 5*time.Minute {
			wait *= 2
		}
	}
}

func parseIDs(s string) (map[int64]bool, error) {
	out := map[int64]bool{}
	for _, f := range strings.Split(s, ",") {
		if f = strings.TrimSpace(f); f == "" {
			continue
		}
		n, err := strconv.ParseInt(f, 10, 64)
		if err != nil {
			return nil, err
		}
		out[n] = true
	}
	return out, nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if n, err := strconv.Atoi(os.Getenv(key)); err == nil && n > 0 {
		return n
	}
	return def
}
