package main

import (
	"fmt"
	"math/rand"
	"sync"
	"time"
)

// Arena size in cells. Each cell renders two columns wide, so the board is
// 78 columns plus a border: it fits a stock 80x24 terminal.
const (
	W = 39
	H = 18

	tickRate      = 50 * time.Millisecond
	countdownLen  = 60  // ticks (3s)
	roundOverLen  = 80  // ticks (4s)
	boostLen      = 16  // ticks of double speed
	boostCooldown = 100 // ticks from boost start until it is ready again
	maxCycles     = 8
	feedLen       = 5
)

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
	Countdown Phase = iota
	Playing
	RoundOver
)

type Player struct {
	ID       int
	Name     string
	Color    int
	Bot      bool
	X, Y     int
	Dir      Dir
	queue    []Dir
	Alive    bool
	InRound  bool
	Wins     int
	Kills    int
	Boost    int // ticks of boost remaining
	Cooldown int // ticks until boost is ready
}

// Game is one arena. A single goroutine advances it; SSH sessions read
// snapshots and push inputs, all under mu.
type Game struct {
	mu        sync.Mutex
	room      string
	minCycles int
	players   map[int]*Player
	grid      [H][W]int // 0 = empty, otherwise owner player ID
	phase     Phase
	phaseLeft int
	tick      int
	round     int
	feed      []string
	banner    string
	nextID    int
	usedColor [maxCycles]bool
	quit      chan struct{}
}

func NewGame(room string, minCycles int) *Game {
	g := &Game{
		room:      room,
		minCycles: minCycles,
		players:   map[int]*Player{},
		nextID:    1,
		quit:      make(chan struct{}),
	}
	return g
}

// Start begins the first round. The lobby calls it after the first human
// has joined so the opening round gets the right number of bots.
func (g *Game) Start() {
	g.mu.Lock()
	g.startRound()
	g.mu.Unlock()
	go g.loop()
}

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
			g.mu.Unlock()
		}
	}
}

func (g *Game) Stop() { close(g.quit) }

func (g *Game) humans() int {
	n := 0
	for _, p := range g.players {
		if !p.Bot {
			n++
		}
	}
	return n
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

// Join adds a human. If the arena is full of humans it returns ok=false.
// A player who joins mid-round spectates until the next round starts.
func (g *Game) Join(name string) (id int, ok bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.players) >= maxCycles {
		// Make room by dropping a bot; humans always take priority.
		for _, p := range g.players {
			if p.Bot {
				g.removePlayer(p.ID)
				break
			}
		}
	}
	if len(g.players) >= maxCycles {
		return 0, false
	}
	p := g.addPlayer(name, false)
	g.pushFeed(fmt.Sprintf("%s joined", p.Name))
	// Joining during the countdown gets you straight into this round,
	// taking a bot's place if the arena already has enough cycles.
	if g.phase == Countdown && g.round > 0 {
		if len(g.players) > g.minCycles {
			for _, b := range g.players {
				if b.Bot {
					g.removePlayer(b.ID)
					break
				}
			}
		}
		for _, s := range spawns {
			if g.grid[s.y][s.x] == 0 {
				p.X, p.Y, p.Dir = s.x, s.y, s.d
				p.Alive, p.InRound = true, true
				g.grid[p.Y][p.X] = p.ID
				break
			}
		}
	}
	return p.ID, true
}

func (g *Game) Leave(id int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if p, ok := g.players[id]; ok {
		g.pushFeed(fmt.Sprintf("%s left", p.Name))
		g.removePlayer(id)
	}
}

func (g *Game) removePlayer(id int) {
	p := g.players[id]
	g.clearTrail(id)
	g.usedColor[p.Color] = false
	delete(g.players, id)
}

func (g *Game) clearTrail(id int) {
	for y := range g.grid {
		for x := range g.grid[y] {
			if g.grid[y][x] == id {
				g.grid[y][x] = 0
			}
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

var spawns = []struct {
	x, y int
	d    Dir
}{
	{3, H / 4, Right}, {W - 4, H - 1 - H/4, Left},
	{3, H - 1 - H/4, Right}, {W - 4, H / 4, Left},
	{W / 4, 2, Down}, {W - 1 - W/4, H - 3, Up},
	{W - 1 - W/4, 2, Down}, {W / 4, H - 3, Up},
}

var botNames = []string{"bot-ada", "bot-byte", "bot-cyd", "bot-dex", "bot-echo", "bot-flux", "bot-gigi", "bot-hex"}

func (g *Game) freeBotName() string {
	for _, i := range rand.Perm(len(botNames)) {
		if g.uniqueName(botNames[i]) == botNames[i] {
			return botNames[i]
		}
	}
	return "bot"
}

func (g *Game) startRound() {
	// Top up (or trim) bots so there is always someone to beat.
	want := g.minCycles - g.humans()
	bots := []int{}
	for _, p := range g.players {
		if p.Bot {
			bots = append(bots, p.ID)
		}
	}
	for len(bots) > max(want, 0) {
		g.removePlayer(bots[len(bots)-1])
		bots = bots[:len(bots)-1]
	}
	for len(bots) < want && len(g.players) < maxCycles {
		p := g.addPlayer(g.freeBotName(), true)
		bots = append(bots, p.ID)
	}

	g.grid = [H][W]int{}
	order := rand.Perm(len(spawns))
	i := 0
	for _, p := range g.players {
		s := spawns[order[i]]
		i++
		p.X, p.Y, p.Dir = s.x, s.y, s.d
		p.queue = nil
		p.Alive, p.InRound = true, true
		p.Boost, p.Cooldown = 0, 0
		g.grid[p.Y][p.X] = p.ID
	}
	g.round++
	g.tick = 0
	g.phase, g.phaseLeft = Countdown, countdownLen
	g.banner = ""
}

func (g *Game) step() {
	switch g.phase {
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
			g.startRound()
		}
	}
}

func (g *Game) blocked(x, y int) bool {
	return x < 0 || y < 0 || x >= W || y >= H || g.grid[y][x] != 0
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
		case m.x < 0 || m.y < 0 || m.x >= W || m.y >= H:
			dead[m.p.ID] = fmt.Sprintf("%s hit the wall", m.p.Name)
		case g.grid[m.y][m.x] != 0:
			owner := g.players[g.grid[m.y][m.x]]
			if owner == nil || owner.ID == m.p.ID {
				dead[m.p.ID] = fmt.Sprintf("%s crashed into their own trail", m.p.Name)
			} else {
				owner.Kills++
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
			if msg != "" {
				g.pushFeed(msg)
			}
			continue
		}
		m.p.X, m.p.Y = m.x, m.y
		g.grid[m.y][m.x] = m.p.ID
	}
	// Dead cycles' trails vanish, opening up the arena mid-round.
	for id := range dead {
		g.clearTrail(id)
	}
}

func (g *Game) checkRoundOver() {
	var alive []*Player
	humansAlive := 0
	for _, p := range g.players {
		if p.InRound && p.Alive {
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
	switch {
	case len(alive) == 1:
		alive[0].Wins++
		g.banner = fmt.Sprintf("%s wins round %d!", alive[0].Name, g.round)
	case len(alive) == 0:
		g.banner = fmt.Sprintf("Round %d is a draw", g.round)
	default:
		g.banner = fmt.Sprintf("The bots outlasted everyone in round %d", g.round)
	}
	g.pushFeed(g.banner)
	g.phase, g.phaseLeft = RoundOver, roundOverLen
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
	var seen [H][W]bool
	stack := [][2]int{{sx, sy}}
	seen[sy][sx] = true
	n := 0
	for len(stack) > 0 && n < limit {
		c := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		n++
		for _, d := range deltas {
			x, y := c[0]+d[0], c[1]+d[1]
			if !g.blocked(x, y) && !seen[y][x] {
				seen[y][x] = true
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

// Snapshot is a copy of the game state that a session can render without
// holding the lock.
type Snapshot struct {
	Room      string
	Grid      [H][W]int
	Players   []Player
	Phase     Phase
	PhaseLeft int
	Round     int
	Feed      []string
	Banner    string
}

func (g *Game) Snapshot() Snapshot {
	g.mu.Lock()
	defer g.mu.Unlock()
	s := Snapshot{
		Room:      g.room,
		Grid:      g.grid,
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
	return s
}

// Lobby holds one Game per room name. Rooms are created on first join and
// torn down when the last human leaves.
type Lobby struct {
	mu        sync.Mutex
	games     map[string]*Game
	minCycles int
}

func NewLobby(minCycles int) *Lobby {
	return &Lobby{games: map[string]*Game{}, minCycles: minCycles}
}

func (l *Lobby) Join(room, name string) (*Game, int, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	g, ok := l.games[room]
	if !ok {
		g = NewGame(room, l.minCycles)
		l.games[room] = g
		id, ok := g.Join(name)
		g.Start()
		return g, id, ok
	}
	id, ok := g.Join(name)
	return g, id, ok
}

func (l *Lobby) Leave(g *Game, id int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	g.Leave(id)
	g.mu.Lock()
	empty := g.humans() == 0
	g.mu.Unlock()
	if empty {
		g.Stop()
		delete(l.games, g.room)
	}
}
