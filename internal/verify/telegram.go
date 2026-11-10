package verify

import (
	"context"
	"errors"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/Aryan-mor/telegram-channel-index/internal/store"
)

func flood(err error) (time.Duration, bool) {
	return tgerr.AsFloodWait(err)
}

// Telegram reads public channels and private invite links. It does not join.
type Telegram struct {
	api *tg.Client
}

// NewTelegram builds a Checker on a signed-in client.
func NewTelegram(api *tg.Client) *Telegram { return &Telegram{api: api} }

// Check resolves a public username or previews a private invite.
func (t *Telegram) Check(ctx context.Context, ch store.Channel) (store.Report, error) {
	if ch.Username != "" {
		rep, err := t.checkPublic(ctx, ch.Username)
		return rep, gone(err)
	}
	if ch.InviteHash != "" {
		rep, err := t.checkPrivate(ctx, ch.InviteHash)
		return rep, gone(err)
	}
	return store.Report{}, ErrGone
}

func gone(err error) error {
	if err == nil {
		return nil
	}
	if tgerr.Is(err, "USERNAME_NOT_OCCUPIED", "USERNAME_INVALID", "INVITE_HASH_INVALID", "INVITE_HASH_EXPIRED", "CHANNEL_INVALID") {
		return ErrGone
	}
	return err
}

func (t *Telegram) checkPublic(ctx context.Context, name string) (store.Report, error) {
	res, err := t.api.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{Username: name})
	if err != nil {
		return store.Report{}, err
	}
	peer, ok := res.Peer.(*tg.PeerChannel)
	if !ok {
		return store.Report{}, ErrGone
	}
	for _, raw := range res.Chats {
		ch, ok := raw.(*tg.Channel)
		if ok && ch.ID == peer.ChannelID {
			if ch.Username == "" {
				ch.Username = name
			}
			return t.fromChannel(ctx, ch, false)
		}
	}
	return store.Report{}, ErrGone
}

func (t *Telegram) checkPrivate(ctx context.Context, hash string) (store.Report, error) {
	inv, err := t.api.MessagesCheckChatInvite(ctx, hash)
	if err != nil {
		return store.Report{}, err
	}
	switch v := inv.(type) {
	case *tg.ChatInvite:
		if !v.Channel || !v.Broadcast || v.Megagroup {
			return store.Report{Reject: true, Title: v.Title, Members: v.ParticipantsCount}, nil
		}
		return store.Report{Title: v.Title, Members: v.ParticipantsCount}, nil
	case *tg.ChatInviteAlready:
		return t.fromChat(ctx, v.Chat, true)
	case *tg.ChatInvitePeek:
		return t.fromChat(ctx, v.Chat, true)
	default:
		return store.Report{}, errors.New("unexpected invite")
	}
}

func (t *Telegram) fromChat(ctx context.Context, chat tg.ChatClass, allowPrivate bool) (store.Report, error) {
	ch, ok := chat.(*tg.Channel)
	if !ok {
		return store.Report{}, ErrGone
	}
	return t.fromChannel(ctx, ch, allowPrivate)
}

func (t *Telegram) fromChannel(ctx context.Context, ch *tg.Channel, allowPrivate bool) (store.Report, error) {
	if ch.Megagroup || !ch.Broadcast {
		return store.Report{Reject: true, PeerID: ch.ID, Username: ch.Username, Title: ch.Title}, nil
	}
	rep := store.Report{PeerID: ch.ID, Username: ch.Username, Title: ch.Title}
	full, err := t.api.ChannelsGetFullChannel(ctx, &tg.InputChannel{ChannelID: ch.ID, AccessHash: ch.AccessHash})
	if err != nil {
		if !(allowPrivate && tgerr.Is(err, "CHANNEL_PRIVATE")) {
			return store.Report{}, err
		}
	} else if cf, ok := full.FullChat.(*tg.ChannelFull); ok {
		rep.Members = cf.ParticipantsCount
		if cf.About != "" && rep.Title == "" {
			rep.Title = ch.Title
		}
	}
	hist, err := t.api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
		Peer:  &tg.InputPeerChannel{ChannelID: ch.ID, AccessHash: ch.AccessHash},
		Limit: 1,
	})
	if err != nil {
		if allowPrivate && tgerr.Is(err, "CHANNEL_PRIVATE") {
			return rep, nil
		}
		return store.Report{}, err
	}
	if msg, ok := latest(hist); ok {
		rep.HasPost = true
		rep.LastPost = int64(msg.Date)
	}
	return rep, nil
}

func latest(res tg.MessagesMessagesClass) (*tg.Message, bool) {
	var msgs []tg.MessageClass
	switch v := res.(type) {
	case *tg.MessagesMessages:
		msgs = v.Messages
	case *tg.MessagesMessagesSlice:
		msgs = v.Messages
	case *tg.MessagesChannelMessages:
		msgs = v.Messages
	}
	for _, raw := range msgs {
		if m, ok := raw.(*tg.Message); ok {
			return m, true
		}
	}
	return nil, false
}
