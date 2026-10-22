package main

import (
	"bytes"
	"testing"
)

func TestSoundFilterStripsSplitSequences(t *testing.T) {
	var out bytes.Buffer
	var played []Sound
	f := &soundFilter{out: &out, play: func(s Sound) { played = append(played, s) }}
	stream := "frame1" + string(soundSequence(SndFahhh)) + "frame2\x1b[0m" + string(soundSequence(SndWin)) + "end"
	// Feed it one byte at a time so every sequence is split across writes.
	for i := range len(stream) {
		if _, err := f.Write([]byte{stream[i]}); err != nil {
			t.Fatal(err)
		}
	}
	if got := out.String(); got != "frame1frame2\x1b[0mend" {
		t.Fatalf("filtered output = %q", got)
	}
	if len(played) != 2 || played[0] != SndFahhh || played[1] != SndWin {
		t.Fatalf("played %v", played)
	}
}

func TestSoundsSynthesise(t *testing.T) {
	for _, s := range allSounds {
		b := wav(synth(s))
		if len(b) < 1000 || string(b[:4]) != "RIFF" {
			t.Fatalf("%s: bad wav (%d bytes)", s, len(b))
		}
	}
}

func TestCasePassesToKiller(t *testing.T) {
	g, a, b := newTestGame(t)
	g.caseOwner = a.ID
	c := g.addPlayer("c", KindHuman) // keep the round going after a dies
	c.X, c.Y, c.Dir, c.Alive, c.InRound = 20, 15, Left, true, true
	g.set(c.X, c.Y, c.ID)
	g.set(6, 5, b.ID)
	g.step()
	if g.caseOwner != b.ID {
		t.Fatalf("case should move to the killer, owner=%d", g.caseOwner)
	}
}

func TestClosingTheCaseIsRecorded(t *testing.T) {
	g, a, b := newTestGame(t)
	got := make(chan []RoundStat, 1)
	g.onRound = func(s []RoundStat) { got <- s }
	g.caseOwner = b.ID
	g.set(6, 5, b.ID)
	g.step()
	for _, s := range <-got {
		if s.Name == "b" && !s.ClosedCase {
			t.Fatalf("b won holding the case: %+v", s)
		}
		if s.Name == "a" && s.ClosedCase {
			t.Fatalf("a didn't close anything: %+v", s)
		}
	}
	_ = a
}

func TestRosterAndMitdkSpeed(t *testing.T) {
	g := NewGame("r", Settings{Size: 1, MaxPlayers: 8, Bots: true}, nil)
	g.Join("shsh")
	g.step() // Waiting → Countdown, bots join
	kinds := map[Kind]int{}
	for _, p := range g.players {
		kinds[p.Kind]++
	}
	if kinds[KindHarasmus] != 1 || kinds[KindMitdk] != 1 || kinds[KindAIBro] == 0 {
		t.Fatalf("expected HARASMUS, mitdk and an AI bro, got %v", kinds)
	}
	var mitdk *Player
	for _, p := range g.players {
		if p.Kind == KindMitdk {
			mitdk = p
		}
	}
	g.phase = Playing
	x, y := mitdk.X, mitdk.Y
	moves := 0
	for range 8 {
		g.tick++
		g.moveAll()
		if mitdk.X != x || mitdk.Y != y {
			moves++
			x, y = mitdk.X, mitdk.Y
		}
	}
	if mitdk.Alive && moves != 2 {
		t.Fatalf("mitdk should move every 4th tick: %d moves in 8 ticks", moves)
	}
}
