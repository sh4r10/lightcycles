package main

import (
	"bytes"
	"encoding/binary"
	"io"
	"math"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
)

// Sound effects. The server never plays audio itself: it tells each
// player's terminal what happened.
//
//   - Plain ssh gets a terminal bell on the big moments (crash, win).
//   - The built-in client announces itself with LIGHTCYCLES_CLIENT=1, and
//     the server then sends private OSC sequences that the client strips
//     from the screen output and plays as real sounds.

type Sound string

const (
	SndBeep     Sound = "beep"       // countdown
	SndGo       Sound = "go"         // round starts
	SndFahhh    Sound = "fahhh"      // you crashed on your own (wall, own trail)
	SndFahhhMax Sound = "fahhh-meme" // someone else took you out
	SndKill     Sound = "kill"       // someone hit your trail
	SndWin      Sound = "win"        // you won the round
	SndCase     Sound = "case"       // you've been assigned the case
	SndHarasmus Sound = "harasmus"   // HARASMUS is right behind you
)

var allSounds = []Sound{SndBeep, SndGo, SndFahhh, SndFahhhMax, SndKill, SndWin, SndCase, SndHarasmus}

// bellSounds are the ones worth a terminal bell for plain ssh players.
var bellSounds = map[Sound]bool{SndFahhh: true, SndFahhhMax: true, SndWin: true}

const (
	oscPrefix = "\x1b]7770;lightcycles:"
	oscEnd    = '\a'
)

func soundSequence(s Sound) []byte { return []byte(oscPrefix + string(s) + string(rune(oscEnd))) }

// soundFilter passes terminal output through and plays (rather than
// prints) any sound sequences, even when one is split across writes.
type soundFilter struct {
	out  io.Writer
	play func(Sound)
	buf  []byte
}

func (f *soundFilter) Write(p []byte) (int, error) {
	f.buf = append(f.buf, p...)
	for {
		i := bytes.Index(f.buf, []byte(oscPrefix))
		if i < 0 {
			// Keep a possible partial prefix at the end for the next write.
			keep := 0
			for n := min(len(oscPrefix)-1, len(f.buf)); n > 0; n-- {
				if bytes.HasSuffix(f.buf, []byte(oscPrefix[:n])) {
					keep = n
					break
				}
			}
			if _, err := f.out.Write(f.buf[:len(f.buf)-keep]); err != nil {
				return 0, err
			}
			f.buf = append(f.buf[:0], f.buf[len(f.buf)-keep:]...)
			return len(p), nil
		}
		if _, err := f.out.Write(f.buf[:i]); err != nil {
			return 0, err
		}
		rest := f.buf[i+len(oscPrefix):]
		end := bytes.IndexByte(rest, oscEnd)
		if end < 0 {
			f.buf = append(f.buf[:0], f.buf[i:]...)
			return len(p), nil
		}
		f.play(Sound(rest[:end]))
		f.buf = append(f.buf[:0], rest[end+1:]...)
	}
}

// player plays sounds with whatever audio tool the OS already has, so the
// client needs no cgo or audio libraries.
type player struct {
	dir  string
	cmd  []string
	once sync.Once
}

func newPlayer(configDir string) *player {
	p := &player{}
	switch runtime.GOOS {
	case "darwin":
		p.cmd = []string{"afplay"}
	case "windows":
		p.cmd = []string{"powershell", "-NoProfile", "-c", "(New-Object Media.SoundPlayer $args[0]).PlaySync()"}
	default:
		for _, c := range [][]string{{"paplay"}, {"pw-play"}, {"aplay", "-q"}, {"ffplay", "-nodisp", "-autoexit", "-loglevel", "quiet"}} {
			if _, err := exec.LookPath(c[0]); err == nil {
				p.cmd = c
				break
			}
		}
	}
	if p.cmd == nil {
		return nil
	}
	p.dir = filepath.Join(configDir, "sounds")
	return p
}

// Play is fire-and-forget. A WAV in the sounds folder (e.g. a teammate
// actually yelling fahhh.wav) replaces the built-in synthesised one.
func (p *player) Play(s Sound) {
	if p == nil {
		return
	}
	p.once.Do(p.writeDefaults)
	path := filepath.Join(p.dir, string(s)+".wav")
	if _, err := os.Stat(path); err != nil {
		path = filepath.Join(p.dir, "default", string(s)+".wav")
	}
	cmd := exec.Command(p.cmd[0], append(p.cmd[1:], path)...)
	if cmd.Start() == nil {
		go func() { _ = cmd.Wait() }()
	}
}

func (p *player) writeDefaults() {
	dir := filepath.Join(p.dir, "default")
	_ = os.MkdirAll(dir, 0o755)
	for _, s := range allSounds {
		_ = os.WriteFile(filepath.Join(dir, string(s)+".wav"), wav(synth(s)), 0o644)
	}
}

const sampleRate = 22050

func synth(s Sound) []float64 {
	switch s {
	case SndBeep:
		return tone(880, 0.08, 0.4)
	case SndGo:
		return tone(1320, 0.18, 0.4)
	case SndKill:
		return sweep(600, 1400, 0.12, 0.35)
	case SndWin:
		var out []float64
		for _, f := range []float64{523, 659, 784, 1047} {
			out = append(out, tone(f, 0.11, 0.35)...)
		}
		return out
	case SndCase:
		// An office phone: two 440/480Hz rings.
		var out []float64
		for range 2 {
			ring := make([]float64, samples(0.35))
			for i := range ring {
				t := float64(i) / sampleRate
				on := math.Sin(2*math.Pi*20*t) > 0
				if on {
					ring[i] = 0.25 * (math.Sin(2*math.Pi*440*t) + math.Sin(2*math.Pi*480*t))
				}
			}
			out = append(out, ring...)
			out = append(out, make([]float64, samples(0.15))...)
		}
		return out
	case SndHarasmus:
		var out []float64
		for range 2 {
			out = append(out, tone(988, 0.12, 0.3)...)
			out = append(out, tone(740, 0.12, 0.3)...)
		}
		return out
	case SndFahhh:
		return fahhh()
	case SndFahhhMax:
		return fahhhMeme()
	}
	return nil
}

// fahhh is a crude vocal "fahhh": a burst of breathy noise for the F, then
// an "ahh" vowel (a falling pitch shaped by /a/ formants) that trails off.
// It's the mild one, for crashing on your own.
func fahhh() []float64 {
	return voice(0.85, 0.12, 0.25, func(t float64) float64 { return 210 - 90*t },
		func(t float64) float64 { return math.Min(1, t*20) * math.Exp(-t*2.2) }, 1)
}

// fahhhMeme is the full meme FAHHHHH, for when someone else takes you out:
// a hard F, a higher strained pitch with a shaky vibrato, held at full
// volume and overdriven before it finally collapses.
func fahhhMeme() []float64 {
	const dur = 1.5
	pitch := func(t float64) float64 {
		f := 330 + 40*math.Min(t/0.2, 1) // strains upward first
		if t > 1.0 {
			f -= 160 * (t - 1.0) // then the voice gives out
		}
		return f + 14*math.Sin(2*math.Pi*6.5*t)
	}
	env := func(t float64) float64 {
		vt := dur - 0.18 - t
		return math.Min(1, t*40) * math.Min(1, math.Max(vt, 0)*3.5)
	}
	return voice(dur, 0.18, 0.6, pitch, env, 3.5)
}

// voice synthesises an F then an "ahh". drive > 1 overdrives it.
func voice(dur, fDur, fVol float64, pitch, env func(t float64) float64, drive float64) []float64 {
	out := make([]float64, samples(dur))
	fN := samples(fDur)
	formant := func(f float64) float64 {
		g := func(c, w float64) float64 { return math.Exp(-math.Pow((f-c)/w, 2)) }
		return g(750, 120) + 0.6*g(1150, 140) + 0.25*g(2600, 250)
	}
	phase := 0.0
	for i := range out {
		if i < fN {
			out[i] = (rand.Float64()*2 - 1) * fVol * float64(i) / float64(fN)
			continue
		}
		vt := float64(i-fN) / sampleRate
		f0 := pitch(vt)
		phase += 2 * math.Pi * f0 / sampleRate
		v := 0.0
		for k := 1.0; k*f0 < 4000; k++ {
			v += formant(k*f0) * math.Sin(k*phase) / math.Sqrt(k)
		}
		e := env(vt)
		x := 0.3*v*e + (rand.Float64()*2-1)*0.03*e
		if drive > 1 {
			x = math.Tanh(x*drive) * 0.9
		}
		out[i] = x
	}
	return out
}

func tone(freq, dur, vol float64) []float64 {
	out := make([]float64, samples(dur))
	for i := range out {
		t := float64(i) / sampleRate
		env := math.Min(1, math.Min(t, dur-t)*200)
		out[i] = vol * env * math.Sin(2*math.Pi*freq*t)
	}
	return out
}

func sweep(from, to, dur, vol float64) []float64 {
	out := make([]float64, samples(dur))
	phase := 0.0
	for i := range out {
		t := float64(i) / sampleRate
		phase += 2 * math.Pi * (from + (to-from)*t/dur) / sampleRate
		env := math.Min(1, math.Min(t, dur-t)*200)
		out[i] = vol * env * math.Sin(phase)
	}
	return out
}

// wav encodes mono 16-bit PCM.
func wav(samples []float64) []byte {
	var b bytes.Buffer
	data := len(samples) * 2
	w := func(v any) { _ = binary.Write(&b, binary.LittleEndian, v) }
	b.WriteString("RIFF")
	w(uint32(36 + data))
	b.WriteString("WAVEfmt ")
	w(uint32(16))
	w(uint16(1)) // PCM
	w(uint16(1)) // mono
	w(uint32(sampleRate))
	w(uint32(sampleRate * 2))
	w(uint16(2))
	w(uint16(16))
	b.WriteString("data")
	w(uint32(data))
	for _, s := range samples {
		w(int16(math.Max(-1, math.Min(1, s)) * 32767))
	}
	return b.Bytes()
}

func samples(dur float64) int { return int(dur * sampleRate) }
