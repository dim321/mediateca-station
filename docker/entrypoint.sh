#!/bin/sh
# Writes a mode-0600 config (the process rejects a group-readable file) and
# starts the station. A mounted config at STATION_CONFIG is used as-is.
set -eu

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

http_listen="${HTTP_LISTEN:-0.0.0.0:8080}"
http_public="${HTTP_PUBLIC:-}"
http_secret="${HTTP_SECRET:-dev-only-http-secret-not-for-production}"
if [ "${#http_secret}" -lt 32 ]; then
  echo "station: HTTP_SECRET must be at least 32 characters" >&2
  exit 1
fi

poll_interval="${POLL_INTERVAL:-15s}"
late_threshold="${LATE_THRESHOLD:-2s}"
data_dir="${DATA_DIR:-/var/lib/mediateca-station}"
adb_serial="${ADB_SERIAL:-tv:5555}"

# http_listen is the bind address (0.0.0.0 inside the container). The Android
# emulator has to open a real address. When the tv service is on the lan
# network, advertise this container's address on that network.
if [ -z "$http_public" ]; then
  tv_ip=$(getent ahostsv4 tv 2>/dev/null | awk '{ print $1; exit }')
  if [ -n "$tv_ip" ]; then
    src=$(ip route get "$tv_ip" | awk '{ for (i = 1; i <= NF; i++) if ($i == "src") { print $(i + 1); exit } }')
    if [ -n "$src" ]; then
      http_public="${src}:${http_listen##*:}"
    fi
  fi
fi

# Development HostAuthorization allows IP literals and rejects the Compose
# name "web" (403). Leave HUB_BASE_URL unset so the station calls the hub
# container by the address it has on mediateca_broadcast_default.
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
