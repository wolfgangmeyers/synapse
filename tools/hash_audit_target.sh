#!/usr/bin/env bash
# Hash the audit code target without mutable audit evidence.
set -euo pipefail

root="$(git -C "$(dirname "$0")/.." rev-parse --show-toplevel)"
manifest="$(mktemp)"
trap 'rm -f "$manifest"' EXIT

# REVIEW_LEDGER.md and AUDIT_REQUIREMENTS.md describe the target and its
# evidence. They are deliberately outside the code-target identity.
{
  while IFS= read -r -d '' path; do
    case "$path" in
      REVIEW_LEDGER.md|AUDIT_REQUIREMENTS.md) ;;
      *) printf '%s\0' "$path" ;;
    esac
  done < <(git -C "$root" ls-files -z)
  for path in \
    router-proxy/internal/transport/policy.go \
    router-proxy/internal/transport/policy_test.go; do
    if [[ ! -f "$root/$path" ]]; then
      printf 'hash_audit_target.sh: required path is missing: %s\n' "$path" >&2
      exit 1
    fi
    printf '%s\0' "$path"
  done
} | LC_ALL=C sort -z -u >"$manifest"

paths="$(tr -cd '\0' <"$manifest" | wc -c | tr -d ' ')"
hash="$(
  cd "$root"
  env -u TAR_OPTIONS tar \
    --create \
    --file=- \
    --format=posix \
    --sort=name \
    --mtime='@0' \
    --owner=0 \
    --group=0 \
    --numeric-owner \
    --mode='u=rw,go=r' \
    --pax-option=delete=atime,delete=ctime \
    --null \
    --verbatim-files-from \
    --files-from="$manifest" \
    | sha256sum \
    | awk '{print $1}'
)"
printf '%s paths=%s\n' "$hash" "$paths"
