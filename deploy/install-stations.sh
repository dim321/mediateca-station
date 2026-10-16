#!/bin/sh
set -eu

HUB_HOSTNAME=broadcast.mediateca.store
HUB_HOSTS_IP=192.168.100.250
HUB_BASE_URL=https://broadcast.mediateca.store
TAB=$(printf '\t')

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
broadcast_bin: "/home/stream/bin/broadcast"
fifo_dir: "/tmp"
voxel_apk: "/home/stream/apk/voxel-debug.apk"
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

install_one() {
	ip=$1
	token=$2
	binpath=$3
	ssh_user=${SSH_USER:-$USER}
	ssh_opts="-o BatchMode=yes"

	stage=setup
	if ! ssh $ssh_opts "${ssh_user}@${ip}" sudo bash -s <<'EOF' >/dev/null 2>&1
set -eu
getent group dnsmasq-leases >/dev/null || groupadd --system dnsmasq-leases
if ! getent passwd mediateca >/dev/null; then
	useradd --system --create-home --home-dir /var/lib/mediateca-station mediateca
fi
usermod -a -G dnsmasq-leases mediateca 2>/dev/null || true
install -d -m 0700 -o mediateca -g mediateca /var/lib/mediateca-station
install -d -m 0750 /etc/mediateca-station
EOF
	then
		echo "failed $ip $stage"
		return 1
	fi

	stage=binary
	if ! scp $ssh_opts "$binpath" "${ssh_user}@${ip}:/tmp/mediateca-station.new" >/dev/null 2>&1; then
		echo "failed $ip $stage"
		return 1
	fi
	if ! ssh $ssh_opts "${ssh_user}@${ip}" 'sudo install -m 0755 /tmp/mediateca-station.new /usr/local/bin/mediateca-station && rm -f /tmp/mediateca-station.new' >/dev/null 2>&1; then
		echo "failed $ip $stage"
		return 1
	fi

	stage=config
	local_config=$(umask 077; mktemp "${TMPDIR:-/tmp}/mediateca-config.XXXXXX")
	if ! render_config "$token" >"$local_config"; then
		rm -f "$local_config"
		echo "failed $ip $stage"
		return 1
	fi
	if ! scp $ssh_opts "$local_config" "${ssh_user}@${ip}:/tmp/config.yaml.new" >/dev/null 2>&1; then
		rm -f "$local_config"
		echo "failed $ip $stage"
		return 1
	fi
	rm -f "$local_config"
	if ! ssh $ssh_opts "${ssh_user}@${ip}" 'sudo install -m 0600 -o mediateca -g mediateca /tmp/config.yaml.new /etc/mediateca-station/config.yaml && rm -f /tmp/config.yaml.new' >/dev/null 2>&1; then
		echo "failed $ip $stage"
		return 1
	fi

	stage=hosts
	local_hosts=$(mktemp "${TMPDIR:-/tmp}/mediateca-hosts-in.XXXXXX")
	local_rendered=$(mktemp "${TMPDIR:-/tmp}/mediateca-hosts-out.XXXXXX")
	if ! ssh $ssh_opts "${ssh_user}@${ip}" 'sudo cat /etc/hosts' >"$local_hosts" 2>/dev/null; then
		rm -f "$local_hosts" "$local_rendered"
		echo "failed $ip $stage"
		return 1
	fi
	if ! render_hosts_file <"$local_hosts" >"$local_rendered"; then
		rm -f "$local_hosts" "$local_rendered"
		echo "failed $ip $stage"
		return 1
	fi
	rm -f "$local_hosts"
	if ! scp $ssh_opts "$local_rendered" "${ssh_user}@${ip}:/tmp/hosts.new" >/dev/null 2>&1; then
		rm -f "$local_rendered"
		echo "failed $ip $stage"
		return 1
	fi
	rm -f "$local_rendered"
	if ! ssh $ssh_opts "${ssh_user}@${ip}" 'tmp=$(mktemp) && sudo cp /tmp/hosts.new "$tmp" && sudo mv "$tmp" /etc/hosts && rm -f /tmp/hosts.new' >/dev/null 2>&1; then
		echo "failed $ip $stage"
		return 1
	fi

	stage=unit
	if ! render_unit | ssh $ssh_opts "${ssh_user}@${ip}" 'sudo tee /etc/systemd/system/mediateca-station.service >/dev/null' >/dev/null 2>&1; then
		echo "failed $ip $stage"
		return 1
	fi

	stage=systemd
	if ! ssh $ssh_opts "${ssh_user}@${ip}" 'sudo systemctl daemon-reload && sudo systemctl enable --now mediateca-station && sudo systemctl restart mediateca-station' >/dev/null 2>&1; then
		echo "failed $ip $stage"
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
	tmpbin=$(mktemp "${TMPDIR:-/tmp}/mediateca-station.XXXXXX")
	trap 'rm -f "$tmpbin"' EXIT

	if ! (cd "$repo_root" && GOOS=linux GOARCH=amd64 go build -o "$tmpbin" ./cmd/station); then
		echo "build failed" >&2
		exit 1
	fi

	failed=0
	while IFS=$TAB read -r ip token; do
		if ! install_one "$ip" "$token" "$tmpbin"; then
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
