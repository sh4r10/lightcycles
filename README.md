# Light Cycles

A Tron-style multiplayer arena for the terminal. Play it over SSH with
nothing to install, or with the native client.

## Quick start

You need [Go 1.24+](https://go.dev/dl/).

```
git clone https://github.com/sh4r10/lightcycles && cd lightcycles
go run . serve                  # SSH on :2222, native client on :2223
```

Then, from any terminal (same machine or another one):

```
ssh -p 2222 -t yourname@localhost           # over SSH, nothing to install
go run . play localhost                     # or the native client
go run . play -name zed -room friends my-server.example.com
```

To install the binary instead: `go install github.com/sh4r10/lightcycles@latest`,
then `lightcycles serve` / `lightcycles play HOST`.

Over SSH your username is your player name, and an extra argument picks the
room (`ssh -p 2222 -t you@host friends`). The client uses `-name` and `-room`.
SSH players and client players share the same rooms. Up to 8 cycles per
room; empty seats are filled with bots (`-min-cycles`, default 4) so a solo
player always has someone to beat.

## How to play

- **Arrows / WASD / hjkl** steer. You can queue a quick U-turn (e.g. ↑ then ←).
- **Space** boosts: double speed for a moment, then a 5s cooldown.
- Hit a wall or any trail and you're out. When you crash, your trail
  vanishes and opens space for everyone else.
- Last cycle alive wins the round. Running someone into your trail is a kill.
- Rounds restart automatically; the scoreboard ranks by wins, then kills.

Needs an 80x24 terminal; at 110+ columns you get a side scoreboard and feed.

## Server options

```
go run . serve -port 2222 -game-port 2223 -min-cycles 0   # humans only
go test ./...
```

A host key is generated at `.ssh/lightcycles_ed25519` on first run. To
host it publicly, run the binary on any VPS and open ports 2222 and 2223. Using port
22 means people can drop `-p`, but move your admin sshd elsewhere first.

## Layout

- `game.go`: arena state, simultaneous move resolution, bots, rooms
- `ui.go`: Bubble Tea view rendered per SSH session
- `main.go`: `serve` and `play` commands; Wish SSH server, one Bubble Tea program per connection
- `net.go`: native client protocol (gob over TCP: the server streams snapshots, the client sends inputs)
