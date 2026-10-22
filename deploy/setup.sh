#!/usr/bin/env bash
# Sets up a Debian/Ubuntu VM to run Light Cycles with rootless Podman and
# hardens the host. Safe to run again, and written for a VM that already runs
# other things: it adds to the firewall instead of resetting it, and asks
# before turning the firewall on.
#
# Run it on the server from a copy of this folder, as your admin user:
#
#   sudo ./setup.sh [--ssh-port PORT] [--admin USER] [--allow PORT]... [--yes]
#
#   --ssh-port N   port to move admin SSH to, freeing 22 for the game (default 2200)
#   --repo REPO    image repository CI pushes to (default docker.io/iamsh4r10/lightcycles)
#   --admin USER   the account you log in with (default: whoever ran sudo)
#   --allow PORT   another TCP port to keep open (e.g. --allow 443)
#   --yes          don't ask before enabling the firewall
#
# See DEPLOY.md for what it does and why.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
admin=${SUDO_USER:-}
ssh_port=2200
repo=docker.io/iamsh4r10/lightcycles
extra_ports=()
assume_yes=false
while [ $# -gt 0 ]; do
	case $1 in
	--ssh-port) ssh_port=$2; shift 2 ;;
	--repo) repo=$2; shift 2 ;;
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
. /etc/os-release
case "$ID" in
ubuntu) printf '24.04\n%s\n' "$VERSION_ID" | sort -V -C || die "Ubuntu $VERSION_ID ships Podman 3.4, which has no Quadlet; rebuild the VM with Ubuntu 24.04 or newer" ;;
debian) [ "${VERSION_ID%%.*}" -ge 13 ] 2>/dev/null || die "Debian $VERSION_ID's Podman has no Quadlet; use Debian 13 or newer" ;;
esac
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

say "Moving admin SSH to port $ssh_port (keys only, no root login)"
[[ $ssh_port =~ ^[0-9]+$ ]] && [[ ! " 22 2222 2301 2302 " == *" $ssh_port "* ]] ||
	die "--ssh-port must be a number other than 22, 2222, 2301 and 2302 (the game uses those)"
grep -Eq '^\s*Include\s+/etc/ssh/sshd_config.d/' /etc/ssh/sshd_config ||
	die "/etc/ssh/sshd_config doesn't include sshd_config.d/; add 'Include /etc/ssh/sshd_config.d/*.conf' at the top"
# Open the new port before sshd moves to it, so a running firewall can't
# lock you out.
ufw limit "$ssh_port/tcp" comment ssh >/dev/null
dropin=/etc/ssh/sshd_config.d/00-lightcycles-hardening.conf
{ cat "$here/sshd-hardening.conf"; printf 'Port %s\n' "$ssh_port"; } >"$dropin"
chmod 644 "$dropin"
undo_ssh() { rm -f "$dropin"; die "$1; SSH config left as it was"; }
sshd -t || undo_ssh "sshd rejected the config"
[ "$(sshd -T | awk '$1 == "port" {print $2}')" = "$ssh_port" ] ||
	undo_ssh "sshd would still listen on other ports; comment out any 'Port' line in /etc/ssh/sshd_config"
# Ubuntu 24.04 starts sshd from ssh.socket, which keeps listening on 22
# whatever sshd_config says. Switch to the plain service so the port applies.
if systemctl is-enabled ssh.socket >/dev/null 2>&1; then
	systemctl disable --now ssh.socket >/dev/null 2>&1
	systemctl enable ssh.service >/dev/null 2>&1
fi
systemctl restart ssh 2>/dev/null || systemctl restart sshd
sleep 1
if ! ss -Htln "sport = :$ssh_port" | grep -q .; then
	rm -f "$dropin"
	systemctl restart ssh 2>/dev/null || systemctl restart sshd
	die "sshd didn't come up on port $ssh_port; moved it back"
fi

say "Setting up fail2ban for SSH"
sed "s/^port = .*/port = $ssh_port/" "$here/fail2ban-sshd.local" >/etc/fail2ban/jail.d/lightcycles-sshd.local
systemctl enable fail2ban >/dev/null
systemctl restart fail2ban

say "Configuring the firewall (game on 22, admin SSH on $ssh_port)"
# Public ports 22 and 2222 are NAT-redirected (by lightcycles-update) to the
# live colour's port, and the firewall sees the redirected port. Rootless
# podman can't bind ports below 1024, and a redirect is narrower than letting
# every user bind them.
ufw allow 2301:2302/tcp comment lightcycles >/dev/null
for p in "${extra_ports[@]}"; do ufw allow "$p/tcp" >/dev/null; done
if ufw status | grep -q '^Status: active'; then
	ufw reload >/dev/null
else
	# Anything else listening publicly would be cut off, so list it first.
	keep=" $ssh_port 22 2222 2301 2302 ${extra_ports[*]} "
	others=$(ss -Htln | awk '{print $4}' | grep -Ev '^(127\.|\[::1\]|\[?::ffff:127\.)' |
		sed 's/.*://' | sort -un | while read -r p; do [[ $keep == *" $p "* ]] || echo "$p"; done)
	if [ -n "$others" ]; then
		echo "These ports are listening but will be blocked:" $others
		echo "Re-run with --allow PORT for any that should stay open."
	fi
	if confirm "Enable the firewall now (incoming: SSH $ssh_port, game 22/2222${extra_ports:+, ${extra_ports[*]}})?"; then
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
	"$cfg/containers/registries.d" "$cfg/systemd" "$cfg/systemd/user"
inst() { install -m 644 -o lightcycles -g lightcycles "$here/$1" "$2"; }
# Files with @REPO@ (and the unit's @COLOR@/@PORT@) get filled in.
render() {
	sed "s|@REPO@|$repo|g; s|@COLOR@|${3:-}|g; s|@PORT@|${4:-}|g" "$here/$1" >"$2"
	chown lightcycles:lightcycles "$2"
	chmod 644 "$2"
}
render lightcycles.container "$cfg/containers/systemd/lightcycles-blue.container" blue 2301
render lightcycles.container "$cfg/containers/systemd/lightcycles-green.container" green 2302
render policy.json "$cfg/containers/policy.json"
render registries.yaml "$cfg/containers/registries.d/lightcycles.yaml"
inst cosign.pub "$cfg/containers/cosign.pub"
inst lightcycles-backup.service "$cfg/systemd/user/lightcycles-backup.service"
inst lightcycles-backup.timer "$cfg/systemd/user/lightcycles-backup.timer"
install -m 755 "$here/lightcycles-update" /usr/local/sbin/lightcycles-update
install -m 644 "$here/lightcycles-update.service" "$here/lightcycles-update.timer" /etc/systemd/system/
if [ ! -e "$lc_home/players" ]; then
	printf '# Public SSH keys of players allowed in, one per line (authorized_keys format).\n' >"$lc_home/players"
	chown lightcycles:lightcycles "$lc_home/players"
	chmod 644 "$lc_home/players"
fi

if [ ! -e "$lc_home/lightcycles.env" ]; then
	read -r -s -p "Game password players join with (empty for none): " game_pw
	echo
	read -r -s -p "Admin console password for ssh admin@host (empty disables it): " admin_pw
	echo
	# The file is read by podman, not a shell, so no quoting is needed.
	printf 'LIGHTCYCLES_JOIN_PASSWORD=%s\nLIGHTCYCLES_ADMIN_PASSWORD=%s\n' "$game_pw" "$admin_pw" >"$lc_home/lightcycles.env"
	chown lightcycles:lightcycles "$lc_home/lightcycles.env"
	chmod 600 "$lc_home/lightcycles.env"
fi

say "Logging in to ${repo%%/*}"
if [ ! -s "$cfg/containers/auth.json" ]; then
	# Needed for a private repo, and on Docker Hub it also lifts the
	# anonymous pull limit.
	echo "Create a Docker Hub access token with Read-only access"
	echo "(https://app.docker.com/settings/personal-access-tokens) and paste it here."
	read -r -p "Docker Hub username: " reg_user
	read -r -s -p "Token: " reg_token
	echo
	printf '%s' "$reg_token" | as_lc podman login "${repo%%/*}" -u "$reg_user" --password-stdin \
		--authfile "$cfg/containers/auth.json"
	chmod 600 "$cfg/containers/auth.json"
fi

say "Starting the game"
as_lc systemctl --user daemon-reload
systemctl daemon-reload
/usr/local/sbin/lightcycles-update
systemctl enable --now lightcycles-update.timer
as_lc systemctl --user enable --now lightcycles-backup.timer

cat <<EOF

Done. Light Cycles is on port 22 (and 2222). Admin SSH is now on $ssh_port.

  Add players:       sudo nano $lc_home/players   (one public key per line)
  Logs:              sudo journalctl -f CONTAINER_NAME=lightcycles-blue + CONTAINER_NAME=lightcycles-green
  Which is live:     sudo lightcycles-update status
  Deploy right now:  sudo systemctl start lightcycles-update
  Backup right now:  lcctl systemctl --user start lightcycles-backup

Before you log out, open a NEW terminal and check you can still log in:
  ssh -p $ssh_port $admin@<this server>
EOF
