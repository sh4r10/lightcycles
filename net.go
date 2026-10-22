package main

import (
	"bufio"
	"encoding/gob"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/charmbracelet/log"
)

// The native client speaks a small gob protocol over TCP:
//
//	client → server: Hello, then any number of Input
//	server → client: Welcome, then a Snapshot every tick
//
// The client renders locally with the same view the SSH server uses.

const protocolVersion = 1

type Hello struct {
	Version int
	Name    string
	Room    string
}

type Welcome struct {
	ID  int
	Err string
}

type Input struct {
	Turn  bool
	Dir   Dir
	Boost bool
}

func serveTCP(lobby *Lobby, addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				log.Error("game listener stopped", "error", err)
				return
			}
			go handleTCP(lobby, conn)
		}
	}()
	return nil
}

func handleTCP(lobby *Lobby, conn net.Conn) {
	defer conn.Close()
	w := bufio.NewWriter(conn)
	enc, dec := gob.NewEncoder(w), gob.NewDecoder(bufio.NewReader(conn))
	reply := func(v any) error {
		if err := enc.Encode(v); err != nil {
			return err
		}
		return w.Flush()
	}

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var hello Hello
	if err := dec.Decode(&hello); err != nil {
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	if hello.Version != protocolVersion {
		_ = reply(Welcome{Err: fmt.Sprintf("server speaks protocol v%d, your client v%d: please update", protocolVersion, hello.Version)})
		return
	}
	g, id, ok := lobby.Join(cleanName(hello.Room, "main"), cleanName(hello.Name, "player"))
	if !ok {
		_ = reply(Welcome{Err: "that room is full (8 players), try another with -room"})
		return
	}
	defer lobby.Leave(g, id)
	log.Info("client connected", "addr", conn.RemoteAddr(), "name", hello.Name, "room", hello.Room)
	if err := reply(Welcome{ID: id}); err != nil {
		return
	}

	go func() {
		for {
			var in Input
			if err := dec.Decode(&in); err != nil {
				conn.Close()
				return
			}
			switch {
			case in.Turn:
				g.Turn(id, in.Dir)
			case in.Boost:
				g.Boost(id)
			}
		}
	}()

	t := time.NewTicker(tickRate)
	defer t.Stop()
	for range t.C {
		_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if err := reply(g.Snapshot()); err != nil {
			return
		}
	}
}

// remoteGame is the client's mirror of a game running on the server.
type remoteGame struct {
	mu   sync.Mutex
	snap Snapshot
	enc  *gob.Encoder
	w    *bufio.Writer
	err  error
}

func dial(addr, name, room string) (*remoteGame, int, error) {
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return nil, 0, err
	}
	w := bufio.NewWriter(conn)
	r := &remoteGame{enc: gob.NewEncoder(w), w: w}
	dec := gob.NewDecoder(bufio.NewReader(conn))
	if err := r.send(Hello{Version: protocolVersion, Name: name, Room: room}); err != nil {
		conn.Close()
		return nil, 0, err
	}
	var wel Welcome
	if err := dec.Decode(&wel); err != nil {
		conn.Close()
		return nil, 0, fmt.Errorf("handshake failed: %w", err)
	}
	if wel.Err != "" {
		conn.Close()
		return nil, 0, fmt.Errorf("%s", wel.Err)
	}
	go func() {
		defer conn.Close()
		for {
			var s Snapshot
			if err := dec.Decode(&s); err != nil {
				r.mu.Lock()
				r.err = err
				r.mu.Unlock()
				return
			}
			r.mu.Lock()
			r.snap = s
			r.mu.Unlock()
		}
	}()
	return r, wel.ID, nil
}

func (r *remoteGame) send(v any) error {
	if err := r.enc.Encode(v); err != nil {
		return err
	}
	return r.w.Flush()
}

func (r *remoteGame) Snapshot() Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snap
}

func (r *remoteGame) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

func (r *remoteGame) Turn(_ int, d Dir) { r.input(Input{Turn: true, Dir: d}) }
func (r *remoteGame) Boost(_ int)       { r.input(Input{Boost: true}) }

func (r *remoteGame) input(in Input) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err == nil {
		if err := r.send(in); err != nil {
			r.err = err
		}
	}
}
