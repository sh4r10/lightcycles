package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/log"
	_ "modernc.org/sqlite"
)

// Store keeps the leaderboard in SQLite. Names are case-insensitive. A name
// is bound to the first SSH key that signs in with it, so nobody else can
// play (and lose) under your name.
type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS players (
	name      TEXT PRIMARY KEY COLLATE NOCASE,
	key       TEXT NOT NULL DEFAULT '',
	wins      INTEGER NOT NULL DEFAULT 0,
	kills     INTEGER NOT NULL DEFAULT 0,
	deaths    INTEGER NOT NULL DEFAULT 0,
	rounds    INTEGER NOT NULL DEFAULT 0,
	created   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	last_seen TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS players_rank ON players (wins DESC, kills DESC);
`

func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	// SQLite allows one writer at a time; a single connection keeps it simple.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	// Columns added after the first release. SQLite has no ADD COLUMN IF
	// NOT EXISTS, so a "duplicate column" error just means it's done.
	if _, err := db.Exec(`ALTER TABLE players ADD COLUMN cases INTEGER NOT NULL DEFAULT 0`); err != nil && !strings.Contains(err.Error(), "duplicate column") {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

var ErrNameClaimed = errors.New("that name belongs to another player's SSH key")

// Claim signs a name in. A new name is bound to keyFP (if there is one).
// An existing name must be used with its key; a name created without a key
// is bound to the first key that uses it.
func (s *Store) Claim(name, keyFP string) error {
	var owner string
	err := s.db.QueryRow(`SELECT key FROM players WHERE name = ?`, name).Scan(&owner)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, err = s.db.Exec(`INSERT INTO players (name, key) VALUES (?, ?)`, name, keyFP)
		return err
	case err != nil:
		return err
	case owner != "" && owner != keyFP:
		return ErrNameClaimed
	}
	_, err = s.db.Exec(`UPDATE players SET key = CASE WHEN key = '' THEN ? ELSE key END, last_seen = CURRENT_TIMESTAMP WHERE name = ?`, keyFP, name)
	return err
}

func (s *Store) Record(stats []RoundStat) {
	tx, err := s.db.Begin()
	if err != nil {
		log.Error("leaderboard write failed", "error", err)
		return
	}
	defer tx.Rollback()
	for _, st := range stats {
		_, err := tx.Exec(`
			INSERT INTO players (name, wins, kills, deaths, cases, rounds) VALUES (?, ?, ?, ?, ?, 1)
			ON CONFLICT(name) DO UPDATE SET
				wins = wins + excluded.wins, kills = kills + excluded.kills,
				deaths = deaths + excluded.deaths, cases = cases + excluded.cases,
				rounds = rounds + 1, last_seen = CURRENT_TIMESTAMP`,
			st.Name, b2i(st.Won), st.Kills, b2i(st.Died), b2i(st.ClosedCase))
		if err != nil {
			log.Error("leaderboard write failed", "error", err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		log.Error("leaderboard write failed", "error", err)
	}
}

type Stat struct {
	Rank   int
	Name   string
	Wins   int
	Kills  int
	Deaths int
	Cases  int // rounds won while holding the case
	Rounds int
}

func (s *Store) Top(n int) ([]Stat, error) {
	rows, err := s.db.Query(`
		SELECT name, wins, kills, deaths, cases, rounds FROM players WHERE rounds > 0
		ORDER BY wins DESC, kills DESC, rounds ASC, name LIMIT ?`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Stat
	for rows.Next() {
		st := Stat{Rank: len(out) + 1}
		if err := rows.Scan(&st.Name, &st.Wins, &st.Kills, &st.Deaths, &st.Cases, &st.Rounds); err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// Lookup returns one player's stats and rank (rank 0 if unranked).
func (s *Store) Lookup(name string) (Stat, error) {
	st := Stat{Name: name}
	err := s.db.QueryRow(`SELECT name, wins, kills, deaths, cases, rounds FROM players WHERE name = ?`, name).
		Scan(&st.Name, &st.Wins, &st.Kills, &st.Deaths, &st.Cases, &st.Rounds)
	if errors.Is(err, sql.ErrNoRows) || st.Rounds == 0 {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	err = s.db.QueryRow(`
		SELECT COUNT(*) + 1 FROM players WHERE rounds > 0 AND
		(wins > ?1 OR (wins = ?1 AND (kills > ?2 OR (kills = ?2 AND (rounds < ?3 OR (rounds = ?3 AND name < ?4 COLLATE NOCASE))))))`,
		st.Wins, st.Kills, st.Rounds, st.Name).Scan(&st.Rank)
	return st, err
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func validName(name string) error {
	switch {
	case len([]rune(name)) < 2:
		return errors.New("names need at least 2 characters")
	case strings.HasPrefix(strings.ToLower(name), "bot"):
		return errors.New("names starting with \"bot\" are reserved for bots")
	case strings.EqualFold(name, adminUser):
		return errors.New("that name is reserved")
	}
	return nil
}

// The admin console's view of the players table.

// statColumns are the leaderboard columns an admin may set by hand.
var statColumns = map[string]bool{"wins": true, "kills": true, "deaths": true, "cases": true, "rounds": true}

// PlayerRow is one player as the admin console shows them.
type PlayerRow struct {
	Stat
	Key      string
	LastSeen string
}

// Players lists players whose name contains filter, most recently seen first.
func (s *Store) Players(filter string, n int) ([]PlayerRow, error) {
	rows, err := s.db.Query(`
		SELECT name, key, wins, kills, deaths, cases, rounds, last_seen FROM players
		WHERE name LIKE '%' || ? || '%' ORDER BY last_seen DESC LIMIT ?`, filter, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PlayerRow
	for rows.Next() {
		var p PlayerRow
		if err := rows.Scan(&p.Name, &p.Key, &p.Wins, &p.Kills, &p.Deaths, &p.Cases, &p.Rounds, &p.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

var ErrNoPlayer = errors.New("no player with that name")

// SetStat overwrites one leaderboard column for a player.
func (s *Store) SetStat(name, column string, n int) error {
	if !statColumns[column] {
		return fmt.Errorf("unknown stat %q (use wins, kills, deaths, cases or rounds)", column)
	}
	return s.update(`UPDATE players SET `+column+` = ? WHERE name = ?`, n, name)
}

// ResetStats zeroes a player's stats but keeps their name and key.
func (s *Store) ResetStats(name string) error {
	return s.update(`UPDATE players SET wins = 0, kills = 0, deaths = 0, cases = 0, rounds = 0 WHERE name = ?`, name)
}

// Unbind frees a name from its SSH key; the next key to use it claims it.
func (s *Store) Unbind(name string) error {
	return s.update(`UPDATE players SET key = '' WHERE name = ?`, name)
}

// Delete forgets a player entirely.
func (s *Store) Delete(name string) error {
	return s.update(`DELETE FROM players WHERE name = ?`, name)
}

func (s *Store) update(query string, args ...any) error {
	res, err := s.db.Exec(query, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNoPlayer
	}
	return nil
}

// WipeStats zeroes everyone's stats. Names stay bound to their keys.
func (s *Store) WipeStats() (int64, error) {
	res, err := s.db.Exec(`UPDATE players SET wins = 0, kills = 0, deaths = 0, cases = 0, rounds = 0`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Info returns the number of players and the database size in bytes.
func (s *Store) Info() (players, bytes int64, err error) {
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM players`).Scan(&players); err != nil {
		return
	}
	err = s.db.QueryRow(`SELECT page_count * page_size FROM pragma_page_count(), pragma_page_size()`).Scan(&bytes)
	return
}

// SQL runs a raw statement. Statements that return rows come back as a
// header and string cells (at most limit rows); others report rows changed.
func (s *Store) SQL(query string, limit int) (cols []string, cells [][]string, changed int64, err error) {
	if !returnsRows(query) {
		res, err := s.db.Exec(query)
		if err != nil {
			return nil, nil, 0, err
		}
		changed, _ = res.RowsAffected()
		return nil, nil, changed, nil
	}
	rows, err := s.db.Query(query)
	if err != nil {
		return nil, nil, 0, err
	}
	defer rows.Close()
	if cols, err = rows.Columns(); err != nil {
		return nil, nil, 0, err
	}
	for rows.Next() && len(cells) < limit {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, nil, 0, err
		}
		row := make([]string, len(cols))
		for i, v := range vals {
			switch v := v.(type) {
			case nil:
				row[i] = "NULL"
			case []byte:
				row[i] = string(v)
			default:
				row[i] = fmt.Sprint(v)
			}
		}
		cells = append(cells, row)
	}
	return cols, cells, 0, rows.Err()
}

func returnsRows(query string) bool {
	f := strings.Fields(strings.ToLower(query))
	if len(f) == 0 {
		return false
	}
	switch f[0] {
	case "select", "pragma", "with", "explain", "values":
		return true
	}
	return strings.Contains(strings.ToLower(query), "returning")
}
