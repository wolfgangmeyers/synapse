// No Electron download: observe the real packaging caller at its external boundary.
export async function resolve(specifier, context, nextResolve) {
  if (specifier === "@electron/packager") return { url: new URL("./packager-stub.mjs", import.meta.url).href, shortCircuit: true };
  return nextResolve(specifier, context);
}
