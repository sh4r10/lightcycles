package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/charmbracelet/keygen"
	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
	"golang.org/x/term"
)

// play is a small built-in SSH client, so players don't need OpenSSH or an
// SSH key of their own: it keeps a key in the user's config directory (which
// ties their leaderboard name to this machine) and trusts a server's host
// key on first use.
func play(args []string) {
	fs := flag.NewFlagSet("play", flag.ExitOnError)
	name := fs.String("name", os.Getenv("USER"), "name to suggest on the sign-in screen")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: lightcycles play [flags] HOST[:PORT]   (port defaults to 2222)")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		fs.Usage()
		os.Exit(2)
	}
	addr := fs.Arg(0)
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr = net.JoinHostPort(addr, "2222")
	}
	if err := runClient(addr, *name); err != nil {
		fmt.Fprintln(os.Stderr, "lightcycles:", err)
		os.Exit(1)
	}
}

func configDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "lightcycles")
	return dir, os.MkdirAll(dir, 0o700)
}

func runClient(addr, user string) error {
	dir, err := configDir()
	if err != nil {
		return err
	}
	kp, err := keygen.New(filepath.Join(dir, "id_ed25519"), keygen.WithKeyType(keygen.Ed25519), keygen.WithWrite())
	if err != nil {
		return fmt.Errorf("loading your player key: %w", err)
	}
	hostKeys, err := trustOnFirstUse(filepath.Join(dir, "known_hosts"))
	if err != nil {
		return err
	}
	if user == "" {
		user = "player"
	}
	client, err := gossh.Dial("tcp", addr, &gossh.ClientConfig{
		User:            user,
		Auth:            []gossh.AuthMethod{gossh.PublicKeys(kp.Signer())},
		HostKeyCallback: hostKeys,
		Timeout:         10 * time.Second,
	})
	if err != nil {
		return fmt.Errorf("connecting to %s: %w", addr, err)
	}
	defer client.Close()

	sess, err := client.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()

	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return errors.New("play needs to run in a terminal")
	}
	w, h, err := term.GetSize(fd)
	if err != nil {
		w, h = 80, 24
	}
	termType := os.Getenv("TERM")
	if termType == "" {
		termType = "xterm-256color"
	}
	if err := sess.RequestPty(termType, h, w, gossh.TerminalModes{gossh.ECHO: 0}); err != nil {
		return err
	}
	old, err := term.MakeRaw(fd)
	if err != nil {
		return err
	}
	defer term.Restore(fd, old)

	sess.Stdout, sess.Stderr = os.Stdout, os.Stderr
	stdin, err := sess.StdinPipe()
	if err != nil {
		return err
	}
	go func() { _, _ = io.Copy(stdin, os.Stdin) }()
	if err := sess.Shell(); err != nil {
		return err
	}

	// Poll for resizes: portable, unlike SIGWINCH.
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(250 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				if nw, nh, err := term.GetSize(fd); err == nil && (nw != w || nh != h) {
					w, h = nw, nh
					_ = sess.WindowChange(h, w)
				}
			}
		}
	}()
	err = sess.Wait()
	close(done)
	var exitErr *gossh.ExitMissingError
	if errors.As(err, &exitErr) {
		return nil
	}
	return err
}

// trustOnFirstUse remembers each server's host key the first time we see it
// and refuses to connect if it later changes.
func trustOnFirstUse(path string) (gossh.HostKeyCallback, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDONLY, 0o600)
	if err != nil {
		return nil, err
	}
	f.Close()
	known, err := knownhosts.New(path)
	if err != nil {
		return nil, err
	}
	return func(hostname string, remote net.Addr, key gossh.PublicKey) error {
		err := known(hostname, remote, key)
		var ke *knownhosts.KeyError
		if errors.As(err, &ke) && len(ke.Want) == 0 {
			f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
			if err != nil {
				return err
			}
			defer f.Close()
			_, err = fmt.Fprintln(f, knownhosts.Line([]string{knownhosts.Normalize(hostname)}, key))
			return err
		}
		if errors.As(err, &ke) {
			return fmt.Errorf("%s's host key has changed since you last played; if that's expected, remove its line from %s", hostname, path)
		}
		return err
	}, nil
}
