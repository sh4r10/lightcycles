# Bundled sounds

WAVs dropped here are compiled into the `lightcycles` binary when you
build it (`go build`). Name them after a sound: `fahhh`, `fahhh-meme`,
`ky`, `beep`, `go`, `kill`, `win`, `case`, `harasmus` (e.g.
`fahhh-meme.wav`).

A binary with a bundled clip plays it in its own client and, when used
as the server, sends it to every built-in client that connects.
`fahhh-meme.wav` is the FAHHH meme sound effect.
