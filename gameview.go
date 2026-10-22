package main

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

var palette = []string{
	"#ff5f5f", "#5fd7ff", "#ffd75f", "#87ff5f", "#d787ff", "#ff9f43", "#5fffd7", "#ff87d7",
	"#5f87ff", "#d7ff5f", "#ff5fd7", "#afafff", "#ffaf87", "#00d787", "#d7af5f", "#e4e4e4",
}
var colorNames = []string{
	"red", "cyan", "yellow", "green", "purple", "orange", "teal", "pink",
	"blue", "lime", "magenta", "lavender", "peach", "jade", "gold", "white",
}

const sideW = 30 // width of the scoreboard / minimap column

type styles struct {
	cycle  []lipgloss.Style // per-colour trail styles
	heads  []lipgloss.Style
	faint  []lipgloss.Style // minimap trails
	border lipgloss.Style
	wall   lipgloss.Style
	dim    lipgloss.Style
	bold   lipgloss.Style
	title  lipgloss.Style
	banner lipgloss.Style
	sel    lipgloss.Style
	caseHd lipgloss.Style // head of whoever holds the case
	ky     lipgloss.Style // the KY toolkit pickup
	fahhh  lipgloss.Style
	err    lipgloss.Style
	key    lipgloss.Style
}

func newStyles(r *lipgloss.Renderer) styles {
	s := styles{
		border: r.NewStyle().Foreground(lipgloss.Color("#5f5f87")),
		wall:   r.NewStyle().Foreground(lipgloss.Color("#87005f")),
		dim:    r.NewStyle().Foreground(lipgloss.Color("#808080")),
		bold:   r.NewStyle().Bold(true),
		title:  r.NewStyle().Bold(true).Foreground(lipgloss.Color("#00d7ff")),
		banner: r.NewStyle().Bold(true).Foreground(lipgloss.Color("#ffffff")).Background(lipgloss.Color("#5f00af")).Padding(0, 1),
		sel:    r.NewStyle().Bold(true).Foreground(lipgloss.Color("#000000")).Background(lipgloss.Color("#00d7ff")),
		caseHd: r.NewStyle().Bold(true).Foreground(lipgloss.Color("#000000")).Background(lipgloss.Color("#ffd700")),
		ky:     r.NewStyle().Bold(true).Foreground(lipgloss.Color("#ffff00")).Background(lipgloss.Color("#0000af")),
		fahhh:  r.NewStyle().Bold(true).Foreground(lipgloss.Color("#ffffff")).Background(lipgloss.Color("#d70000")).Padding(0, 1),
		err:    r.NewStyle().Foreground(lipgloss.Color("#ff5f5f")),
		key:    r.NewStyle().Bold(true).Foreground(lipgloss.Color("#ffd75f")),
	}
	for _, c := range palette {
		s.cycle = append(s.cycle, r.NewStyle().Foreground(lipgloss.Color(c)))
		s.heads = append(s.heads, r.NewStyle().Foreground(lipgloss.Color("#ffffff")).Background(lipgloss.Color(c)).Bold(true))
		s.faint = append(s.faint, r.NewStyle().Foreground(lipgloss.Color(c)).Faint(true))
	}
	return s
}

// gameView renders one player's view of a game: a camera that follows
// their cycle (or whoever they're spectating) plus a side panel.
type gameView struct {
	pid      int
	spectate int // player ID being followed while you're out
	snap     *Snapshot
	muted    bool
	width    int
	height   int
	st       *styles
}

func (v *gameView) me() *Player { return v.snap.Player(v.pid) }

// target is the cycle the camera follows.
func (v *gameView) target() *Player {
	if me := v.me(); me != nil && me.InRound && me.Alive {
		return me
	}
	if p := v.snap.Player(v.spectate); p != nil && p.InRound && p.Alive {
		return p
	}
	v.nextSpectate()
	if p := v.snap.Player(v.spectate); p != nil {
		return p
	}
	return v.me()
}

func (v *gameView) nextSpectate() {
	var alive []int
	for _, p := range v.snap.Players {
		if p.InRound && p.Alive && p.ID != v.pid {
			alive = append(alive, p.ID)
		}
	}
	sort.Ints(alive)
	for _, id := range alive {
		if id > v.spectate {
			v.spectate = id
			return
		}
	}
	if len(alive) > 0 {
		v.spectate = alive[0]
	}
}

func (v *gameView) View() string {
	me := v.me()
	if me == nil {
		return "Joining…"
	}
	side := v.width >= 60+sideW
	cols := v.width - 2
	if side {
		cols -= sideW + 1
	}
	vw := min(v.snap.W, cols/2)
	vh := min(v.snap.H, v.height-5)
	if !side {
		vh = min(v.snap.H, v.height-6) // room for the newest feed line
	}

	board := v.renderBoard(vw, vh)
	header := v.renderHeader(me)
	footer := v.renderFooter(me)
	if side {
		return lipgloss.JoinVertical(lipgloss.Left,
			header,
			lipgloss.JoinHorizontal(lipgloss.Top, board, " ", v.renderSidePanel(vw < v.snap.W || vh < v.snap.H)),
			footer,
		)
	}
	last := ""
	if n := len(v.snap.Feed); n > 0 {
		last = v.st.dim.Render(trunc(v.snap.Feed[n-1], v.width-1))
	}
	return lipgloss.JoinVertical(lipgloss.Left, header, board, last, footer)
}

func secs(ticks int) int { return ticks*int(tickRate)/int(time.Second) + 1 }

func (v *gameView) renderHeader(me *Player) string {
	st, s := v.st, v.snap
	info := st.title.Render(s.Settings.Name) +
		st.dim.Render(fmt.Sprintf(" · %s · round %d · you are ", sizes[s.Settings.Size].Name, s.Round)) +
		st.cycle[me.Color].Bold(true).Render(colorNames[me.Color])

	var status string
	switch {
	case s.Phase == Waiting:
		status = st.bold.Render("Waiting for another player") + st.dim.Render("… bots are off in this game, invite a friend!")
	case s.Phase == RoundOver:
		status = st.banner.Render(s.Banner) + st.dim.Render(fmt.Sprintf("  next round in %d", secs(s.PhaseLeft)))
	case !me.InRound:
		status = st.dim.Render("Spectating, you'll join at the start of the next round · TAB to switch view")
	case s.Phase == Countdown:
		status = st.bold.Render(fmt.Sprintf("Get ready… %d", secs(s.PhaseLeft))) + st.dim.Render("   your cycle is the flashing ") +
			st.cycle[me.Color].Bold(true).Render(colorNames[me.Color]) + st.dim.Render(" one")
	case !me.Alive:
		if k := s.Player(me.KilledBy); k != nil {
			status = st.fahhh.Render("FAHHHHHHHH!!!") + st.dim.Render(" "+k.Name+" got you. Spectating · TAB to switch view")
		} else {
			status = st.fahhh.Render("fahhh.") + st.dim.Render(" You crashed. Spectating the rest of the round · TAB to switch view")
		}
	default:
		alive := 0
		for _, p := range s.Players {
			if p.InRound && p.Alive {
				alive++
			}
		}
		status = st.bold.Render("GO!") + st.dim.Render(fmt.Sprintf("  %d cycles left", alive))
		if me.Lag > 0 {
			pct := kyProgress(me.Lag)
			bar := strings.Repeat("▰", max(pct/10, 1)) + strings.Repeat("▱", 10-max(pct/10, 1))
			status += "  " + st.ky.Render(fmt.Sprintf(" Loading KY toolkit… %d%% ", pct)) + " " + st.dim.Render(bar+" your steering is lagging")
		} else if v.harasmusNear(me) {
			status += "  " + st.fahhh.Render("HARASMUS is right behind you!")
		} else if s.CaseOwner == me.ID {
			status += "  " + st.caseHd.Render(" you hold the case ") + st.dim.Render(" win to close it")
		}
	}
	return info + "\n" + status
}

func (v *gameView) renderBoard(vw, vh int) string {
	st, s := v.st, v.snap
	byID := map[int]*Player{}
	for i := range s.Players {
		byID[s.Players[i].ID] = &s.Players[i]
	}

	// Centre the camera on the target, but never scroll past the walls
	// by more than a cell, so you can always see an edge coming.
	x0, y0 := 0, 0
	if t := v.target(); t != nil {
		x0, y0 = t.X-vw/2, t.Y-vh/2
	}
	clampAxis := func(c, view, size int) int {
		if view >= size {
			return 0
		}
		return min(max(c, -1), size-view+1)
	}
	x0, y0 = clampAxis(x0, vw, s.W), clampAxis(y0, vh, s.H)

	// Flash your own head during the countdown so you can find yourself.
	flash := s.Phase == Countdown && (s.PhaseLeft/5)%2 == 0

	// cell returns the owner at (x, y), -1 for outside the arena.
	cell := func(x, y int) int {
		if x < 0 || y < 0 || x >= s.W || y >= s.H {
			return -1
		}
		if s.KY != nil && s.KY[0] == x && s.KY[1] == y {
			return -2
		}
		if id := s.At(x, y); byID[id] != nil {
			return id
		}
		return 0
	}
	isHead := func(p *Player, x, y int) bool { return p.Alive && p.X == x && p.Y == y }

	var b strings.Builder
	b.WriteString(st.border.Render("╭" + strings.Repeat("─", vw*2) + "╮"))
	b.WriteByte('\n')
	for y := y0; y < y0+vh; y++ {
		b.WriteString(st.border.Render("│"))
		// Group runs of identical cells so we emit few escape sequences.
		x := x0
		for x < x0+vw {
			id := cell(x, y)
			run := x
			switch id {
			case -1:
				for run < x0+vw && cell(run, y) == -1 {
					run++
				}
				b.WriteString(st.wall.Render(strings.Repeat("░░", run-x)))
			case -2:
				b.WriteString(st.ky.Render("KY"))
				run++
			case 0:
				for run < x0+vw && cell(run, y) == 0 {
					run++
				}
				b.WriteString(strings.Repeat("  ", run-x))
			default:
				p := byID[id]
				if isHead(p, x, y) {
					glyph := [4]string{"▲▲", "▶▶", "▼▼", "◀◀"}[p.Dir]
					if p.ID == v.pid && flash {
						glyph = "  "
					}
					hs := st.heads[p.Color]
					if p.ID == s.CaseOwner {
						hs = st.caseHd
					}
					b.WriteString(hs.Render(glyph))
					run++
					break
				}
				for run < x0+vw && cell(run, y) == id && !isHead(p, run, y) {
					run++
				}
				b.WriteString(st.cycle[p.Color].Render(strings.Repeat("██", run-x)))
			}
			x = run
		}
		b.WriteString(st.border.Render("│"))
		b.WriteByte('\n')
	}
	b.WriteString(st.border.Render("╰" + strings.Repeat("─", vw*2) + "╯"))
	return b.String()
}

func (v *gameView) ranked() []Player {
	ps := append([]Player(nil), v.snap.Players...)
	sort.Slice(ps, func(i, j int) bool {
		if ps[i].Wins != ps[j].Wins {
			return ps[i].Wins > ps[j].Wins
		}
		if ps[i].Kills != ps[j].Kills {
			return ps[i].Kills > ps[j].Kills
		}
		return ps[i].ID < ps[j].ID
	})
	return ps
}

func (v *gameView) renderSidePanel(minimap bool) string {
	st := v.st
	var b strings.Builder
	rows := v.height - 5
	if minimap {
		mm := v.renderMinimap()
		b.WriteString(mm)
		rows -= lipgloss.Height(mm)
	}
	b.WriteString(st.bold.Render("SCOREBOARD") + st.dim.Render("        wins kills") + "\n")
	rows--
	ranked := v.ranked()
	// Give the feed at least 8 lines; the jokes need room.
	maxRows := min(len(ranked), max(rows-10, 3))
	for i, p := range ranked {
		if i >= maxRows {
			b.WriteString(st.dim.Render(fmt.Sprintf("  …and %d more", len(ranked)-maxRows)) + "\n")
			break
		}
		mark := "■"
		if p.ID == v.snap.CaseOwner {
			mark = "§"
		}
		if p.InRound && !p.Alive {
			mark = "✕"
		}
		row := fmt.Sprintf("%s %-17s %4d %5d", mark, trunc(p.Name, 17), p.Wins, p.Kills)
		s := st.cycle[p.Color]
		if p.ID == v.pid {
			s = s.Bold(true).Underline(true)
		}
		b.WriteString(s.Render(row) + "\n")
	}
	b.WriteString("\n" + st.bold.Render("FEED") + "\n")
	rows -= min(len(ranked), maxRows+1) + 2
	// Newest lines last, wrapped, keeping as many recent ones as fit.
	var lines []string
	for _, f := range v.snap.Feed {
		lines = append(lines, strings.Split(lipgloss.NewStyle().Width(sideW).Render(f), "\n")...)
	}
	if len(lines) > rows {
		lines = lines[len(lines)-max(rows, 0):]
	}
	for _, l := range lines {
		b.WriteString(st.dim.Render(strings.TrimRight(l, " ")) + "\n")
	}
	return b.String()
}

// renderMinimap squeezes the whole arena into the side column: faint
// dots for trails and bright dots for heads.
func (v *gameView) renderMinimap() string {
	st, s := v.st, v.snap
	mw := sideW - 2
	mh := max(mw*s.H/(s.W*2), 4)
	byID := map[int]*Player{}
	for i := range s.Players {
		byID[s.Players[i].ID] = &s.Players[i]
	}
	type mark struct {
		color int
		head  bool
		me    bool
	}
	cells := make([]*mark, mw*mh)
	for y := 0; y < s.H; y++ {
		for x := 0; x < s.W; x++ {
			if p := byID[s.At(x, y)]; p != nil {
				i := (y*mh/s.H)*mw + x*mw/s.W
				if cells[i] == nil {
					cells[i] = &mark{color: p.Color}
				}
			}
		}
	}
	for _, p := range s.Players {
		if p.InRound && p.Alive {
			cells[(p.Y*mh/s.H)*mw+p.X*mw/s.W] = &mark{color: p.Color, head: true, me: p.ID == v.pid}
		}
	}
	kyCell := -1
	if s.KY != nil {
		kyCell = (s.KY[1]*mh/s.H)*mw + s.KY[0]*mw/s.W
	}
	var b strings.Builder
	b.WriteString(st.border.Render("┌"+strings.Repeat("─", mw)+"┐") + "\n")
	for y := 0; y < mh; y++ {
		b.WriteString(st.border.Render("│"))
		for x := 0; x < mw; x++ {
			switch m := cells[y*mw+x]; {
			case y*mw+x == kyCell:
				b.WriteString(st.ky.Render("K"))
			case m == nil:
				b.WriteByte(' ')
			case m.me:
				b.WriteString(st.heads[m.color].Render("@"))
			case m.head:
				b.WriteString(st.cycle[m.color].Bold(true).Render("●"))
			default:
				b.WriteString(st.faint[m.color].Render("·"))
			}
		}
		b.WriteString(st.border.Render("│") + "\n")
	}
	b.WriteString(st.border.Render("└" + strings.Repeat("─", mw) + "┘"))
	b.WriteByte('\n')
	return b.String()
}

func (v *gameView) renderFooter(me *Player) string {
	st := v.st
	var boost string
	switch {
	case !me.InRound || !me.Alive:
		boost = st.dim.Render("–")
	case me.Boost > 0:
		boost = st.cycle[me.Color].Bold(true).Render("BOOSTING")
	case me.Cooldown == 0:
		boost = st.cycle[me.Color].Bold(true).Render("READY")
	default:
		filled := (boostCooldown - me.Cooldown) * 10 / boostCooldown
		boost = st.dim.Render(strings.Repeat("▰", filled) + strings.Repeat("▱", 10-filled))
	}
	sound := "m mute"
	if v.muted {
		sound = "m unmute"
	}
	return st.dim.Render("arrows/WASD steer · SPACE boost ") + boost + st.dim.Render(" · "+sound+" · q back to lobby")
}

// harasmusNear reports whether HARASMUS is hunting within a few cells.
func (v *gameView) harasmusNear(me *Player) bool {
	if me == nil || !me.Alive || !me.InRound || v.snap.Phase != Playing {
		return false
	}
	for _, p := range v.snap.Players {
		if p.Kind == KindHarasmus && p.Alive && p.InRound && abs(p.X-me.X)+abs(p.Y-me.Y) <= 6 {
			return true
		}
	}
	return false
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
