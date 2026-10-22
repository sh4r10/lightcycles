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
	"github.com/charmbracelet/log"
	"github.com/charmbracelet/ssh"
	"github.com/charmbracelet/wish"
	"github.com/charmbracelet/wish/activeterm"
	"github.com/charmbracelet/wish/bubbletea"
	"github.com/charmbracelet/wish/logging"
	gossh "golang.org/x/crypto/ssh"
)

const usage = `Light Cycles: a multiplayer Tron arena for the terminal.

Usage:
  lightcycles serve [flags]         run a server (SSH on :2222)
  lightcycles play [flags] HOST     join a server with the built-in client

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
	keyPath := fs.String("key", ".ssh/lightcycles_ed25519", "host key path (created if missing)")
	dbPath := fs.String("db", "lightcycles.db", "SQLite leaderboard file (empty disables the leaderboard)")
	_ = fs.Parse(args)

	var store *Store
	if *dbPath != "" {
		var err error
		if store, err = OpenStore(*dbPath); err != nil {
			log.Fatal("could not open leaderboard database", "path", *dbPath, "error", err)
		}
		defer store.Close()
	}
	lobby := NewLobby(store)

	s, err := wish.NewServer(
		wish.WithAddress(net.JoinHostPort(*host, *port)),
		wish.WithHostKeyPath(*keyPath),
		// Anyone may play. We accept any key so we can tie names to it, and
		// fall back to keyboard-interactive (no prompts) for keyless clients.
		wish.WithPublicKeyAuth(func(ssh.Context, ssh.PublicKey) bool { return true }),
		wish.WithKeyboardInteractiveAuth(func(ssh.Context, gossh.KeyboardInteractiveChallenge) bool { return true }),
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

	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGTERM)
	log.Info("Light Cycles server listening", "host", *host, "port", *port, "db", *dbPath)
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

// handler runs once per SSH connection and starts the player at the name
// screen, prefilled with their SSH username.
func handler(lobby *Lobby, s ssh.Session) (tea.Model, []tea.ProgramOption) {
	var keyFP string
	if k := s.PublicKey(); k != nil {
		keyFP = gossh.FingerprintSHA256(k)
	}
	sess := &session{}
	go func() {
		<-s.Context().Done()
		sess.close(lobby)
	}()
	r := bubbletea.MakeRenderer(s)
	return newApp(r, lobby, sess, s.User(), keyFP), []tea.ProgramOption{tea.WithAltScreen()}
}

func cleanName(s, fallback string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			return r
		}
		return -1
	}, s)
	if r := []rune(s); len(r) > 16 {
		s = string(r[:16])
	}
	if s == "" {
		return fallback
	}
	return s
}
