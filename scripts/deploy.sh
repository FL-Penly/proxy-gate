#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

INSTALL_DIR="$HOME/.proxy-gate"
LAUNCHD_LABEL="com.proxygate.server"

if ! command -v go >/dev/null 2>&1; then
  # Try common Homebrew Go paths
  for d in /opt/homebrew/opt/go/bin /usr/local/opt/go/bin /opt/homebrew/opt/go@*/bin; do
    [ -x "$d/go" ] && export PATH="$d:$PATH" && break
  done
fi
if ! command -v go >/dev/null 2>&1; then
  echo "error: go not found in PATH"; exit 1
fi

echo "==> go build"
go build -o proxy-gate .

echo "==> install to $INSTALL_DIR"
mkdir -p "$INSTALL_DIR"
cp proxy-gate "$INSTALL_DIR/proxy-gate"

echo "==> ad-hoc codesign (required, otherwise launchd kills with OS_REASON_CODESIGNING)"
codesign --force --sign - "$INSTALL_DIR/proxy-gate"

if launchctl print "gui/$UID/$LAUNCHD_LABEL" >/dev/null 2>&1; then
  echo "==> launchctl kickstart -k (graceful restart)"
  launchctl kickstart -k "gui/$UID/$LAUNCHD_LABEL"
else
  echo "==> launchctl bootstrap (first install)"
  launchctl bootstrap "gui/$UID" "$HOME/Library/LaunchAgents/$LAUNCHD_LABEL.plist"
fi

for i in 1 2 3 4 5 6 7 8; do
  if curl -s -m 1 http://127.0.0.1:19527/healthz | grep -q ok; then
    echo "==> healthz ok (PID $(pgrep -f 'proxy-gate serve' | head -1))"
    exit 0
  fi
  sleep 1
done

echo "error: v2 did not become healthy after 8s"
echo "logs:"; tail -20 /tmp/proxy-gate.log
exit 1
