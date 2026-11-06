import { expect, test } from "@playwright/test";
import { copyFile, readFile, stat, utimes } from "node:fs/promises";
import { join } from "node:path";
import { DatabaseSync } from "node:sqlite";
import { setupE2EHarness } from "./harness";

const png = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==";

function database<T>(path: string, use: (db: DatabaseSync) => T): T {
  const db = new DatabaseSync(path);
  try { db.exec("PRAGMA busy_timeout=5000; PRAGMA foreign_keys=ON"); return use(db); }
  finally { db.close(); }
}

test("preserves referenced images through failed startup and recovery", async ({ page }) => {
  test.setTimeout(60_000);
  for (const failure of ["migration", "prune"] as const) {
    const f = await setupE2EHarness({ useProxy: true });
    try {
      await page.goto(f.frontend.url);
      const url = f.proxy!.url;
      const accepted = await page.evaluate(async ({ url, png }) => {
        const response = await fetch(`${url}/v1/messages`, { method: "POST", credentials: "omit", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ id: "saved-image", from: "image-sender", to: "image-target", body: "keep this image", created_at: "2026-01-01T00:00:00.000Z", attachments: [{ type: "image", name: "pixel.png", data_url: `data:image/png;base64,${png}` }] }) });
        return { status: response.status, body: await response.json() };
      }, { url, png });
      expect(accepted.status).toBe(201);
      const image = accepted.body.conversation_message.attachments[0];
      const imagePath = join(`${f.router.databasePath}.attachments`, image.id);
      const orphanPath = join(`${f.router.databasePath}.attachments`, "orphan.png");
      expect((await readFile(imagePath)).toString("base64")).toBe(png);
      const savedMetadata = database(f.router.databasePath, db => db.prepare("SELECT attachments_json FROM conversation_messages WHERE message_id='saved-image'").get()!.attachments_json);
      expect(JSON.parse(String(savedMetadata))[0].id).toBe(image.id);

      await expect(f.router.restart(async path => {
        // Prepare old files only after the owned router has stopped; its normal
        // sweeper would otherwise correctly remove the unreferenced fixture.
        await copyFile(imagePath, orphanPath);
        const old = new Date(Date.now() - 25 * 60 * 60 * 1000);
        await utimes(imagePath, old, old);
        await utimes(orphanPath, old, old);
        database(path, db => {
          if (failure === "migration") {
            db.exec(`DELETE FROM schema_migrations WHERE id='126_conversation_receipt_thirty_minutes';
              CREATE TRIGGER fail_receipt_startup BEFORE UPDATE ON conversation_send_receipts BEGIN SELECT RAISE(ABORT,'injected startup migration failure'); END`);
          } else {
            db.exec(`UPDATE conversation_send_receipts SET deadline_at='2000-01-01T00:00:00.000Z' WHERE message_id='saved-image';
              CREATE TRIGGER fail_receipt_startup BEFORE DELETE ON conversation_send_receipts BEGIN SELECT RAISE(ABORT,'injected startup prune failure'); END`);
          }
        });
      })).rejects.toThrow(new RegExp(`injected startup ${failure} failure`));
      expect((await readFile(imagePath)).toString("base64")).toBe(png);
      expect((await readFile(orphanPath)).toString("base64")).toBe(png);
      expect(database(f.router.databasePath, db => db.prepare("SELECT attachments_json FROM conversation_messages WHERE message_id='saved-image'").get()!.attachments_json)).toBe(savedMetadata);

      await f.router.restart(async path => database(path, db => db.exec("DROP TRIGGER fail_receipt_startup")));
      const browser = await page.evaluate(async ({ url, id }) => {
        const response = await fetch(`${url}/v1/attachments/${id}`, { credentials: "omit" });
        const bytes = new Uint8Array(await response.arrayBuffer());
        const registered = await new Promise<string>((resolve, reject) => {
          const ws = new WebSocket(`${url.replace(/^http/, "ws")}/v1/agents`);
          const timer = setTimeout(() => { ws.close(); reject(new Error("registration timed out")); }, 5000);
          ws.onerror = () => { clearTimeout(timer); reject(new Error("WebSocket failed")); };
          ws.onopen = () => ws.send(JSON.stringify({ version: "v1", frame_id: "image-register", type: "register", agent_id: "image-observer", instance_id: "image-instance", agent_role: "agent", client_type: "test", ack_confirmation: "correlated_v1", delivery_receipts: "delivery_items_v1" }));
          ws.onmessage = event => { const frame = JSON.parse(event.data); if (frame.type === "registered") { clearTimeout(timer); ws.close(); resolve(frame.type); } };
        });
        return { status: response.status, bytes: btoa(String.fromCharCode(...bytes)), registered };
      }, { url, id: image.id });
      expect(browser).toEqual({ status: 200, bytes: png, registered: "registered" });
      await expect.poll(async () => { try { await stat(orphanPath); return true; } catch { return false; } }, { timeout: 5000 }).toBe(false);
      expect((await readFile(imagePath)).toString("base64")).toBe(png);
      expect(database(f.router.databasePath, db => db.prepare("SELECT attachments_json FROM conversation_messages WHERE message_id='saved-image'").get()!.attachments_json)).toBe(savedMetadata);
      expect(f.mockLLM.getHistory()).toEqual([]);
    } finally {
      await f.close();
    }
  }
});
