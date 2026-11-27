#!/bin/sh
set -eu

HUB_HOSTNAME=broadcast.mediateca.store
HUB_HOSTS_IP=192.168.100.250
HUB_BASE_URL=https://broadcast.mediateca.store
SSH_USER=voxel
SSH_PORT=5956
TAB=$(printf '\t')
DEFAULT_VOXEL_APK=/home/voxel/apk/voxel-debug.apk
REPO_VOXEL_APK_REL=deploy/voxel-debug.apk

validate_token() {
	token=$1
	[ -n "$token" ] || return 1
	case $token in
	*[!0123456789abcdefABCDEF]*) return 1 ;;
	esac
}

parse_inventory() {
	path=$1
	ips=""
	while IFS= read -r line || [ -n "$line" ]; do
		case $line in
		'' | '#'*) continue ;;
		esac
		case $line in
		*"$TAB"*) ;;
		*)
			echo "invalid inventory line (expected IP and token separated by tab)" >&2
			return 1
			;;
		esac
		ip=${line%%"$TAB"*}
		token=${line#*"$TAB"}
		if ! validate_token "$token"; then
			echo "invalid token for IP $ip" >&2
			return 1
		fi
		case " $ips " in
		*" $ip "*)
			echo "duplicate IP: $ip" >&2
			return 1
			;;
		esac
		ips="$ips $ip"
		printf '%s\t%s\n' "$ip" "$token"
	done <"$path"
}

render_config() {
	token=$1
	if ! validate_token "$token"; then
		echo "invalid token" >&2
		return 1
	fi
	cat <<EOF
hub_base_url: "$HUB_BASE_URL"
agent_token: "$token"
late_threshold: "2s"
poll_interval: "1m"
data_dir: "/var/lib/mediateca-station"
broadcast_bind: "192.168.33.1"
stream_backend: "builtin"
fifo_dir: "/var/lib/mediateca-station/fifo"
voxel_apk: "$DEFAULT_VOXEL_APK"
chunk_size: 18800
feeder_pace: "realtime"
EOF
}

render_hosts_file() {
	while IFS= read -r line || [ -n "$line" ]; do
		[ -z "$line" ] && continue
		set -- $line
		[ "$2" = "$HUB_HOSTNAME" ] && continue
		printf '%s\n' "$line"
	done
	printf '%s %s\n' "$HUB_HOSTS_IP" "$HUB_HOSTNAME"
}

render_unit() {
	cat <<'EOF'
[Unit]
Description=Mediateca station
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/usr/local/bin/mediateca-station -config /etc/mediateca-station/config.yaml
Restart=always
RestartSec=2
User=mediateca
SupplementaryGroups=dnsmasq-leases
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
EOF
}

# Remote setup script: packages + dirs. Does not adb-install to TVs (may be offline).
remote_setup_script() {
	cat <<'EOF'
set -eu

apt_install() {
	pkg=$1
	if command -v apt-get >/dev/null 2>&1; then
		export DEBIAN_FRONTEND=noninteractive
		apt-get install -y "$pkg"
		return
	fi
	echo "cannot install $pkg: no apt-get" >&2
	return 1
}

need_cmd() {
	cmd=$1
	pkg=$2
	if command -v "$cmd" >/dev/null 2>&1; then
		echo "ok: $cmd"
		return 0
	fi
	echo "installing $pkg (provides $cmd)"
	apt_install "$pkg"
	command -v "$cmd" >/dev/null 2>&1
}

echo "==> deps: systemctl"
if ! command -v systemctl >/dev/null 2>&1; then
	echo "systemctl not found; systemd is required" >&2
	exit 1
fi

echo "==> deps: adb"
need_cmd adb adb || need_cmd adb android-tools-adb || {
	echo "adb is required in PATH" >&2
	exit 1
}

echo "==> deps: wake-on-lan"
if command -v wakeonlan >/dev/null 2>&1 || command -v etherwake >/dev/null 2>&1; then
	echo "ok: wol utility present"
else
	apt_install wakeonlan 2>/dev/null || apt_install etherwake 2>/dev/null || true
	if command -v wakeonlan >/dev/null 2>&1 || command -v etherwake >/dev/null 2>&1; then
		echo "ok: wol utility installed"
	else
		echo "warn: neither wakeonlan nor etherwake available (WOL optional)" >&2
	fi
fi

echo "==> deps: dnsmasq"
if command -v dnsmasq >/dev/null 2>&1 || dpkg -s dnsmasq >/dev/null 2>&1; then
	echo "ok: dnsmasq package present (not rewriting site config)"
else
	apt_install dnsmasq
fi
getent group dnsmasq-leases >/dev/null || groupadd --system dnsmasq-leases
if [ -f /var/lib/misc/dnsmasq.leases ]; then
	chgrp dnsmasq-leases /var/lib/misc/dnsmasq.leases 2>/dev/null || true
	chmod 640 /var/lib/misc/dnsmasq.leases 2>/dev/null || true
fi

echo "==> deps: NTP client"
if systemctl list-unit-files chrony.service chronyd.service systemd-timesyncd.service 2>/dev/null | grep -q enabled || \
	systemctl is-active --quiet chrony chronyd systemd-timesyncd 2>/dev/null || \
	command -v chronyd >/dev/null 2>&1 || command -v chronyc >/dev/null 2>&1; then
	echo "ok: NTP client present"
else
	if apt_install chrony; then
		systemctl enable --now chrony 2>/dev/null || systemctl enable --now chronyd 2>/dev/null || true
		echo "ok: chrony installed"
	else
		echo "warn: could not install chrony; ensure an NTP client is running" >&2
	fi
fi

echo "==> user and dirs"
if ! getent passwd mediateca >/dev/null; then
	useradd --system --create-home --home-dir /var/lib/mediateca-station mediateca
fi
usermod -a -G dnsmasq-leases mediateca 2>/dev/null || true
install -d -m 0700 -o mediateca -g mediateca /var/lib/mediateca-station
install -d -m 0700 -o mediateca -g mediateca /var/lib/mediateca-station/fifo
install -d -m 0750 /etc/mediateca-station
# APK under voxel home; mediateca must traverse home and read the file (adb install).
install -d -m 0755 -o voxel -g voxel /home/voxel/apk
chmod o+x /home/voxel
EOF
}

require_repo_apk() {
	repo_root=$1
	apk_src=$repo_root/$REPO_VOXEL_APK_REL
	if [ ! -f "$apk_src" ]; then
		echo "missing $apk_src — commit Voxel APK as $REPO_VOXEL_APK_REL before install" >&2
		return 1
	fi
	printf '%s\n' "$apk_src"
}

install_one() {
	ip=$1
	token=$2
	binpath=$3
	apk_src=$4
	ssh_opts="-o BatchMode=yes -o Port=${SSH_PORT}"

	echo "==> $ip ($SSH_USER): setup"
	stage=setup
	if ! remote_setup_script | ssh $ssh_opts "${SSH_USER}@${ip}" sudo bash -s; then
		echo "failed $ip $stage" >&2
		return 1
	fi

	echo "==> $ip ($SSH_USER): binary"
	stage=binary
	if ! scp $ssh_opts "$binpath" "${SSH_USER}@${ip}:/tmp/mediateca-station.new"; then
		echo "failed $ip $stage" >&2
		return 1
	fi
	if ! ssh $ssh_opts "${SSH_USER}@${ip}" 'sudo install -m 0755 /tmp/mediateca-station.new /usr/local/bin/mediateca-station && rm -f /tmp/mediateca-station.new'; then
		echo "failed $ip $stage" >&2
		return 1
	fi

	echo "==> $ip ($SSH_USER): voxel apk"
	stage=apk
	if ! scp $ssh_opts "$apk_src" "${SSH_USER}@${ip}:/tmp/voxel-debug.apk.new"; then
		echo "failed $ip $stage" >&2
		return 1
	fi
	if ! ssh $ssh_opts "${SSH_USER}@${ip}" "sudo install -d -m 0755 -o voxel -g voxel /home/voxel/apk && sudo chmod o+x /home/voxel && sudo install -m 0644 -o voxel -g voxel /tmp/voxel-debug.apk.new $DEFAULT_VOXEL_APK && rm -f /tmp/voxel-debug.apk.new"; then
		echo "failed $ip $stage" >&2
		return 1
	fi

	echo "==> $ip ($SSH_USER): config"
	stage=config
	local_config=$(umask 077; mktemp "${TMPDIR:-/tmp}/mediateca-config.XXXXXX")
	if ! render_config "$token" >"$local_config"; then
		rm -f "$local_config"
		echo "failed $ip $stage" >&2
		return 1
	fi
	if ! scp $ssh_opts "$local_config" "${SSH_USER}@${ip}:/tmp/config.yaml.new"; then
		rm -f "$local_config"
		echo "failed $ip $stage" >&2
		return 1
	fi
	rm -f "$local_config"
	if ! ssh $ssh_opts "${SSH_USER}@${ip}" 'sudo install -m 0600 -o mediateca -g mediateca /tmp/config.yaml.new /etc/mediateca-station/config.yaml && rm -f /tmp/config.yaml.new'; then
		echo "failed $ip $stage" >&2
		return 1
	fi

	echo "==> $ip ($SSH_USER): hosts"
	stage=hosts
	local_hosts=$(mktemp "${TMPDIR:-/tmp}/mediateca-hosts-in.XXXXXX")
	local_rendered=$(mktemp "${TMPDIR:-/tmp}/mediateca-hosts-out.XXXXXX")
	if ! ssh $ssh_opts "${SSH_USER}@${ip}" 'sudo cat /etc/hosts' >"$local_hosts"; then
		rm -f "$local_hosts" "$local_rendered"
		echo "failed $ip $stage" >&2
		return 1
	fi
	if ! render_hosts_file <"$local_hosts" >"$local_rendered"; then
		rm -f "$local_hosts" "$local_rendered"
		echo "failed $ip $stage" >&2
		return 1
	fi
	rm -f "$local_hosts"
	if ! scp $ssh_opts "$local_rendered" "${SSH_USER}@${ip}:/tmp/hosts.new"; then
		rm -f "$local_rendered"
		echo "failed $ip $stage" >&2
		return 1
	fi
	rm -f "$local_rendered"
	if ! ssh $ssh_opts "${SSH_USER}@${ip}" 'tmp=$(mktemp) && sudo cp /tmp/hosts.new "$tmp" && sudo mv "$tmp" /etc/hosts && rm -f /tmp/hosts.new'; then
		echo "failed $ip $stage" >&2
		return 1
	fi

	echo "==> $ip ($SSH_USER): unit"
	stage=unit
	if ! render_unit | ssh $ssh_opts "${SSH_USER}@${ip}" 'sudo tee /etc/systemd/system/mediateca-station.service >/dev/null'; then
		echo "failed $ip $stage" >&2
		return 1
	fi

	echo "==> $ip ($SSH_USER): systemd"
	stage=systemd
	if ! ssh $ssh_opts "${SSH_USER}@${ip}" 'sudo systemctl daemon-reload && sudo systemctl enable --now mediateca-station && sudo systemctl restart mediateca-station'; then
		echo "failed $ip $stage" >&2
		return 1
	fi

	echo "installed $ip"
}

main() {
	if [ $# -lt 1 ]; then
		echo "usage: $0 <inventory.tsv>" >&2
		exit 2
	fi
	inv=$1
	if [ ! -f "$inv" ]; then
		echo "inventory file not found" >&2
		exit 1
	fi
	mode=$(stat -c %a "$inv")
	if [ "$mode" != "600" ]; then
		echo "inventory file must be mode 0600 (got $mode)" >&2
		exit 1
	fi

	stations=$(parse_inventory "$inv") || exit 1
	if [ -z "$stations" ]; then
		echo "no stations" >&2
		exit 1
	fi

	script_dir=$(CDPATH= cd -- "$(dirname "$0")" && pwd)
	repo_root=$(CDPATH= cd -- "$script_dir/.." && pwd)
	apk_src=$(require_repo_apk "$repo_root") || exit 1
	tmpbin=$(mktemp "${TMPDIR:-/tmp}/mediateca-station.XXXXXX")
	trap 'rm -f "$tmpbin"' EXIT

	if ! (cd "$repo_root" && GOOS=linux GOARCH=amd64 go build -o "$tmpbin" ./cmd/station); then
		echo "build failed" >&2
		exit 1
	fi

	failed=0
	while IFS=$TAB read -r ip token; do
		if ! install_one "$ip" "$token" "$tmpbin" "$apk_src"; then
			failed=1
		fi
	done <<EOF
$stations
EOF

	rm -f "$tmpbin"
	trap - EXIT
	exit "$failed"
}

if [ "${DEPLOY_STATIONS_LIB:-}" = 1 ]; then
	return 0 2>/dev/null || exit 0
fi

main "$@"
