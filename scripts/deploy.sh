#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

INSTALL_DIR="$HOME/.cligate-v2"
LAUNCHD_LABEL="com.codeking.cligate"

if ! command -v go >/dev/null 2>&1; then
  echo "error: go not in PATH"; exit 1
fi
if [ -x "/opt/homebrew/opt/go@1.25/bin/go" ]; then
  export PATH="/opt/homebrew/opt/go@1.25/bin:$PATH"
fi

echo "==> go build"
go build -o cligate .

echo "==> install to $INSTALL_DIR"
mkdir -p "$INSTALL_DIR"
cp cligate "$INSTALL_DIR/cligate"

echo "==> ad-hoc codesign (required, otherwise launchd kills with OS_REASON_CODESIGNING)"
codesign --force --sign - "$INSTALL_DIR/cligate"

if launchctl print "gui/$UID/$LAUNCHD_LABEL" >/dev/null 2>&1; then
  echo "==> launchctl kickstart -k (graceful restart)"
  launchctl kickstart -k "gui/$UID/$LAUNCHD_LABEL"
else
  echo "==> launchctl bootstrap (first install)"
  launchctl bootstrap "gui/$UID" "$HOME/Library/LaunchAgents/$LAUNCHD_LABEL.plist"
fi

for i in 1 2 3 4 5 6 7 8; do
  if curl -s -m 1 http://127.0.0.1:19527/healthz | grep -q ok; then
    echo "==> healthz ok (PID $(pgrep -f 'cligate serve' | head -1))"
    exit 0
  fi
  sleep 1
done

echo "error: v2 did not become healthy after 8s"
echo "logs:"; tail -20 /tmp/cligate.log
exit 1
