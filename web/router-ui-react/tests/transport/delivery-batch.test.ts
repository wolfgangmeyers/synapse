import { describe, expect, it } from "vitest";
import { prepareDelivery, type DeliveryFailure } from "../../src/transport/router-transport";
import { deliveryItem, message } from "../support/fixture-data.mjs";

describe("delivery batches", () => {
  it("pairs every message by index and uses one receipt identity", () => {
    const firstItem = deliveryItem("m1", 1);
    const items = [firstItem, deliveryItem("m2", 2)];
    const result = prepareDelivery({ type: "message", message: message("m1"), message_incarnation_id: firstItem.message_incarnation_id, attempt_number: 1, delivery_items: items, delivery_messages: [message("m1"), message("m2")] });
    expect(result.messages.map((entry) => entry.id)).toEqual(["m1", "m2"]);
    expect(result.receipt.message_id).toBe("m1"); expect(result.receipt.attempt_number).toBe(1); expect(result.receipt.delivery_items).toEqual(items);
  });

  it("rejects a mismatched index pair", () => {
    expect(() => prepareDelivery({ type: "message", message: message("m1"), message_incarnation_id: deliveryItem("m1").message_incarnation_id, attempt_number: 1, delivery_items: [deliveryItem("m1")], delivery_messages: [message("m2")] })).toThrow();
  });

  it("creates one failure receipt for a batch", () => {
    const failure: DeliveryFailure = { reason: "processing_failed" };
    const result = prepareDelivery({ type: "message", message: message("m1"), message_incarnation_id: deliveryItem("m1").message_incarnation_id, attempt_number: 1, delivery_items: [deliveryItem("m1")], delivery_messages: [message("m1")] }, failure);
    expect(result.failure?.type).toBe("nack"); expect(result.failure?.message_id).toBe("m1");
  });
});
