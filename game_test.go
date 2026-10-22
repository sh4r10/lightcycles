package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// newTestGame returns a game with two humans placed by hand and no loop.
func newTestGame(t *testing.T) (*Game, *Player, *Player) {
	t.Helper()
	g := NewGame("test", Settings{Name: "t", Size: 0, MaxPlayers: 8}, nil)
	a := g.addPlayer("a", KindHuman)
	b := g.addPlayer("b", KindHuman)
	g.startRound()
	clear(g.grid)
	place := func(p *Player, x, y int, d Dir) {
		p.X, p.Y, p.Dir = x, y, d
		g.set(x, y, p.ID)
	}
	place(a, 5, 5, Right)
	place(b, 9, 5, Left)
	g.phase = Playing
	g.tick = 1 // next step is an even tick, so both cycles move
	return g, a, b
}

func TestHeadOnKillsBoth(t *testing.T) {
	g, a, b := newTestGame(t)
	g.step() // a→6, b→8
	g.tick = 1
	g.step() // both into 7
	if a.Alive || b.Alive {
		t.Fatalf("head-on should kill both: a=%v b=%v", a.Alive, b.Alive)
	}
	if g.phase != RoundOver || g.banner == "" {
		t.Fatalf("expected round over with a banner, got phase %v %q", g.phase, g.banner)
	}
}

func TestTrailKillCreditsOwnerAndWinsRound(t *testing.T) {
	g, a, b := newTestGame(t)
	got := make(chan []RoundStat, 1)
	g.onRound = func(s []RoundStat) { got <- s }
	g.set(6, 5, b.ID) // b's trail right in front of a
	g.step()
	if a.Alive || !b.Alive {
		t.Fatalf("a should crash into b's trail")
	}
	if b.Kills != 1 || b.Wins != 1 {
		t.Fatalf("b should get the kill and the win, got kills=%d wins=%d", b.Kills, b.Wins)
	}
	if g.at(5, 5) != 0 {
		t.Fatalf("a's trail should vanish when a dies")
	}
	stats := <-got
	if len(stats) != 2 {
		t.Fatalf("expected stats for both humans, got %v", stats)
	}
	for _, s := range stats {
		if s.Name == "b" && (!s.Won || s.Kills != 1 || s.Died) {
			t.Fatalf("bad stats for b: %+v", s)
		}
	}
}

func TestReverseTurnIgnored(t *testing.T) {
	g, a, _ := newTestGame(t)
	g.Turn(a.ID, Left)
	if len(a.queue) != 0 {
		t.Fatalf("reversing into your own trail should be ignored")
	}
	g.Turn(a.ID, Up)
	g.Turn(a.ID, Left) // quick U-turn: up then left is allowed
	if len(a.queue) != 2 {
		t.Fatalf("expected two queued turns, got %v", a.queue)
	}
}

func TestBoostMovesEveryTick(t *testing.T) {
	g, a, _ := newTestGame(t)
	g.tick = 0 // next step is an odd tick: normally no move
	g.boost(a)
	g.step()
	if a.X != 6 {
		t.Fatalf("boosted cycle should move on odd ticks, x=%d", a.X)
	}
}

func TestSpawnsInsideEveryArena(t *testing.T) {
	for i, sz := range sizes {
		g := NewGame("s", Settings{Size: i, MaxPlayers: maxCycles}, nil)
		seen := map[[2]int]bool{}
		for _, s := range g.spawns() {
			if s.x < 1 || s.y < 1 || s.x >= sz.W-1 || s.y >= sz.H-1 {
				t.Fatalf("%s: spawn %v too close to the wall", sz.Name, s)
			}
			if seen[[2]int{s.x, s.y}] {
				t.Fatalf("%s: duplicate spawn %v", sz.Name, s)
			}
			seen[[2]int{s.x, s.y}] = true
		}
	}
}

func TestWaitsWithoutBotsUntilSecondHuman(t *testing.T) {
	g := NewGame("w", Settings{Size: 0, MaxPlayers: 4, Bots: false}, nil)
	if _, err := g.Join("a"); err != nil {
		t.Fatal(err)
	}
	g.step()
	if g.phase != Waiting {
		t.Fatalf("one human and no bots should wait, got phase %v", g.phase)
	}
	if _, err := g.Join("b"); err != nil {
		t.Fatal(err)
	}
	g.step()
	if g.phase != Countdown {
		t.Fatalf("two humans should start a countdown, got phase %v", g.phase)
	}
}

func TestBotsFillAndYieldToHumans(t *testing.T) {
	g := NewGame("b", Settings{Size: 0, MaxPlayers: 4, Bots: true}, nil)
	g.Join("a")
	g.step()
	if len(g.players) != 4 {
		t.Fatalf("expected 1 human + 3 bots, got %d players", len(g.players))
	}
	for _, n := range []string{"b", "c", "d"} {
		if _, err := g.Join(n); err != nil {
			t.Fatal(err)
		}
	}
	if g.humans() != 4 || len(g.players) != 4 {
		t.Fatalf("humans should replace bots: humans=%d players=%d", g.humans(), len(g.players))
	}
	if _, err := g.Join("e"); err != ErrGameFull {
		t.Fatalf("fifth human should be refused, got %v", err)
	}
}

func TestLobbyAndLeaderboard(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "lb.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	l := NewLobby(store)

	if err := l.SignIn("Alice", "key-a"); err != nil {
		t.Fatal(err)
	}
	if err := l.SignIn("alice", "key-a"); err != ErrNameOnline {
		t.Fatalf("same name twice online should fail, got %v", err)
	}
	l.SignOut("Alice")
	if err := l.SignIn("ALICE", "key-b"); err != ErrNameClaimed {
		t.Fatalf("another key should not get alice's name, got %v", err)
	}
	if err := l.SignIn("alice", "key-a"); err != nil {
		t.Fatalf("alice's own key should sign back in: %v", err)
	}

	g, err := l.Create(Settings{Name: "Duel", Host: "alice", Size: 0, MaxPlayers: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Create(Settings{Name: "duel"}); err != ErrGameName {
		t.Fatalf("duplicate game names should fail, got %v", err)
	}
	if n := len(l.List()); n != 2 {
		t.Fatalf("expected public arena + duel, got %d games", n)
	}
	_, pid, err := l.Join(g.ID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	l.Leave(g, pid)
	if n := len(l.List()); n != 1 {
		t.Fatalf("empty hosted game should be removed, got %d games", n)
	}

	store.Record([]RoundStat{{Name: "alice", Won: true, Kills: 2}, {Name: "bob", Died: true}})
	store.Record([]RoundStat{{Name: "alice", Died: true}, {Name: "bob", Won: true, Kills: 1}})
	store.Record([]RoundStat{{Name: "alice", Won: true}, {Name: "bob", Died: true}})
	top, err := store.Top(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(top) != 2 || !strings.EqualFold(top[0].Name, "alice") || top[0].Wins != 2 || top[0].Kills != 2 || top[0].Rounds != 3 {
		t.Fatalf("unexpected leaderboard %+v", top)
	}
	bob, err := store.Lookup("bob")
	if err != nil || bob.Rank != 2 || bob.Deaths != 2 {
		t.Fatalf("unexpected bob %+v %v", bob, err)
	}
}
