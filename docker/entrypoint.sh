#!/bin/sh
# Writes a mode-0600 config (the process rejects a group-readable file) and
# starts the station. A mounted config at STATION_CONFIG is used as-is.
set -eu

# shellcheck disable=SC1091
. /usr/local/lib/devnet.sh

# dnsmasq is the mini-PC DHCP service, not part of the station process.
# It must be listening before the television container requests a lease.
start_television_dhcp() {
  render_dnsmasq_conf /usr/local/lib/dnsmasq.conf /run/dnsmasq.conf \
    "${TV_DHCP_MAC:-78:53:33:4b:2c:29}" "${TV_DHCP_IP:-192.168.33.182}"
  install -d -m 0755 /var/lib/misc
  dnsmasq -d -C /run/dnsmasq.conf &
  pid=$!
  i=0
  while [ "$i" -lt 25 ]; do
    if ss -H -lun | grep -q ':67[[:space:]]'; then
      echo "station: dnsmasq DHCP on eth0, leases /var/lib/misc/dnsmasq.leases" >&2
      return 0
    fi
    if ! kill -0 "$pid" 2>/dev/null; then
      echo "station: dnsmasq exited" >&2
      exit 1
    fi
    i=$((i + 1))
    sleep 0.2
  done
  echo "station: dnsmasq did not bind UDP/67" >&2
  exit 1
}

align_station_nics
start_television_dhcp

config_path="${STATION_CONFIG:-/etc/mediateca-station/config.yaml}"

if [ -f "$config_path" ]; then
  exec /usr/local/bin/mediateca-station -config "$config_path"
fi

: "${AGENT_TOKEN:?set AGENT_TOKEN to the hub station token (Station#assign_agent_token!)}"
: "${SCREEN_ID:?set SCREEN_ID to a hub screen id}"

case "$SCREEN_ID" in
  *[!0-9]* | "")
    echo "station: SCREEN_ID must be a positive integer" >&2
    exit 1
    ;;
esac
if [ "$SCREEN_ID" -eq 0 ]; then
  echo "station: SCREEN_ID must be a positive integer" >&2
  exit 1
fi

http_listen="${HTTP_LISTEN:-192.168.33.1:8080}"
http_public="${HTTP_PUBLIC:-}"
http_secret="${HTTP_SECRET:-dev-only-http-secret-not-for-production}"
if [ "${#http_secret}" -lt 32 ]; then
  echo "station: HTTP_SECRET must be at least 32 characters" >&2
  exit 1
fi

poll_interval="${POLL_INTERVAL:-15s}"
late_threshold="${LATE_THRESHOLD:-2s}"
data_dir="${DATA_DIR:-/var/lib/mediateca-station}"
adb_serial="${ADB_SERIAL:-192.168.33.182:5555}"

# Televisions live on eth0 (192.168.33.0/24). Advertise and bind that address.
# Do not bind 0.0.0.0: that would also serve clips on eth1, the Hub plane.
tv_host=$(serial_host "$adb_serial")
tv_ip=""
case "$tv_host" in
  *.*.*.*) tv_ip=$tv_host ;;
  *) tv_ip=$(getent ahostsv4 "$tv_host" 2>/dev/null | awk '{ print $1; exit }') ;;
esac
if [ -n "$tv_ip" ]; then
  src=$(ip route get "$tv_ip" | awk '{
    for (i = 1; i <= NF; i++) if ($i == "src") { print $(i + 1); exit }
  }')
  if [ -z "$src" ]; then
    echo "station: no route to television ${tv_ip}" >&2
    exit 1
  fi
  if in_tv_subnet "$tv_ip" && ! in_tv_subnet "$src"; then
    echo "station: route to television ${tv_ip} uses ${src}, not eth0" >&2
    exit 1
  fi
  http_listen=$(listen_on_plane "$http_listen" "$src")
  if [ -z "$http_public" ]; then
    http_public="${src}:${http_listen##*:}"
  fi
  if in_tv_subnet "$tv_ip"; then
    pub_host=${http_public%:*}
    listen_host=${http_listen%:*}
    if ! in_tv_subnet "$pub_host" || ! in_tv_subnet "$listen_host"; then
      echo "station: media address must be on 192.168.33.0/24 (listen ${http_listen}, public ${http_public})" >&2
      exit 1
    fi
  fi
fi

# Development HostAuthorization allows IP literals and rejects the Compose
# name "web" (403). Leave HUB_BASE_URL unset so the station calls the hub
# container by the address it has on mediateca_broadcast_default (eth1).
if [ -n "${HUB_BASE_URL:-}" ]; then
  hub_base_url="$HUB_BASE_URL"
else
  hub_ip=$(getent ahostsv4 web | awk '{ print $1; exit }')
  if [ -z "$hub_ip" ]; then
    echo "station: cannot resolve hub service web." >&2
    echo "station: start Broadcast Hub first (mediateca-broadcast: docker compose up)." >&2
    exit 1
  fi
  hub_base_url="http://${hub_ip}:3000"
  hub_src=$(ip route get "$hub_ip" | awk '{
    for (i = 1; i <= NF; i++) if ($i == "src") { print $(i + 1); exit }
  }')
  if in_tv_subnet "$hub_src"; then
    echo "station: route to Hub ${hub_ip} uses television address ${hub_src}" >&2
    exit 1
  fi
fi

yaml_escape() {
  printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g'
}

install -d -m 0700 "$(dirname "$config_path")"
umask 077
cat > "$config_path" <<EOF
hub_base_url: "$(yaml_escape "$hub_base_url")"
agent_token: "$(yaml_escape "$AGENT_TOKEN")"
http_listen: "$(yaml_escape "$http_listen")"
$(if [ -n "$http_public" ]; then printf 'http_public: "%s"\n' "$(yaml_escape "$http_public")"; fi)
http_secret: "$(yaml_escape "$http_secret")"
late_threshold: "$(yaml_escape "$late_threshold")"
poll_interval: "$(yaml_escape "$poll_interval")"
data_dir: "$(yaml_escape "$data_dir")"
screens:
  - screen_id: ${SCREEN_ID}
    adb_serial: "$(yaml_escape "$adb_serial")"
EOF

exec /usr/local/bin/mediateca-station -config "$config_path"
