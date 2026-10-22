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
   leaves. There's always a permanent **Team 7 HQ** arena.
3. **Check the leaderboard** with `l`.

## How to play

- **Arrows / WASD / hjkl** steer. You can queue a quick U-turn (e.g. ↑ then ←).
- **Space** boosts: double speed for a moment, then a 5s cooldown.
- Hit a wall or any trail and you're out. When you crash, your trail
  vanishes and opens space for everyone else.
- Before each round everyone gets a ready check: press **Enter** to ready up.
  The round starts once every human is ready, or after 30 seconds.
- Last cycle alive wins the round. Running someone into your trail is a kill.
- While you're out, **Tab** switches which player the camera follows.
- **q** goes back to the lobby.

Arenas come in Small (40x20, fits one screen), Medium (80x40) and Large
(140x70). On the bigger ones the camera follows you and a minimap shows
where everyone is. Terminals need to be at least 60x20; at 90+ columns you
get the scoreboard, minimap and feed alongside.

## Operations Team 7 edition

- **HARASMUS** is in every game with bots, and he hunts the nearest human.
  You'll see "HARASMUS is right behind you!" when he's close. Getting
  killed by him means you got harassed by HARASMUS.
- **mitdk**, shsh's baby, rides at half speed. Make mitdk crash and shsh
  will hear about it.
- **bot-gpt, bot-copilot and bot-claude** won't stop talking about AI in the
  feed. Kafe is thrilled every time one of them crashes (and even more so if
  Kafe did it).
- **The case**: each round one human gets a new case. *WHY DID YOU ASSIGN
  ME THIS CASE.* Their head turns gold. Kill the holder and the case is
  reassigned to you. Win the round while holding it to close the case,
  which counts in the leaderboard's CASES column.
- **KY toolkit**: every so often a blue **KY** box appears on the map. Drive
  over it and everyone else has to open KY toolkit: for four seconds their
  steering lags well behind their keys while a progress bar crawls from 1%
  to 3% to… 99%. Lagged bots mostly just ride straight.
- Crash on your own (wall, your own trail) and you get a mild *fahhh*.
  Get taken out by someone else and you get the full meme **FAHHHHHHHH!!!**

### Sound

Plain `ssh` players get a terminal bell when they crash or win. The
built-in client (`lightcycles play`) plays real sounds: countdown beeps, a
mild fahhh for solo crashes, an overdriven meme FAHHH when someone gets you, kill blips, a win jingle, an office phone when you're
assigned the case, and a siren when HARASMUS is close. Press `m` in game to
mute, or pass `-mute`.

**Bundled sounds.** Drop WAVs named after a sound into `soundpack/`
(for example the real FAHHH meme as `soundpack/fahhh-meme.wav`) and run
`go build`: they're compiled into the binary, played by its client, and
sent by its server to every built-in client. The real FAHHH meme
(`soundpack/fahhh-meme.wav`) ships this way, so `go install` builds play it
when someone else takes you out.

**Server sounds.** Whoever runs the server can put WAVs named after the
sounds (for example the real FAHHH meme clip as `fahhh-meme.wav`) in a
`sounds/` folder next to it (`-sounds` to change). Every built-in client
downloads them when it connects and plays them instead of the built-in
ones, so the whole team hears the same clip without it living in this
repo. Only WAVs up to 4MB named after a known sound are sent; to convert
an mp3, run `ffmpeg -i clip.mp3 fahhh-meme.wav`.

The client uses your OS's own player (`afplay` on macOS, `paplay`/`pw-play`/
`aplay` on Linux, PowerShell on Windows). To replace a sound just for yourself, drop a WAV named after it (`fahhh`,
`fahhh-meme`, `ky`, `beep`, `go`, `kill`, `win`, `case`, `harasmus`) into
`~/.config/lightcycles/sounds/` (or your OS's config directory).

## Leaderboard

Wins, kills, deaths, closed cases and rounds are stored in SQLite (`-db`, default
`lightcycles.db`; pass `-db ""` to run without one). Only rounds with at
least two human players count, so wins against bots don't inflate the
board.

## Server options

```
go run . serve -port 2222 -db lightcycles.db -key .ssh/lightcycles_ed25519
go test ./...
```

A host key is generated on first run. `-allow-keys FILE` limits the game to
the public keys in an authorized_keys-style file (re-read on every login),
and `lightcycles backup -db lightcycles.db -dir backups` writes a consistent
copy of the leaderboard while the server is running.

To host it, see [deploy/DEPLOY.md](deploy/DEPLOY.md). It covers a hardened VM
running the ko-built image under rootless Podman, which updates itself from
signed CI builds.

## Layout

- `game.go`: one arena: rounds, simultaneous move resolution, bots
- `lobby.go`: running games, hosting, who's online
- `store.go`: SQLite leaderboard and name ownership
- `app.go`: per-player screens (name, lobby, host form, leaderboard)
- `gameview.go`: in-game view with camera, minimap and scoreboard
- `client.go`: built-in SSH client (`play`)
- `flavor.go`: Team 7 bot personas and feed lines
- `sound.go`: sound cues, synthesised sounds and the client's audio player
- `main.go`: commands and the Wish SSH server
- `deploy/`: container unit, host hardening script and deploy guide
