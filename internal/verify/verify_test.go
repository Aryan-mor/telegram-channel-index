package verify

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/Aryan-mor/telegram-channel-index/internal/store"
)

type fake struct {
	rep store.Report
	err error
	n   int
}

func (f *fake) Check(context.Context, store.Channel) (store.Report, error) {
	f.n++
	return f.rep, f.err
}

func TestWorkerOutcomes(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "v.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	add := func(name, invite string) store.Channel {
		t.Helper()
		ch, err := st.Submit(ctx, name, invite, 1, now)
		if err != nil {
			t.Fatal(err)
		}
		return ch
	}
	run := func(ch store.Channel, f *fake) {
		t.Helper()
		w := &Worker{Store: st, Check: f, PerMin: 600, MaxTries: 10, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return now }}
		w.one(ctx, ch, 0)
	}

	live := add("newschan", "")
	run(live, &fake{rep: store.Report{PeerID: 5, Username: "newschan", Title: "News", Members: 20, HasPost: true, LastPost: now.Add(-time.Hour).Unix()}})
	got, ok, err := st.Find(ctx, "newschan", "", 0)
	if err != nil || !ok || got.State != store.StateLive || got.Members != 20 {
		t.Fatalf("live %+v ok=%v err=%v", got, ok, err)
	}

	old := add("oldchan", "")
	run(old, &fake{rep: store.Report{HasPost: true, LastPost: now.Add(-100 * 24 * time.Hour).Unix()}})
	got, _, _ = st.Find(ctx, "oldchan", "", 0)
	if got.State != store.StateDead {
		t.Fatalf("stale state %s", got.State)
	}

	group := add("groupchan", "")
	run(group, &fake{rep: store.Report{Reject: true, HasPost: true, LastPost: now.Unix()}})
	got, _, _ = st.Find(ctx, "groupchan", "", 0)
	if got.State != store.StateDead {
		t.Fatalf("group state %s", got.State)
	}

	priv := add("", "InviteHash1")
	run(priv, &fake{rep: store.Report{Title: "Hidden", Members: 3}})
	got, _, _ = st.Find(ctx, "", "InviteHash1", 0)
	if got.State != store.StateLive || got.Title != "Hidden" {
		t.Fatalf("private %+v", got)
	}

	flaky := add("flaky", "")
	f := &fake{err: context.DeadlineExceeded}
	for i := 0; i < 9; i++ {
		run(flaky, f)
	}
	got, _, _ = st.Find(ctx, "flaky", "", 0)
	if got.State != store.StatePending || got.Tries != 9 {
		t.Fatalf("retry state %s tries %d", got.State, got.Tries)
	}
	run(flaky, f)
	got, _, _ = st.Find(ctx, "flaky", "", 0)
	if got.State != store.StateDead {
		t.Fatalf("tenth try state %s", got.State)
	}
}
