# Light Cycles

A Tron-style multiplayer arena for the terminal. Players join over SSH with
nothing to install, or with the built-in client.

## Quick start

You need [Go 1.24+](https://go.dev/dl/).

```
git clone https://github.com/sh4r10/lightcycles && cd lightcycles
go run . serve                 # SSH on :2222, leaderboard in ./lightcycles.db
```

Then, from any terminal (same machine or another one):

```
ssh -p 2222 -t localhost              # plain SSH, nothing to install
go run . play localhost               # or the built-in client
go run . play my-server.example.com
```

To install the binary instead: `go install github.com/sh4r10/lightcycles@latest`,
then `lightcycles serve` / `lightcycles play HOST`.

## The lobby

1. **Pick a name.** It's tied to your SSH key the first time you use it, so
   nobody else can play under your name. The built-in client makes a key for
   you (in your config directory, e.g. `~/.config/lightcycles/`).
2. **Join a game or host your own.** The lobby lists every running game with
   its host, arena size, players and status. Press `n` to host one: choose a
   name, an arena size, max players (2 to 16) and whether bots fill empty
   seats. Hosted games appear for everyone and end when the last player
   leaves. There's always a permanent **Public Arena**.
3. **Check the leaderboard** with `l`.

## How to play

- **Arrows / WASD / hjkl** steer. You can queue a quick U-turn (e.g. ↑ then ←).
- **Space** boosts: double speed for a moment, then a 5s cooldown.
- Hit a wall or any trail and you're out. When you crash, your trail
  vanishes and opens space for everyone else.
- Last cycle alive wins the round. Running someone into your trail is a kill.
- While you're out, **Tab** switches which player the camera follows.
- **q** goes back to the lobby.

Arenas come in Small (40x20, fits one screen), Medium (80x40) and Large
(140x70). On the bigger ones the camera follows you and a minimap shows
where everyone is. Terminals need to be at least 60x20; at 90+ columns you
get the scoreboard, minimap and feed alongside.

## Leaderboard

Wins, kills, deaths and rounds are stored in SQLite (`-db`, default
`lightcycles.db`; pass `-db ""` to run without one). Only rounds with at
least two human players count, so wins against bots don't inflate the
board.

## Server options

```
go run . serve -port 2222 -db lightcycles.db -key .ssh/lightcycles_ed25519
go test ./...
```

A host key is generated on first run. To host it publicly, run the binary on
any VPS and open the SSH port. Using port 22 means people can drop `-p`, but
move your admin sshd elsewhere first.

## Layout

- `game.go`: one arena: rounds, simultaneous move resolution, bots
- `lobby.go`: running games, hosting, who's online
- `store.go`: SQLite leaderboard and name ownership
- `app.go`: per-player screens (name, lobby, host form, leaderboard)
- `gameview.go`: in-game view with camera, minimap and scoreboard
- `client.go`: built-in SSH client (`play`)
- `main.go`: commands and the Wish SSH server
