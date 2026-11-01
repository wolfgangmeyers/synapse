#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
FORK_DIR="$(dirname "$ROOT_DIR")/pi-packages"

fail() { printf 'initialize.sh: %s\n' "$*" >&2; exit 1; }
step() {
  local label="$1" status
  shift
  printf '\n==> %s\n' "$label"
  if "$@"; then return 0; else status=$?; fi
  printf 'initialize.sh: %s failed (exit %s). Fix the error above and rerun.\n' "$label" "$status" >&2
  exit "$status"
}

# Check host tools before installing packages or modifying local state.
for tool in git node npm go make; do
  command -v "$tool" >/dev/null 2>&1 || fail "Install $tool, then rerun tools/initialize.sh."
done
node_version="$(node --version)"
if [[ "$node_version" =~ ^v([0-9]+)\.([0-9]+)\.([0-9]+) ]]; then
  node_major="${BASH_REMATCH[1]}"
  node_minor="${BASH_REMATCH[2]}"
  (( node_major > 22 || (node_major == 22 && node_minor >= 19) )) || fail "Node.js 22.19 or newer is required (found $node_version)."
else
  fail "Cannot determine Node.js version: $node_version (need 22.19 or newer)."
fi
if [[ -e "$FORK_DIR" && ( ! -e "$FORK_DIR/.git" || ! -f "$FORK_DIR/packages/pi-subagents/src/index.ts" ) ]]; then
  fail "$FORK_DIR already exists but is not the required pi-packages checkout (missing .git or packages/pi-subagents/src/index.ts). Move it aside yourself or repair the checkout."
fi

# These are application-owned leaves; never chmod HOME or other ancestors.
umask 077
state_dir="$HOME/.mecha-developer"
mkdir -p "$state_dir" "$state_dir/managed-pi" "$state_dir/managed-pi/sessions" "$state_dir/go-build-cache" "$HOME/.pi/agent"
chmod 700 "$state_dir" "$state_dir/managed-pi" "$state_dir/managed-pi/sessions" "$state_dir/go-build-cache" "$HOME/.pi/agent"

if ! command -v pi >/dev/null 2>&1; then
  step "Install upstream Pi (npm global prefix must be writable by your user; configure a user-owned prefix if needed)" \
    npm install -g @earendil-works/pi-coding-agent@latest
else
  printf 'Using existing Pi: %s\n' "$(command -v pi)"
fi
if [[ ! -e "$FORK_DIR" ]]; then
  step "Clone maintained Pi subagents fork" git clone --branch main https://github.com/wolfgangmeyers/pi-packages.git "$FORK_DIR"
else
  printf 'Keeping existing fork checkout and local edits: %s\n' "$FORK_DIR"
fi
pnpm_package="$(node -p 'require(process.argv[1]).packageManager' "$FORK_DIR/package.json")"
[[ "$pnpm_package" =~ ^pnpm@[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail "Expected a pinned pnpm version in $FORK_DIR/package.json; found $pnpm_package."
step "Install fork workspace dependencies" npm exec --yes --package "$pnpm_package" -- pnpm --dir "$FORK_DIR" install --frozen-lockfile
step "Install repository dependencies" npm --prefix "$ROOT_DIR" ci
step "Install auth helper dependencies" npm --prefix "$ROOT_DIR/auth-helper" ci
step "Install RPC extension dependencies" npm --prefix "$ROOT_DIR/pi-extensions/mecha-rpc" ci
step "Install Web UI dependencies" npm --prefix "$ROOT_DIR/web/router-ui-react" ci
step "Build Web UI" npm --prefix "$ROOT_DIR/web/router-ui-react" run build
export GOCACHE="${GOCACHE:-$state_dir/go-build-cache}"
step "Build router" make -C "$ROOT_DIR/router-proxy" build-router-local

printf '\nInitialization complete.\n\n'
printf 'Start the router:\n  cd %q\n  MECHA_ROUTER_MANAGED_PI_SUBAGENTS=%q tools/run_router.sh\n' "$ROOT_DIR" "$FORK_DIR/packages/pi-subagents/src/index.ts"
printf '\nStart the Web UI in another terminal:\n  cd %q\n  tools/run_webui.sh\n' "$ROOT_DIR"
printf '\nOpen http://localhost:8787 and sign in through Providers before launching workers.\n'
