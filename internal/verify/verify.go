// Package verify checks submitted channels with a signed-in user account.
//
// It runs slowly, waits out FLOOD_WAIT, and never joins a channel. A megagroup, or a public
// channel with no post in 90 days, is marked dead. Pending channels stay out of search.
package verify

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/Aryan-mor/telegram-channel-index/internal/store"
)

// ErrGone means the channel or the invite does not exist.
var ErrGone = errors.New("channel not found")

// Checker reads one channel.
type Checker interface {
	Check(ctx context.Context, ch store.Channel) (store.Report, error)
}

// stalePost is how long a channel may go without a post.
const stalePost = 90 * 24 * time.Hour

// Worker checks pending channels one by one.
type Worker struct {
	Store *store.Store
	Check Checker
	// PerMin is how many channels are checked per minute.
	PerMin int
	// MaxTries is how many failed checks turn a channel dead.
	MaxTries int
	Log      *slog.Logger
	Now      func() time.Time
}

// Run checks channels until ctx ends.
func (w *Worker) Run(ctx context.Context) {
	if w.Now == nil {
		w.Now = time.Now
	}
	if w.Log == nil {
		w.Log = slog.Default()
	}
	per := w.PerMin
	if per <= 0 {
		per = 20
	}
	if w.MaxTries <= 0 {
		w.MaxTries = 10
	}
	gap := time.Minute / time.Duration(per)
	for ctx.Err() == nil {
		next, err := w.Store.Pending(ctx, 1)
		if err != nil || len(next) == 0 {
			sleep(ctx, 5*time.Second)
			continue
		}
		sleep(ctx, w.one(ctx, next[0], gap))
	}
}

func (w *Worker) one(ctx context.Context, ch store.Channel, gap time.Duration) time.Duration {
	rep, err := w.Check.Check(ctx, ch)
	now := w.Now()
	switch {
	case err == nil && !acceptable(ch, rep, now):
		err = w.Store.MarkDead(ctx, ch.ID, now)
		w.Log.Info("rejected", "id", ch.ID)
	case err == nil:
		err = w.Store.MarkLive(ctx, ch.ID, rep, now)
		w.Log.Info("live", "id", ch.ID)
	case errors.Is(err, ErrGone):
		err = w.Store.MarkDead(ctx, ch.ID, now)
		w.Log.Info("dead", "id", ch.ID)
	default:
		if d, ok := flood(err); ok {
			w.Log.Warn("flood wait", "wait", d)
			return d + time.Second
		}
		w.Log.Warn("check failed", "id", ch.ID, "err", err)
		err = w.Store.Retry(ctx, ch.ID, w.MaxTries, now)
	}
	if err != nil && ctx.Err() == nil {
		w.Log.Error("store update failed", "id", ch.ID, "err", err)
	}
	return gap
}

func acceptable(ch store.Channel, rep store.Report, now time.Time) bool {
	if rep.Reject {
		return false
	}
	public := ch.Username != "" || ch.InviteHash == ""
	if !rep.HasPost {
		return !public
	}
	return now.Sub(time.Unix(rep.LastPost, 0)) <= stalePost
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-time.After(d):
	case <-ctx.Done():
	}
}
