import test from "node:test";
import assert from "node:assert/strict";

const PRELOAD_PATH = new URL("./test-preload-mock-fetch.mjs", import.meta.url).pathname;

// Import preload to install global mock routing and egress guards
await import(PRELOAD_PATH);

test("OAuth fixture: valid authorization_code returns HTTP 200 with tokens", async () => {
  const res = await fetch("https://oauth2.googleapis.com/token", {
    method: "POST",
    headers: { "Content-Type": "application/x-www-form-urlencoded" },
    body: "code=valid-auth-code&grant_type=authorization_code&client_id=cid&redirect_uri=ruri",
  });
  assert.equal(res.status, 200);
  const data = await res.json();
  assert.equal(data.token_type, "Bearer");
  assert.equal(data.access_token, "synthetic-antigravity-access-token");
  assert.equal(data.refresh_token, "synthetic-antigravity-refresh-token");
});

test("OAuth fixture: valid refresh_token returns HTTP 200 with refreshed access token", async () => {
  const res = await fetch("https://oauth2.googleapis.com/token", {
    method: "POST",
    headers: { "Content-Type": "application/x-www-form-urlencoded" },
    body: "grant_type=refresh_token&refresh_token=valid-refresh-token&client_id=cid&client_secret=csec",
  });
  assert.equal(res.status, 200);
  const data = await res.json();
  assert.equal(data.token_type, "Bearer");
  assert.equal(data.access_token, "synthetic-refreshed-antigravity-access-token");
  assert.equal(data.refresh_token, "synthetic-antigravity-refresh-token");
});

test("OAuth fixture: unsupported grant_type returns HTTP 400", async () => {
  const res = await fetch("https://oauth2.googleapis.com/token", {
    method: "POST",
    headers: { "Content-Type": "application/x-www-form-urlencoded" },
    body: "grant_type=client_credentials&client_id=cid",
  });
  assert.equal(res.status, 400);
  const data = await res.json();
  assert.equal(data.error, "unsupported_grant_type");
});

test("OAuth fixture: empty body returns HTTP 400", async () => {
  const res = await fetch("https://oauth2.googleapis.com/token", {
    method: "POST",
    headers: { "Content-Type": "application/x-www-form-urlencoded" },
    body: "",
  });
  assert.equal(res.status, 400);
  const data = await res.json();
  assert.equal(data.error, "invalid_request");
});

test("OAuth fixture: JSON payload without grant_type returns HTTP 400", async () => {
  const res = await fetch("https://oauth2.googleapis.com/token", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ grant: "password", user: "foo" }),
  });
  assert.equal(res.status, 400);
  const data = await res.json();
  assert.equal(data.error, "invalid_request");
});

test("OAuth fixture: unrecognized form parameters return HTTP 400", async () => {
  const res = await fetch("https://oauth2.googleapis.com/token", {
    method: "POST",
    headers: { "Content-Type": "application/x-www-form-urlencoded" },
    body: "foo=bar&baz=qux",
  });
  assert.equal(res.status, 400);
  const data = await res.json();
  assert.equal(data.error, "invalid_request");
});

test("OAuth fixture: invalid authorization_code returns HTTP 400 invalid_grant", async () => {
  const res = await fetch("https://oauth2.googleapis.com/token", {
    method: "POST",
    headers: { "Content-Type": "application/x-www-form-urlencoded" },
    body: "grant_type=authorization_code&code=invalid-code",
  });
  assert.equal(res.status, 400);
  const data = await res.json();
  assert.equal(data.error, "invalid_grant");
});

test("OAuth fixture: expired refresh_token returns HTTP 400 invalid_grant", async () => {
  const res = await fetch("https://oauth2.googleapis.com/token", {
    method: "POST",
    headers: { "Content-Type": "application/x-www-form-urlencoded" },
    body: "grant_type=refresh_token&refresh_token=expired-refresh-token",
  });
  assert.equal(res.status, 400);
  const data = await res.json();
  assert.equal(data.error, "invalid_grant");
});
