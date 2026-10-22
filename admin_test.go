package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func newTestConsole(t *testing.T) (*console, *strings.Builder) {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "lb.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	out := &strings.Builder{}
	return &console{admin: newAdmin(NewLobby(store), store, "hunter2hunter2", &logRing{}), out: out, who: "test"}, out
}

// do runs one command and returns what it printed.
func do(c *console, out *strings.Builder, line string) string {
	out.Reset()
	c.run(line)
	return out.String()
}

func TestAdminPassword(t *testing.T) {
	a := newAdmin(nil, nil, "secret", nil)
	if !a.check("secret") || a.check("Secret") || a.check("") {
		t.Fatal("password check is wrong")
	}
	if off := newAdmin(nil, nil, "", nil); off.check("") {
		t.Fatal("an empty password must not open the console")
	}
	for i := 1; i < adminMaxFails; i++ {
		if a.failed("1.2.3.4") {
			t.Fatalf("locked out after only %d failures", i)
		}
	}
	if !a.failed("1.2.3.4") || a.locked("1.2.3.4") <= 0 {
		t.Fatal("expected a lockout")
	}
	if a.locked("5.6.7.8") > 0 {
		t.Fatal("other addresses should not be locked out")
	}
	a.succeeded("1.2.3.4")
	if a.locked("1.2.3.4") > 0 {
		t.Fatal("a successful login should clear the lockout")
	}
}

func TestAdminReservedName(t *testing.T) {
	if validName("Admin") == nil {
		t.Fatal("players should not be able to call themselves admin")
	}
}

func TestAdminGames(t *testing.T) {
	c, out := newTestConsole(t)
	g, err := c.lobby.Create(Settings{Name: "Duel", Host: "alice", MaxPlayers: 2})
	if err != nil {
		t.Fatal(err)
	}
	if s := do(c, out, "games"); !strings.Contains(s, "Duel") || !strings.Contains(s, "Team 7 HQ (permanent)") {
		t.Fatalf("games:\n%s", s)
	}
	if s := do(c, out, "drop duel"); !strings.Contains(s, "Dropped Duel.") {
		t.Fatalf("drop: %s", s)
	}
	if !g.Stopped() || c.lobby.Find(g.ID) != nil {
		t.Fatal("dropped game should be stopped and gone")
	}
	hq := c.lobby.Find("Team 7 HQ")
	if s := do(c, out, "drop "+hq.ID); !strings.Contains(s, "restarted") {
		t.Fatalf("drop HQ: %s", s)
	}
	if fresh := c.lobby.Find("team 7 hq"); fresh == nil || fresh == hq || !hq.Stopped() {
		t.Fatal("Team 7 HQ should come back as a fresh game")
	}
	if s := do(c, out, "drop nope"); !strings.Contains(s, "no game") {
		t.Fatalf("drop unknown: %s", s)
	}
}

func TestAdminKick(t *testing.T) {
	c, out := newTestConsole(t)
	if err := c.lobby.SignIn("alice", "key-a"); err != nil {
		t.Fatal(err)
	}
	var got string
	c.lobby.OnKick("alice", func(reason string) { got = reason })
	if s := do(c, out, "online"); !strings.Contains(s, "alice") {
		t.Fatalf("online: %s", s)
	}
	do(c, out, "kick Alice stop camping")
	if got != "stop camping" {
		t.Fatalf("kick reason = %q", got)
	}
	if s := do(c, out, "kick bob"); !strings.Contains(s, "not online") {
		t.Fatalf("kick offline: %s", s)
	}
}

func TestAdminLeaderboard(t *testing.T) {
	c, out := newTestConsole(t)
	c.store.Record([]RoundStat{{Name: "alice", Won: true, Kills: 3}, {Name: "bob", Died: true}})
	if err := c.store.Claim("alice", "key-a"); err != nil {
		t.Fatal(err)
	}

	do(c, out, "set alice wins 42")
	if st, _ := c.store.Lookup("alice"); st.Wins != 42 {
		t.Fatalf("wins = %d", st.Wins)
	}
	if s := do(c, out, "set alice key 1"); !strings.Contains(s, "unknown stat") {
		t.Fatalf("set should only touch stat columns: %s", s)
	}
	if s := do(c, out, "top"); !strings.Contains(s, "alice") || !strings.Contains(s, "42") {
		t.Fatalf("top: %s", s)
	}

	do(c, out, "reset alice")
	if st, _ := c.store.Lookup("alice"); st.Wins != 0 || st.Kills != 0 {
		t.Fatalf("reset left %+v", st)
	}
	do(c, out, "unbind alice")
	if err := c.store.Claim("alice", "key-new"); err != nil {
		t.Fatalf("unbound name should be claimable: %v", err)
	}
	if s := do(c, out, "reset nobody"); !strings.Contains(s, ErrNoPlayer.Error()) {
		t.Fatalf("reset unknown: %s", s)
	}

	if s := do(c, out, "wipe"); !strings.Contains(s, "wipe yes") {
		t.Fatalf("wipe without yes: %s", s)
	}
	if s := do(c, out, "wipe yes"); !strings.Contains(s, "2 players") {
		t.Fatalf("wipe: %s", s)
	}

	if s := do(c, out, "sql SELECT name, key FROM players ORDER BY name"); !strings.Contains(s, "key-new") || !strings.Contains(s, "(2 rows)") {
		t.Fatalf("sql select: %s", s)
	}
	if s := do(c, out, "sql UPDATE players SET kills = 7"); !strings.Contains(s, "2 rows changed") {
		t.Fatalf("sql update: %s", s)
	}
	if s := do(c, out, "sql nonsense"); !strings.Contains(s, "Error") {
		t.Fatalf("bad sql: %s", s)
	}
	do(c, out, "delete bob")
	if s := do(c, out, "players"); strings.Contains(s, "bob") {
		t.Fatalf("bob should be gone: %s", s)
	}
}

func TestAdminMisc(t *testing.T) {
	c, out := newTestConsole(t)
	if s := do(c, out, "help"); !strings.Contains(s, "kick <name>") {
		t.Fatalf("help: %s", s)
	}
	if s := do(c, out, "status"); !strings.Contains(s, "uptime") || !strings.Contains(s, "database") {
		t.Fatalf("status: %s", s)
	}
	if s := do(c, out, "frobnicate"); !strings.Contains(s, "Unknown command") {
		t.Fatalf("unknown: %s", s)
	}
	if !c.run("quit") {
		t.Fatal("quit should close the console")
	}
	r := &logRing{}
	r.Write([]byte("one\ntw"))
	r.Write([]byte("o\nthree\n"))
	if got := strings.Join(r.Last(2), ","); got != "two,three" {
		t.Fatalf("log ring = %q", got)
	}
}
