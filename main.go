package main

import (
	"context"
	"crypto/subtle"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
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
  lightcycles backup [flags]        copy the leaderboard database

Run "lightcycles <command> -h" for flags.
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
	case "backup":
		backup(args)
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
	soundsDir := fs.String("sounds", "sounds", "folder of custom WAVs (e.g. fahhh-meme.wav) sent to built-in clients")
	adminPassword := fs.String("admin-password", os.Getenv("LIGHTCYCLES_ADMIN_PASSWORD"), "password for the admin console (ssh admin@host); empty disables it. Defaults to $LIGHTCYCLES_ADMIN_PASSWORD")
	allowKeys := fs.String("allow-keys", "", "authorized_keys file of players allowed in (empty lets anyone play); re-read on every login")
	password := fs.String("password", os.Getenv("LIGHTCYCLES_PASSWORD"), "password players can join with (default $LIGHTCYCLES_PASSWORD; empty means none)")
	drain := fs.Duration("drain", 30*time.Minute, "on SIGTERM, keep serving connected players this long before closing (new connections are refused)")
	idle := fs.Duration("idle-timeout", 30*time.Minute, "drop connections with no traffic for this long (0 disables)")
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
	logs := captureLogs()
	if *adminPassword == "" {
		log.Info("admin console disabled (set LIGHTCYCLES_ADMIN_PASSWORD to enable it)")
	} else if len(*adminPassword) < 12 {
		log.Warn("the admin password is short; anyone who guesses it can wipe the leaderboard")
	}

	s, err := wish.NewServer(
		wish.WithAddress(net.JoinHostPort(*host, *port)),
		wish.WithHostKeyPath(*keyPath),
		wish.WithIdleTimeout(*idle),
		authOption(*allowKeys, *password),
		wish.WithMiddleware(
			bubbletea.Middleware(func(s ssh.Session) (tea.Model, []tea.ProgramOption) {
				return handler(lobby, s)
			}),
			activeterm.Middleware(),
			adminMiddleware(newAdmin(lobby, store, *adminPassword, logs)),
			soundPackMiddleware(*soundsDir),
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
	// A newer server takes new connections from here (see deploy/), so stop
	// listening but let everyone connected finish, up to the drain limit.
	log.Info("draining: refusing new connections", "max", *drain)
	lobby.Drain()
	go func() {
		// A second ctrl+c (or SIGTERM) skips the drain.
		<-done
		_ = s.Close()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), *drain)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		log.Info("drain limit reached, closing remaining connections")
		_ = s.Close()
	}
}

// authOption decides who may connect. With no allowlist and no password
// anyone may play: we accept any key so we can tie names to it, and fall back
// to keyboard-interactive (no prompts) for keyless clients. Otherwise a
// player gets in with a key from the allowlist or with the password; keys
// that aren't listed are refused so the client moves on to the password.
func authOption(allowKeys, password string) ssh.Option {
	if allowKeys == "" && password == "" {
		return func(s *ssh.Server) error {
			s.PublicKeyHandler = func(ssh.Context, ssh.PublicKey) bool { return true }
			s.KeyboardInteractiveHandler = func(ssh.Context, gossh.KeyboardInteractiveChallenge) bool { return true }
			return nil
		}
	}
	if allowKeys != "" {
		if _, err := os.Stat(allowKeys); err != nil {
			log.Fatal("could not read the player allowlist", "path", allowKeys, "error", err)
		}
	}
	return func(s *ssh.Server) error {
		s.PublicKeyHandler = func(_ ssh.Context, key ssh.PublicKey) bool {
			if allowKeys == "" {
				return false
			}
			ok, err := keyAllowed(allowKeys, key)
			if err != nil {
				log.Error("could not read the player allowlist", "path", allowKeys, "error", err)
			}
			return ok
		}
		if password != "" {
			s.PasswordHandler = func(_ ssh.Context, given string) bool {
				return subtle.ConstantTimeCompare([]byte(given), []byte(password)) == 1
			}
		}
		return nil
	}
}

// keyAllowed reports whether key is listed in an authorized_keys file. The
// file is read on every login so players can be added without a restart.
func keyAllowed(path string, key ssh.PublicKey) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	for len(data) > 0 {
		var allowed gossh.PublicKey
		allowed, _, _, data, err = gossh.ParseAuthorizedKey(data)
		if err != nil {
			// No more keys (blank lines and comments are skipped).
			return false, nil
		}
		if ssh.KeysEqual(allowed, key) {
			return true, nil
		}
	}
	return false, nil
}

func backup(args []string) {
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	dbPath := fs.String("db", "lightcycles.db", "SQLite leaderboard file")
	dir := fs.String("dir", "backups", "folder to write the copy into")
	keep := fs.Int("keep", 14, "number of backups to keep in the folder (0 keeps all)")
	_ = fs.Parse(args)

	store, err := OpenStore(*dbPath)
	if err != nil {
		log.Fatal("could not open leaderboard database", "path", *dbPath, "error", err)
	}
	defer store.Close()
	if err := os.MkdirAll(*dir, 0o700); err != nil {
		log.Fatal("could not create backup folder", "error", err)
	}
	dest := filepath.Join(*dir, "lightcycles-"+time.Now().UTC().Format("20060102-150405")+".db")
	if err := store.Backup(dest); err != nil {
		log.Fatal("backup failed", "error", err)
	}
	log.Info("backup written", "path", dest)

	if *keep > 0 {
		// Names sort by time, so everything before the newest keep is old.
		old, _ := filepath.Glob(filepath.Join(*dir, "lightcycles-*.db"))
		sort.Strings(old)
		for len(old) > *keep {
			if err := os.Remove(old[0]); err != nil {
				log.Error("could not remove old backup", "path", old[0], "error", err)
			}
			old = old[1:]
		}
	}
}

// soundPackMiddleware answers `ssh host sounds` (no PTY) with the server's
// custom sounds, which the built-in client fetches before it starts.
func soundPackMiddleware(dir string) wish.Middleware {
	return func(next ssh.Handler) ssh.Handler {
		return func(s ssh.Session) {
			if cmd := s.Command(); len(cmd) == 1 && cmd[0] == "sounds" {
				if err := writeSoundPack(s, dir); err != nil {
					log.Error("sending sounds failed", "error", err)
				}
				_ = s.Exit(0)
				return
			}
			next(s)
		}
	}
}

// handler runs once per SSH connection and starts the player at the name
// screen, prefilled with their SSH username.
func handler(lobby *Lobby, s ssh.Session) (tea.Model, []tea.ProgramOption) {
	var keyFP string
	if k := s.PublicKey(); k != nil {
		keyFP = gossh.FingerprintSHA256(k)
	}
	sess := &session{out: s, kick: func(reason string) {
		// Leave the alt screen so the message stays visible.
		fmt.Fprintf(s, "\x1b[?1049l\r\nYou were kicked by an admin: %s\r\n", reason)
		_ = s.Close()
	}}
	for _, e := range s.Environ() {
		if e == "LIGHTCYCLES_CLIENT=1" {
			sess.richSound = true
		}
	}
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
