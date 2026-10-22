package main

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const frameRate = 33 * time.Millisecond

var palette = []string{"#ff5f5f", "#5fd7ff", "#ffd75f", "#87ff5f", "#d787ff", "#ff9f43", "#5fffd7", "#ff87d7"}
var colorNames = []string{"red", "cyan", "yellow", "green", "purple", "orange", "teal", "pink"}

type frameMsg struct{}

func nextFrame() tea.Cmd {
	return tea.Tick(frameRate, func(time.Time) tea.Msg { return frameMsg{} })
}

type model struct {
	game   arena
	lost   error // set when a remote server connection drops
	id     int
	snap   Snapshot
	width  int
	height int

	cycle  []lipgloss.Style // per-colour trail styles
	heads  []lipgloss.Style
	border lipgloss.Style
	dim    lipgloss.Style
	bold   lipgloss.Style
	title  lipgloss.Style
	banner lipgloss.Style
}

// arena is what the view needs from a game. A *Game serves SSH players
// directly; a *remoteGame mirrors one over the network for the client.
type arena interface {
	Snapshot() Snapshot
	Turn(id int, d Dir)
	Boost(id int)
}

func newModel(r *lipgloss.Renderer, g arena, id int) model {
	m := model{
		game:   g,
		id:     id,
		snap:   g.Snapshot(),
		border: r.NewStyle().Foreground(lipgloss.Color("#5f5f87")),
		dim:    r.NewStyle().Foreground(lipgloss.Color("#808080")),
		bold:   r.NewStyle().Bold(true),
		title:  r.NewStyle().Bold(true).Foreground(lipgloss.Color("#00d7ff")),
		banner: r.NewStyle().Bold(true).Foreground(lipgloss.Color("#ffffff")).Background(lipgloss.Color("#5f00af")).Padding(0, 1),
	}
	for _, c := range palette {
		m.cycle = append(m.cycle, r.NewStyle().Foreground(lipgloss.Color(c)))
		m.heads = append(m.heads, r.NewStyle().Foreground(lipgloss.Color("#ffffff")).Background(lipgloss.Color(c)).Bold(true))
	}
	return m
}

func (m model) Init() tea.Cmd { return nextFrame() }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case frameMsg:
		if e, ok := m.game.(interface{ Err() error }); ok && e.Err() != nil {
			m.lost = e.Err()
			return m, tea.Quit
		}
		m.snap = m.game.Snapshot()
		return m, nextFrame()
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		case "up", "w", "k":
			m.game.Turn(m.id, Up)
		case "down", "s", "j":
			m.game.Turn(m.id, Down)
		case "left", "a", "h":
			m.game.Turn(m.id, Left)
		case "right", "d", "l":
			m.game.Turn(m.id, Right)
		case " ", "shift+up", "shift+down", "shift+left", "shift+right":
			m.game.Boost(m.id)
		}
	}
	return m, nil
}

func (m model) me() *Player {
	for i := range m.snap.Players {
		if m.snap.Players[i].ID == m.id {
			return &m.snap.Players[i]
		}
	}
	return nil
}

func (m model) View() string {
	if m.width > 0 && (m.width < 80 || m.height < 24) {
		return fmt.Sprintf("\n  Light Cycles needs a terminal of at least 80x24.\n  Yours is %dx%d, please make it bigger.\n\n  (q to quit)", m.width, m.height)
	}
	me := m.me()
	if me == nil {
		return "Connecting…"
	}

	board := m.renderBoard()
	header := m.renderHeader(me)
	footer := m.renderFooter(me)

	if m.width >= 80+28 {
		side := m.renderSidePanel(m.width - 82)
		return lipgloss.JoinVertical(lipgloss.Left,
			header,
			lipgloss.JoinHorizontal(lipgloss.Top, board, "  ", side),
			footer,
		)
	}
	return lipgloss.JoinVertical(lipgloss.Left, header, board, m.renderScoreLine(), footer)
}

func (m model) renderHeader(me *Player) string {
	title := m.title.Render("LIGHT CYCLES")
	info := m.dim.Render(fmt.Sprintf("  room %s · round %d · you are ", m.snap.Room, m.snap.Round)) +
		m.cycle[me.Color].Bold(true).Render(colorNames[me.Color])

	var status string
	switch {
	case m.snap.Phase == RoundOver:
		status = m.banner.Render(m.snap.Banner) + m.dim.Render(fmt.Sprintf("  next round in %d", m.snap.PhaseLeft*int(tickRate)/int(time.Second)+1))
	case !me.InRound:
		status = m.dim.Render("Spectating, you'll join at the start of the next round")
	case m.snap.Phase == Countdown:
		n := m.snap.PhaseLeft*int(tickRate)/int(time.Second) + 1
		status = m.bold.Render(fmt.Sprintf("Get ready… %d", n)) + m.dim.Render("   your cycle is the flashing ") + m.cycle[me.Color].Bold(true).Render(colorNames[me.Color]) + m.dim.Render(" one")
	case !me.Alive:
		status = m.dim.Render("You crashed! Watching the rest of the round…")
	default:
		alive := 0
		for _, p := range m.snap.Players {
			if p.InRound && p.Alive {
				alive++
			}
		}
		status = m.bold.Render("GO!") + m.dim.Render(fmt.Sprintf("  %d cycles left", alive))
	}
	return title + info + "\n" + status
}

func (m model) renderBoard() string {
	byID := map[int]*Player{}
	for i := range m.snap.Players {
		byID[m.snap.Players[i].ID] = &m.snap.Players[i]
	}
	// Flash your own head during the countdown so you can find yourself.
	flash := m.snap.Phase == Countdown && (m.snap.PhaseLeft/5)%2 == 0

	var b strings.Builder
	b.WriteString(m.border.Render("╭" + strings.Repeat("─", W*2) + "╮"))
	b.WriteByte('\n')
	for y := 0; y < H; y++ {
		b.WriteString(m.border.Render("│"))
		// Group runs of identical cells so we emit few escape sequences.
		x := 0
		for x < W {
			id := m.snap.Grid[y][x]
			p := byID[id]
			if id == 0 || p == nil {
				run := x
				for run < W && (m.snap.Grid[y][run] == 0 || byID[m.snap.Grid[y][run]] == nil) {
					run++
				}
				b.WriteString(strings.Repeat("  ", run-x))
				x = run
				continue
			}
			if p.Alive && p.X == x && p.Y == y {
				glyph := [4]string{"▲▲", "▶▶", "▼▼", "◀◀"}[p.Dir]
				if p.ID == m.id && flash {
					glyph = "  "
				}
				b.WriteString(m.heads[p.Color].Render(glyph))
				x++
				continue
			}
			run := x
			for run < W && m.snap.Grid[y][run] == id && !(p.Alive && p.X == run && p.Y == y) {
				run++
			}
			b.WriteString(m.cycle[p.Color].Render(strings.Repeat("██", run-x)))
			x = run
		}
		b.WriteString(m.border.Render("│"))
		b.WriteByte('\n')
	}
	b.WriteString(m.border.Render("╰" + strings.Repeat("─", W*2) + "╯"))
	return b.String()
}

func (m model) ranked() []Player {
	ps := append([]Player(nil), m.snap.Players...)
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

func (m model) renderSidePanel(width int) string {
	var b strings.Builder
	b.WriteString(m.bold.Render("SCOREBOARD") + m.dim.Render("   wins kills") + "\n")
	for _, p := range m.ranked() {
		name := p.Name
		if len(name) > 12 {
			name = name[:12]
		}
		mark := "■"
		if p.InRound && !p.Alive {
			mark = "✕"
		}
		row := fmt.Sprintf("%s %-12s %4d %5d", mark, name, p.Wins, p.Kills)
		st := m.cycle[p.Color]
		if p.ID == m.id {
			st = st.Bold(true).Underline(true)
		}
		b.WriteString(st.Render(row) + "\n")
	}
	b.WriteString("\n" + m.bold.Render("FEED") + "\n")
	for _, f := range m.snap.Feed {
		if r := []rune(f); len(r) > width {
			f = string(r[:width-1]) + "…"
		}
		b.WriteString(m.dim.Render(f) + "\n")
	}
	return b.String()
}

func (m model) renderScoreLine() string {
	var parts []string
	for _, p := range m.ranked() {
		name := p.Name
		if len(name) > 8 {
			name = name[:8]
		}
		parts = append(parts, m.cycle[p.Color].Render(fmt.Sprintf("■ %s %d", name, p.Wins)))
	}
	return strings.Join(parts, "  ")
}

func (m model) renderFooter(me *Player) string {
	var boost string
	switch {
	case !me.InRound || !me.Alive:
		boost = m.dim.Render("–")
	case me.Boost > 0:
		boost = m.cycle[me.Color].Bold(true).Render("BOOSTING")
	case me.Cooldown == 0:
		boost = m.cycle[me.Color].Bold(true).Render("READY")
	default:
		filled := (boostCooldown - me.Cooldown) * 10 / boostCooldown
		boost = m.dim.Render(strings.Repeat("▰", filled) + strings.Repeat("▱", 10-filled))
	}
	return m.dim.Render("arrows/WASD steer · SPACE boost ") + boost + m.dim.Render(" · q quit")
}
