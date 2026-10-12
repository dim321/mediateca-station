#!/bin/sh
# Pure helpers for the development television LAN. Interface rename is checked
# by running the compose stack; these cases need no Docker.
set -eu

cd "$(dirname "$0")/.."
# shellcheck disable=SC1091
. ./docker/devnet.sh

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

got=$(serial_host "192.168.33.182:5555")
[ "$got" = "192.168.33.182" ] || fail "serial host: $got"

in_tv_subnet 192.168.33.1 || fail "station address should be on the television LAN"
in_tv_subnet 192.168.33.182 || fail "emulator address should be on the television LAN"
if in_tv_subnet 172.19.0.2; then
  fail "Hub address must stay off the television LAN"
fi

got=$(listen_on_plane "0.0.0.0:8080" "192.168.33.1")
[ "$got" = "192.168.33.1:8080" ] || fail "wildcard listen: $got"

got=$(listen_on_plane "192.168.33.1:8080" "192.168.33.1")
[ "$got" = "192.168.33.1:8080" ] || fail "explicit listen: $got"

echo "devnet helpers ok"
