// Package store owns the SQLite database.
package store

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/valicm/vura/internal/useragent"
)

//go:embed schema.sql
var schema string

// schemaVersion bumps when schema.sql changes in a way CREATE IF NOT EXISTS
// cannot express; migrate() handles the stepwise changes.
const schemaVersion = 7

type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the database at path with mode 0600.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	// Create the file 0600 before SQLite does: the -wal and -shm sidecars
	// inherit the main file's mode, so this is the only place to set it.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	f.Close()
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Chmod(path+suffix, 0o600); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}
	// _pragma via DSN: WAL for concurrent daemon+CLI, busy timeout so the CLI
	// never fails on a daemon write.
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // sqlite; serialise writers, keep it simple
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

// DB exposes the handle for read-only reporting code.
func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) migrate() error {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='presence'`).Scan(&n); err != nil {
		return err
	}
	fresh := n == 0
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("schema: %w", err)
	}
	if fresh {
		// schema.sql is always current; nothing to step through.
		_, err := s.db.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion))
		return err
	}
	var v int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		return err
	}
	for ; v < schemaVersion; v++ {
		var err error
		switch v {
		case 1:
			// v1 parsed editor/plugin too strictly and left AI heartbeats blank.
			err = s.reparseAgents()
		case 2:
			err = s.addColumn("shell", "sub", "TEXT")
		case 3:
			// sessions is derived: drop and recreate with the new columns.
			_, err = s.db.Exec(`DROP TABLE IF EXISTS sessions; ` + tableSQL("sessions"))
		case 4:
			// anchors table: created by schema.sql (IF NOT EXISTS); nothing to move.
		case 5:
			err = s.addColumn("events", "source", "TEXT NOT NULL DEFAULT 'ics'")
		case 6:
			err = s.addColumn("worklogs", "error", "TEXT")
		}
		if err != nil {
			return fmt.Errorf("migrate %d->%d: %w", v, v+1, err)
		}
	}
	_, err := s.db.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion))
	return err
}

// addColumn is ALTER TABLE ADD COLUMN, a no-op if the column already exists.
func (s *Store) addColumn(table, col, typ string) error {
	rows, err := s.db.Query(fmt.Sprintf(`PRAGMA table_info(%s)`, table))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return err
		}
		if name == col {
			return nil
		}
	}
	_, err = s.db.Exec(fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s %s`, table, col, typ))
	return err
}

// tableSQL extracts one CREATE TABLE statement (with its indexes) from schema.sql.
func tableSQL(name string) string {
	var out strings.Builder
	for _, stmt := range strings.Split(schema, ";") {
		t := strings.TrimSpace(stmt)
		if strings.Contains(t, "TABLE IF NOT EXISTS "+name+" ") ||
			strings.Contains(t, "TABLE IF NOT EXISTS "+name+"(") ||
			strings.Contains(t, " ON "+name+"(") {
			out.WriteString(t)
			out.WriteString(";\n")
		}
	}
	return out.String()
}

// reparseAgents re-derives editor/plugin from the stored raw JSON.
func (s *Store) reparseAgents() error {
	rows, err := s.db.Query(`SELECT id, raw FROM heartbeats WHERE editor IS NULL AND raw IS NOT NULL`)
	if err != nil {
		return err
	}
	type upd struct {
		id             int64
		editor, plugin string
	}
	var ups []upd
	for rows.Next() {
		var id int64
		var raw string
		if err := rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return err
		}
		var hb struct {
			UserAgent string `json:"user_agent"`
		}
		if json.Unmarshal([]byte(raw), &hb) != nil || hb.UserAgent == "" {
			continue
		}
		e, p := useragent.Parse(hb.UserAgent)
		ups = append(ups, upd{id, e, p})
	}
	rows.Close()
	for _, u := range ups {
		if _, err := s.db.Exec(`UPDATE heartbeats SET editor=?, plugin=? WHERE id=?`, nullStr(u.editor), nullStr(u.plugin), u.id); err != nil {
			return err
		}
	}
	return nil
}

// --- presence ---------------------------------------------------------

type Presence struct {
	TS           time.Time
	IdleMS       int64
	InhibitFlags int
	Inhibitors   string
	Device       string
}

func (s *Store) InsertPresence(ctx context.Context, p Presence) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO presence(ts, idle_ms, inhibit_flags, inhibitors, device) VALUES(?,?,?,?,?)`,
		p.TS.Unix(), p.IdleMS, p.InhibitFlags, nullStr(p.Inhibitors), nullStr(p.Device))
	return err
}

// --- heartbeats -------------------------------------------------------

type Heartbeat struct {
	TS       float64
	Entity   string
	Type     string
	Category string
	Project  string
	Branch   string
	Language string
	IsWrite  bool
	Plugin   string
	Editor   string
	Device   string
	Raw      string
}

// InsertHeartbeat ignores exact duplicates, which wakatime-cli produces when
// it replays its offline queue.
func (s *Store) InsertHeartbeat(ctx context.Context, h Heartbeat) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO heartbeats(ts, entity, type, category, project, branch, language, is_write, plugin, editor, device, raw)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		h.TS, h.Entity, nullStr(h.Type), nullStr(h.Category), nullStr(h.Project), nullStr(h.Branch),
		nullStr(h.Language), b2i(h.IsWrite), nullStr(h.Plugin), nullStr(h.Editor), nullStr(h.Device), nullStr(h.Raw))
	return err
}

// --- audio --------------------------------------------------------------

type AudioStream struct {
	Stream string // pactl index
	App    string
	Binary string
	Media  string
	Source string
	Device string
}

// OpenAudioStreams returns the stream indexes of rows with end IS NULL.
func (s *Store) OpenAudioStreams(ctx context.Context) (map[string]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, stream FROM audio WHERE end IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var id int64
		var st string
		if err := rows.Scan(&id, &st); err != nil {
			return nil, err
		}
		out[st] = id
	}
	return out, rows.Err()
}

func (s *Store) StartAudio(ctx context.Context, at time.Time, a AudioStream) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO audio(start, last_seen, app, binary, media, source, stream, device) VALUES(?,?,?,?,?,?,?,?)`,
		at.Unix(), at.Unix(), nullStr(a.App), nullStr(a.Binary), nullStr(a.Media), nullStr(a.Source), a.Stream, nullStr(a.Device))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) TouchAudio(ctx context.Context, id int64, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE audio SET last_seen=? WHERE id=?`, at.Unix(), id)
	return err
}

// EndAudio closes a row; end is the last time the stream was seen, not now,
// so a daemon restart does not stretch a call.
func (s *Store) EndAudio(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE audio SET end=last_seen WHERE id=? AND end IS NULL`, id)
	return err
}

// --- shell ---------------------------------------------------------------

type ShellCmd struct {
	ID       string
	TS       time.Time
	CWD      string
	Binary   string
	Sub      string
	Duration time.Duration
	Exit     int
	Session  string
	Bucket   string
	Device   string
}

// UpsertShell inserts or refreshes a command; atuin writes the row at
// preexec and fills duration/exit at precmd, so a re-import corrects them.
func (s *Store) UpsertShell(ctx context.Context, c ShellCmd) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO shell(id, ts, cwd, binary, sub, duration, exit, session, bucket, device)
		VALUES(?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET duration=excluded.duration, exit=excluded.exit`,
		c.ID, c.TS.Unix(), c.CWD, c.Binary, nullStr(c.Sub), c.Duration.Milliseconds(), c.Exit,
		nullStr(c.Session), nullStr(c.Bucket), nullStr(c.Device))
	return err
}

// --- commits -------------------------------------------------------------

type Commit struct {
	SHA     string
	TS      time.Time
	Repo    string
	Branch  string
	Bucket  string
	Ticket  string
	Subject string
	Files   int
	Ins     int
	Del     int
}

func (s *Store) UpsertCommit(ctx context.Context, c Commit) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO commits(sha, ts, repo, branch, bucket, ticket, subject, files, ins, del)
		VALUES(?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(sha) DO UPDATE SET branch=excluded.branch, bucket=excluded.bucket,
		    ticket=excluded.ticket, subject=excluded.subject`,
		c.SHA, c.TS.Unix(), c.Repo, nullStr(c.Branch), nullStr(c.Bucket), nullStr(c.Ticket),
		nullStr(c.Subject), c.Files, c.Ins, c.Del)
	return err
}

// PruneCommits removes rows for repo with ts >= since whose sha is not in
// keep: commits that were amended or rebased away.
func (s *Store) PruneCommits(ctx context.Context, repo string, since time.Time, keep []string) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `CREATE TEMP TABLE IF NOT EXISTS keep(sha TEXT PRIMARY KEY)`); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM keep`); err != nil {
		return 0, err
	}
	for _, sha := range keep {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO keep(sha) VALUES(?)`, sha); err != nil {
			return 0, err
		}
	}
	res, err := tx.ExecContext(ctx,
		`DELETE FROM commits WHERE repo=? AND ts>=? AND sha NOT IN (SELECT sha FROM keep)`, repo, since.Unix())
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, tx.Commit()
}

// --- state ---------------------------------------------------------------

func (s *Store) GetState(ctx context.Context, key string) (string, error) {
	var v sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT value FROM state WHERE key=?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v.String, err
}

// StatesLike returns every state row whose key has the prefix.
func (s *Store) StatesLike(ctx context.Context, prefix string) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM state WHERE key LIKE ? || '%'`, prefix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

func (s *Store) SetState(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO state(key, value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// --- day readers (for the sessioniser) --------------------------------------

type HeartbeatRow struct {
	TS      time.Time
	Entity  string
	Type    string
	Project string
	Editor  string
}

func (s *Store) HeartbeatsRange(ctx context.Context, from, to time.Time) ([]HeartbeatRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT ts, entity, COALESCE(type,''), COALESCE(project,''), COALESCE(editor,'')
		FROM heartbeats WHERE ts >= ? AND ts < ? ORDER BY ts`, float64(from.Unix()), float64(to.Unix()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HeartbeatRow
	for rows.Next() {
		var r HeartbeatRow
		var ts float64
		if err := rows.Scan(&ts, &r.Entity, &r.Type, &r.Project, &r.Editor); err != nil {
			return nil, err
		}
		r.TS = time.Unix(0, int64(ts*1e9))
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) PresenceRange(ctx context.Context, from, to time.Time) ([]Presence, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT ts, idle_ms, inhibit_flags FROM presence WHERE ts >= ? AND ts < ? ORDER BY ts`,
		from.Unix(), to.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Presence
	for rows.Next() {
		var p Presence
		var ts int64
		if err := rows.Scan(&ts, &p.IdleMS, &p.InhibitFlags); err != nil {
			return nil, err
		}
		p.TS = time.Unix(ts, 0)
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) ShellRange(ctx context.Context, from, to time.Time) ([]ShellCmd, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, ts, cwd, binary, COALESCE(sub,''), duration, exit
		FROM shell WHERE ts >= ? AND ts < ? ORDER BY ts`, from.Unix(), to.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ShellCmd
	for rows.Next() {
		var c ShellCmd
		var ts, dur int64
		if err := rows.Scan(&c.ID, &ts, &c.CWD, &c.Binary, &c.Sub, &dur, &c.Exit); err != nil {
			return nil, err
		}
		c.TS = time.Unix(ts, 0)
		c.Duration = time.Duration(dur) * time.Millisecond
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) CommitsRange(ctx context.Context, from, to time.Time) ([]Commit, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT sha, ts, repo, COALESCE(branch,''), COALESCE(ticket,''), COALESCE(subject,'')
		FROM commits WHERE ts >= ? AND ts < ? ORDER BY ts`, from.Unix(), to.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Commit
	for rows.Next() {
		var c Commit
		var ts int64
		if err := rows.Scan(&c.SHA, &ts, &c.Repo, &c.Branch, &c.Ticket, &c.Subject); err != nil {
			return nil, err
		}
		c.TS = time.Unix(ts, 0)
		out = append(out, c)
	}
	return out, rows.Err()
}

type AudioRow struct {
	Start, End  time.Time
	App, Source string
}

// AudioRange returns capture intervals overlapping [from, to); open rows end
// at last_seen. Monitor sources (loopback of speakers) are excluded.
func (s *Store) AudioRange(ctx context.Context, from, to time.Time) ([]AudioRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT start, COALESCE(end, last_seen), COALESCE(app, binary, '?'), COALESCE(source,'')
		FROM audio WHERE COALESCE(end, last_seen) > ? AND start < ? AND (source IS NULL OR source NOT LIKE '%.monitor')
		ORDER BY start`, from.Unix(), to.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AudioRow
	for rows.Next() {
		var r AudioRow
		var a, b int64
		if err := rows.Scan(&a, &b, &r.App, &r.Source); err != nil {
			return nil, err
		}
		r.Start, r.End = time.Unix(a, 0), time.Unix(b, 0)
		out = append(out, r)
	}
	return out, rows.Err()
}

// --- sessions -----------------------------------------------------------------

type Session struct {
	Day     string
	Bucket  string
	Label   string
	Tickets string
	Source  string
	Start   time.Time
	End     time.Time
	Remote  bool
	Device  string
}

// ReplaceSessions atomically swaps the derived sessions for one day.
func (s *Store) ReplaceSessions(ctx context.Context, day string, ss []Session) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE day=?`, day); err != nil {
		return err
	}
	for _, x := range ss {
		if _, err := tx.ExecContext(ctx, `INSERT INTO sessions(day, bucket, start, end, seconds, source, label, tickets, remote, device)
			VALUES(?,?,?,?,?,?,?,?,?,?)`,
			day, x.Bucket, x.Start.Unix(), x.End.Unix(), int64(x.End.Sub(x.Start).Seconds()),
			nullStr(x.Source), nullStr(x.Label), nullStr(x.Tickets), b2i(x.Remote), nullStr(x.Device)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// --- anchors (external activity) ----------------------------------------------

type Anchor struct {
	ID     string
	TS     time.Time
	Source string
	Kind   string
	Ref    string
	Title  string
	URL    string
}

func (s *Store) UpsertAnchor(ctx context.Context, a Anchor) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO anchors(id, ts, source, kind, ref, title, url) VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET ts=excluded.ts, kind=excluded.kind, ref=excluded.ref, title=excluded.title`,
		a.ID, a.TS.Unix(), a.Source, a.Kind, a.Ref, nullStr(a.Title), nullStr(a.URL))
	return err
}

func (s *Store) AnchorsRange(ctx context.Context, from, to time.Time) ([]Anchor, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, ts, source, kind, ref, COALESCE(title,''), COALESCE(url,'')
		FROM anchors WHERE ts >= ? AND ts < ? ORDER BY ts`, from.Unix(), to.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Anchor
	for rows.Next() {
		var a Anchor
		var ts int64
		if err := rows.Scan(&a.ID, &ts, &a.Source, &a.Kind, &a.Ref, &a.Title, &a.URL); err != nil {
			return nil, err
		}
		a.TS = time.Unix(ts, 0)
		out = append(out, a)
	}
	return out, rows.Err()
}

// --- events (calendar) --------------------------------------------------------

type Event struct {
	ID        string
	Start     time.Time
	End       time.Time
	Title     string
	Attendees string // comma-joined emails
	Source    string // ics:<feed> | slack:<workspace>
	Bucket    string // feed default; "" = decide by rule
}

// ReplaceEvents swaps every event of one source starting inside [from, to).
func (s *Store) ReplaceEvents(ctx context.Context, source string, from, to time.Time, rows []Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM events WHERE source=? AND start >= ? AND start < ?`, source, from.Unix(), to.Unix()); err != nil {
		return err
	}
	for _, e := range rows {
		if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO events(id, start, end, title, attendees, source, bucket) VALUES(?,?,?,?,?,?,?)`,
			e.ID, e.Start.Unix(), e.End.Unix(), nullStr(e.Title), nullStr(e.Attendees), source, nullStr(e.Bucket)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// EventsRange returns events overlapping [from, to).
func (s *Store) EventsRange(ctx context.Context, from, to time.Time) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, start, end, COALESCE(title,''), COALESCE(attendees,''), source, COALESCE(bucket,'') FROM events
		WHERE end > ? AND start < ? ORDER BY start`, from.Unix(), to.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var a, b int64
		if err := rows.Scan(&e.ID, &a, &b, &e.Title, &e.Attendees, &e.Source, &e.Bucket); err != nil {
			return nil, err
		}
		e.Start, e.End = time.Unix(a, 0), time.Unix(b, 0)
		out = append(out, e)
	}
	return out, rows.Err()
}

// --- days, notes, worklogs (reconcile) ---------------------------------------

const (
	DayPending = "pending"
	DayDone    = "done"
	DaySkipped = "skipped"
)

// DayState returns "" for a day never decided.
func (s *Store) DayState(ctx context.Context, day string) (string, error) {
	var st string
	err := s.db.QueryRowContext(ctx, `SELECT state FROM days WHERE day=?`, day).Scan(&st)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return st, err
}

func (s *Store) SetDayState(ctx context.Context, day, state string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO days(day, state, decided_at) VALUES(?,?,?)
		ON CONFLICT(day) DO UPDATE SET state=excluded.state, decided_at=excluded.decided_at`,
		day, state, time.Now().Unix())
	return err
}

// DecidedDays returns day -> state for every day with a decision.
func (s *Store) DecidedDays(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT day, state FROM days`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var d, st string
		if err := rows.Scan(&d, &st); err != nil {
			return nil, err
		}
		out[d] = st
	}
	return out, rows.Err()
}

// FirstRun returns when vurad first started, or zero if never.
func (s *Store) FirstRun(ctx context.Context) (time.Time, error) {
	var ts sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT MIN(ts) FROM audit WHERE action='vurad.start'`).Scan(&ts)
	if err != nil || !ts.Valid {
		return time.Time{}, err
	}
	return time.Unix(ts.Int64, 0), nil
}

type Note struct {
	ID     int64
	TS     time.Time
	Day    string
	Bucket string
	Hours  float64
	Text   string
}

func (s *Store) AddNote(ctx context.Context, n Note) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO notes(ts, day, bucket, hours, text) VALUES(?,?,?,?,?)`,
		n.TS.Unix(), n.Day, n.Bucket, n.Hours, nullStr(n.Text))
	return err
}

func (s *Store) NotesForDay(ctx context.Context, day string) ([]Note, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, ts, day, bucket, COALESCE(hours,0), COALESCE(text,'') FROM notes WHERE day=? ORDER BY ts`, day)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Note
	for rows.Next() {
		var n Note
		var ts int64
		if err := rows.Scan(&n.ID, &ts, &n.Day, &n.Bucket, &n.Hours, &n.Text); err != nil {
			return nil, err
		}
		n.TS = time.Unix(ts, 0)
		out = append(out, n)
	}
	return out, rows.Err()
}

const (
	WorklogDraft  = "draft"
	WorklogPushed = "pushed"
	WorklogFailed = "failed"
)

type Worklog struct {
	ID       int64
	Day      string
	Bucket   string
	Issue    string
	Start    time.Time
	Seconds  int // logged
	Observed int
	Desc     string
	TempoID  string
	State    string
	Error    string
	PushedAt time.Time
}

// ReplaceDrafts swaps the day's worklogs for new drafts. It refuses if any
// pushed worklog exists for the day; callers must amend (delete remote) first.
func (s *Store) ReplaceDrafts(ctx context.Context, day string, wls []Worklog) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var pushed int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM worklogs WHERE day=? AND state=?`, day, WorklogPushed).Scan(&pushed); err != nil {
		return err
	}
	if pushed > 0 {
		return fmt.Errorf("day %s has %d pushed worklogs; use --amend", day, pushed)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM worklogs WHERE day=?`, day); err != nil {
		return err
	}
	for _, w := range wls {
		if _, err := tx.ExecContext(ctx, `INSERT INTO worklogs(day, bucket, issue, start, seconds, observed, desc, state)
			VALUES(?,?,?,?,?,?,?,?)`, day, w.Bucket, w.Issue, w.Start.Unix(), w.Seconds, w.Observed, nullStr(w.Desc), WorklogDraft); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) WorklogsForDay(ctx context.Context, day string) ([]Worklog, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, day, bucket, issue, start, seconds, observed, COALESCE(desc,''), COALESCE(tempo_id,''), state, COALESCE(error,''), COALESCE(pushed_at,0)
		FROM worklogs WHERE day=? ORDER BY start`, day)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Worklog
	for rows.Next() {
		var w Worklog
		var start, pushed int64
		if err := rows.Scan(&w.ID, &w.Day, &w.Bucket, &w.Issue, &start, &w.Seconds, &w.Observed, &w.Desc, &w.TempoID, &w.State, &w.Error, &pushed); err != nil {
			return nil, err
		}
		w.Start = time.Unix(start, 0)
		if pushed > 0 {
			w.PushedAt = time.Unix(pushed, 0)
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func (s *Store) MarkPushed(ctx context.Context, id int64, tempoID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE worklogs SET state=?, tempo_id=?, error=NULL, pushed_at=? WHERE id=?`,
		WorklogPushed, tempoID, time.Now().Unix(), id)
	return err
}

func (s *Store) MarkFailed(ctx context.Context, id int64, reason string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE worklogs SET state=?, error=? WHERE id=?`, WorklogFailed, nullStr(reason), id)
	return err
}

// UpdateWorklog changes a draft or failed worklog before a retry.
func (s *Store) UpdateWorklog(ctx context.Context, id int64, seconds int, desc string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE worklogs SET seconds=?, desc=?, state=?, error=NULL WHERE id=? AND state!=?`,
		seconds, nullStr(desc), WorklogDraft, id, WorklogPushed)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("worklog %d is pushed or missing", id)
	}
	return nil
}

// DeleteWorklog removes a local row (after the remote one is gone).
func (s *Store) DeleteWorklog(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM worklogs WHERE id=?`, id)
	return err
}

// Backup writes a consistent copy of the database to path (VACUUM INTO),
// safe while the daemon is writing under WAL.
func (s *Store) Backup(ctx context.Context, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	_ = os.Remove(path)
	if _, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, path); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

// --- audit ---------------------------------------------------------------

func (s *Store) Audit(ctx context.Context, action, detail string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO audit(ts, action, detail) VALUES(?,?,?)`,
		time.Now().Unix(), action, nullStr(detail))
	return err
}

// PrunePresence deletes raw presence rows older than keep.
func (s *Store) PrunePresence(ctx context.Context, keep time.Duration) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM presence WHERE ts < ?`, time.Now().Add(-keep).Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
