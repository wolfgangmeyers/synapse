# ADR 0004: Single-User Workstation Security Model

- **Status**: Accepted
- **Deciders**: Wolfgang
- **Date**: 2025-10-28

---

## Context & Problem Statement

Agentic workflows and automated tools frequently handle filesystem paths, credential vaults, sockets, and local state directories (e.g., `~/.mecha-developer/`, `~/.claude/skills/`, SQLite databases).

AI models and automated security linters frequently hallucinate multi-tenant threat models. They attempt to enforce enterprise multi-user containment policies—such as requiring `0700` (`rwx------`) permissions on all parent directories up to `/home` or `/`, or blocking access if `/home/wolfgang` has default Linux permissions (`0755` or `0750`).

On personal developer workstations (macOS and Linux), user home directories are routinely `0750` or `0755` by system default. Enforcing `0700` on ancestor paths breaks standard directory layouts, causes artificial permission errors, disrupts legitimate system services, and adds useless operational friction.

---

## Decision

Explicitly define and enforce the **Single-User Workstation Security Model** across all `mecha-developer` tools, scripts, and validation routines:

1. **Single-User Developer Workstation Assumption**:
   - The operating environment is a dedicated, single-user workstation, not a shared multi-tenant server or hostile multi-user mainframe.
   - Do not invent unrequested multi-user containment restrictions or hostile same-user permission gates.
2. **Permission Validation Invariants**:
   - **Ancestor Paths**: Ancestor directories (e.g., `/home`, `/home/wolfgang`) must **never** be required to be `0700`. The only check allowed for ancestor paths is verifying that they are not group-writable or world-writable (`mode & 0o022 == 0`).
   - **Leaf Directory**: Private leaf directories storing secrets or sensitive credentials (e.g., `~/.mecha-developer/credentials/`) may be enforced as owner-only (`0700`).
3. **No Artificial Inter-Agent Hostility**:
   - Agents running under the same user UID share local resources cooperative-first. Security boundaries exist at the machine/network edge (firewalls, loopback binding, tailnet authentication), not between local processes owned by Wolfgang.

---

## Invariants & Guarantees

- **Ancestor Permission Rule**: `ancestor_mode & 0o022 == 0`. Ancestor paths may be `0755` or `0750`. Requiring `0700` on any non-leaf directory is an architectural bug.
- **Leaf Permission Rule**: Private state directories created by tools must use `0700`, and sensitive credential files must use `0600`.
- **Single-User Context**: Tools must not refuse to run or fail healthchecks merely because `/home/wolfgang` is readable by other users on a standard Linux distro, provided it is not world-writable.

---

## Consequences

### Positive
- Eliminates recurring false-positive validation crashes when installing or running agent tooling on standard Linux and macOS machines.
- Prevents agents from altering system-level permissions on user home directories, which can break system daemons (e.g., sshd, web servers, pulseaudio).
- Clean, predictable security policy that focuses defense where it matters: the external network perimeter.

### Negative
- Tools must clearly differentiate between leaf directories and ancestor paths during directory audits and setup checks.

---

## References

- Operating Policy: `docs/wolfgang-agent-operating-policy.md` (Section: Single-User Workstation Assumption and Directory Permissions)
- Project Instructions: `/home/wolfgang/AGENTS.md`
- Installer Implementation: `.claude/skills/install/SKILL.md`
