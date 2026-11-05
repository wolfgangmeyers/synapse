# Completed standalone CLI archive migration

This records a one-off repository move, not an active runtime feature or E2E contract. Ordinary CI does not need mecha-archive or the original source Git objects. No spec-index entry is required.

## Preserved content and publication evidence

Source commit: `82d72a8c531af5c1d60ca81d49e589a17e737a5d` in mecha-developer.
Archive base: `704d5e96054c3097a939f2d1e4f6e96ef6706713` in mecha-archive.

Published archive commits:

- `fa754c2dc00dc51d5eaffb82f84d9e6e29198fc2`: adopted the existing identical Fireflies, Jira, Linear, and Notion copies and recorded source provenance.
- `87ec38784e1ff8d68b8d5c7c9cde30703e356b29`: corrected only five install-directory lines across the four READMEs.
- `719ae8914410781d27426e8cc43ad07cc31f6d16`: preserved the unchanged alien-symbol extraction utility and its provenance.

The publication handoff recorded `719ae8914410781d27426e8cc43ad07cc31f6d16 refs/heads/main` after a normal push and again before source deletion. This is historical evidence, not a claim that remote main will remain at that SHA.

The move preserved 31 original files: Fireflies 7, Jira 8, Linear 8, Notion 7, and `tools/extract_alien_symbols.py` 1. Compiled artifacts and executable modes were retained. Archive `docs/mcp-cli-move-provenance.md` records the original CLI paths, counts, byte lengths, Git modes, and SHA-256 hashes; `tools/extract_alien_symbols.provenance.md` records the utility's equivalent evidence.

Final archived bytes equal the source blobs except the five authorized README install-directory substitutions to `~/code/mecha-archive/tools/<package>`. No extra files were accepted inside the four package directories. MCP configuration, credentials, dependencies, runtime behavior, and unrelated files were outside the move. Only clean source files were deleted after publication confirmation; source staging, commits, and refs remained the parent's responsibility.

Offline Jira (14) and Linear (3) tests were attempted with isolated HOME/config but dependency-blocked (`commander`, `ERR_MODULE_NOT_FOUND`). Typechecks/rebuild comparison were also dependency-blocked. No dependencies were installed, external APIs called, or compiled-output equivalence claimed beyond exact preservation of source-commit artifacts. The utility was syntax-checked without executing it.

## Explicit preservation audit

From the mecha-developer repository root:

```sh
node tools/audits/cli-archive-migration.audit.mjs
# Or select a non-sibling archive checkout:
MECHA_ARCHIVE_ROOT=/absolute/path/to/mecha-archive node tools/audits/cli-archive-migration.audit.mjs
```

This optional, read-only historical verification requires the original source Git objects and the actual published archive files. It uses only Node built-ins and Git: no Playwright, browser, router, LLM, network, installation, or build. Missing archives, missing history, and mismatches fail with a nonzero exit; nothing silently skips. It is deliberately outside automatic E2E discovery.

The three checks verify:

1. All 31 original blobs against archive bytes and provenance, including package counts, exact package file sets, byte lengths, SHA-256, and executable modes.
2. All five install-directory instructions across four READMEs.
3. SHA-256 equality for every final file and rejection of an in-memory corrupted copy. Source absence alone is never proof of preservation.

Original one-task handoff artifacts were under `/tmp/mcp-cli-move-82d72a8/`, including manifests, reviews, and remote-main proof. They are temporary parent-owned task artifacts, not a runtime store or prerequisites for this audit. The durable provenance and publication SHAs above retain the useful evidence without requiring those temporary files.
