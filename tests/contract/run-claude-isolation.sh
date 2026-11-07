#!/bin/sh
set -eu
if [ "${MECHA_RUN_REAL_CLAUDE_CONTRACT:-}" != 1 ]; then echo 'SKIP: set MECHA_RUN_REAL_CLAUDE_CONTRACT=1'; exit 0; fi
command -v bwrap >/dev/null || { echo 'bubblewrap required' >&2; exit 1; }
bin=$(command -v claude) || exit 1
bin=$(readlink -f "$bin")
host_net=$(readlink /proc/self/ns/net)
script=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)/claude-isolation.py
# The sandbox sees no host /home, /run, /var, /etc or Unix sockets.
exec bwrap --die-with-parent --new-session --unshare-all --ro-bind /usr /usr --ro-bind /lib /lib --ro-bind /lib64 /lib64 --ro-bind /bin /bin --ro-bind "$bin" /claude --ro-bind "$script" /contract.py --tmpfs /tmp --proc /proc --dev /dev --clearenv --setenv MECHA_ISOLATED_CONTRACT 1 --setenv HOST_NET_NS "$host_net" -- /usr/bin/python3 /contract.py
