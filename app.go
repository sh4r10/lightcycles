package main

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const frameRate = 50 * time.Millisecond

type frameMsg struct{}

func nextFrame() tea.Cmd {
	return tea.Tick(frameRate, func(time.Time) tea.Msg { return frameMsg{} })
}

type screen int

const (
	scrName screen = iota
	scrLobby
	scrHost
	scrBoard
	scrGame
)

// session is the per-connection state that must outlive Bubble Tea's
// value-copied models, so the SSH handler can clean up on disconnect.
type session struct {
	mu   sync.Mutex
	name string
	game *Game
	pid  int

	out       io.Writer // the player's terminal, for sounds
	richSound bool      // the built-in client: send real sounds, not bells
	kick      func(reason string)
}

func (s *session) play(snd Sound) {
	switch {
	case s.out == nil:
	case s.richSound:
		_, _ = s.out.Write(soundSequence(snd))
	case bellSounds[snd]:
		_, _ = s.out.Write([]byte{'\a'})
	}
}

func (s *session) close(l *Lobby) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.game != nil {
		l.Leave(s.game, s.pid)
		s.game = nil
	}
	if s.name != "" {
		l.SignOut(s.name)
		s.name = ""
	}
}

type app struct {
	lobby *Lobby
	sess  *session
	keyFP string
	name  string // signed-in name; sess.name is the copy used for cleanup
	st    *styles

	screen screen
	width  int
	height int
	err    string

	nameInput textinput.Model

	games  []GameInfo
	sel    int
	online int

	host hostForm

	top  []Stat
	mine Stat

	view         gameView
	lastHarasmus time.Time
}

func newApp(r *lipgloss.Renderer, l *Lobby, sess *session, user, keyFP string) app {
	st := newStyles(r)
	ti := textinput.New()
	ti.Placeholder = "your name"
	ti.CharLimit = 16
	if validName(cleanName(user, "")) == nil {
		ti.SetValue(cleanName(user, ""))
	}
	ti.Focus()
	return app{lobby: l, sess: sess, keyFP: keyFP, st: &st, nameInput: ti, view: gameView{st: &st}}
}

func (a app) Init() tea.Cmd { return tea.Batch(textinput.Blink, nextFrame()) }

func (a app) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
		a.view.width, a.view.height = msg.Width, msg.Height
		return a, nil
	case frameMsg:
		a.refresh()
		return a, nextFrame()
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return a, tea.Quit
		}
	}
	switch a.screen {
	case scrName:
		return a.updateName(msg)
	case scrLobby:
		return a.updateLobby(msg)
	case scrHost:
		return a.updateHost(msg)
	case scrBoard:
		return a.updateBoard(msg)
	case scrGame:
		return a.updateGame(msg)
	}
	return a, nil
}

// refresh pulls fresh state for whatever screen is showing.
func (a *app) refresh() {
	switch a.screen {
	case scrLobby:
		a.games = a.lobby.List()
		a.online = a.lobby.Online()
		a.sel = min(a.sel, max(len(a.games)-1, 0))
	case scrGame:
		a.sess.mu.Lock()
		if g := a.sess.game; g != nil && g.Stopped() {
			// An admin dropped the game under us.
			a.lobby.Leave(g, a.sess.pid)
			a.sess.game = nil
			a.sess.mu.Unlock()
			a.screen = scrLobby
			a.err = "An admin closed that game."
			a.loadBoard()
			a.refresh()
			return
		}
		if a.sess.game != nil {
			prev := a.view.snap
			a.view.snap = a.sess.game.Snapshot()
			if prev != nil && prev.ID == a.view.snap.ID {
				a.sounds(prev, a.view.snap)
			}
		}
		a.sess.mu.Unlock()
	}
}

// sounds works out what just happened to this player between two frames.
func (a *app) sounds(prev, cur *Snapshot) {
	if a.view.muted {
		return
	}
	was, me := prev.Player(a.view.pid), cur.Player(a.view.pid)
	if was == nil || me == nil {
		return
	}
	play := a.sess.play
	switch {
	case cur.Phase == Countdown && (prev.Phase != Countdown || secs(prev.PhaseLeft) != secs(cur.PhaseLeft)):
		play(SndBeep)
	case cur.Phase == Playing && prev.Phase == Countdown && me.InRound:
		play(SndGo)
	}
	if was.Alive && !me.Alive && me.InRound {
		if me.KilledBy != 0 {
			play(SndFahhhMax)
		} else {
			play(SndFahhh)
		}
	}
	if me.Kills > was.Kills {
		play(SndKill)
	}
	if me.Wins > was.Wins {
		play(SndWin)
	}
	if me.Lag > 0 && was.Lag == 0 {
		play(SndKY)
	}
	if cur.CaseOwner == me.ID && prev.CaseOwner != me.ID {
		play(SndCase)
	}
	if a.view.harasmusNear(me) && time.Since(a.lastHarasmus) > 3*time.Second {
		a.lastHarasmus = time.Now()
		play(SndHarasmus)
	}
}

func (a *app) loadBoard() {
	if a.lobby.store == nil {
		return
	}
	a.top, _ = a.lobby.store.Top(10)
	a.mine, _ = a.lobby.store.Lookup(a.name)
}

func (a app) updateName(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "esc":
			return a, tea.Quit
		case "enter":
			name := cleanName(a.nameInput.Value(), "")
			if err := validName(name); err != nil {
				a.err = err.Error()
				return a, nil
			}
			if err := a.lobby.SignIn(name, a.keyFP); err != nil {
				a.err = err.Error()
				return a, nil
			}
			a.sess.mu.Lock()
			a.sess.name = name
			a.sess.mu.Unlock()
			if a.sess.kick != nil {
				a.lobby.OnKick(name, a.sess.kick)
			}
			a.name = name
			a.err = ""
			a.screen = scrLobby
			a.loadBoard()
			a.refresh()
			return a, nil
		}
	}
	var cmd tea.Cmd
	a.nameInput, cmd = a.nameInput.Update(msg)
	return a, cmd
}

func (a app) updateLobby(msg tea.Msg) (tea.Model, tea.Cmd) {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return a, nil
	}
	switch k.String() {
	case "q", "esc":
		return a, tea.Quit
	case "up", "k", "w":
		a.sel = max(a.sel-1, 0)
	case "down", "j", "s":
		a.sel = min(a.sel+1, max(len(a.games)-1, 0))
	case "n":
		a.host = newHostForm(a.name)
		a.screen = scrHost
		a.err = ""
	case "l":
		a.loadBoard()
		a.screen = scrBoard
	case "enter":
		if a.sel < len(a.games) {
			a.join(a.games[a.sel].ID)
		}
	}
	return a, nil
}

func (a *app) join(id string) {
	g, pid, err := a.lobby.Join(id, a.name)
	if err != nil {
		a.err = err.Error()
		return
	}
	a.sess.mu.Lock()
	a.sess.game, a.sess.pid = g, pid
	a.sess.mu.Unlock()
	a.view.pid, a.view.spectate = pid, 0
	a.err = ""
	a.screen = scrGame
	a.refresh()
}

func (a app) updateGame(msg tea.Msg) (tea.Model, tea.Cmd) {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return a, nil
	}
	a.sess.mu.Lock()
	g, pid := a.sess.game, a.sess.pid
	a.sess.mu.Unlock()
	if g == nil {
		return a, nil
	}
	switch k.String() {
	case "q", "esc":
		a.sess.mu.Lock()
		a.lobby.Leave(g, pid)
		a.sess.game = nil
		a.sess.mu.Unlock()
		a.screen = scrLobby
		a.loadBoard()
		a.refresh()
	case "up", "w", "k":
		g.Turn(pid, Up)
	case "down", "s", "j":
		g.Turn(pid, Down)
	case "left", "a", "h":
		g.Turn(pid, Left)
	case "right", "d", "l":
		g.Turn(pid, Right)
	case " ", "shift+up", "shift+down", "shift+left", "shift+right":
		g.Boost(pid)
	case "enter", "r":
		if me := a.view.me(); me != nil && a.view.snap.Phase == Ready {
			g.SetReady(pid, !me.Ready)
		}
	case "tab":
		a.view.nextSpectate()
	case "m":
		a.view.muted = !a.view.muted
	}
	return a, nil
}

func (a app) updateBoard(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "q", "esc", "l", "enter":
			a.screen = scrLobby
		}
	}
	return a, nil
}

func (a app) View() string {
	if a.width > 0 && (a.width < 60 || a.height < 20) {
		return fmt.Sprintf("\n  Light Cycles needs a terminal of at least 60x20.\n  Yours is %dx%d, please make it bigger.\n\n  (ctrl+c to quit)", a.width, a.height)
	}
	var body string
	switch a.screen {
	case scrName:
		body = a.viewName()
	case scrLobby:
		body = a.viewLobby()
	case scrHost:
		body = a.viewHost()
	case scrBoard:
		body = a.viewBoard()
	case scrGame:
		if a.view.snap == nil {
			return "Joining…"
		}
		return a.view.View()
	}
	return lipgloss.NewStyle().Padding(1, 2).Render(body)
}

const logo = `╦  ╦╔═╗╦ ╦╔╦╗  ╔═╗╦ ╦╔═╗╦  ╔═╗╔═╗
║  ║║ ╦╠═╣ ║   ║  ╚╦╝║  ║  ║╣ ╚═╗
╩═╝╩╚═╝╩ ╩ ╩   ╚═╝ ╩ ╚═╝╩═╝╚═╝╚═╝`

func (a app) viewName() string {
	st := a.st
	var b strings.Builder
	b.WriteString(st.title.Render(logo) + "\n" + st.dim.Render("an Operations Team 7 production") + "\n\n")
	b.WriteString("Multiplayer light cycles. Steer with the arrows, boost with space,\nand make everyone else crash into your trail.\n\n")
	b.WriteString(st.bold.Render("What should we call you?") + "\n\n")
	b.WriteString(a.nameInput.View() + "\n\n")
	if a.err != "" {
		b.WriteString(st.err.Render(a.err) + "\n\n")
	}
	b.WriteString(st.dim.Render("Your name is tied to your SSH key, so your leaderboard spot is yours.") + "\n" +
		st.key.Render("enter") + st.dim.Render(" continue · ") + st.key.Render("esc") + st.dim.Render(" quit"))
	return b.String()
}

func (a app) viewLobby() string {
	st := a.st
	var b strings.Builder
	b.WriteString(st.title.Render("LIGHT CYCLES") + st.dim.Render("   signed in as ") + st.bold.Render(a.name) +
		st.dim.Render(fmt.Sprintf(" · %d online · %d games · build %s", a.online, len(a.games), version)) + "\n\n")

	header := fmt.Sprintf("  %-22s %-12s %-7s %-12s %s", "GAME", "HOST", "SIZE", "PLAYERS", "STATUS")
	b.WriteString(st.dim.Render(header) + "\n")
	for i, g := range a.games {
		players := fmt.Sprintf("%d/%d", g.Humans, g.MaxPlayers)
		if g.Bots > 0 {
			players += fmt.Sprintf(" +%d bots", g.Bots)
		}
		status := fmt.Sprintf("round %d", g.Round)
		switch g.Phase {
		case Waiting:
			status = "waiting"
		case Ready:
			status = "ready check"
		}
		row := fmt.Sprintf("  %-22s %-12s %-7s %-12s %s", trunc(g.Name, 22), trunc(g.Host, 12), g.Size, players, status)
		if i == a.sel {
			row = st.sel.Render("▸" + row[1:])
		}
		b.WriteString(row + "\n")
	}
	if a.err != "" {
		b.WriteString("\n" + st.err.Render(a.err) + "\n")
	}
	if a.lobby.Draining() {
		b.WriteString("\n" + st.bold.Render(drainNotice) + "\n")
	}
	b.WriteString("\n" + st.key.Render("↑↓") + st.dim.Render(" pick · ") + st.key.Render("enter") + st.dim.Render(" join · ") +
		st.key.Render("n") + st.dim.Render(" host a new game · ") + st.key.Render("l") + st.dim.Render(" leaderboard · ") +
		st.key.Render("q") + st.dim.Render(" quit"))

	if a.width >= 110 && len(a.top) > 0 {
		return lipgloss.JoinHorizontal(lipgloss.Top, b.String(), "    ", a.miniBoard(5))
	}
	return b.String()
}

func (a app) miniBoard(n int) string {
	st := a.st
	var b strings.Builder
	b.WriteString(st.bold.Render("TOP PLAYERS") + "\n")
	for i, s := range a.top {
		if i >= n {
			break
		}
		b.WriteString(fmt.Sprintf("%2d. %-16s %3dW\n", s.Rank, trunc(s.Name, 16), s.Wins))
	}
	if a.mine.Rank > 0 {
		b.WriteString(st.dim.Render(fmt.Sprintf("\nyou: #%d, %d wins", a.mine.Rank, a.mine.Wins)))
	}
	return b.String()
}

func (a app) viewBoard() string {
	st := a.st
	var b strings.Builder
	b.WriteString(st.title.Render("LEADERBOARD") + "\n\n")
	if a.lobby.store == nil {
		b.WriteString(st.dim.Render("This server runs without a leaderboard database.") + "\n")
	} else if len(a.top) == 0 {
		b.WriteString(st.dim.Render("Nobody's on the board yet.") + "\n")
	} else {
		b.WriteString(st.dim.Render(fmt.Sprintf("%4s  %-16s %5s %6s %7s %6s %6s %5s", "#", "PLAYER", "WINS", "KILLS", "DEATHS", "CASES", "ROUNDS", "WIN%")) + "\n")
		for _, s := range a.top {
			row := fmt.Sprintf("%4d  %-16s %5d %6d %7d %6d %6d %4d%%", s.Rank, trunc(s.Name, 16), s.Wins, s.Kills, s.Deaths, s.Cases, s.Rounds, s.Wins*100/max(s.Rounds, 1))
			if strings.EqualFold(s.Name, a.name) {
				row = st.bold.Render(row)
			}
			b.WriteString(row + "\n")
		}
		if a.mine.Rank > 10 {
			b.WriteString(st.dim.Render(fmt.Sprintf("  …\n%4d  %-16s %5d %6d %7d %6d %6d", a.mine.Rank, a.mine.Name, a.mine.Wins, a.mine.Kills, a.mine.Deaths, a.mine.Cases, a.mine.Rounds)) + "\n")
		}
	}
	b.WriteString("\n" + st.dim.Render("CASES are rounds won while holding the case. Only rounds with at least two human players count. ") + st.key.Render("esc") + st.dim.Render(" back"))
	return b.String()
}

// hostForm is the "host a new game" screen.
type hostForm struct {
	name  textinput.Model
	field int // 0 name, 1 size, 2 max players, 3 bots
	size  int
	max   int
	bots  bool
}

func newHostForm(owner string) hostForm {
	ti := textinput.New()
	ti.CharLimit = 22
	ti.SetValue(owner + "'s game")
	ti.Focus()
	return hostForm{name: ti, size: 1, max: 8, bots: true}
}

func (a app) updateHost(msg tea.Msg) (tea.Model, tea.Cmd) {
	f := &a.host
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "esc":
			a.screen = scrLobby
			a.err = ""
			return a, nil
		case "up", "shift+tab":
			f.field = (f.field + 3) % 4
		case "down", "tab":
			f.field = (f.field + 1) % 4
		case "left", "right":
			step := 1
			if k.String() == "left" {
				step = -1
			}
			switch f.field {
			case 1:
				f.size = (f.size + step + len(sizes)) % len(sizes)
			case 2:
				f.max = min(max(f.max+step, 2), maxCycles)
			case 3:
				f.bots = !f.bots
			}
			if f.field != 0 {
				return a, nil
			}
		case "enter":
			name := strings.TrimSpace(f.name.Value())
			if name == "" {
				a.err = "give your game a name"
				return a, nil
			}
			g, err := a.lobby.Create(Settings{Name: name, Host: a.name, Size: f.size, MaxPlayers: f.max, Bots: f.bots})
			if err != nil {
				a.err = err.Error()
				return a, nil
			}
			a.join(g.ID)
			return a, nil
		default:
			if f.field != 0 {
				return a, nil
			}
		}
		if f.field == 0 {
			f.name.Focus()
		} else {
			f.name.Blur()
		}
	}
	var cmd tea.Cmd
	if f.field == 0 {
		f.name, cmd = f.name.Update(msg)
	}
	return a, cmd
}

func (a app) viewHost() string {
	st, f := a.st, a.host
	var b strings.Builder
	b.WriteString(st.title.Render("HOST A GAME") + "\n\n")
	label := func(i int, s string) string {
		if f.field == i {
			return st.key.Render("▸ " + s)
		}
		return st.dim.Render("  " + s)
	}
	choice := func(i int, s string) string {
		if f.field == i {
			return "◀ " + st.bold.Render(s) + " ▶"
		}
		return "  " + s
	}
	sz := sizes[f.size]
	bots := "off, humans only"
	if f.bots {
		bots = fmt.Sprintf("on, fill up to %d cycles", min(sz.Bots, f.max))
	}
	b.WriteString(label(0, "Name        ") + " " + f.name.View() + "\n\n")
	b.WriteString(label(1, "Arena       ") + choice(1, fmt.Sprintf("%s (%dx%d)", sz.Name, sz.W, sz.H)) + "\n\n")
	b.WriteString(label(2, "Max players ") + choice(2, fmt.Sprint(f.max)) + "\n\n")
	b.WriteString(label(3, "Bots        ") + choice(3, bots) + "\n\n")
	if a.err != "" {
		b.WriteString(st.err.Render(a.err) + "\n\n")
	}
	b.WriteString(st.key.Render("↑↓") + st.dim.Render(" field · ") + st.key.Render("←→") + st.dim.Render(" change · ") +
		st.key.Render("enter") + st.dim.Render(" create and join · ") + st.key.Render("esc") + st.dim.Render(" back"))
	b.WriteString("\n\n" + st.dim.Render("Your game shows up in everyone's lobby and ends when the last player leaves."))
	return b.String()
}
