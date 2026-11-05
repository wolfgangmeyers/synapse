import assert from "node:assert/strict";
import test from "node:test";
import { parsePioneerSquareRoute, parsePioneerSquareRouteSubject, pioneerSquareRouteSubject, pioneerSquareRouteTarget } from "../pi-extensions/mecha-messaging/mailbox-client.ts";

test("Pioneer Square route helpers round-trip canonical routes and targets", () => {
  const route = "pioneer-square:#dnsid:123456789012345678";
  const parsed = { channelName: "dnsid", channelId: "123456789012345678", route };
  assert.deepEqual(parsePioneerSquareRoute(route), parsed);
  assert.deepEqual(parsePioneerSquareRouteSubject(pioneerSquareRouteSubject(route)), parsed);
  assert.equal(pioneerSquareRouteSubject(route), `pioneer-square.route.v1:${route}`);
  assert.deepEqual(pioneerSquareRouteTarget(route), { to: "pioneer-square", subject: `pioneer-square.route.v1:${route}` });
});

test("Pioneer Square route helpers reject missing hash, whitespace, uppercase, delimiters, and wrong prefixes", () => {
  const invalidRoutes = [
    "pioneer-square:dnsid:123456789012345678",
    "pioneer-square:#dns_id:123456789012345678",
    `pioneer-square:#${"a".repeat(100)}:123456789012345678`,
    "pioneer-square:#DNSID:123456789012345678",
    "pioneer-square:#dns id:123456789012345678",
    "pioneer-square:#dnsid:1234567890123456",
    "pioneer-square:#dnsid:123456789012345678901",
    "pioneer-square:#dnsid:123456789012345678:extra",
    "other:#dnsid:123456789012345678",
    " pioneer-square:#dnsid:123456789012345678",
  ];
  for (const route of invalidRoutes) {
    assert.equal(parsePioneerSquareRoute(route), undefined, route);
    assert.throws(() => pioneerSquareRouteSubject(route), /invalid Pioneer Square route/);
  }
  assert.deepEqual(parsePioneerSquareRouteSubject("pioneer-square.route.v1:pioneer-square:#dnsid:123456789012345678"), {
    channelName: "dnsid", channelId: "123456789012345678", route: "pioneer-square:#dnsid:123456789012345678",
  });
  assert.equal(parsePioneerSquareRouteSubject("pioneer-square.route.v1:pioneer-square:#dnsid:123456789012345678:extra"), undefined);
  const maxLengthRoute = `pioneer-square:#${"a".repeat(99)}:123456789012345678`;
  assert.equal(parsePioneerSquareRoute(maxLengthRoute)?.channelName.length, 99);
});
