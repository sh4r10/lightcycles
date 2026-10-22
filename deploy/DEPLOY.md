# Deploying Light Cycles

The server runs as a [ko](https://ko.build)-built container under rootless
Podman on a Debian/Ubuntu VM. GitHub Actions builds and signs a new image on
every push to `main`. The VM checks for one every 5 minutes, verifies the
signature, and swaps it in. If the new container fails to start, the VM
rolls back to the old one.

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
| Host login | SSH with keys only. No root login, no passwords, 3 tries per connection. fail2ban bans an IP for an hour after 5 failures. |
| Host updates | `unattended-upgrades` installs security updates daily. |
| Network | `ufw` denies everything incoming except SSH (rate-limited) and the game port 2222. Other ports stay open only if you pass `--allow`. |
| Game process | Runs in a container owned by a separate `lightcycles` user that has no login shell and no sudo. Inside the container it runs as uid 65532 with every capability dropped, a read-only filesystem, no privilege escalation, and limits of 512 MB memory, 1 CPU and 256 processes. The image is distroless: no shell, no package manager, just the binary. |
| Game access | The game's SSH server only runs the game, never a shell or other commands. With `-allow-keys` (on by default here), only the public keys in `~lightcycles/players` can connect. Idle connections drop after 30 minutes. |
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
sudo ./lightcycles-deploy/setup.sh            # add --allow 443 etc. for other services
```

The script asks before enabling the firewall and lists anything else
listening that would be blocked. It also asks for a GitHub token with only
the `read:packages` scope so it can pull the private image.

**Before you close that SSH session**, open a second terminal and check that
you can still log in. Root login and passwords are off from that point on.

Also add a Linode Cloud Firewall (or your provider's equivalent) with the
same two inbound ports. It's a second layer that sits outside the VM.

### 3. Players

With the allowlist on, add each player's public key (`~/.ssh/id_ed25519.pub`)
to the file, one per line. It's re-read on every login, so you don't need
to restart:

```sh
sudo nano ~lightcycles/players
```

To let anyone in instead, delete `-allow-keys /config/players` from
`lightcycles.container`, re-run `setup.sh`, and restart the game.

## Day to day

```sh
lcctl podman logs -f lightcycles                     # logs
lcctl systemctl --user start podman-auto-update      # deploy the latest image now
lcctl systemctl --user restart lightcycles           # restart
lcctl systemctl --user start lightcycles-backup      # back up now
lcctl podman auto-update --dry-run                   # is a new image waiting?
```

A deploy restarts the server, so anyone mid-round gets dropped and can
rejoin.

## Backups

Every night at 04:30, the leaderboard is copied with SQLite's `VACUUM INTO`
to `/data/backups` in the container volume, and the last 14 copies are kept.
That protects against a bad deploy or a corrupted database, but not against
losing the VM, so also turn on your provider's backups (Linode Backups). To
copy a backup off the box:

```sh
lcctl podman cp lightcycles:/data/backups/ /tmp/lc-backups && sudo cp -r /tmp/lc-backups ~/
```

The volume also holds the game's SSH host key (`/data/host_ed25519`), so
players don't get "host key changed" warnings across deploys. Keep it in
your backups if you ever move servers.

## Moving hosts

Everything is in this folder plus the `lightcycles-data` volume. On a new VM,
run `setup.sh`, then restore the volume with `podman volume export` /
`podman volume import`.
