#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
DEPLOY_STATIONS_LIB=1
# shellcheck disable=SC1091
. ./deploy/install-stations.sh

fail() { echo "FAIL: $*" >&2; exit 1; }

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
printf '%s\n' '# comment' '' '192.168.1.204	abcdef' > "$tmp/inv"
got=$(parse_inventory "$tmp/inv")
[ "$got" = "192.168.1.204	abcdef" ] || fail "parse: $got"

printf '%s\n' '192.168.1.204	abcdef' '192.168.1.204	ffffff' > "$tmp/dup"
if parse_inventory "$tmp/dup" >/dev/null 2>"$tmp/err"; then
  fail "duplicate ip must fail"
fi
grep -q abcdef "$tmp/err" && fail "token leaked into the error"

cfg=$(render_config "abcdef")
printf '%s\n' "$cfg" | grep -qx 'hub_base_url: "https://broadcast.mediateca.store"' || fail "hub url"
printf '%s\n' "$cfg" | grep -qx 'agent_token: "abcdef"' || fail "token"
if printf '%s\n' "$cfg" | grep -q '^screens:'; then
  fail "deploy config must not list screens"
fi
printf '%s\n' "$cfg" | grep -qx 'broadcast_bind: "192.168.33.1"' || fail "bind"

hosts=$(printf '%s\n' '127.0.0.1 localhost' '1.2.3.4 broadcast.mediateca.store' | render_hosts_file)
printf '%s\n' "$hosts" | grep -qx '127.0.0.1 localhost' || fail "localhost dropped"
printf '%s\n' "$hosts" | grep -c 'broadcast.mediateca.store' | grep -qx 1 || fail "name count"
printf '%s\n' "$hosts" | grep -qx '192.168.100.250 broadcast.mediateca.store' || fail "hub line"

unit=$(render_unit)
printf '%s\n' "$unit" | grep -qx 'User=mediateca' || fail "user"
printf '%s\n' "$unit" | grep -qx 'ExecStart=/usr/local/bin/mediateca-station -config /etc/mediateca-station/config.yaml' || fail "exec"

echo "ok"
