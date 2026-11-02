import { createRequire } from "node:module";
import { pathToFileURL } from "node:url";
import { writeFile } from "node:fs/promises";
import { files } from "./fixture.mjs";
export async function packager(options) {
  // Exercise installed Packager's real metadata phase, never its download/build phase.
  const { getMetadataFromPackageJSON } = await import(new URL("./infer.js", pathToFileURL(createRequire(import.meta.url).resolve("@electron/packager"))));
  await getMetadataFromPackageJSON([options.platform], options, options.dir);
  await writeFile(process.env.DESKTOP_CAPTURE, JSON.stringify({ options, files: await files(options.dir) }));
  if (process.env.DESKTOP_PACKAGE_FAIL === "1") throw new Error("synthetic packager failure");
  return ["synthetic.app"];
}
