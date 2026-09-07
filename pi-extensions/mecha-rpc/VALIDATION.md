# Phase 1 validation

This is the fixed thin Pi extension. It registers the generated managed-tool manifest and carries control messages. Router handlers, durable router ledgers, mailbox recovery, and browser acceptance are outside Phase 1.

## Fork workspace prerequisite

This package intentionally uses the Wolfgang `pi-packages` fork on `main`. It has no registry release or registry version pin. The `file:../../../pi-packages/packages/pi-subagents` dependency requires a sibling checkout at `../pi-packages` from the mecha-developer root. A clean mecha-developer clone alone cannot install this package.

Before `npm ci`, clone the fork beside this repository, install its workspace dependencies, and keep the checkout clean on the current `origin/main`:

```sh
git clone --branch main https://github.com/wolfgangmeyers/pi-packages.git ../pi-packages
pnpm --dir ../pi-packages install --frozen-lockfile
npm run check:mecha-rpc:fork
npm --prefix pi-extensions/mecha-rpc ci
```

`check:mecha-rpc:fork` verifies the required source entry, clean `main`, and `HEAD == origin/main`, then prints the source commit used for the run. It is direct provenance for the local source dependency, not a claim that an isolated clone works.

## Required checks

```sh
npm run check:mecha-rpc:fork
pnpm --dir ../pi-packages --filter @gotgenes/pi-subagents run check
pnpm --dir ../pi-packages --filter @gotgenes/pi-subagents run lint
pnpm --dir ../pi-packages --filter @gotgenes/pi-subagents run test
pnpm --dir ../pi-packages --filter @gotgenes/pi-subagents run build:types
pnpm --dir ../pi-packages --filter @gotgenes/pi-subagents run verify:public-types
npm run test:mecha-rpc:contract
npm run test:mecha-rpc:native
npm --prefix pi-extensions/mecha-rpc run test:coverage
npm --prefix pi-extensions/mecha-rpc run build
```

`test:coverage` writes JSON coverage to `/tmp/mecha-rpc-coverage/` and instruments only `pi-extensions/mecha-rpc/**/*.ts`.

## Focused extension coverage

The focused suite covers canonical base64url and malformed envelopes, strict schema keywords and enum bounds, manifest ordering and 16–32 limits, raw argument closure, registration collisions and fatal writes, current 17–32 manifests, changed-definition hash names, and accepted/rejected full and supplemental delivery replay. Installation receipts are bounded to 128 per extension runtime and clear at session shutdown.

## Stock Pi proof

`tests/mecha_rpc_native_pi_e2e.test.ts` starts installed Pi `0.85.0` in fresh RPC processes with the checked fork entry and a local deterministic provider. It prompts all six hidden commands and all nine legacy commands, proves a real child receives its manifest before its first request, checks child result append and stale-result rejection, and starts a fresh generation that rejects an old fence and accepts a changed hash-named definition.

The expected evidence matrix is `tests/fixtures/mecha-rpc/acceptance-evidence-v1.json`. Successful native tests write the matching run artifacts under `/tmp/mecha-rpc-acceptance/`. The fork suite supplies the duplicate-registration, disposer, per-spawn snapshot, and owner-release checks for the public child-factory API.
