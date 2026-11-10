// Package service turns one incoming update into actions. It knows nothing about HTTP,
// so it can be tested without a network.
package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Aryan-mor/telegram-channel-index/internal/i18n"
	"github.com/Aryan-mor/telegram-channel-index/internal/store"
	"github.com/Aryan-mor/telegram-channel-index/internal/tags"
)

// Config holds the limits.
type Config struct {
	// Admins may do everything. They are set by the operator, not by a command.
	// An admin's vote weighs 2. Everyone else, including a bot, weighs 1.
	Admins map[int64]bool
	// Open lets any sender who is not banned take part. When false only members
	// and admins get answers.
	Open bool
	// PrivateLinks lets /add take a t.me/+hash link.
	PrivateLinks    bool
	VotesPerHour    int
	SubmitsPerDay   int
	SearchesPerHour int
	// Now is the clock. Nil means time.Now.
	Now func() time.Time
}

// From is the sender.
type From struct {
	ID    int64
	IsBot bool
	Lang  string
}

// Incoming is one message or one button press.
type Incoming struct {
	From              From
	ChatID            int64
	Text              string
	ReplyToID         int64
	CallbackID        string
	CallbackData      string
	CallbackMessageID int64
}

// Button is one inline button.
type Button struct {
	Text string
	Data string
}

// Action is one thing to do. Kind is send, edit or answer.
// ListID asks the caller to store the new message id on that list after a send.
type Action struct {
	Kind       string
	ChatID     int64
	MessageID  int64
	Text       string
	Rows       [][]Button
	CallbackID string
	ListID     int64
}

// Service answers updates.
type Service struct {
	cfg Config
	st  *store.Store
	lim *limiter
}

// New builds a Service.
func New(cfg Config, st *store.Store) *Service {
	if cfg.VotesPerHour == 0 {
		cfg.VotesPerHour = 30
	}
	if cfg.SubmitsPerDay == 0 {
		cfg.SubmitsPerDay = 10
	}
	if cfg.SearchesPerHour == 0 {
		cfg.SearchesPerHour = 60
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{cfg: cfg, st: st, lim: newLimiter()}
}

// BindList stores the message id of a list that was just sent.
func (s *Service) BindList(ctx context.Context, id, chatID, messageID int64) error {
	return s.st.BindList(ctx, id, chatID, messageID)
}

const helpText = `commands
/tags
/search movie
/add @channel movie
/tag @channel movie
/info @channel`

// Handle answers one update. No actions means stay silent.
func (s *Service) Handle(ctx context.Context, in Incoming) []Action {
	role, err := s.role(ctx, in.From.ID)
	if err != nil {
		return s.fail(in)
	}
	if role == 0 {
		return nil
	}
	if in.CallbackID != "" {
		if role&store.RoleRead == 0 {
			return nil
		}
		return s.onCallback(ctx, in)
	}
	cmd, ok := parseCommand(in.Text)
	if !ok {
		return nil
	}
	switch cmd.name {
	case "/start", "/help":
		return s.say(in, helpText)
	case "/tags":
		return s.say(in, strings.Join(tags.All, "\n"))
	case "/search":
		if role&store.RoleRead == 0 {
			return nil
		}
		return s.search(ctx, in, cmd.args)
	case "/add":
		if role&store.RoleWrite == 0 {
			return nil
		}
		return s.add(ctx, in, cmd.args, true)
	case "/tag":
		if role&store.RoleWrite == 0 {
			return nil
		}
		return s.add(ctx, in, cmd.args, false)
	case "/info":
		if role&store.RoleRead == 0 {
			return nil
		}
		return s.info(ctx, in, cmd.args)
	case "/accept":
		return s.voteCmd(ctx, in, cmd.args, 1)
	case "/reject":
		return s.voteCmd(ctx, in, cmd.args, -1)
	case "/unvote":
		return s.voteCmd(ctx, in, cmd.args, 0)
	case "/hide", "/untag", "/deny", "/allow", "/purge":
		if !s.cfg.Admins[in.From.ID] {
			return nil
		}
		return s.admin(ctx, in, cmd.name, cmd.args)
	default:
		return nil
	}
}

func (s *Service) role(ctx context.Context, id int64) (int, error) {
	if s.cfg.Admins[id] {
		return store.RoleWrite | store.RoleRead, nil
	}
	r, err := s.st.Role(ctx, id)
	if err != nil || r&store.RoleBanned != 0 {
		return 0, err
	}
	if r == 0 && s.cfg.Open {
		return store.RoleWrite | store.RoleRead, nil
	}
	return r, nil
}

func (s *Service) say(in Incoming, text string) []Action {
	if text == "" {
		return nil
	}
	return []Action{{Kind: "send", ChatID: in.ChatID, Text: text}}
}

func (s *Service) fail(in Incoming) []Action {
	if in.CallbackID != "" {
		return []Action{{Kind: "answer", CallbackID: in.CallbackID, Text: "error: try again later"}}
	}
	return s.say(in, "error: try again later")
}

func (s *Service) answer(in Incoming, text string) []Action {
	return []Action{{Kind: "answer", CallbackID: in.CallbackID, Text: text}}
}

func (s *Service) idKey(prefix string, id int64) string {
	return prefix + strconv.FormatInt(id, 10)
}

func (s *Service) weight(from From) int {
	if !from.IsBot && s.cfg.Admins[from.ID] {
		return 2
	}
	return 1
}

func (s *Service) add(ctx context.Context, in Incoming, args string, many bool) []Action {
	f := strings.Fields(args)
	need := 2
	if !many && len(f) != 2 {
		return nil
	}
	if many && len(f) < need {
		return nil
	}
	tgt, ok := parseTarget(f[0], s.cfg.PrivateLinks)
	if !ok || tgt.PeerID != 0 {
		return s.say(in, "no")
	}
	var list []string
	for _, raw := range f[1:] {
		t := tags.Canon(raw)
		if t == "" {
			return s.say(in, "no")
		}
		if !slices.Contains(list, t) {
			list = append(list, t)
		}
	}
	if len(list) == 0 || len(list) > 10 {
		return s.say(in, "no")
	}
	if !s.lim.allow(s.idKey("s:", in.From.ID), s.cfg.SubmitsPerDay, 24*time.Hour, s.cfg.Now()) {
		return s.say(in, "limited")
	}
	ch, err := s.st.Submit(ctx, tgt.Username, tgt.Invite, in.From.ID, s.cfg.Now())
	if err != nil {
		if errors.Is(err, store.ErrHidden) {
			return s.say(in, "no")
		}
		return s.fail(in)
	}
	if err := s.st.AddTags(ctx, ch.ID, in.From.ID, list, s.cfg.Now()); err != nil {
		return s.fail(in)
	}
	word := "pending"
	if ch.State == store.StateLive {
		word = "ok"
	}
	return s.say(in, word+" "+refLabel(ch.Username, ch.InviteHash, ch.PeerID)+" "+strings.Join(list, " "))
}

func (s *Service) info(ctx context.Context, in Incoming, args string) []Action {
	f := strings.Fields(args)
	if len(f) != 1 {
		return nil
	}
	if !s.lim.allow(s.idKey("r:", in.From.ID), s.cfg.SearchesPerHour, time.Hour, s.cfg.Now()) {
		return s.say(in, "limited")
	}
	tgt, ok := parseTarget(f[0], true)
	if !ok {
		return s.say(in, "no")
	}
	ch, ok, err := s.st.Find(ctx, tgt.Username, tgt.Invite, tgt.PeerID)
	if err != nil {
		return s.fail(in)
	}
	if !ok {
		return s.say(in, "no")
	}
	ts, err := s.st.Tags(ctx, ch.ID)
	if err != nil {
		return s.fail(in)
	}
	line := fmt.Sprintf("%s %s %d", refLabel(ch.Username, ch.InviteHash, ch.PeerID), ch.State, ch.Members)
	if title := oneLine(ch.Title); title != "" {
		line += " " + title
	}
	var b strings.Builder
	b.WriteString(line)
	for _, t := range ts {
		fmt.Fprintf(&b, "\n%s %+d", t.Tag, t.Score)
	}
	return s.say(in, b.String())
}

func (s *Service) search(ctx context.Context, in Incoming, args string) []Action {
	f := strings.Fields(args)
	if len(f) < 1 || len(f) > 2 {
		return nil
	}
	tag := tags.Canon(f[0])
	if tag == "" {
		return nil
	}
	page := 1
	if len(f) == 2 {
		n, err := strconv.Atoi(f[1])
		if err != nil || n < 1 {
			return nil
		}
		page = n
	}
	if !s.lim.allow(s.idKey("r:", in.From.ID), s.cfg.SearchesPerHour, time.Hour, s.cfg.Now()) {
		return s.say(in, "limited")
	}
	text, rows, _, _, empty, err := s.page(ctx, in.ChatID, in.From.ID, tag, page, 0, false, in.From.Lang)
	if err != nil {
		return s.fail(in)
	}
	if empty {
		return s.say(in, "none")
	}
	return []Action{{Kind: "send", ChatID: in.ChatID, Text: text, Rows: rows, ListID: listIDFrom(rows)}}
}

func listIDFrom(rows [][]Button) int64 {
	for _, row := range rows {
		for _, b := range row {
			_, id, _, ok := parseCallback(b.Data)
			if ok {
				return id
			}
		}
	}
	return 0
}

// page loads a result page. listID 0 creates a new snapshot. Otherwise it replaces that list.
func (s *Service) page(ctx context.Context, chat, owner int64, tag string, page int, listID int64, down bool, lang string) (string, [][]Button, int, int, bool, error) {
	total, err := s.st.CountLive(ctx, tag)
	if err != nil {
		return "", nil, 0, 0, false, err
	}
	pages := 1
	if total > 0 {
		pages = (total + pageSize - 1) / pageSize
	}
	if page < 1 {
		page = 1
	}
	if page > pages {
		page = pages
	}
	if total == 0 {
		return "", nil, page, pages, true, nil
	}
	hits, err := s.st.Page(ctx, tag, pageSize, (page-1)*pageSize)
	if err != nil {
		return "", nil, 0, 0, false, err
	}
	if len(hits) == 0 {
		return "", nil, page, pages, true, nil
	}
	ids := make([]int64, len(hits))
	for i := range hits {
		hits[i].N = i + 1
		ids[i] = hits[i].ChannelID
	}
	if listID == 0 {
		listID, err = s.st.CreateList(ctx, chat, owner, tag, page, ids, s.cfg.Now())
		if err != nil {
			return "", nil, 0, 0, false, err
		}
	} else if err := s.st.ReplaceList(ctx, listID, page, ids); err != nil {
		return "", nil, 0, 0, false, err
	}
	lang = i18n.Lang(lang)
	return formatList(listID, tag, page, pages, hits), keyboard(listID, len(hits), page, down, lang), page, pages, false, nil
}

func (s *Service) onCallback(ctx context.Context, in Incoming) []Action {
	op, listID, arg, ok := parseCallback(in.CallbackData)
	if !ok {
		return s.answer(in, "no")
	}
	list, found, err := s.st.ListByID(ctx, listID)
	if err != nil {
		return s.fail(in)
	}
	if !found || list.ChatID != in.ChatID {
		return s.answer(in, "no")
	}
	lang := i18n.Lang(in.From.Lang)
	switch op {
	case "p":
		if arg < 1 {
			return s.answer(in, "no")
		}
		if !s.lim.allow(s.idKey("r:", in.From.ID), s.cfg.SearchesPerHour, time.Hour, s.cfg.Now()) {
			return s.answer(in, "limited")
		}
		text, rows, _, _, empty, err := s.page(ctx, in.ChatID, list.OwnerID, list.Tag, arg, list.ID, false, lang)
		if err != nil {
			return s.fail(in)
		}
		if empty {
			return s.answer(in, "no")
		}
		return []Action{
			{Kind: "answer", CallbackID: in.CallbackID, Text: "ok"},
			{Kind: "edit", ChatID: in.ChatID, MessageID: in.CallbackMessageID, Text: text, Rows: rows},
		}
	case "u", "d":
		if arg == 0 {
			hits, err := s.st.Snapshot(ctx, list.ID, list.Tag)
			if err != nil {
				return s.fail(in)
			}
			return []Action{
				{Kind: "answer", CallbackID: in.CallbackID, Text: "ok"},
				{Kind: "edit", ChatID: in.ChatID, MessageID: in.CallbackMessageID, Text: formatList(list.ID, list.Tag, list.Page, s.pages(ctx, list.Tag), hits), Rows: keyboard(list.ID, len(hits), list.Page, op == "d", lang)},
			}
		}
		return s.buttonVote(ctx, in, list, op, arg, lang)
	default:
		return s.answer(in, "no")
	}
}

func (s *Service) pages(ctx context.Context, tag string) int {
	total, err := s.st.CountLive(ctx, tag)
	if err != nil || total < 1 {
		return 1
	}
	return (total + pageSize - 1) / pageSize
}

func (s *Service) buttonVote(ctx context.Context, in Incoming, list store.List, op string, n int, lang string) []Action {
	hits, err := s.st.Snapshot(ctx, list.ID, list.Tag)
	if err != nil {
		return s.fail(in)
	}
	var hit store.Hit
	var found bool
	for _, h := range hits {
		if h.N == n {
			hit, found = h, true
			break
		}
	}
	if !found {
		return s.answer(in, "no")
	}
	value := 1
	if op == "d" {
		value = -1
	}
	got, _, err := s.cast(ctx, in.From, hit.ChannelID, list.Tag, value, true)
	if errors.Is(err, errLimited) {
		return s.answer(in, "limited")
	}
	if errors.Is(err, store.ErrNoTag) {
		return s.answer(in, "no")
	}
	if err != nil {
		return s.fail(in)
	}
	if got == 0 {
		return s.answer(in, i18n.T(lang, "cleared"))
	}
	if got < 0 {
		return s.answer(in, "-1")
	}
	return s.answer(in, "+1")
}

var errLimited = errors.New("limited")

func (s *Service) voteCmd(ctx context.Context, in Incoming, args string, value int) []Action {
	spec, ok := parseVote(args)
	if !ok {
		return nil
	}
	var channelID int64
	var tag string
	switch spec.kind {
	case "item":
		if in.ReplyToID == 0 {
			return nil
		}
		list, found, err := s.st.ListByMessage(ctx, in.ChatID, in.ReplyToID)
		if err != nil {
			return s.fail(in)
		}
		if !found {
			return nil
		}
		id, ok, err := s.itemChannel(ctx, list, spec.n)
		if err != nil {
			return s.fail(in)
		}
		if !ok {
			return nil
		}
		channelID, tag = id, list.Tag
	case "ref":
		list, found, err := s.st.ListByID(ctx, spec.listID)
		if err != nil {
			return s.fail(in)
		}
		if !found {
			return nil
		}
		id, ok, err := s.itemChannel(ctx, list, spec.n)
		if err != nil {
			return s.fail(in)
		}
		if !ok {
			return nil
		}
		channelID, tag = id, list.Tag
	default:
		ch, found, err := s.st.Find(ctx, spec.target.Username, spec.target.Invite, spec.target.PeerID)
		if err != nil {
			return s.fail(in)
		}
		if !found || ch.State != store.StateLive {
			return s.say(in, "no")
		}
		has, err := s.st.HasTag(ctx, ch.ID, spec.tag)
		if err != nil {
			return s.fail(in)
		}
		if !has {
			return s.say(in, "no")
		}
		got, score, err := s.cast(ctx, in.From, ch.ID, spec.tag, value, false)
		if errors.Is(err, errLimited) {
			return s.say(in, "limited")
		}
		if err != nil {
			return s.fail(in)
		}
		return s.say(in, voteLine(spec.tag, refLabel(ch.Username, ch.InviteHash, ch.PeerID), got, score))
	}
	ch, err := s.st.Channel(ctx, channelID)
	if err != nil {
		return s.fail(in)
	}
	if ch.State != store.StateLive {
		return s.say(in, "no")
	}
	got, score, err := s.cast(ctx, in.From, channelID, tag, value, false)
	if errors.Is(err, errLimited) {
		return s.say(in, "limited")
	}
	if errors.Is(err, store.ErrNoTag) {
		return s.say(in, "no")
	}
	if err != nil {
		return s.fail(in)
	}
	return s.say(in, voteLine(tag, refLabel(ch.Username, ch.InviteHash, ch.PeerID), got, score))
}

func (s *Service) itemChannel(ctx context.Context, list store.List, n int) (int64, bool, error) {
	hits, err := s.st.Snapshot(ctx, list.ID, list.Tag)
	if err != nil {
		return 0, false, err
	}
	for _, h := range hits {
		if h.N == n {
			return h.ChannelID, true, nil
		}
	}
	return 0, false, nil
}

func (s *Service) cast(ctx context.Context, from From, channelID int64, tag string, value int, toggle bool) (int, int, error) {
	if !s.lim.allow(s.idKey("v:", from.ID), s.cfg.VotesPerHour, time.Hour, s.cfg.Now()) {
		return 0, 0, errLimited
	}
	return s.st.Vote(ctx, from.ID, channelID, tag, value, s.weight(from), from.IsBot, toggle, s.cfg.Now())
}

func (s *Service) admin(ctx context.Context, in Incoming, name, args string) []Action {
	f := strings.Fields(args)
	switch name {
	case "/hide":
		if len(f) != 1 {
			return nil
		}
		ch, ok, err := s.lookup(ctx, f[0])
		if err != nil {
			return s.fail(in)
		}
		if !ok {
			return s.say(in, "no")
		}
		if err := s.st.Hide(ctx, ch.ID); err != nil {
			return s.fail(in)
		}
		return s.say(in, "ok")
	case "/untag":
		if len(f) != 2 {
			return nil
		}
		tag := tags.Canon(f[1])
		ch, ok, err := s.lookup(ctx, f[0])
		if err != nil {
			return s.fail(in)
		}
		if !ok || tag == "" {
			return s.say(in, "no")
		}
		removed, err := s.st.Untag(ctx, ch.ID, tag)
		if err != nil {
			return s.fail(in)
		}
		if !removed {
			return s.say(in, "no")
		}
		return s.say(in, "ok")
	case "/allow", "/deny":
		if len(f) != 1 {
			return nil
		}
		id, err := strconv.ParseInt(f[0], 10, 64)
		if err != nil || id == 0 {
			return s.say(in, "no")
		}
		role := store.RoleBanned
		if name == "/allow" {
			role = store.RoleWrite | store.RoleRead
		}
		if err := s.st.SetRole(ctx, id, role); err != nil {
			return s.fail(in)
		}
		return s.say(in, "ok")
	case "/purge":
		if len(f) != 1 {
			return nil
		}
		id, err := strconv.ParseInt(f[0], 10, 64)
		if err != nil {
			return s.say(in, "no")
		}
		if _, err := s.st.PurgeVoter(ctx, id); err != nil {
			return s.fail(in)
		}
		return s.say(in, "ok")
	default:
		return nil
	}
}

func (s *Service) lookup(ctx context.Context, raw string) (store.Channel, bool, error) {
	tgt, ok := parseTarget(raw, true)
	if !ok {
		return store.Channel{}, false, nil
	}
	return s.st.Find(ctx, tgt.Username, tgt.Invite, tgt.PeerID)
}
