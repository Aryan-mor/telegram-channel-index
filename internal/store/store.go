// Package store keeps channels, tags, votes and list snapshots in SQLite.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// Channel states.
const (
	StatePending = "pending"
	StateLive    = "live"
	StateDead    = "dead"
	StateHidden  = "hidden"
)

// Member roles are bits.
const (
	RoleWrite = 1
	RoleRead  = 2
	// RoleBanned silences a sender whatever the other bits say.
	RoleBanned = 4
)

// ErrHidden means an admin hid the channel.
var ErrHidden = errors.New("hidden")

// ErrNoTag means the channel does not carry that tag.
var ErrNoTag = errors.New("no tag")

// Channel is one indexed channel.
type Channel struct {
	ID          int64
	PeerID      int64
	Username    string
	InviteHash  string
	Title       string
	Members     int
	LastPost    int64
	State       string
	SubmittedBy int64
	Created     int64
	Checked     int64
	Tries       int
}

// Hit is one row of a search page.
type Hit struct {
	ChannelID  int64
	Username   string
	InviteHash string
	PeerID     int64
	Title      string
	Members    int
	Score      int
	N          int
}

// List is one rendered search message.
type List struct {
	ID        int64
	ChatID    int64
	MessageID int64
	OwnerID   int64
	Tag       string
	Page      int
}

// TagScore is a tag on a channel and its score.
type TagScore struct {
	Tag   string
	Score int
}

// Store is the database handle.
type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS channels(
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  peer_id       INTEGER UNIQUE,
  username      TEXT COLLATE NOCASE UNIQUE,
  invite_hash   TEXT UNIQUE,
  title         TEXT NOT NULL DEFAULT '',
  members       INTEGER NOT NULL DEFAULT 0,
  last_post_unix INTEGER NOT NULL DEFAULT 0,
  state         TEXT NOT NULL DEFAULT 'pending',
  submitted_by  INTEGER NOT NULL,
  created_unix  INTEGER NOT NULL,
  checked_unix  INTEGER NOT NULL DEFAULT 0,
  tries         INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS channel_tags(
  channel_id   INTEGER NOT NULL,
  tag          TEXT NOT NULL,
  score        INTEGER NOT NULL DEFAULT 0,
  up           INTEGER NOT NULL DEFAULT 0,
  down         INTEGER NOT NULL DEFAULT 0,
  added_by     INTEGER NOT NULL,
  created_unix INTEGER NOT NULL,
  PRIMARY KEY(channel_id, tag)
);
CREATE INDEX IF NOT EXISTS channel_tags_rank ON channel_tags(tag, score DESC, channel_id);
CREATE TABLE IF NOT EXISTS votes(
  voter_id     INTEGER NOT NULL,
  channel_id   INTEGER NOT NULL,
  tag          TEXT NOT NULL,
  value        INTEGER NOT NULL,
  weight       INTEGER NOT NULL,
  voter_is_bot INTEGER NOT NULL,
  at_unix      INTEGER NOT NULL,
  PRIMARY KEY(voter_id, channel_id, tag)
);
CREATE TABLE IF NOT EXISTS lists(
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  chat_id      INTEGER NOT NULL,
  message_id   INTEGER,
  owner_id     INTEGER NOT NULL,
  tag          TEXT NOT NULL,
  page         INTEGER NOT NULL,
  created_unix INTEGER NOT NULL,
  UNIQUE(chat_id, message_id)
);
CREATE TABLE IF NOT EXISTS list_items(
  list_id    INTEGER NOT NULL,
  n          INTEGER NOT NULL,
  channel_id INTEGER NOT NULL,
  PRIMARY KEY(list_id, n)
);
CREATE TABLE IF NOT EXISTS members(
  id   INTEGER PRIMARY KEY,
  role INTEGER NOT NULL
);
`

// Open opens or creates the database file.
func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}
	if err := renameTag(db, "film", "movie"); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// renameTag moves rows off a retired slug. A row that already has the new slug keeps that one.
func renameTag(db *sql.DB, from, to string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	steps := []string{
		`DELETE FROM channel_tags WHERE tag=? AND channel_id IN (SELECT channel_id FROM channel_tags WHERE tag=?)`,
		`UPDATE channel_tags SET tag=? WHERE tag=?`,
		`DELETE FROM votes WHERE tag=? AND EXISTS (SELECT 1 FROM votes v WHERE v.voter_id=votes.voter_id AND v.channel_id=votes.channel_id AND v.tag=?)`,
		`UPDATE votes SET tag=? WHERE tag=?`,
		`UPDATE lists SET tag=? WHERE tag=?`,
	}
	args := [][2]string{{from, to}, {to, from}, {from, to}, {to, from}, {to, from}}
	for i, q := range steps {
		if _, err := tx.Exec(q, args[i][0], args[i][1]); err != nil {
			return fmt.Errorf("rename tag %s: %w", from, err)
		}
	}
	return tx.Commit()
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

func nullStr(v string) any {
	if v == "" {
		return nil
	}
	return v
}

const channelCols = `id, IFNULL(peer_id,0), IFNULL(username,''), IFNULL(invite_hash,''), title, members, last_post_unix, state, submitted_by, created_unix, checked_unix, tries`

func scanChannel(row interface{ Scan(...any) error }) (Channel, error) {
	var c Channel
	err := row.Scan(&c.ID, &c.PeerID, &c.Username, &c.InviteHash, &c.Title, &c.Members, &c.LastPost, &c.State, &c.SubmittedBy, &c.Created, &c.Checked, &c.Tries)
	return c, err
}

// Submit finds a channel by username or invite hash, or inserts a pending one.
// A dead channel goes back to pending. A hidden channel returns ErrHidden.
func (s *Store) Submit(ctx context.Context, username, invite string, by int64, now time.Time) (Channel, error) {
	ch, ok, err := s.Find(ctx, username, invite, 0)
	if err != nil {
		return Channel{}, err
	}
	if ok {
		if ch.State == StateHidden {
			return ch, ErrHidden
		}
		if ch.State == StateDead {
			if _, err := s.db.ExecContext(ctx, `UPDATE channels SET state=?, tries=0, checked_unix=0 WHERE id=?`, StatePending, ch.ID); err != nil {
				return Channel{}, err
			}
			ch.State = StatePending
			ch.Tries = 0
		}
		return ch, nil
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO channels(username, invite_hash, state, submitted_by, created_unix) VALUES(?,?,?,?,?)`,
		nullStr(username), nullStr(invite), StatePending, by, now.Unix())
	if err != nil {
		return Channel{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Channel{}, err
	}
	return s.Channel(ctx, id)
}

// Find returns a channel by username, invite hash or peer id.
func (s *Store) Find(ctx context.Context, username, invite string, peer int64) (Channel, bool, error) {
	var q string
	var arg any
	switch {
	case username != "":
		q = `SELECT ` + channelCols + ` FROM channels WHERE username=?`
		arg = username
	case invite != "":
		q = `SELECT ` + channelCols + ` FROM channels WHERE invite_hash=?`
		arg = invite
	case peer != 0:
		q = `SELECT ` + channelCols + ` FROM channels WHERE peer_id=?`
		arg = peer
	default:
		return Channel{}, false, nil
	}
	ch, err := scanChannel(s.db.QueryRowContext(ctx, q, arg))
	if errors.Is(err, sql.ErrNoRows) {
		return Channel{}, false, nil
	}
	if err != nil {
		return Channel{}, false, err
	}
	return ch, true, nil
}

// Channel loads one channel by id.
func (s *Store) Channel(ctx context.Context, id int64) (Channel, error) {
	ch, err := scanChannel(s.db.QueryRowContext(ctx, `SELECT `+channelCols+` FROM channels WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Channel{}, sql.ErrNoRows
	}
	return ch, err
}

// AddTags attaches tags. A tag that is already there is left as it is.
func (s *Store) AddTags(ctx context.Context, channelID, by int64, tags []string, now time.Time) error {
	for _, t := range tags {
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO channel_tags(channel_id, tag, added_by, created_unix) VALUES(?,?,?,?)
			 ON CONFLICT(channel_id, tag) DO NOTHING`,
			channelID, t, by, now.Unix()); err != nil {
			return err
		}
	}
	return nil
}

// HasTag reports whether the channel carries the tag.
func (s *Store) HasTag(ctx context.Context, channelID int64, tag string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM channel_tags WHERE channel_id=? AND tag=?`, channelID, tag).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// Tags returns the tags on a channel, by name.
func (s *Store) Tags(ctx context.Context, channelID int64) ([]TagScore, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT tag, score FROM channel_tags WHERE channel_id=? ORDER BY tag`, channelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TagScore
	for rows.Next() {
		var t TagScore
		if err := rows.Scan(&t.Tag, &t.Score); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Hide marks a channel hidden.
func (s *Store) Hide(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE channels SET state=? WHERE id=?`, StateHidden, id)
	return err
}

// Untag drops a tag and the votes on it.
func (s *Store) Untag(ctx context.Context, channelID int64, tag string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM votes WHERE channel_id=? AND tag=?`, channelID, tag); err != nil {
		return false, err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM channel_tags WHERE channel_id=? AND tag=?`, channelID, tag)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, tx.Commit()
}

// CountLive counts live channels that carry a tag.
func (s *Store) CountLive(ctx context.Context, tag string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM channel_tags t JOIN channels c ON c.id=t.channel_id WHERE t.tag=? AND c.state=?`,
		tag, StateLive).Scan(&n)
	return n, err
}

// Page returns one page of live channels for a tag, best score first.
func (s *Store) Page(ctx context.Context, tag string, limit, offset int) ([]Hit, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT c.id, IFNULL(c.username,''), IFNULL(c.invite_hash,''), IFNULL(c.peer_id,0), c.title, c.members, t.score
		 FROM channel_tags t JOIN channels c ON c.id=t.channel_id
		 WHERE t.tag=? AND c.state=?
		 ORDER BY t.score DESC, c.members DESC, c.id
		 LIMIT ? OFFSET ?`, tag, StateLive, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Hit
	for rows.Next() {
		var h Hit
		if err := rows.Scan(&h.ChannelID, &h.Username, &h.InviteHash, &h.PeerID, &h.Title, &h.Members, &h.Score); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// CreateList stores a page snapshot. The message id is filled in later by BindList.
func (s *Store) CreateList(ctx context.Context, chatID, owner int64, tag string, page int, ids []int64, now time.Time) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx,
		`INSERT INTO lists(chat_id, owner_id, tag, page, created_unix) VALUES(?,?,?,?,?)`,
		chatID, owner, tag, page, now.Unix())
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if err := insertItems(ctx, tx, id, ids); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

func insertItems(ctx context.Context, tx *sql.Tx, listID int64, ids []int64) error {
	for i, cid := range ids {
		if _, err := tx.ExecContext(ctx, `INSERT INTO list_items(list_id, n, channel_id) VALUES(?,?,?)`, listID, i+1, cid); err != nil {
			return err
		}
	}
	return nil
}

// BindList records the Telegram message id of a list.
func (s *Store) BindList(ctx context.Context, id, chatID, messageID int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE lists SET message_id=? WHERE id=? AND chat_id=?`, messageID, id, chatID)
	return err
}

// ReplaceList swaps the snapshot of a list for a new page.
func (s *Store) ReplaceList(ctx context.Context, id int64, page int, ids []int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE lists SET page=? WHERE id=?`, page, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM list_items WHERE list_id=?`, id); err != nil {
		return err
	}
	if err := insertItems(ctx, tx, id, ids); err != nil {
		return err
	}
	return tx.Commit()
}

// ListByID loads a list.
func (s *Store) ListByID(ctx context.Context, id int64) (List, bool, error) {
	return scanList(s.db.QueryRowContext(ctx,
		`SELECT id, chat_id, IFNULL(message_id,0), owner_id, tag, page FROM lists WHERE id=?`, id))
}

// ListByMessage loads the list that was rendered as this message.
func (s *Store) ListByMessage(ctx context.Context, chatID, messageID int64) (List, bool, error) {
	return scanList(s.db.QueryRowContext(ctx,
		`SELECT id, chat_id, IFNULL(message_id,0), owner_id, tag, page FROM lists WHERE chat_id=? AND message_id=?`,
		chatID, messageID))
}

func scanList(row *sql.Row) (List, bool, error) {
	var l List
	err := row.Scan(&l.ID, &l.ChatID, &l.MessageID, &l.OwnerID, &l.Tag, &l.Page)
	if errors.Is(err, sql.ErrNoRows) {
		return List{}, false, nil
	}
	return l, err == nil, err
}

// Snapshot returns the channels saved on a list, in the order they were shown.
func (s *Store) Snapshot(ctx context.Context, listID int64, tag string) ([]Hit, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT li.n, c.id, IFNULL(c.username,''), IFNULL(c.invite_hash,''), IFNULL(c.peer_id,0), c.title, c.members, IFNULL(t.score,0)
		 FROM list_items li
		 JOIN channels c ON c.id=li.channel_id
		 LEFT JOIN channel_tags t ON t.channel_id=c.id AND t.tag=?
		 WHERE li.list_id=?
		 ORDER BY li.n`, tag, listID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Hit
	for rows.Next() {
		var h Hit
		if err := rows.Scan(&h.N, &h.ChannelID, &h.Username, &h.InviteHash, &h.PeerID, &h.Title, &h.Members, &h.Score); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// Vote sets one voter's vote on a channel tag.
// value is +1, -1, or 0 to clear. toggle makes a repeat of the same value clear it.
// The returned value is the vote that remains, and score is the tag score afterwards.
func (s *Store) Vote(ctx context.Context, voter, channelID int64, tag string, value, weight int, isBot, toggle bool, now time.Time) (int, int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	var one int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM channel_tags WHERE channel_id=? AND tag=?`, channelID, tag).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, ErrNoTag
	}
	if err != nil {
		return 0, 0, err
	}
	var oldValue, oldWeight int
	err = tx.QueryRowContext(ctx, `SELECT value, weight FROM votes WHERE voter_id=? AND channel_id=? AND tag=?`, voter, channelID, tag).Scan(&oldValue, &oldWeight)
	have := err == nil
	if errors.Is(err, sql.ErrNoRows) {
		have = false
		err = nil
	}
	if err != nil {
		return 0, 0, err
	}
	newValue := value
	if value == 0 || (have && toggle && oldValue == value) {
		newValue = 0
	}
	if have && !toggle && oldValue == value && value != 0 {
		var score int
		if err := tx.QueryRowContext(ctx, `SELECT score FROM channel_tags WHERE channel_id=? AND tag=?`, channelID, tag).Scan(&score); err != nil {
			return 0, 0, err
		}
		return oldValue, score, nil
	}
	if newValue == 0 {
		if have {
			if _, err := tx.ExecContext(ctx, `DELETE FROM votes WHERE voter_id=? AND channel_id=? AND tag=?`, voter, channelID, tag); err != nil {
				return 0, 0, err
			}
		} else {
			oldValue, oldWeight = 0, 0
		}
	} else if have {
		if _, err := tx.ExecContext(ctx,
			`UPDATE votes SET value=?, weight=?, voter_is_bot=?, at_unix=? WHERE voter_id=? AND channel_id=? AND tag=?`,
			newValue, weight, boolInt(isBot), now.Unix(), voter, channelID, tag); err != nil {
			return 0, 0, err
		}
	} else {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO votes(voter_id, channel_id, tag, value, weight, voter_is_bot, at_unix) VALUES(?,?,?,?,?,?,?)`,
			voter, channelID, tag, newValue, weight, boolInt(isBot), now.Unix()); err != nil {
			return 0, 0, err
		}
		oldValue, oldWeight = 0, 0
	}
	score, err := applyDelta(ctx, tx, channelID, tag, oldValue, oldWeight, newValue, weight)
	if err != nil {
		return 0, 0, err
	}
	return newValue, score, tx.Commit()
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func applyDelta(ctx context.Context, tx *sql.Tx, channelID int64, tag string, oldValue, oldWeight, newValue, newWeight int) (int, error) {
	var score, up, down int
	if err := tx.QueryRowContext(ctx, `SELECT score, up, down FROM channel_tags WHERE channel_id=? AND tag=?`, channelID, tag).Scan(&score, &up, &down); err != nil {
		return 0, err
	}
	if oldValue > 0 {
		up -= oldWeight
	} else if oldValue < 0 {
		down -= oldWeight
	}
	if newValue > 0 {
		up += newWeight
	} else if newValue < 0 {
		down += newWeight
	}
	if up < 0 {
		up = 0
	}
	if down < 0 {
		down = 0
	}
	score += newValue*newWeight - oldValue*oldWeight
	if _, err := tx.ExecContext(ctx, `UPDATE channel_tags SET score=?, up=?, down=? WHERE channel_id=? AND tag=?`, score, up, down, channelID, tag); err != nil {
		return 0, err
	}
	return score, nil
}

// ScoreOf returns the stored score. Tests use it to compare with the vote rows.
func (s *Store) ScoreOf(ctx context.Context, channelID int64, tag string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT score FROM channel_tags WHERE channel_id=? AND tag=?`, channelID, tag).Scan(&n)
	return n, err
}

// VoteSum is the sum of value*weight for one tag. It must match the stored score.
func (s *Store) VoteSum(ctx context.Context, channelID int64, tag string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(value*weight),0) FROM votes WHERE channel_id=? AND tag=?`, channelID, tag).Scan(&n)
	return n, err
}

// PurgeVoter deletes one voter's votes and recomputes the scores they touched.
func (s *Store) PurgeVoter(ctx context.Context, voter int64) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT channel_id, tag FROM votes WHERE voter_id=?`, voter)
	if err != nil {
		return 0, err
	}
	type pair struct {
		id  int64
		tag string
	}
	var pairs []pair
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.id, &p.tag); err != nil {
			rows.Close()
			return 0, err
		}
		pairs = append(pairs, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM votes WHERE voter_id=?`, voter)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	for _, p := range pairs {
		if _, err := recompute(ctx, tx, p.id, p.tag); err != nil {
			return 0, err
		}
	}
	return int(n), tx.Commit()
}

func recompute(ctx context.Context, tx *sql.Tx, channelID int64, tag string) (int, error) {
	var score, up, down int
	if err := tx.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(value*weight),0),
		COALESCE(SUM(CASE WHEN value=1 THEN weight ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN value=-1 THEN weight ELSE 0 END),0)
		FROM votes WHERE channel_id=? AND tag=?`, channelID, tag).Scan(&score, &up, &down); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE channel_tags SET score=?, up=?, down=? WHERE channel_id=? AND tag=?`, score, up, down, channelID, tag); err != nil {
		return 0, err
	}
	return score, nil
}

// Pending returns channels that still wait for a check.
func (s *Store) Pending(ctx context.Context, limit int) ([]Channel, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+channelCols+` FROM channels WHERE state=? ORDER BY checked_unix, id LIMIT ?`, StatePending, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Channel
	for rows.Next() {
		ch, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ch)
	}
	return out, rows.Err()
}

// Report is what the checker learned.
type Report struct {
	PeerID   int64
	Username string
	Title    string
	Members  int
	LastPost int64
	HasPost  bool
	Reject   bool
}

// MarkLive stores a successful check. It does nothing once the channel is no longer pending.
func (s *Store) MarkLive(ctx context.Context, id int64, r Report, now time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE channels SET peer_id=?, username=COALESCE(?, username), title=?, members=?, last_post_unix=?, state=?, checked_unix=?, tries=tries+1
		 WHERE id=? AND state=?`,
		nullID(r.PeerID), nullStr(r.Username), r.Title, r.Members, r.LastPost, StateLive, now.Unix(), id, StatePending)
	return err
}

func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// MarkDead records that a channel should stay out of search.
func (s *Store) MarkDead(ctx context.Context, id int64, now time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE channels SET state=?, checked_unix=?, tries=tries+1 WHERE id=? AND state=?`,
		StateDead, now.Unix(), id, StatePending)
	return err
}

// Retry notes a failed check. After maxTries the channel is dead.
func (s *Store) Retry(ctx context.Context, id int64, maxTries int, now time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE channels SET tries=tries+1, checked_unix=?,
		   state=CASE WHEN tries+1 >= ? THEN ? ELSE state END
		 WHERE id=? AND state=?`,
		now.Unix(), maxTries, StateDead, id, StatePending)
	return err
}

// Role returns the role bits of a member. Zero means not a member.
func (s *Store) Role(ctx context.Context, id int64) (int, error) {
	var r int
	err := s.db.QueryRowContext(ctx, `SELECT role FROM members WHERE id=?`, id).Scan(&r)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return r, err
}

// SetRole adds or changes a member. Role zero removes the member.
func (s *Store) SetRole(ctx context.Context, id int64, role int) error {
	if role == 0 {
		_, err := s.db.ExecContext(ctx, `DELETE FROM members WHERE id=?`, id)
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO members(id, role) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET role=excluded.role`, id, role)
	return err
}
