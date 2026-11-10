package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestVoteTransitions(t *testing.T) {
	st := openTest(t)
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	ch, err := st.Submit(ctx, "somechannel", "", 1, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddTags(ctx, ch.ID, 1, []string{"news"}, now); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkLive(ctx, ch.ID, Report{PeerID: 99, Username: "somechannel", Title: "Title", Members: 10, HasPost: true, LastPost: now.Unix()}, now); err != nil {
		t.Fatal(err)
	}

	steps := []struct {
		value, weight, wantValue, wantScore int
		toggle                              bool
	}{
		{value: 1, weight: 1, wantValue: 1, wantScore: 1},
		{value: 1, weight: 1, wantValue: 1, wantScore: 1},
		{value: -1, weight: 1, wantValue: -1, wantScore: -1},
		{value: 0, weight: 1, wantValue: 0, wantScore: 0},
		{value: 1, weight: 2, wantValue: 1, wantScore: 2},
		{value: 1, weight: 2, toggle: true, wantValue: 0, wantScore: 0},
	}
	for i, step := range steps {
		got, score, err := st.Vote(ctx, 7, ch.ID, "news", step.value, step.weight, false, step.toggle, now)
		if err != nil {
			t.Fatal(err)
		}
		if got != step.wantValue || score != step.wantScore {
			t.Fatalf("step %d got value %d score %d, want value %d score %d", i, got, score, step.wantValue, step.wantScore)
		}
		sum, err := st.VoteSum(ctx, ch.ID, "news")
		if err != nil || sum != score {
			t.Fatalf("step %d score %d sum %d err %v", i, score, sum, err)
		}
	}
	if _, score, err := st.Vote(ctx, 8, ch.ID, "news", 1, 1, true, false, now); err != nil || score != 1 {
		t.Fatalf("bot vote score=%d err=%v", score, err)
	}
	n, err := st.PurgeVoter(ctx, 8)
	if err != nil || n != 1 {
		t.Fatalf("purge n=%d err=%v", n, err)
	}
	score, err := st.ScoreOf(ctx, ch.ID, "news")
	sum, err2 := st.VoteSum(ctx, ch.ID, "news")
	if err != nil || err2 != nil || score != 0 || sum != 0 {
		t.Fatalf("after purge score=%d sum=%d", score, sum)
	}
}

func TestRetiredFilmSlugBecomesMovie(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "index.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	ch, err := st.Submit(ctx, "somechannel", "", 1, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddTags(ctx, ch.ID, 1, []string{"film"}, now); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	got, err := st.Tags(ctx, ch.ID)
	if err != nil || len(got) != 1 || got[0].Tag != "movie" {
		t.Fatalf("%+v %v", got, err)
	}
}
