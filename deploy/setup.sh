#!/usr/bin/env bash
# Sets up a Debian/Ubuntu VM to run Light Cycles with rootless Podman and
# hardens the host. Safe to run again, and written for a VM that already runs
# other things: it adds to the firewall instead of resetting it, and asks
# before turning the firewall on.
#
# Run it on the server from a copy of this folder, as your admin user:
#
#   sudo ./setup.sh [--admin USER] [--allow PORT]... [--yes]
#
#   --admin USER   the account you log in with (default: whoever ran sudo)
#   --allow PORT   another TCP port to keep open (e.g. --allow 443)
#   --yes          don't ask before enabling the firewall
#
# See DEPLOY.md for what it does and why.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
admin=${SUDO_USER:-}
game_port=2222
extra_ports=()
assume_yes=false
while [ $# -gt 0 ]; do
	case $1 in
	--admin) admin=$2; shift 2 ;;
	--allow) extra_ports+=("$2"); shift 2 ;;
	--yes) assume_yes=true; shift ;;
	*) echo "unknown option: $1" >&2; exit 2 ;;
	esac
done

say() { printf '\n\033[1m==> %s\033[0m\n' "$*"; }
die() { printf '\033[31merror:\033[0m %s\n' "$*" >&2; exit 1; }
confirm() {
	$assume_yes && return 0
	read -r -p "$1 [y/N] " a
	[[ $a == [yY]* ]]
}

[ "$(id -u)" = 0 ] || die "run this with sudo"
command -v apt-get >/dev/null || die "this script supports Debian and Ubuntu only"
[ -s "$here/cosign.pub" ] || die "deploy/cosign.pub is missing; see 'Signing key' in DEPLOY.md"

say "Installing packages"
export DEBIAN_FRONTEND=noninteractive
apt-get update -q
apt-get install -y -q podman uidmap slirp4netns passt fail2ban ufw unattended-upgrades
pv=$(podman version --format '{{.Client.Version}}')
if ! printf '4.4.0\n%s\n' "$pv" | sort -V -C; then
	die "podman $pv is too old for Quadlet (needs 4.4+); use Ubuntu 24.04+ or Debian 13+"
fi

say "Turning on automatic security updates"
cat >/etc/apt/apt.conf.d/20auto-upgrades <<'EOF'
APT::Periodic::Update-Package-Lists "1";
APT::Periodic::Unattended-Upgrade "1";
EOF
systemctl enable --now unattended-upgrades >/dev/null

say "Checking the admin account"
# Root login is about to be switched off, so make sure there's another way in.
if [ -z "$admin" ] || [ "$admin" = root ]; then
	read -r -p "Name for a new admin account (you'll log in as this instead of root): " admin
	[ -n "$admin" ] && [ "$admin" != root ] || die "need a non-root admin account"
fi
if ! id "$admin" >/dev/null 2>&1; then
	[ -s /root/.ssh/authorized_keys ] || die "root has no SSH keys to copy to $admin"
	useradd -m -s /bin/bash -G sudo "$admin"
	install -d -m 700 -o "$admin" -g "$admin" "/home/$admin/.ssh"
	install -m 600 -o "$admin" -g "$admin" /root/.ssh/authorized_keys "/home/$admin/.ssh/authorized_keys"
	echo "Created $admin with root's SSH keys. Pick a password for sudo:"
	passwd "$admin"
fi
admin_home=$(getent passwd "$admin" | cut -d: -f6)
[ -s "$admin_home/.ssh/authorized_keys" ] || die "$admin has no SSH keys in $admin_home/.ssh/authorized_keys; add one first"
id -nG "$admin" | grep -qw sudo || die "$admin is not in the sudo group"

say "Hardening SSH (keys only, no root login)"
grep -Eq '^\s*Include\s+/etc/ssh/sshd_config.d/' /etc/ssh/sshd_config ||
	die "/etc/ssh/sshd_config doesn't include sshd_config.d/; add 'Include /etc/ssh/sshd_config.d/*.conf' at the top"
install -m 644 "$here/sshd-hardening.conf" /etc/ssh/sshd_config.d/00-lightcycles-hardening.conf
sshd -t || { rm -f /etc/ssh/sshd_config.d/00-lightcycles-hardening.conf; die "sshd rejected the config; nothing changed"; }
systemctl reload-or-restart ssh 2>/dev/null || systemctl reload-or-restart sshd
mapfile -t ssh_ports < <(sshd -T | awk '$1 == "port" {print $2}')

say "Setting up fail2ban for SSH"
install -m 644 "$here/fail2ban-sshd.local" /etc/fail2ban/jail.d/lightcycles-sshd.local
systemctl enable fail2ban >/dev/null
systemctl restart fail2ban

say "Configuring the firewall"
for p in "${ssh_ports[@]}"; do ufw limit "$p/tcp" comment ssh >/dev/null; done
ufw allow "$game_port/tcp" comment lightcycles >/dev/null
for p in "${extra_ports[@]}"; do ufw allow "$p/tcp" >/dev/null; done
if ! ufw status | grep -q '^Status: active'; then
	# Anything else listening publicly would be cut off, so list it first.
	keep=" ${ssh_ports[*]} $game_port ${extra_ports[*]} "
	others=$(ss -Htln | awk '{print $4}' | grep -Ev '^(127\.|\[::1\]|\[?::ffff:127\.)' |
		sed 's/.*://' | sort -un | while read -r p; do [[ $keep == *" $p "* ]] || echo "$p"; done)
	if [ -n "$others" ]; then
		echo "These ports are listening but will be blocked:" $others
		echo "Re-run with --allow PORT for any that should stay open."
	fi
	if confirm "Enable the firewall now (incoming: SSH ${ssh_ports[*]}, game $game_port${extra_ports:+, ${extra_ports[*]}})?"; then
		ufw default deny incoming >/dev/null
		ufw default allow outgoing >/dev/null
		ufw --force enable
	else
		echo "Skipped. Run 'sudo ufw enable' when you're ready."
	fi
fi
if command -v docker >/dev/null; then
	echo "Note: Docker is installed. Ports published by Docker containers bypass ufw."
fi

say "Creating the rootless lightcycles user"
if ! id lightcycles >/dev/null 2>&1; then
	useradd -m -s /usr/sbin/nologin lightcycles
	passwd -l lightcycles >/dev/null
fi
if ! grep -q '^lightcycles:' /etc/subuid; then
	start=$(awk -F: '{e = $2 + $3; if (e > m) m = e} END {print (m > 100000 ? m : 100000)}' /etc/subuid /etc/subgid)
	usermod --add-subuids "$start-$((start + 65535))" --add-subgids "$start-$((start + 65535))" lightcycles
fi
lc_uid=$(id -u lightcycles)
lc_home=$(getent passwd lightcycles | cut -d: -f6)
# Let rootless containers have memory, CPU and pid limits.
install -d /etc/systemd/system/user@.service.d
printf '[Service]\nDelegate=cpu cpuset io memory pids\n' >/etc/systemd/system/user@.service.d/delegate.conf
systemctl daemon-reload
loginctl enable-linger lightcycles
systemctl start "user@$lc_uid.service"

say "Installing the game's units and image policy"
install -m 755 "$here/lcctl" /usr/local/bin/lcctl
as_lc() { /usr/local/bin/lcctl "$@"; }
cfg=$lc_home/.config
install -d -o lightcycles -g lightcycles "$cfg" "$cfg/containers" "$cfg/containers/systemd" \
	"$cfg/containers/registries.d" "$cfg/systemd" "$cfg/systemd/user" \
	"$cfg/systemd/user/podman-auto-update.timer.d"
inst() { install -m 644 -o lightcycles -g lightcycles "$here/$1" "$2"; }
inst lightcycles.container "$cfg/containers/systemd/lightcycles.container"
inst policy.json "$cfg/containers/policy.json"
inst registries-ghcr.yaml "$cfg/containers/registries.d/ghcr.yaml"
inst cosign.pub "$cfg/containers/cosign.pub"
inst lightcycles-backup.service "$cfg/systemd/user/lightcycles-backup.service"
inst lightcycles-backup.timer "$cfg/systemd/user/lightcycles-backup.timer"
inst auto-update-override.conf "$cfg/systemd/user/podman-auto-update.timer.d/override.conf"
if [ ! -e "$lc_home/players" ]; then
	printf '# Public SSH keys of players allowed in, one per line (authorized_keys format).\n' >"$lc_home/players"
	chown lightcycles:lightcycles "$lc_home/players"
	chmod 644 "$lc_home/players"
fi

say "Logging in to GitHub's container registry"
if [ ! -s "$cfg/containers/auth.json" ]; then
	echo "The image is private. Create a classic GitHub token with only the read:packages scope"
	echo "(https://github.com/settings/tokens/new?scopes=read:packages) and paste it here."
	read -r -p "GitHub username: " gh_user
	read -r -s -p "Token: " gh_token
	echo
	printf '%s' "$gh_token" | as_lc podman login ghcr.io -u "$gh_user" --password-stdin \
		--authfile "$cfg/containers/auth.json"
	chmod 600 "$cfg/containers/auth.json"
fi

say "Starting the game"
as_lc systemctl --user daemon-reload
as_lc systemctl --user restart lightcycles.service
as_lc systemctl --user enable --now podman-auto-update.timer lightcycles-backup.timer

cat <<EOF

Done. Light Cycles is listening on port $game_port.

  Add players:       sudo nano $lc_home/players   (one public key per line)
  Logs:              lcctl podman logs -f lightcycles
  Deploy right now:  lcctl systemctl --user start podman-auto-update
  Backup right now:  lcctl systemctl --user start lightcycles-backup

Before you log out, open a NEW terminal and check you can still log in:
  ssh $admin@<this server>
EOF
