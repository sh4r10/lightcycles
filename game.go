package main

import (
	"fmt"
	"math"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"
)

const (
	tickRate      = 50 * time.Millisecond
	countdownLen  = 60  // ticks (3s)
	roundOverLen  = 80  // ticks (4s)
	boostLen      = 16  // ticks of double speed
	boostCooldown = 100 // ticks from boost start until it is ready again
	maxCycles     = 16
	feedLen       = 6
)

// Arena sizes a host can pick. Small fits on one screen; the others scroll
// with a camera that follows your cycle.
var sizes = []struct {
	Name string
	W, H int
	Bots int // bots topped up to this many cycles when bots are on
}{
	{"Small", 40, 20, 4},
	{"Medium", 80, 40, 6},
	{"Large", 140, 70, 10},
}

type Dir int

const (
	Up Dir = iota
	Right
	Down
	Left
)

var deltas = [4][2]int{{0, -1}, {1, 0}, {0, 1}, {-1, 0}}

func (d Dir) reverse() Dir { return (d + 2) % 4 }

type Phase int

const (
	Waiting Phase = iota // not enough cycles to start
	Countdown
	Playing
	RoundOver
)

type Player struct {
	ID         int
	Name       string
	Color      int
	Bot        bool
	X, Y       int
	Dir        Dir
	queue      []Dir
	Alive      bool
	InRound    bool
	Wins       int
	Kills      int
	RoundKills int
	Boost      int // ticks of boost remaining
	Cooldown   int // ticks until boost is ready
}

// Settings are chosen by whoever hosts the game.
type Settings struct {
	Name       string
	Host       string
	Size       int // index into sizes
	MaxPlayers int
	Bots       bool
	Permanent  bool // never torn down, even when empty
}

// RoundStat is one human's result for a finished round, for the leaderboard.
type RoundStat struct {
	Name  string
	Won   bool
	Died  bool
	Kills int
}

// Game is one arena. A single goroutine advances it; sessions push inputs
// under mu and read the latest published Snapshot without locking.
type Game struct {
	mu        sync.Mutex
	ID        string
	Settings  Settings
	W, H      int
	players   map[int]*Player
	grid      []int // W*H, 0 = empty, otherwise owner player ID
	phase     Phase
	phaseLeft int
	tick      int
	round     int
	feed      []string
	banner    string
	nextID    int
	usedColor [maxCycles]bool
	onRound   func([]RoundStat)
	snap      atomic.Pointer[Snapshot]
	quit      chan struct{}
	stopOnce  sync.Once
}

func NewGame(id string, s Settings, onRound func([]RoundStat)) *Game {
	sz := sizes[s.Size]
	g := &Game{
		ID:       id,
		Settings: s,
		W:        sz.W,
		H:        sz.H,
		players:  map[int]*Player{},
		grid:     make([]int, sz.W*sz.H),
		nextID:   1,
		onRound:  onRound,
		quit:     make(chan struct{}),
	}
	g.publish()
	return g
}

func (g *Game) Start() { go g.loop() }

func (g *Game) loop() {
	t := time.NewTicker(tickRate)
	defer t.Stop()
	for {
		select {
		case <-g.quit:
			return
		case <-t.C:
			g.mu.Lock()
			g.step()
			g.publish()
			g.mu.Unlock()
		}
	}
}

func (g *Game) Stop() { g.stopOnce.Do(func() { close(g.quit) }) }

func (g *Game) at(x, y int) int { return g.grid[y*g.W+x] }

func (g *Game) set(x, y, id int) { g.grid[y*g.W+x] = id }

func (g *Game) humans() int {
	n := 0
	for _, p := range g.players {
		if !p.Bot {
			n++
		}
	}
	return n
}

func (g *Game) Humans() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.humans()
}

func (g *Game) wantBots() int {
	if !g.Settings.Bots {
		return 0
	}
	want := min(sizes[g.Settings.Size].Bots, g.Settings.MaxPlayers) - g.humans()
	return max(want, 0)
}

func (g *Game) addPlayer(name string, bot bool) *Player {
	color := 0
	for i, used := range g.usedColor {
		if !used {
			color = i
			break
		}
	}
	g.usedColor[color] = true
	p := &Player{ID: g.nextID, Name: g.uniqueName(name), Color: color, Bot: bot}
	g.nextID++
	g.players[p.ID] = p
	return p
}

func (g *Game) uniqueName(name string) string {
	taken := func(n string) bool {
		for _, p := range g.players {
			if p.Name == n {
				return true
			}
		}
		return false
	}
	if !taken(name) {
		return name
	}
	for i := 2; ; i++ {
		if n := fmt.Sprintf("%s%d", name, i); !taken(n) {
			return n
		}
	}
}

var ErrGameFull = fmt.Errorf("that game is full")

// Join adds a human. A player who joins mid-round spectates until the next
// round; joining during the countdown or while waiting gets you straight in.
func (g *Game) Join(name string) (int, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.humans() >= g.Settings.MaxPlayers {
		return 0, ErrGameFull
	}
	// Humans always take priority over bots.
	if len(g.players) >= min(g.Settings.MaxPlayers, maxCycles) || (g.phase == Countdown && g.botCount() > g.wantBots()-1) {
		g.dropBot()
	}
	p := g.addPlayer(name, false)
	g.pushFeed(fmt.Sprintf("%s joined", p.Name))
	if g.phase == Countdown {
		g.placeLate(p)
	}
	g.publish()
	return p.ID, nil
}

func (g *Game) botCount() int { return len(g.players) - g.humans() }

func (g *Game) dropBot() {
	for _, p := range g.players {
		if p.Bot {
			g.removePlayer(p.ID)
			return
		}
	}
}

func (g *Game) placeLate(p *Player) {
	for _, s := range g.spawns() {
		if g.at(s.x, s.y) == 0 {
			p.X, p.Y, p.Dir = s.x, s.y, s.d
			p.Alive, p.InRound = true, true
			g.set(p.X, p.Y, p.ID)
			return
		}
	}
}

func (g *Game) Leave(id int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if p, ok := g.players[id]; ok {
		g.pushFeed(fmt.Sprintf("%s left", p.Name))
		g.removePlayer(id)
		g.publish()
	}
}

func (g *Game) removePlayer(id int) {
	p := g.players[id]
	g.clearTrail(id)
	g.usedColor[p.Color] = false
	delete(g.players, id)
}

func (g *Game) clearTrail(id int) {
	for i, v := range g.grid {
		if v == id {
			g.grid[i] = 0
		}
	}
}

func (g *Game) pushFeed(s string) {
	g.feed = append(g.feed, s)
	if len(g.feed) > feedLen {
		g.feed = g.feed[len(g.feed)-feedLen:]
	}
}

// Turn queues a direction change. Queuing up to three turns lets fast
// players do tight U-turns (e.g. up then left) within a single move.
func (g *Game) Turn(id int, d Dir) {
	g.mu.Lock()
	defer g.mu.Unlock()
	p, ok := g.players[id]
	if !ok || !p.InRound || !p.Alive {
		return
	}
	last := p.Dir
	if len(p.queue) > 0 {
		last = p.queue[len(p.queue)-1]
	}
	if d == last || d == last.reverse() || len(p.queue) >= 3 {
		return
	}
	p.queue = append(p.queue, d)
}

func (g *Game) Boost(id int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if p, ok := g.players[id]; ok && g.phase == Playing && p.Alive {
		g.boost(p)
	}
}

func (g *Game) boost(p *Player) {
	if p.Cooldown == 0 {
		p.Boost = boostLen
		p.Cooldown = boostCooldown
	}
}

type spawn struct {
	x, y int
	d    Dir
}

// spawns places cycles on a ring, all heading clockwise, so nobody starts
// nose to nose with a neighbour.
func (g *Game) spawns() []spawn {
	cx, cy := float64(g.W-1)/2, float64(g.H-1)/2
	rx, ry := float64(g.W)*0.36, float64(g.H)*0.36
	out := make([]spawn, maxCycles)
	for i := range out {
		a := 2 * math.Pi * float64(i) / maxCycles
		x := int(math.Round(cx + rx*math.Cos(a)))
		y := int(math.Round(cy + ry*math.Sin(a)))
		// Clockwise tangent on screen (y grows downward) is (-sin, cos).
		tx, ty := -math.Sin(a)*rx, math.Cos(a)*ry
		var d Dir
		if math.Abs(tx) > math.Abs(ty) {
			d = Right
			if tx < 0 {
				d = Left
			}
		} else {
			d = Down
			if ty < 0 {
				d = Up
			}
		}
		out[i] = spawn{x, y, d}
	}
	return out
}

var botNames = []string{
	"bot-ada", "bot-byte", "bot-cyd", "bot-dex", "bot-echo", "bot-flux", "bot-gigi", "bot-hex",
	"bot-ion", "bot-jinx", "bot-kilo", "bot-lux", "bot-mips", "bot-nyx", "bot-ohm", "bot-pip",
}

func (g *Game) freeBotName() string {
	for _, i := range rand.Perm(len(botNames)) {
		if g.uniqueName(botNames[i]) == botNames[i] {
			return botNames[i]
		}
	}
	return "bot"
}

func (g *Game) canStart() bool {
	return g.humans() > 0 && g.humans()+g.wantBots() >= 2
}

func (g *Game) startRound() {
	want := g.wantBots()
	for g.botCount() > want {
		g.dropBot()
	}
	for g.botCount() < want && len(g.players) < maxCycles {
		g.addPlayer(g.freeBotName(), true)
	}

	clear(g.grid)
	sp := g.spawns()
	order := rand.Perm(len(sp))
	i := 0
	for _, p := range g.players {
		s := sp[order[i]]
		i++
		p.X, p.Y, p.Dir = s.x, s.y, s.d
		p.queue = nil
		p.Alive, p.InRound = true, true
		p.Boost, p.Cooldown, p.RoundKills = 0, 0, 0
		g.set(p.X, p.Y, p.ID)
	}
	g.round++
	g.tick = 0
	g.phase, g.phaseLeft = Countdown, countdownLen
	g.banner = ""
}

func (g *Game) step() {
	switch g.phase {
	case Waiting:
		if g.canStart() {
			g.startRound()
		}
	case Countdown:
		g.phaseLeft--
		if g.phaseLeft <= 0 {
			g.phase = Playing
		}
	case Playing:
		g.tick++
		g.moveAll()
		g.checkRoundOver()
	case RoundOver:
		g.phaseLeft--
		if g.phaseLeft <= 0 {
			if g.canStart() {
				g.startRound()
			} else {
				g.phase = Waiting
			}
		}
	}
}

func (g *Game) blocked(x, y int) bool {
	return x < 0 || y < 0 || x >= g.W || y >= g.H || g.at(x, y) != 0
}

func (g *Game) moveAll() {
	type move struct {
		p    *Player
		x, y int
	}
	var moves []move
	for _, p := range g.players {
		if !p.InRound || !p.Alive {
			continue
		}
		if p.Cooldown > 0 {
			p.Cooldown--
		}
		// Normal speed is one cell every other tick; boost moves every tick.
		if p.Boost > 0 {
			p.Boost--
		} else if g.tick%2 != 0 {
			continue
		}
		if p.Bot {
			g.botThink(p)
		}
		if len(p.queue) > 0 {
			if d := p.queue[0]; d != p.Dir.reverse() {
				p.Dir = d
			}
			p.queue = p.queue[1:]
		}
		dx, dy := deltas[p.Dir][0], deltas[p.Dir][1]
		moves = append(moves, move{p, p.X + dx, p.Y + dy})
	}

	// Resolve all moves simultaneously so nobody wins a head-on by ID order.
	dead := map[int]string{}
	for _, m := range moves {
		switch {
		case m.x < 0 || m.y < 0 || m.x >= g.W || m.y >= g.H:
			dead[m.p.ID] = fmt.Sprintf("%s hit the wall", m.p.Name)
		case g.at(m.x, m.y) != 0:
			owner := g.players[g.at(m.x, m.y)]
			if owner == nil || owner.ID == m.p.ID {
				dead[m.p.ID] = fmt.Sprintf("%s crashed into their own trail", m.p.Name)
			} else {
				owner.Kills++
				owner.RoundKills++
				dead[m.p.ID] = fmt.Sprintf("%s ⟶ %s's trail", m.p.Name, owner.Name)
			}
		}
	}
	for i, a := range moves {
		for _, b := range moves[i+1:] {
			if a.x == b.x && a.y == b.y {
				dead[a.p.ID] = fmt.Sprintf("%s and %s collided head-on", a.p.Name, b.p.Name)
				dead[b.p.ID] = ""
			}
		}
	}
	for _, m := range moves {
		if msg, ok := dead[m.p.ID]; ok {
			m.p.Alive = false
			m.p.Boost = 0
			if msg != "" {
				g.pushFeed(msg)
			}
			continue
		}
		m.p.X, m.p.Y = m.x, m.y
		g.set(m.x, m.y, m.p.ID)
	}
	// Dead cycles' trails vanish, opening up the arena mid-round.
	for id := range dead {
		g.clearTrail(id)
	}
}

func (g *Game) checkRoundOver() {
	var alive []*Player
	humansAlive, humansInRound := 0, 0
	for _, p := range g.players {
		if !p.InRound {
			continue
		}
		if !p.Bot {
			humansInRound++
		}
		if p.Alive {
			alive = append(alive, p)
			if !p.Bot {
				humansAlive++
			}
		}
	}
	// End once one cycle is left, or once every human is out so nobody
	// waits on a bots-only fight.
	if len(alive) > 1 && humansAlive > 0 {
		return
	}
	var winner *Player
	switch {
	case len(alive) == 1:
		winner = alive[0]
		winner.Wins++
		g.banner = fmt.Sprintf("%s wins round %d!", winner.Name, g.round)
	case len(alive) == 0:
		g.banner = fmt.Sprintf("Round %d is a draw", g.round)
	default:
		g.banner = fmt.Sprintf("The bots outlasted everyone in round %d", g.round)
	}
	g.pushFeed(g.banner)
	g.phase, g.phaseLeft = RoundOver, roundOverLen

	// Only rounds with at least two humans count towards the leaderboard,
	// so nobody can farm wins against bots in a private game.
	if g.onRound != nil && humansInRound >= 2 {
		var stats []RoundStat
		for _, p := range g.players {
			if p.InRound && !p.Bot {
				stats = append(stats, RoundStat{Name: p.Name, Won: p == winner, Died: !p.Alive, Kills: p.RoundKills})
			}
		}
		go g.onRound(stats)
	}
}

// botThink picks the neighbouring cell with the most reachable open space,
// with a little noise so bots don't all play identically.
func (g *Game) botThink(p *Player) {
	p.queue = nil
	best, bestScore := p.Dir, -1.0
	for _, d := range []Dir{p.Dir, (p.Dir + 1) % 4, (p.Dir + 3) % 4} {
		nx, ny := p.X+deltas[d][0], p.Y+deltas[d][1]
		if g.blocked(nx, ny) {
			continue
		}
		score := float64(g.space(nx, ny, 120))
		if d == p.Dir {
			score += 3
		}
		// Shy away from cells next to another cycle's head.
		for _, o := range g.players {
			if o != p && o.Alive && o.InRound && abs(o.X-nx)+abs(o.Y-ny) <= 1 {
				score -= 40
			}
		}
		score += rand.Float64() * 6
		if score > bestScore {
			best, bestScore = d, score
		}
	}
	p.queue = []Dir{best}
	if p.Cooldown == 0 && rand.Intn(60) == 0 {
		g.boost(p)
	}
}

func (g *Game) space(sx, sy, limit int) int {
	seen := map[int]bool{sy*g.W + sx: true}
	stack := [][2]int{{sx, sy}}
	n := 0
	for len(stack) > 0 && n < limit {
		c := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		n++
		for _, d := range deltas {
			x, y := c[0]+d[0], c[1]+d[1]
			if !g.blocked(x, y) && !seen[y*g.W+x] {
				seen[y*g.W+x] = true
				stack = append(stack, [2]int{x, y})
			}
		}
	}
	return n
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// Snapshot is an immutable copy of the game state, published once per tick.
type Snapshot struct {
	ID        string
	Settings  Settings
	W, H      int
	Grid      []int
	Players   []Player
	Phase     Phase
	PhaseLeft int
	Round     int
	Feed      []string
	Banner    string
}

func (s *Snapshot) At(x, y int) int { return s.Grid[y*s.W+x] }

func (s *Snapshot) Player(id int) *Player {
	for i := range s.Players {
		if s.Players[i].ID == id {
			return &s.Players[i]
		}
	}
	return nil
}

// publish must be called with mu held.
func (g *Game) publish() {
	s := &Snapshot{
		ID:        g.ID,
		Settings:  g.Settings,
		W:         g.W,
		H:         g.H,
		Grid:      append([]int(nil), g.grid...),
		Phase:     g.phase,
		PhaseLeft: g.phaseLeft,
		Round:     g.round,
		Feed:      append([]string(nil), g.feed...),
		Banner:    g.banner,
	}
	for _, p := range g.players {
		cp := *p
		cp.queue = nil
		s.Players = append(s.Players, cp)
	}
	g.snap.Store(s)
}

func (g *Game) Snapshot() *Snapshot { return g.snap.Load() }
