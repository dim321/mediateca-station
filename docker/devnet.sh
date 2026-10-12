#!/bin/sh
# Development layout of the location mini-PC.
# eth0 is the television LAN 192.168.33.1/24. eth1 is the administration
# plane, the only path to Broadcast Hub. Docker attaches the external Hub
# network first, so the entrypoint renames the interfaces before serving.

in_tv_subnet() {
  case "$1" in
    192.168.33.*) return 0 ;;
    *) return 1 ;;
  esac
}

serial_host() {
  printf '%s\n' "${1%%:*}"
}

# listen_on_plane LISTEN SRC
# A wildcard bind follows the address that reaches the television.
listen_on_plane() {
  listen=$1
  src=$2
  host=${listen%:*}
  port=${listen##*:}
  case "$host" in
    0.0.0.0 | "") printf '%s:%s\n' "$src" "$port" ;;
    *) printf '%s\n' "$listen" ;;
  esac
}

iface_with_prefix() {
  prefix=$1
  ip -4 -o addr show | awk -v pfx="$prefix" '
    $3 == "inet" && index($4, pfx) == 1 {
      split($2, name, "@")
      print name[1]
      exit
    }
  '
}

other_iface() {
  skip=$1
  ip -4 -o addr show | awk -v skip="$skip" '
    $3 == "inet" {
      split($2, name, "@")
      iface = name[1]
      if (iface != "lo" && iface != skip) {
        print iface
        exit
      }
    }
  '
}

align_station_nics() {
  tv_if=$(iface_with_prefix "192.168.33.")
  if [ -z "$tv_if" ]; then
    echo "station: television LAN 192.168.33.0/24 is not configured" >&2
    exit 1
  fi
  admin_if=$(other_iface "$tv_if")
  if [ -z "$admin_if" ]; then
    echo "station: administration interface (path to the Hub) is missing" >&2
    exit 1
  fi

  def_gw=$(ip route show default | awk '{
    for (i = 1; i <= NF; i++) if ($i == "via") { print $(i + 1); exit }
  }')

  if [ "$tv_if" != "eth0" ] || [ "$admin_if" != "eth1" ]; then
    if ! ip link set "$tv_if" down || ! ip link set "$admin_if" down; then
      echo "station: renaming eth0/eth1 needs CAP_NET_ADMIN" >&2
      exit 1
    fi
    ip link set "$admin_if" name hold0
    ip link set "$tv_if" name eth0
    ip link set hold0 name eth1
    ip link set eth0 up
    ip link set eth1 up
  fi

  if [ -n "$def_gw" ] && ! in_tv_subnet "$def_gw"; then
    ip route replace default via "$def_gw" dev eth1
  fi

  eth0_addr=$(ip -4 -o addr show dev eth0 | awk '$3 == "inet" { print $4; exit }')
  eth1_addr=$(ip -4 -o addr show dev eth1 | awk '$3 == "inet" { print $4; exit }')
  case "$eth0_addr" in
    192.168.33.1/*) ;;
    *)
      echo "station: eth0 has ${eth0_addr:-no address}, want 192.168.33.1/24" >&2
      exit 1
      ;;
  esac
  if [ -z "$eth1_addr" ] || in_tv_subnet "${eth1_addr%%/*}"; then
    echo "station: eth1 has ${eth1_addr:-no address}, want the Hub network" >&2
    exit 1
  fi
  echo "station: eth0 ${eth0_addr} television LAN, eth1 ${eth1_addr} administration" >&2
}
