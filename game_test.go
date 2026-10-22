package main

import "testing"

// newTestGame returns a game with two humans placed by hand and no loop.
func newTestGame(t *testing.T) (*Game, *Player, *Player) {
	t.Helper()
	g := NewGame("test", 0)
	a := g.addPlayer("a", false)
	b := g.addPlayer("b", false)
	g.startRound()
	g.grid = [H][W]int{}
	place := func(p *Player, x, y int, d Dir) {
		p.X, p.Y, p.Dir = x, y, d
		g.grid[y][x] = p.ID
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
	g.grid[5][6] = b.ID // b's trail right in front of a
	g.step()
	if a.Alive || !b.Alive {
		t.Fatalf("a should crash into b's trail")
	}
	if b.Kills != 1 || b.Wins != 1 {
		t.Fatalf("b should get the kill and the win, got kills=%d wins=%d", b.Kills, b.Wins)
	}
	if g.grid[5][5] != 0 {
		t.Fatalf("a's trail should vanish when a dies")
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
