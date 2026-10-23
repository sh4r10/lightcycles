package main

import (
	"bytes"
	"os"
	"path/filepath"
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
	g.step() // Waiting → Ready
	readyAll(g)
	g.step() // Ready → Countdown, bots join
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

func TestKilledBySetForTrailKills(t *testing.T) {
	g, a, b := newTestGame(t)
	g.set(6, 5, b.ID)
	g.step()
	if a.KilledBy != b.ID {
		t.Fatalf("a should be marked as killed by b, got %d", a.KilledBy)
	}
}

func TestKYToolkitLagsEveryoneElse(t *testing.T) {
	g, a, b := newTestGame(t)
	g.ky = &[2]int{6, 5} // right in front of a
	g.step()
	if g.ky != nil || a.Lag != 0 || b.Lag != kyLagLen {
		t.Fatalf("a grabs it, b lags: ky=%v a.Lag=%d b.Lag=%d", g.ky, a.Lag, b.Lag)
	}
	g.Turn(b.ID, Up)
	if len(b.queue) != 0 || len(b.lagged) != 1 {
		t.Fatalf("b's turn should be held back, queue=%v lagged=%v", b.queue, b.lagged)
	}
	for range kyDelay - 1 {
		g.tick++
		g.loadLaggedTurns()
	}
	if len(b.queue) != 0 {
		t.Fatalf("turn released too early")
	}
	g.tick++
	g.loadLaggedTurns()
	if len(b.queue) != 1 || b.queue[0] != Up {
		t.Fatalf("turn should load after %d ticks, queue=%v", kyDelay, b.queue)
	}
	if kyProgress(kyLagLen) != 1 || kyProgress(1) != 99 {
		t.Fatalf("progress bar should crawl from 1%% to 99%%")
	}
}

func TestSoundPackRoundTrip(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	wavData := wav(synth(SndBeep))
	if err := os.WriteFile(filepath.Join(src, "fahhh-meme.wav"), wavData, 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(src, "not-a-sound.wav"), []byte("x"), 0o644)
	var pack bytes.Buffer
	if err := writeSoundPack(&pack, src); err != nil {
		t.Fatal(err)
	}
	if err := readSoundPack(&pack, dst); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dst)
	if len(entries) != 1 || entries[0].Name() != "fahhh-meme.wav" {
		t.Fatalf("only known sounds should come across, got %v", entries)
	}
	got, _ := os.ReadFile(filepath.Join(dst, "fahhh-meme.wav"))
	if !bytes.Equal(got, wavData) {
		t.Fatalf("sound changed in transit")
	}
}

func TestWindowsPlayerQuotesPath(t *testing.T) {
	cmd := windowsPlayer("powershell", func(p string) string { return p })(`C:\Users\o'brien\fahhh.wav`)
	want := `(New-Object Media.SoundPlayer 'C:\Users\o''brien\fahhh.wav').PlaySync()`
	if got := cmd.Args[len(cmd.Args)-1]; got != want {
		t.Fatalf("script = %q, want %q", got, want)
	}
}
