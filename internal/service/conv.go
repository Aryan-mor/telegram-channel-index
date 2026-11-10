package service

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/Aryan-mor/telegram-channel-index/internal/tags"
)

// Target is a channel a command named.
type Target struct {
	Username string
	Invite   string
	PeerID   int64
}

type command struct {
	name string
	args string
}

func parseCommand(text string) (command, bool) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return command{}, false
	}
	name, args, _ := strings.Cut(text, " ")
	args = strings.TrimSpace(args)
	if i := strings.IndexByte(name, '@'); i >= 0 {
		name = name[:i]
	}
	if name == "/" || name == "" {
		return command{}, false
	}
	return command{name: strings.ToLower(name), args: args}, true
}

var userName = regexp.MustCompile(`^[a-z][a-z0-9_]{4,31}$`)
var inviteHash = regexp.MustCompile(`^[A-Za-z0-9_-]{8,}$`)
var optionN = regexp.MustCompile(`^option(\d+)$`)

// parseTarget reads @name, a t.me link, c/<id>, or a private +hash.
// private must be true before a new invite link is accepted.
func parseTarget(raw string, private bool) (Target, bool) {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	low := strings.ToLower(s)
	if strings.HasPrefix(low, "t.me/") {
		s = s[len("t.me/"):]
		low = strings.ToLower(s)
		switch {
		case strings.HasPrefix(s, "+"):
			if !private || !inviteHash.MatchString(s[1:]) {
				return Target{}, false
			}
			return Target{Invite: s[1:]}, true
		case strings.HasPrefix(low, "joinchat/"):
			h := s[len("joinchat/"):]
			if !private || !inviteHash.MatchString(h) {
				return Target{}, false
			}
			return Target{Invite: h}, true
		default:
			name := s
			if i := strings.IndexByte(name, '/'); i >= 0 {
				name = name[:i]
			}
			u, ok := cleanUser(name)
			return Target{Username: u}, ok
		}
	}
	if strings.HasPrefix(s, "@") {
		u, ok := cleanUser(s[1:])
		return Target{Username: u}, ok
	}
	if strings.HasPrefix(s, "+") {
		if !private || !inviteHash.MatchString(s[1:]) {
			return Target{}, false
		}
		return Target{Invite: s[1:]}, true
	}
	if strings.HasPrefix(low, "c/") {
		n, err := strconv.ParseInt(s[2:], 10, 64)
		if err != nil || n <= 0 {
			return Target{}, false
		}
		return Target{PeerID: n}, true
	}
	u, ok := cleanUser(s)
	return Target{Username: u}, ok
}

func cleanUser(s string) (string, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if !userName.MatchString(s) {
		return "", false
	}
	return s, true
}

type voteSpec struct {
	kind   string
	n      int
	listID int64
	target Target
	tag    string
}

func parseVote(args string) (voteSpec, bool) {
	f := strings.Fields(args)
	switch len(f) {
	case 1:
		if n, err := strconv.Atoi(f[0]); err == nil && n >= 1 && n <= 10 {
			return voteSpec{kind: "item", n: n}, true
		}
		if m := optionN.FindStringSubmatch(strings.ToLower(f[0])); m != nil {
			n, _ := strconv.Atoi(m[1])
			if n >= 1 && n <= 10 {
				return voteSpec{kind: "item", n: n}, true
			}
		}
		listID, n, ok := splitRef(f[0])
		if !ok {
			return voteSpec{}, false
		}
		return voteSpec{kind: "ref", listID: listID, n: n}, true
	case 2:
		tgt, ok := parseTarget(f[0], true)
		tag := tags.Canon(f[1])
		if !ok || tag == "" {
			return voteSpec{}, false
		}
		return voteSpec{kind: "direct", target: tgt, tag: tag}, true
	default:
		return voteSpec{}, false
	}
}

func splitRef(s string) (int64, int, bool) {
	list, item, ok := strings.Cut(s, ":")
	if !ok {
		return 0, 0, false
	}
	id, err := strconv.ParseInt(list, 10, 64)
	n, err2 := strconv.Atoi(item)
	if err != nil || err2 != nil || id <= 0 || n < 1 || n > 10 {
		return 0, 0, false
	}
	return id, n, true
}

func parseCallback(data string) (op string, listID int64, arg int, ok bool) {
	p := strings.Split(data, ":")
	if len(p) != 4 || p[0] != "v1" {
		return
	}
	op = p[1]
	if op != "p" && op != "u" && op != "d" {
		return
	}
	var err, err2 error
	listID, err = strconv.ParseInt(p[2], 10, 64)
	arg, err2 = strconv.Atoi(p[3])
	ok = err == nil && err2 == nil && listID > 0 && arg >= 0
	return
}
