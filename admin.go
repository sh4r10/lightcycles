package main

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/charmbracelet/log"
	"github.com/charmbracelet/ssh"
	"github.com/charmbracelet/wish"
	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/term"
)

// adminUser is the SSH username that opens the admin console instead of
// the game. The name is reserved so no player can take it.
const adminUser = "admin"

const (
	adminTries    = 3                // password attempts per connection
	adminMaxFails = 5                // failures from one address before a lockout
	adminLockout  = time.Minute      // first lockout; doubles each time
	adminMaxLock  = time.Hour        // longest lockout
	adminFailWait = 1 * time.Second  // pause after each wrong password
	adminIdle     = 30 * time.Minute // console idle timeout
)

var started = time.Now()

// admin holds what the console needs and guards the password.
type admin struct {
	lobby *Lobby
	store *Store
	hash  [32]byte // sha256 of the password, so compares are constant time
	on    bool
	logs  *logRing

	mu    sync.Mutex
	fails map[string]*adminFails // by remote IP
}

type adminFails struct {
	count    int
	lockouts int
	until    time.Time
}

func newAdmin(lobby *Lobby, store *Store, password string, logs *logRing) *admin {
	return &admin{
		lobby: lobby, store: store, logs: logs,
		hash: sha256.Sum256([]byte(password)), on: password != "",
		fails: map[string]*adminFails{},
	}
}

func (a *admin) check(password string) bool {
	h := sha256.Sum256([]byte(password))
	return a.on && subtle.ConstantTimeCompare(h[:], a.hash[:]) == 1
}

// locked reports how long an address must wait before trying again.
func (a *admin) locked(ip string) time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	if f := a.fails[ip]; f != nil {
		return time.Until(f.until).Round(time.Second)
	}
	return 0
}

// failed records a wrong password and reports whether the address is now locked out.
func (a *admin) failed(ip string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	f := a.fails[ip]
	if f == nil {
		f = &adminFails{}
		a.fails[ip] = f
	}
	f.count++
	if f.count < adminMaxFails {
		return false
	}
	f.count = 0
	f.until = time.Now().Add(min(adminLockout<<f.lockouts, adminMaxLock))
	f.lockouts++
	return true
}

func (a *admin) succeeded(ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.fails, ip)
}

func remoteIP(s ssh.Session) string {
	host, _, err := net.SplitHostPort(s.RemoteAddr().String())
	if err != nil {
		return s.RemoteAddr().String()
	}
	return host
}

// adminMiddleware sends `ssh admin@host` to the admin console. It must run
// before activeterm so it can explain what to do without a terminal.
func adminMiddleware(a *admin) wish.Middleware {
	return func(next ssh.Handler) ssh.Handler {
		return func(s ssh.Session) {
			if s.User() != adminUser {
				next(s)
				return
			}
			a.serve(s)
		}
	}
}

func (a *admin) serve(s ssh.Session) {
	defer s.Close()
	ip := remoteIP(s)
	if !a.on {
		wish.Println(s, "The admin console is disabled. Start the server with LIGHTCYCLES_ADMIN_PASSWORD set to enable it.")
		_ = s.Exit(1)
		return
	}
	pty, winCh, ok := s.Pty()
	if !ok {
		wish.Println(s, "The admin console needs a terminal. Connect with: ssh -t admin@<host> [command]")
		_ = s.Exit(1)
		return
	}
	if wait := a.locked(ip); wait > 0 {
		log.Warn("admin login refused, address locked out", "ip", ip, "for", wait)
		wish.Printf(s, "Too many wrong passwords. Try again in %s.\r\n", wait)
		_ = s.Exit(1)
		return
	}

	t := term.NewTerminal(s, "")
	_ = t.SetSize(pty.Window.Width, pty.Window.Height)
	go func() {
		for w := range winCh {
			_ = t.SetSize(w.Width, w.Height)
		}
	}()

	// Hang up on an idle console.
	idle := time.AfterFunc(adminIdle, func() { _ = s.Close() })
	defer idle.Stop()

	authed := false
	for range adminTries {
		pw, err := t.ReadPassword("Admin password: ")
		if err != nil {
			return
		}
		if a.check(pw) {
			authed = true
			break
		}
		log.Warn("admin login failed", "ip", ip)
		if a.failed(ip) {
			fmt.Fprintf(t, "Too many wrong passwords. This address is locked out for a while.\r\n")
			_ = s.Exit(1)
			return
		}
		time.Sleep(adminFailWait)
		fmt.Fprintln(t, "Wrong password.")
	}
	if !authed {
		_ = s.Exit(1)
		return
	}
	a.succeeded(ip)
	who := ip
	if k := s.PublicKey(); k != nil {
		who = ip + " " + gossh.FingerprintSHA256(k)
	}
	log.Info("admin logged in", "who", who)
	defer log.Info("admin logged out", "who", who)

	c := &console{admin: a, out: t, who: who}

	// `ssh -t admin@host games` runs one command and exits.
	if cmd := s.Command(); len(cmd) > 0 {
		c.run(strings.Join(cmd, " "))
		_ = s.Exit(0)
		return
	}

	fmt.Fprintln(t, "Light Cycles admin console. Type \"help\" for commands, \"quit\" to leave.")
	t.SetPrompt("admin> ")
	for {
		line, err := t.ReadLine()
		if err != nil {
			return
		}
		idle.Reset(adminIdle)
		if c.run(line) {
			_ = s.Exit(0)
			return
		}
	}
}

// console runs admin commands, writing output to out.
type console struct {
	*admin
	out io.Writer
	who string // for the audit log
}

type adminCmd struct {
	args string
	help string
	run  func(c *console, args []string, rest string) error
}

var adminCmds map[string]adminCmd

// adminOrder is the order commands are listed in help.
var adminOrder = []string{
	"status", "logs", "games", "show", "drop", "announce", "online", "kick",
	"players", "top", "set", "reset", "unbind", "delete", "wipe", "sql", "quit",
}

func init() {
	adminCmds = map[string]adminCmd{
		"status":   {"", "uptime, memory, players and games", (*console).status},
		"logs":     {"[n]", "the last n server log lines (default 30)", (*console).showLogs},
		"games":    {"", "running games", (*console).games},
		"show":     {"<game>", "who is in a game (game = ID or name)", (*console).show},
		"drop":     {"<game>", "end a game and send its players to the lobby (Team 7 HQ restarts fresh)", (*console).drop},
		"announce": {"<message>", "post a message in every game's feed", (*console).announce},
		"online":   {"", "signed-in players", (*console).listOnline},
		"kick":     {"<name> [reason]", "disconnect a player", (*console).kick},
		"players":  {"[filter]", "players in the database, most recent first", (*console).players},
		"top":      {"[n]", "the leaderboard (default 10)", (*console).top},
		"set":      {"<name> <wins|kills|deaths|cases|rounds> <n>", "overwrite one stat", (*console).set},
		"reset":    {"<name>", "zero a player's stats (keeps their name)", (*console).reset},
		"unbind":   {"<name>", "free a name from its SSH key, e.g. for a new laptop", (*console).unbind},
		"delete":   {"<name>", "forget a player entirely", (*console).deletePlayer},
		"wipe":     {"yes", "zero everyone's stats (type \"wipe yes\")", (*console).wipe},
		"sql":      {"<statement>", "run raw SQL against the database", (*console).sql},
		"quit":     {"", "leave the console", nil},
	}
}

// run executes one command line and reports whether the console should close.
func (c *console) run(line string) (quit bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return false
	}
	name, rest, _ := strings.Cut(line, " ")
	name = strings.ToLower(name)
	rest = strings.TrimSpace(rest)
	switch name {
	case "quit", "exit", "logout":
		return true
	case "help", "?":
		c.help()
		return false
	}
	cmd, ok := adminCmds[name]
	if !ok {
		fmt.Fprintf(c.out, "Unknown command %q. Type \"help\".\n", name)
		return false
	}
	log.Info("admin command", "who", c.who, "cmd", line)
	if err := cmd.run(c, strings.Fields(rest), rest); err != nil {
		fmt.Fprintf(c.out, "Error: %v\n", err)
	}
	return false
}

func (c *console) help() {
	w := tabwriter.NewWriter(c.out, 0, 0, 2, ' ', 0)
	for _, n := range adminOrder {
		cmd := adminCmds[n]
		fmt.Fprintf(w, "  %s %s\t%s\n", n, cmd.args, cmd.help)
	}
	w.Flush()
}

func usageErr(name string) error {
	return fmt.Errorf("usage: %s %s", name, adminCmds[name].args)
}

func (c *console) needStore() error {
	if c.store == nil {
		return fmt.Errorf("the leaderboard is disabled on this server (-db \"\")")
	}
	return nil
}

func (c *console) status(_ []string, _ string) error {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	games := c.lobby.List()
	humans := 0
	for _, g := range games {
		humans += g.Humans
	}
	host, _ := os.Hostname()
	fmt.Fprintf(c.out, "host       %s (%s, %s)\n", host, runtime.Version(), runtime.GOARCH)
	fmt.Fprintf(c.out, "uptime     %s (since %s)\n", time.Since(started).Round(time.Second), started.Format(time.RFC3339))
	fmt.Fprintf(c.out, "memory     %.1f MB in use, %.1f MB from the OS\n", float64(m.Alloc)/1e6, float64(m.Sys)/1e6)
	fmt.Fprintf(c.out, "goroutines %d\n", runtime.NumGoroutine())
	fmt.Fprintf(c.out, "online     %d signed in, %d in games\n", c.lobby.Online(), humans)
	fmt.Fprintf(c.out, "games      %d of %d\n", len(games), maxGames)
	if c.store != nil {
		n, size, err := c.store.Info()
		if err != nil {
			return err
		}
		fmt.Fprintf(c.out, "database   %d players, %.1f KB\n", n, float64(size)/1e3)
	} else {
		fmt.Fprintln(c.out, "database   disabled")
	}
	return nil
}

func (c *console) showLogs(args []string, _ string) error {
	n := 30
	if len(args) > 0 {
		var err error
		if n, err = strconv.Atoi(args[0]); err != nil || n < 1 {
			return usageErr("logs")
		}
	}
	for _, l := range c.logs.Last(n) {
		fmt.Fprintln(c.out, l)
	}
	return nil
}

func (c *console) games(_ []string, _ string) error {
	w := tabwriter.NewWriter(c.out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tHOST\tSIZE\tHUMANS\tBOTS\tPHASE\tROUND")
	for _, g := range c.lobby.List() {
		name := g.Name
		if g.Permanent {
			name += " (permanent)"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d/%d\t%d\t%s\t%d\n", g.ID, name, g.Host, g.Size, g.Humans, g.MaxPlayers, g.Bots, phaseName(g.Phase), g.Round)
	}
	return w.Flush()
}

func (c *console) game(rest string, cmd string) (*Game, error) {
	if rest == "" {
		return nil, usageErr(cmd)
	}
	g := c.lobby.Find(rest)
	if g == nil {
		return nil, fmt.Errorf("no game %q (see \"games\")", rest)
	}
	return g, nil
}

func (c *console) show(_ []string, rest string) error {
	g, err := c.game(rest, "show")
	if err != nil {
		return err
	}
	s := g.Snapshot()
	fmt.Fprintf(c.out, "%s [%s] %dx%d, %s, round %d\n", s.Settings.Name, g.ID, s.W, s.H, phaseName(s.Phase), s.Round)
	w := tabwriter.NewWriter(c.out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tKIND\tALIVE\tWINS\tKILLS\tREADY")
	for _, p := range s.Players {
		kind := "human"
		if p.Bot {
			kind = "bot"
		}
		fmt.Fprintf(w, "%s\t%s\t%v\t%d\t%d\t%v\n", p.Name, kind, p.Alive, p.Wins, p.Kills, p.Ready)
	}
	return w.Flush()
}

func (c *console) drop(_ []string, rest string) error {
	g, err := c.game(rest, "drop")
	if err != nil {
		return err
	}
	if fresh := c.lobby.Drop(g); fresh != nil {
		fmt.Fprintf(c.out, "Dropped %s; restarted it as %s.\n", g.Settings.Name, fresh.ID)
	} else {
		fmt.Fprintf(c.out, "Dropped %s.\n", g.Settings.Name)
	}
	return nil
}

func (c *console) announce(_ []string, rest string) error {
	if rest == "" {
		return usageErr("announce")
	}
	n := 0
	for _, info := range c.lobby.List() {
		if g := c.lobby.Find(info.ID); g != nil {
			g.Announce("[admin] " + rest)
			n++
		}
	}
	fmt.Fprintf(c.out, "Posted in %d games.\n", n)
	return nil
}

func (c *console) listOnline(_ []string, _ string) error {
	where := map[string]string{}
	for _, info := range c.lobby.List() {
		if g := c.lobby.Find(info.ID); g != nil {
			for _, p := range g.Snapshot().Players {
				if !p.Bot {
					where[strings.ToLower(p.Name)] = info.Name
				}
			}
		}
	}
	names := c.lobby.OnlineNames()
	w := tabwriter.NewWriter(c.out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tIN GAME")
	for _, n := range names {
		g := where[n]
		if g == "" {
			g = "-"
		}
		fmt.Fprintf(w, "%s\t%s\n", n, g)
	}
	fmt.Fprintf(w, "(%d online)\n", len(names))
	return w.Flush()
}

func (c *console) kick(args []string, _ string) error {
	if len(args) == 0 {
		return usageErr("kick")
	}
	reason := "no reason given"
	if len(args) > 1 {
		reason = strings.Join(args[1:], " ")
	}
	if !c.lobby.Kick(args[0], reason) {
		return fmt.Errorf("%s is not online", args[0])
	}
	fmt.Fprintf(c.out, "Kicked %s.\n", args[0])
	return nil
}

func (c *console) players(_ []string, rest string) error {
	if err := c.needStore(); err != nil {
		return err
	}
	rows, err := c.store.Players(rest, 50)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(c.out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tWINS\tKILLS\tDEATHS\tCASES\tROUNDS\tLAST SEEN\tKEY")
	for _, p := range rows {
		key := p.Key
		if key == "" {
			key = "(none)"
		} else if len(key) > 20 {
			key = key[:20] + "…"
		}
		fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%d\t%d\t%s\t%s\n", p.Name, p.Wins, p.Kills, p.Deaths, p.Cases, p.Rounds, p.LastSeen, key)
	}
	fmt.Fprintf(w, "(%d shown, max 50)\n", len(rows))
	return w.Flush()
}

func (c *console) top(args []string, _ string) error {
	if err := c.needStore(); err != nil {
		return err
	}
	n := 10
	if len(args) > 0 {
		var err error
		if n, err = strconv.Atoi(args[0]); err != nil || n < 1 {
			return usageErr("top")
		}
	}
	stats, err := c.store.Top(n)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(c.out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "#\tNAME\tWINS\tKILLS\tDEATHS\tCASES\tROUNDS")
	for _, s := range stats {
		fmt.Fprintf(w, "%d\t%s\t%d\t%d\t%d\t%d\t%d\n", s.Rank, s.Name, s.Wins, s.Kills, s.Deaths, s.Cases, s.Rounds)
	}
	return w.Flush()
}

func (c *console) set(args []string, _ string) error {
	if err := c.needStore(); err != nil {
		return err
	}
	if len(args) != 3 {
		return usageErr("set")
	}
	n, err := strconv.Atoi(args[2])
	if err != nil || n < 0 {
		return usageErr("set")
	}
	if err := c.store.SetStat(args[0], strings.ToLower(args[1]), n); err != nil {
		return err
	}
	fmt.Fprintf(c.out, "Set %s's %s to %d.\n", args[0], strings.ToLower(args[1]), n)
	return nil
}

// onePlayer runs a store change that takes just a player name.
func (c *console) onePlayer(cmd string, args []string, f func(string) error, done string) error {
	if err := c.needStore(); err != nil {
		return err
	}
	if len(args) != 1 {
		return usageErr(cmd)
	}
	if err := f(args[0]); err != nil {
		return err
	}
	fmt.Fprintf(c.out, done+"\n", args[0])
	return nil
}

func (c *console) reset(args []string, _ string) error {
	return c.onePlayer("reset", args, c.store.ResetStats, "Reset %s's stats.")
}

func (c *console) unbind(args []string, _ string) error {
	return c.onePlayer("unbind", args, c.store.Unbind, "%s is no longer tied to a key; the next key to sign in with it claims it.")
}

func (c *console) deletePlayer(args []string, _ string) error {
	return c.onePlayer("delete", args, c.store.Delete, "Deleted %s.")
}

func (c *console) wipe(args []string, _ string) error {
	if err := c.needStore(); err != nil {
		return err
	}
	if len(args) != 1 || args[0] != "yes" {
		return fmt.Errorf("this zeroes every player's stats; type \"wipe yes\" to do it")
	}
	n, err := c.store.WipeStats()
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "Wiped stats for %d players.\n", n)
	return nil
}

func (c *console) sql(_ []string, rest string) error {
	if err := c.needStore(); err != nil {
		return err
	}
	if rest == "" {
		return usageErr("sql")
	}
	const limit = 200
	cols, cells, changed, err := c.store.SQL(rest, limit)
	if err != nil {
		return err
	}
	if cols == nil {
		fmt.Fprintf(c.out, "OK, %d rows changed.\n", changed)
		return nil
	}
	w := tabwriter.NewWriter(c.out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, strings.ToUpper(strings.Join(cols, "\t")))
	for _, r := range cells {
		fmt.Fprintln(w, strings.Join(r, "\t"))
	}
	if len(cells) == limit {
		fmt.Fprintf(w, "(stopped at %d rows)\n", limit)
	} else {
		fmt.Fprintf(w, "(%d rows)\n", len(cells))
	}
	return w.Flush()
}

func phaseName(p Phase) string {
	switch p {
	case Waiting:
		return "waiting"
	case Ready:
		return "ready check"
	case Countdown:
		return "countdown"
	case Playing:
		return "playing"
	case RoundOver:
		return "round over"
	}
	return "?"
}

// logRing keeps the last lines the server logged, for the "logs" command.
type logRing struct {
	mu    sync.Mutex
	lines []string
	part  []byte
}

const logRingLen = 500

func (r *logRing) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.part = append(r.part, p...)
	for {
		i := bytes.IndexByte(r.part, '\n')
		if i < 0 {
			break
		}
		r.lines = append(r.lines, string(r.part[:i]))
		r.part = r.part[i+1:]
	}
	if len(r.lines) > logRingLen {
		r.lines = append([]string(nil), r.lines[len(r.lines)-logRingLen:]...)
	}
	return len(p), nil
}

func (r *logRing) Last(n int) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.lines[max(len(r.lines)-n, 0):]...)
}

// captureLogs tees the server log into a ring buffer the console can show.
func captureLogs() *logRing {
	r := &logRing{}
	log.SetOutput(io.MultiWriter(os.Stderr, r))
	return r
}
