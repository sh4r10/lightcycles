package main

import (
	"crypto/rand"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

const maxGames = 50

// Lobby tracks every running game and who is online. Hosted games are torn
// down when their last human leaves; the permanent public arena never is.
type Lobby struct {
	mu     sync.Mutex
	games  map[string]*Game
	online map[string]bool // lower-cased names of signed-in players
	store  *Store
}

func NewLobby(store *Store) *Lobby {
	l := &Lobby{games: map[string]*Game{}, online: map[string]bool{}, store: store}
	_, _ = l.Create(Settings{Name: "Public Arena", Host: "server", Size: 1, MaxPlayers: 12, Bots: true, Permanent: true})
	return l
}

// GameInfo is one row of the lobby's game list.
type GameInfo struct {
	ID         string
	Name       string
	Host       string
	Size       string
	Humans     int
	Bots       int
	MaxPlayers int
	Phase      Phase
	Round      int
	Permanent  bool
}

func (l *Lobby) List() []GameInfo {
	l.mu.Lock()
	games := make([]*Game, 0, len(l.games))
	for _, g := range l.games {
		games = append(games, g)
	}
	l.mu.Unlock()

	var out []GameInfo
	for _, g := range games {
		s := g.Snapshot()
		info := GameInfo{
			ID: g.ID, Name: s.Settings.Name, Host: s.Settings.Host, Size: sizes[s.Settings.Size].Name,
			MaxPlayers: s.Settings.MaxPlayers, Phase: s.Phase, Round: s.Round, Permanent: s.Settings.Permanent,
		}
		for _, p := range s.Players {
			if p.Bot {
				info.Bots++
			} else {
				info.Humans++
			}
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Permanent != out[j].Permanent {
			return out[i].Permanent
		}
		if out[i].Humans != out[j].Humans {
			return out[i].Humans > out[j].Humans
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func (l *Lobby) Online() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.online)
}

var (
	ErrTooManyGames = errors.New("the server is running too many games, try joining one")
	ErrGameName     = errors.New("a game with that name is already running")
	ErrGameGone     = errors.New("that game has ended")
)

func (l *Lobby) Create(s Settings) (*Game, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.games) >= maxGames {
		return nil, ErrTooManyGames
	}
	for _, g := range l.games {
		if strings.EqualFold(g.Settings.Name, s.Name) {
			return nil, ErrGameName
		}
	}
	s.MaxPlayers = min(max(s.MaxPlayers, 2), maxCycles)
	id := newID()
	g := NewGame(id, s, l.record)
	l.games[id] = g
	g.Start()
	return g, nil
}

func (l *Lobby) record(stats []RoundStat) {
	if l.store != nil {
		l.store.Record(stats)
	}
}

func (l *Lobby) Join(id, name string) (*Game, int, error) {
	l.mu.Lock()
	g, ok := l.games[id]
	l.mu.Unlock()
	if !ok {
		return nil, 0, ErrGameGone
	}
	pid, err := g.Join(name)
	return g, pid, err
}

func (l *Lobby) Leave(g *Game, pid int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	g.Leave(pid)
	if !g.Settings.Permanent && g.Humans() == 0 {
		g.Stop()
		delete(l.games, g.ID)
	}
}

var ErrNameOnline = errors.New("someone with that name is already playing")

// SignIn reserves a name for this connection. The store decides whether the
// name belongs to someone else's SSH key.
func (l *Lobby) SignIn(name, keyFP string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	k := strings.ToLower(name)
	if l.online[k] {
		return ErrNameOnline
	}
	if l.store != nil {
		if err := l.store.Claim(name, keyFP); err != nil {
			return err
		}
	}
	l.online[k] = true
	return nil
}

func (l *Lobby) SignOut(name string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.online, strings.ToLower(name))
}

func newID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)
}
