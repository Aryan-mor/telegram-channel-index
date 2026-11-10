package userauth

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/gotd/td/tg"
)

// Say sends each text to a bot from the signed-in account, one after the other, and prints the
// bot's answer to each. It is a way to try a running bot by hand.
func Say(ctx context.Context, apiID int, apiHash, dir, bot string, texts []string, out io.Writer) error {
	client := NewClient(apiID, apiHash, dir)
	return client.Run(ctx, func(ctx context.Context) error {
		if err := Authorized(ctx, client); err != nil {
			return err
		}
		api := client.API()
		res, err := api.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{Username: strings.TrimPrefix(bot, "@")})
		if err != nil {
			return err
		}
		var peer *tg.InputPeerUser
		for _, raw := range res.Users {
			if u, ok := raw.(*tg.User); ok && u.Bot {
				peer = &tg.InputPeerUser{UserID: u.ID, AccessHash: u.AccessHash}
			}
		}
		if peer == nil {
			return errors.New("that name is not a bot")
		}
		for _, text := range texts {
			var b [8]byte
			if _, err := rand.Read(b[:]); err != nil {
				return err
			}
			up, err := api.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{
				Peer: peer, Message: text, RandomID: int64(binary.LittleEndian.Uint64(b[:])),
			})
			if err != nil {
				return err
			}
			sent := sentID(up)
			fmt.Fprintf(out, ">>> %s\n", text)
			reply, err := waitReply(ctx, api, peer, sent, 20*time.Second)
			if err != nil {
				fmt.Fprintf(out, "<<< (%v)\n\n", err)
				continue
			}
			fmt.Fprintf(out, "<<< %s\n\n", reply)
			// Stay under the bot's rate limit between messages.
			time.Sleep(time.Second)
		}
		return nil
	})
}

func sentID(up tg.UpdatesClass) int {
	switch v := up.(type) {
	case *tg.UpdateShortSentMessage:
		return v.ID
	case *tg.Updates:
		for _, u := range v.Updates {
			if m, ok := u.(*tg.UpdateMessageID); ok {
				return m.ID
			}
		}
	}
	return 0
}

func waitReply(ctx context.Context, api *tg.Client, peer *tg.InputPeerUser, after int, wait time.Duration) (string, error) {
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		select {
		case <-time.After(time.Second):
		case <-ctx.Done():
			return "", ctx.Err()
		}
		res, err := api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: peer, Limit: 10, MinID: after})
		if err != nil {
			return "", err
		}
		var msgs []tg.MessageClass
		switch v := res.(type) {
		case *tg.MessagesMessages:
			msgs = v.Messages
		case *tg.MessagesMessagesSlice:
			msgs = v.Messages
		}
		answer := ""
		for _, raw := range msgs {
			if m, ok := raw.(*tg.Message); ok && !m.Out && m.ID > after {
				answer = m.Message
			}
		}
		if answer != "" {
			return answer, nil
		}
	}
	return "", errors.New("no answer")
}
