package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/log"
	"github.com/charmbracelet/ssh"
	"github.com/charmbracelet/wish"
	"github.com/charmbracelet/wish/activeterm"
	"github.com/charmbracelet/wish/bubbletea"
	"github.com/charmbracelet/wish/logging"
)

const usage = `Light Cycles: a multiplayer Tron arena for the terminal.

Usage:
  lightcycles serve [flags]         run a server (SSH on :2222, client port :2223)
  lightcycles play [flags] HOST     join a server with the native client

Run "lightcycles serve -h" or "lightcycles play -h" for flags.
Without a command, lightcycles runs "serve".
`

func main() {
	args := os.Args[1:]
	cmd := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "serve":
		serve(args)
	case "play":
		play(args)
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
}

func serve(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	host := fs.String("host", "0.0.0.0", "address to listen on")
	port := fs.String("port", "2222", "SSH port")
	gamePort := fs.String("game-port", "2223", "port for the native client (empty disables it)")
	keyPath := fs.String("key", ".ssh/lightcycles_ed25519", "host key path (created if missing)")
	minCycles := fs.Int("min-cycles", 4, "fill each round with bots up to this many cycles (0 disables bots)")
	_ = fs.Parse(args)

	lobby := NewLobby(*minCycles)

	s, err := wish.NewServer(
		wish.WithAddress(net.JoinHostPort(*host, *port)),
		wish.WithHostKeyPath(*keyPath),
		wish.WithMiddleware(
			bubbletea.Middleware(func(s ssh.Session) (tea.Model, []tea.ProgramOption) {
				return handler(lobby, s)
			}),
			activeterm.Middleware(),
			logging.Middleware(),
		),
	)
	if err != nil {
		log.Fatal("could not create server", "error", err)
	}
	if *gamePort != "" {
		if err := serveTCP(lobby, net.JoinHostPort(*host, *gamePort)); err != nil {
			log.Fatal("could not listen for clients", "error", err)
		}
	}

	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGTERM)
	log.Info("Light Cycles server listening", "host", *host, "ssh", *port, "client", *gamePort)
	go func() {
		if err := s.ListenAndServe(); err != nil && !errors.Is(err, ssh.ErrServerClosed) {
			log.Error("server stopped", "error", err)
			done <- nil
		}
	}()
	<-done
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.Shutdown(ctx)
}

func play(args []string) {
	fs := flag.NewFlagSet("play", flag.ExitOnError)
	name := fs.String("name", os.Getenv("USER"), "your player name")
	room := fs.String("room", "main", "room to join")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: lightcycles play [flags] HOST[:PORT]   (port defaults to 2223)")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		fs.Usage()
		os.Exit(2)
	}
	addr := fs.Arg(0)
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr = net.JoinHostPort(addr, "2223")
	}

	g, id, err := dial(addr, *name, *room)
	if err != nil {
		fmt.Fprintln(os.Stderr, "could not join:", err)
		os.Exit(1)
	}
	r := lipgloss.NewRenderer(os.Stdout)
	final, err := tea.NewProgram(newModel(r, g, id), tea.WithAltScreen()).Run()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if m, ok := final.(model); ok && m.lost != nil {
		fmt.Fprintln(os.Stderr, "lost connection to server:", m.lost)
		os.Exit(1)
	}
}

// handler runs once per SSH connection. The SSH username is the player's
// name, and an optional command picks the room: `ssh -p 2222 -t host myroom`.
func handler(lobby *Lobby, s ssh.Session) (tea.Model, []tea.ProgramOption) {
	room := "main"
	if args := s.Command(); len(args) > 0 {
		room = cleanName(args[0], "main")
	}
	g, id, ok := lobby.Join(room, cleanName(s.User(), "player"))
	if !ok {
		wish.Fatalln(s, "That room is full (8 players). Try another: ssh -t <host> -p <port> some-room")
		return nil, nil
	}
	go func() {
		<-s.Context().Done()
		lobby.Leave(g, id)
	}()
	r := bubbletea.MakeRenderer(s)
	return newModel(r, g, id), []tea.ProgramOption{tea.WithAltScreen()}
}

func cleanName(s, fallback string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			return r
		}
		return -1
	}, s)
	if r := []rune(s); len(r) > 12 {
		s = string(r[:12])
	}
	if s == "" {
		return fallback
	}
	return s
}
