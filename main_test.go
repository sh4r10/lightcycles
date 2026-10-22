package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
