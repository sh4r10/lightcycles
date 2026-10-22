package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/ssh"
	"github.com/charmbracelet/wish"
	gossh "golang.org/x/crypto/ssh"
)

func newKey(t *testing.T) gossh.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	k, err := gossh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestKeyAllowed(t *testing.T) {
	alice, bob, eve := newKey(t), newKey(t), newKey(t)
	path := filepath.Join(t.TempDir(), "players")
	file := "# Team 7\n\n" + string(gossh.MarshalAuthorizedKey(alice)) +
		"\n" + strings.TrimSpace(string(gossh.MarshalAuthorizedKey(bob))) + " bob@laptop\n"
	if err := os.WriteFile(path, []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		key  gossh.PublicKey
		want bool
	}{"alice": {alice, true}, "bob": {bob, true}, "eve": {eve, false}} {
		got, err := keyAllowed(path, c.key)
		if err != nil || got != c.want {
			t.Errorf("%s: keyAllowed = %v, %v; want %v", name, got, err, c.want)
		}
	}
}

func TestBackup(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(filepath.Join(dir, "game.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Claim("shsh", "fp"); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "copy.db")
	if err := s.Backup(dest); err != nil {
		t.Fatal(err)
	}
	c, err := OpenStore(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Claim("shsh", "other"); err != ErrNameClaimed {
		t.Fatalf("backup lost the claimed name: %v", err)
	}
}

func TestAuthOption(t *testing.T) {
	listed, unlisted := newSigner(t), newSigner(t)
	allow := filepath.Join(t.TempDir(), "players")
	if err := os.WriteFile(allow, gossh.MarshalAuthorizedKey(listed.PublicKey()), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := wish.NewServer(
		wish.WithAddress("127.0.0.1:0"),
		wish.WithHostKeyPath(filepath.Join(t.TempDir(), "host")),
		authOption(allow, "Netcompany123"),
		wish.WithMiddleware(func(ssh.Handler) ssh.Handler {
			return func(s ssh.Session) { _ = s.Exit(0) }
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go s.Serve(ln)
	defer s.Close()

	for name, c := range map[string]struct {
		auth []gossh.AuthMethod
		want bool
	}{
		"listed key":           {[]gossh.AuthMethod{gossh.PublicKeys(listed)}, true},
		"unlisted key":         {[]gossh.AuthMethod{gossh.PublicKeys(unlisted)}, false},
		"right password":       {[]gossh.AuthMethod{gossh.Password("Netcompany123")}, true},
		"wrong password":       {[]gossh.AuthMethod{gossh.Password("hunter2")}, false},
		"unlisted key + pass":  {[]gossh.AuthMethod{gossh.PublicKeys(unlisted), gossh.Password("Netcompany123")}, true},
		"keyboard-interactive": {[]gossh.AuthMethod{gossh.KeyboardInteractive(func(string, string, []string, []bool) ([]string, error) { return nil, nil })}, false},
	} {
		conn, err := gossh.Dial("tcp", ln.Addr().String(), &gossh.ClientConfig{
			User: "player", Auth: c.auth, HostKeyCallback: gossh.InsecureIgnoreHostKey(),
		})
		if got := err == nil; got != c.want {
			t.Errorf("%s: connected = %v (%v), want %v", name, got, err, c.want)
		}
		if err == nil {
			conn.Close()
		}
	}
}

func newSigner(t *testing.T) gossh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := gossh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
