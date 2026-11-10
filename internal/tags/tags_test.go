package tags

import "testing"

func TestCanon(t *testing.T) {
	if Canon("Movie") != "movie" || Canon("#movie") != "movie" {
		t.Fatal("movie")
	}
	if Canon("film") != "movie" {
		t.Fatal("film must be stored as movie")
	}
	if Canon("nope") != "" {
		t.Fatal("unknown")
	}
}
