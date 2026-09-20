#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LAUNCHER="$ROOT_DIR/tools/run_reflex_proxy.sh"
TEMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TEMP_DIR"' EXIT

FAKE_PROXY="$TEMP_DIR/router-proxy"
CAPTURED_ARGS="$TEMP_DIR/args"
cat > "$FAKE_PROXY" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$@" > "$CAPTURED_ARGS"
EOF
chmod +x "$FAKE_PROXY"

assert_args() {
  local expected=$1
  local actual
  actual="$(cat "$CAPTURED_ARGS")"
  if [[ "$actual" != "$expected" ]]; then
    printf 'unexpected proxy arguments\nexpected:\n%s\nactual:\n%s\n' "$expected" "$actual" >&2
    exit 1
  fi
}

expected_default_args=$'--upstream-host\n192.168.0.167\n--upstream-port\n8765\n--listen-port\n19876'
CAPTURED_ARGS="$CAPTURED_ARGS" MECHA_REFLEX_PROXY_BINARY="$FAKE_PROXY" \
  "$LAUNCHER" 192.168.0.167
assert_args "$expected_default_args"

expected_upstream_only_args=$'--upstream-host\n10.0.0.9\n--upstream-port\n9000\n--listen-port\n19876'
CAPTURED_ARGS="$CAPTURED_ARGS" MECHA_REFLEX_PROXY_BINARY="$FAKE_PROXY" \
  "$LAUNCHER" 10.0.0.9 9000
assert_args "$expected_upstream_only_args"

expected_override_args=$'--upstream-host\n10.0.0.9\n--upstream-port\n9000\n--listen-port\n30000'
CAPTURED_ARGS="$CAPTURED_ARGS" MECHA_REFLEX_PROXY_BINARY="$FAKE_PROXY" \
  "$LAUNCHER" 10.0.0.9 9000 30000
assert_args "$expected_override_args"

if CAPTURED_ARGS="$CAPTURED_ARGS" MECHA_REFLEX_PROXY_BINARY="$FAKE_PROXY" \
  "$LAUNCHER" >"$TEMP_DIR/no-target.stdout" 2>"$TEMP_DIR/no-target.stderr"; then
  printf 'launcher accepted a missing target IP\n' >&2
  exit 1
fi
grep -Fq 'target IP is required' "$TEMP_DIR/no-target.stderr"

echo 'PASS: default ports, explicit ports, required target IP, and proxy bind-host default'
