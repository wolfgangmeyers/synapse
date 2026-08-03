export function messageKey(message) {
  return `${message.id}\u001f${message.message_incarnation_id || ""}`;
}

export function messageTime(message) {
  return Date.parse(message.recorded_at || message.created_at || message.createdAt || "") || Date.now();
}

export function mergeMessages(existing, incoming) {
  const byId = new Map(existing.map((message) => [messageKey(message), message]));
  for (const message of incoming) {
    const key = messageKey(message);
    byId.set(key, { ...byId.get(key), ...message });
  }
  return [...byId.values()].sort((left, right) => (left.sequence ?? 0) - (right.sequence ?? 0) || messageTime(left) - messageTime(right));
}

export function reconcileHistoryMessages(existing, snapshot, highWaterSequence) {
  const snapshotKeys = new Set(snapshot.map(messageKey));
  const retainedLive = existing.filter((message) => {
    const sequence = Number(message.sequence || 0);
    return sequence === 0 || sequence > highWaterSequence || snapshotKeys.has(messageKey(message));
  });
  return mergeMessages(retainedLive, snapshot);
}
