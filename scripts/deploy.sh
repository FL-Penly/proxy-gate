#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

INSTALL_DIR="$HOME/.proxy-gate"
LAUNCHD_LABEL="com.proxygate.server"
OS="$(uname -s)"

if ! command -v go >/dev/null 2>&1; then
  # Try common Homebrew Go paths
  for d in /usr/local/go/bin /opt/homebrew/opt/go/bin /usr/local/opt/go/bin /opt/homebrew/opt/go@*/bin; do
    [ -x "$d/go" ] && export PATH="$d:$PATH" && break
  done
fi
if ! command -v go >/dev/null 2>&1; then
  echo "error: go not found in PATH"; exit 1
fi

echo "==> install to $INSTALL_DIR"
mkdir -p "$INSTALL_DIR"
tmp_bin="$(mktemp "$INSTALL_DIR/proxy-gate.tmp.XXXXXX")"
cleanup() {
  rm -f "$tmp_bin"
}
trap cleanup EXIT

echo "==> go build"
go build -o "$tmp_bin" .
chmod 755 "$tmp_bin"
mv -f "$tmp_bin" "$INSTALL_DIR/proxy-gate"
trap - EXIT

if [ "$OS" = "Darwin" ]; then
  echo "==> ad-hoc codesign (required, otherwise launchd kills with OS_REASON_CODESIGNING)"
  codesign --force --sign - "$INSTALL_DIR/proxy-gate"

  if launchctl print "gui/$UID/$LAUNCHD_LABEL" >/dev/null 2>&1; then
    echo "==> launchctl kickstart -k (graceful restart)"
    launchctl kickstart -k "gui/$UID/$LAUNCHD_LABEL"
  else
    echo "==> launchctl bootstrap (first install)"
    launchctl bootstrap "gui/$UID" "$HOME/Library/LaunchAgents/$LAUNCHD_LABEL.plist"
  fi
else
  if pgrep -x proxy-gate >/dev/null 2>&1; then
    echo "==> stopping old proxy-gate"
    pkill -x proxy-gate || true
    for i in 1 2 3 4 5 6 7 8 9 10; do
      if ! pgrep -x proxy-gate >/dev/null 2>&1; then
        break
      fi
      sleep 1
    done
    if pgrep -x proxy-gate >/dev/null 2>&1; then
      echo "error: old proxy-gate did not stop after 10s"
      exit 1
    fi
  fi
  echo "==> starting proxy-gate (nohup)"
  cd "$INSTALL_DIR"
  nohup ./proxy-gate serve --config="$INSTALL_DIR/config.toml" </dev/null >> /tmp/proxy-gate.log 2>&1 &
fi

for i in 1 2 3 4 5 6 7 8; do
  if curl -s -m 1 http://127.0.0.1:19527/healthz | grep -q ok; then
    echo "==> healthz ok (PID $(pgrep -x proxy-gate | head -1))"
    exit 0
  fi
  sleep 1
done

echo "error: v2 did not become healthy after 8s"
echo "logs:"; tail -20 /tmp/proxy-gate.log
exit 1
