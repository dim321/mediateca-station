#!/bin/bash
# Boots the Android TV AVD, publishes its ADB port on the container address,
# and installs VLC (org.videolan.vlc) so the station can start clips.
set -eu

export ANDROID_SDK_ROOT=/opt/android
export ANDROID_AVD_HOME=/root/.android/avd
export PATH="${ANDROID_SDK_ROOT}/platform-tools:${ANDROID_SDK_ROOT}/emulator:${PATH}"
export LD_LIBRARY_PATH="${ANDROID_SDK_ROOT}/emulator/lib64:${ANDROID_SDK_ROOT}/emulator/lib64/qt/lib${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"
export USER=root

memory="${EMULATOR_MEMORY:-2048}"
cores="${EMULATOR_CORES:-2}"
lan_ip=$(ip -4 addr show eth0 | awk '/inet / { sub(/\/.*/, "", $2); print $2; exit }')
if [ -z "$lan_ip" ]; then
  echo "tv: eth0 has no address" >&2
  exit 1
fi

emulator \
  -avd tv \
  -gpu swiftshader_indirect \
  -memory "$memory" \
  -cores "$cores" \
  -no-window \
  -no-audio \
  -no-boot-anim \
  -no-snapshot \
  -ranchu \
  -skip-adb-auth \
  -ports 5554,5555 &
emu_pid=$!

cleanup() {
  kill "$emu_pid" 2>/dev/null || true
  wait "$emu_pid" 2>/dev/null || true
}
trap cleanup TERM INT

echo "tv: waiting for the emulator adb port"
for _ in $(seq 1 90); do
  if (echo > /dev/tcp/127.0.0.1/5555) >/dev/null 2>&1; then
    break
  fi
  if ! kill -0 "$emu_pid" 2>/dev/null; then
    echo "tv: emulator exited before adb was ready" >&2
    wait "$emu_pid" || true
    exit 1
  fi
  sleep 2
done

# 127.0.0.1:5555 is the emulator. Other containers reach this address.
socat TCP-LISTEN:5555,bind="$lan_ip",reuseaddr,fork TCP:127.0.0.1:5555 &
socat TCP-LISTEN:5554,bind="$lan_ip",reuseaddr,fork TCP:127.0.0.1:5554 &

adb wait-for-device
boot=""
for _ in $(seq 1 180); do
  boot=$(adb shell getprop sys.boot_completed 2>/dev/null | tr -d '\r' || true)
  if [ "$boot" = "1" ]; then
    break
  fi
  if ! kill -0 "$emu_pid" 2>/dev/null; then
    echo "tv: emulator exited during boot" >&2
    wait "$emu_pid" || true
    exit 1
  fi
  sleep 2
done
if [ "$boot" != "1" ]; then
  echo "tv: boot did not finish" >&2
  exit 1
fi

echo "tv: installing VLC"
adb install -r -g /opt/vlc.apk
adb shell pm path org.videolan.vlc
echo "tv: ready on ${lan_ip}:5555"

wait "$emu_pid"
