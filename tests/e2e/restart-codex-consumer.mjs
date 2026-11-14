// Embedded restart tests use the Go-owned credential directly, not the RPC Pi
// projection. Accept only the fixture credential and acknowledge the private pipe.
import { createInterface } from "node:readline";

try {
  const lines = [];
  for await (const line of createInterface({ input: process.stdin, crlfDelay: Infinity })) lines.push(line);
  if (lines.length !== 1 || Buffer.byteLength(lines[0]) > 65536) throw Error();
  const value = JSON.parse(lines[0]);
  if (!value || Object.keys(value).sort().join(",") !== "access,accountId,expires,refresh,type" ||
      value.type !== "oauth" || !/^fake\.[A-Za-z0-9_-]+\.terra-test-secret$/.test(value.access) ||
      value.refresh !== "terra-refresh-secret" || value.accountId !== "terra-test-account" ||
      !Number.isSafeInteger(value.expires) || value.expires <= 0) throw Error();
  process.stdout.write('{"synchronized":true}\n');
} catch {
  process.exitCode = 1;
}
