import { readFile } from "node:fs/promises";
import { resolve } from "node:path";

// Serve the repository's renderer, preview and lexer at the synthetic origin.
// The width checker's optional renderer file replaces only the entry module.
export async function loadControlUIFixtureAssets(renderer) {
  const files = [
    ["plugin.js", renderer ? resolve(renderer) : new URL("../dist/control-ui/v1/index.js", import.meta.url)],
    ["pr-preview.js", new URL("../dist/control-ui/v1/pr-preview.js", import.meta.url)],
    ["vendor/marked.esm.js", new URL("../dist/control-ui/v1/vendor/marked.esm.js", import.meta.url)],
  ];
  return new Map(await Promise.all(files.map(async ([name, file]) => [
    `https://demo.invalid/${name}`, await readFile(file, "utf8"),
  ])));
}

export function serveControlUIFixtureAsset(route, assets) {
  const body = assets.get(route.request().url());
  return body === undefined ? route.abort() : route.fulfill({ contentType: "text/javascript", body });
}
