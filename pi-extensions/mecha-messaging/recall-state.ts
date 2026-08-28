const STORE_ID = /^[a-z0-9][a-z0-9-]{0,63}$/;
type RecallEnvironment = { MECHA_RECALL_STORES?: string };
type RecallState = {
  enable(value?: string[]): string[];
  disable(): void;
  status(): { enabled: boolean; stores: string[] };
};

export function createRecallState(environment: RecallEnvironment = process.env): RecallState {
  // Consume launcher selection once so extension reloads start disabled with no stores.
  const initial = environment.MECHA_RECALL_STORES;
  delete environment.MECHA_RECALL_STORES;
  let selected: string[] | undefined = initial ? validateRecallStores(initial.split(",")) : undefined;
  let stores: string[] | undefined;
  return {
    enable(value) { selected = validateRecallStores(value ?? ["default"]); stores = selected; return stores; },
    disable() { stores = undefined; selected = undefined; },
    status() { return { enabled: stores !== undefined, stores: stores ?? selected ?? [] }; },
  };
}

function validateRecallStores(stores: unknown): string[] {
  if (!Array.isArray(stores)) throw new Error("mecha_recall requires a complete valid store selection");
  const validStores = stores.filter((store): store is string => typeof store === "string");
  if (validStores.length !== stores.length || validStores.length === 0
    || new Set(validStores).size !== validStores.length
    || validStores.some((store) => store !== "default" && !STORE_ID.test(store))) {
    throw new Error("mecha_recall requires a complete valid store selection");
  }
  return [...validStores];
}
