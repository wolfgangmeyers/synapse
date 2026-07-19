const STORE_ID = /^[a-z0-9][a-z0-9-]{0,63}$/;

export function createRecallState(environment = process.env) {
  // Consume launcher selection once so extension reloads start disabled with no stores.
  const initial = environment.MECHA_RECALL_STORES;
  delete environment.MECHA_RECALL_STORES;
  let selected = initial ? validateRecallStores(initial.split(",")) : undefined;
  let stores;
  return {
    enable(value) { selected = validateRecallStores(value ?? ["default"]); stores = selected; return stores; },
    disable() { stores = undefined; selected = undefined; },
    status() { return { enabled: stores !== undefined, stores: stores ?? selected ?? [] }; },
  };
}

function validateRecallStores(stores) {
  if (!Array.isArray(stores) || stores.length === 0 || new Set(stores).size !== stores.length || stores.some((store) => store !== "default" && !STORE_ID.test(store))) throw new Error("mecha_recall requires a complete valid store selection");
  return [...stores];
}
