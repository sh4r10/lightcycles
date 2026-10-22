# Bundled sounds

WAVs dropped here are compiled into the `lightcycles` binary when you
build it (`go build`). Name them after a sound: `fahhh`, `fahhh-meme`,
`ky`, `beep`, `go`, `kill`, `win`, `case`, `harasmus` (e.g.
`fahhh-meme.wav`).

They're gitignored on purpose: clips like the FAHHH meme aren't ours to
publish, so they only end up in binaries you build yourself. A binary
with a bundled clip plays it in its own client and, when used as the
server, sends it to every built-in client that connects.
