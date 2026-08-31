export function nextEvent(target, type) {
  return new Promise((resolve) => target.addEventListener(type, resolve, { once: true }));
}

export async function flushPromises() {
  await Promise.resolve();
  await Promise.resolve();
}
