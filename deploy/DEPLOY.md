# Deploying Light Cycles

The server runs as a [ko](https://ko.build)-built container under rootless
Podman on a Debian/Ubuntu VM. GitHub Actions builds and signs a new image on
every push to `main`. Every 5 minutes, the VM checks for one, verifies the
signature, and hands over to it without dropping anyone who's playing (see
[Updates](#updates-without-dropping-players)).

```
push to main ──► Actions: test → ko build → push to ghcr.io → cosign sign
                                                     │
VM (rootless podman) ◄── pull every 5 min, reject unsigned ◄┘
```

CI never connects to the VM, so the VM holds no deploy keys and GitHub
holds no SSH keys for it.

## What's hardened

| Layer | Protection |
|---|---|
| Host login | Admin SSH moves to its own port (2200 by default) so the game can have 22. Keys only, no root login, no passwords, 3 tries per connection. fail2ban bans an IP for an hour after 5 failures. |
| Host updates | `unattended-upgrades` installs security updates daily. |
| Network | `ufw` denies all incoming traffic except admin SSH (rate-limited) and the game. Public ports 22 and 2222 are forwarded to the live game container (host port 2301 or 2302), so `ssh host` lands in the game. Other ports stay open only if you pass `--allow`. |
| Game process | Runs in a container owned by a separate `lightcycles` user that has no login shell and no sudo. Inside the container it runs as uid 65532 with every capability dropped, a read-only filesystem, no privilege escalation, and limits of 512 MB memory, 1 CPU and 256 processes. The image is distroless: no shell, no package manager, just the binary. |
| Game access | The game's SSH server only runs the game, never a shell or other commands. Players get in either with a key listed in `~lightcycles/players` or with the game password, which setup.sh asks for and stores in `~lightcycles/lightcycles.env` (mode 600, not in the repo). Players who use the password don't get their name locked to a key. Idle connections drop after 30 minutes. |
| Supply chain | The VM only accepts `ghcr.io/sh4r10/lightcycles` images signed with this repo's cosign key, and rejects images from anywhere else. |
| Secrets | Nothing secret is in the repo. The cosign private key lives in GitHub secrets. The registry token on the VM is read-only (`read:packages`) and stored mode 600 in the `lightcycles` user's home. |

So a stranger with the address can't get a shell on the box. A bug in the
game would leave an attacker inside a locked-down container as an
unprivileged user with access only to the game's data.

## One-time setup

### 1. Signing key (on your laptop)

Install [cosign](https://docs.sigstore.dev/cosign/system_config/installation/) v2, then:

```sh
export GITHUB_TOKEN=<a token that can write repo secrets>
cosign generate-key-pair github://sh4r10/lightcycles
mv cosign.pub deploy/cosign.pub && git add deploy/cosign.pub && git commit -m "Add image signing key" && git push
```

This stores `COSIGN_PRIVATE_KEY` and `COSIGN_PASSWORD` as repo secrets and
gives you the public key to commit. The push to `main` builds and signs the
first image. Check that the **CI** workflow's `image` job is green.

### 2. The VM

The VM needs Ubuntu 24.04+ or Debian 13+, because Quadlet needs Podman 4.4 or
newer. Copy this folder over and run the script as your admin user, not root:

```sh
scp -r deploy you@your-vm:lightcycles-deploy
ssh you@your-vm
sudo ./lightcycles-deploy/setup.sh --ssh-port 2200   # add --allow 443 etc. for other services
```

The script opens the new admin port in the firewall first, then moves sshd
to it and checks that sshd is listening there. If it isn't, it moves sshd
back. Only after that does the game take over 22.

The script asks before enabling the firewall and lists anything else
listening that would be blocked. It also asks for a GitHub token with only
the `read:packages` scope so it can pull the private image.

**Before you close that SSH session**, open a second terminal and check that
you can still log in with `ssh -p 2200 you@your-vm`. Root login and
passwords are off from that point on. If you do get locked out, Linode's
Lish console still works.

If you use a Linode Cloud Firewall (a good second layer outside the VM),
allow inbound TCP 22, 2222, 2301–2302 and 2200 **before** running the
script.
Otherwise it will block the new admin port.

### 3. Players

Players join with `ssh yourname@your-vm` and enter the game password when
asked. The built-in client (`lightcycles play your-vm`)
prompts too, or reads `$LIGHTCYCLES_PASSWORD`.

To skip the password and lock their name to their key, add a player's
public key (`~/.ssh/id_ed25519.pub`) to the allowlist, one per line. It's
re-read on every login, so you don't need to restart:

```sh
sudo nano ~lightcycles/players
```

To change the password, edit `~lightcycles/lightcycles.env`. It takes
effect at the next deploy. Leaving the password empty and dropping
`-allow-keys` opens the game to anyone.

## Day to day

```sh
sudo lightcycles-update status                       # which colour is live, which is draining
sudo systemctl start lightcycles-update              # deploy the latest image now
sudo journalctl -u lightcycles-update                # what the updater did
sudo journalctl -f CONTAINER_NAME=lightcycles-blue + CONTAINER_NAME=lightcycles-green   # game logs
lcctl systemctl --user start lightcycles-backup      # back up now
```

## Updates without dropping players

Games live in the server's memory, so a connected player can't be moved to
a new version. Instead, nobody gets cut off:

1. Two copies of the game unit exist, `lightcycles-blue` (port 2301) and
   `lightcycles-green` (port 2302). One is live. A NAT rule sends new
   connections on 22 and 2222 to it.
2. When a new signed image appears, `lightcycles-update` starts the idle
   colour on it and waits until it's listening. Then it flips the NAT rule.
   From that moment, everyone who connects gets the new version.
3. Connections that already exist keep going to the old server, because the
   kernel's connection tracking remembers where each one was sent. The old
   server is then stopped, which makes it drain: it stops accepting
   connections, tells its players "Update ready! Reconnect whenever you like
   to get it", and keeps their games running. It exits when the last one
   leaves, or after 30 minutes (`-drain`).

Trade-offs:

- For a while there are two lobbies. Players still on the old version don't
  see rooms created on the new one, and vice versa. The leaderboard is
  shared, since both use the same SQLite file in WAL mode.
- A player who stays connected for more than 30 minutes after an update is
  disconnected and has to rejoin.
- If another update lands while the old version is still draining, it waits
  for the next 5-minute check after the drain ends.
- If the new version fails to start listening within a minute, it's stopped
  and the current one stays live. A version that starts but misbehaves
  needs a fix pushed to `main`, and that rolls forward the same way.

## Backups

Every night at 04:30, the leaderboard is copied with SQLite's `VACUUM INTO`
to `/data/backups` in the container volume, and the last 14 copies are kept.
That protects against a bad deploy or a corrupted database, but not against
losing the VM, so also turn on your provider's backups (Linode Backups). To
copy a backup off the box:

```sh
lcctl podman cp "lightcycles-$(cat /var/lib/lightcycles-deploy/active)":/data/backups/ /tmp/lc-backups && sudo cp -r /tmp/lc-backups ~/
```

The volume also holds the game's SSH host key (`/data/host_ed25519`), so
players don't get "host key changed" warnings across deploys. Keep it in
your backups if you ever move servers.

## Moving hosts

Everything is in this folder plus the `lightcycles-data` volume. On a new VM,
run `setup.sh`, then restore the volume with `podman volume export` /
`podman volume import`.
