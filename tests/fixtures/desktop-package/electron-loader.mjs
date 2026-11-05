// Substitute only the external Electron boundary; execute the shipped main/config unchanged.
export async function resolve(specifier, context, nextResolve) {
  if (specifier === "electron") return { url: new URL("./electron-stub.mjs", import.meta.url).href, shortCircuit: true };
  return nextResolve(specifier, context);
}
