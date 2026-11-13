import { build } from "esbuild";
import { readFile, writeFile, readdir, mkdir } from "node:fs/promises";
import { resolve, dirname, relative } from "node:path";
import { fileURLToPath } from "node:url";
import { createHash } from "node:crypto";

const root = dirname(fileURLToPath(import.meta.url));
const out = resolve(root, "../../internal/wtc/assets");
const check = process.argv.includes("--check");
const result = await build({
  absWorkingDir: root, entryPoints: ["entry.mjs"], bundle: true, platform: "node", format: "cjs", target: "node22",
  write: false, metafile: true, sourcemap: false, legalComments: "inline", charset: "ascii", minifyWhitespace: true,
  // Escape multiline strings rather than emitting significant trailing spaces.
  supported: { "template-literal": false },
  plugins: [{ name: "pure-js-websocket-and-legacy-rsa", setup(builder) {
    // Ship the upstream functional JS fallbacks, not native addon loaders.
    builder.onResolve({ filter: /^(bufferutil|utf-8-validate)$/ }, args => ({ path: resolve(root, "node_modules", args.path, "fallback.js") }));
    // Only older Node versions without built-in RSA key generation need this.
    builder.onResolve({ filter: /^node-rsa$/ }, args => ({ path: args.path, namespace: "unavailable-optional" }));
    builder.onLoad({ filter: /.*/, namespace: "unavailable-optional" }, () => ({ contents: 'throw new Error("Unsupported optional legacy/native dependency");', loader: "js" }));
  } }],
});
const bundle = result.outputFiles[0].contents;
// Include notices only for packages actually present in the shipped bundle.
const packages = new Set();
for (const input of Object.keys(result.metafile.inputs)) {
  const parts = input.split("/");
  const index = parts.lastIndexOf("node_modules");
  if (index >= 0) packages.add(parts.slice(0, index + (parts[index + 1].startsWith("@") ? 3 : 2)).join("/"));
}
let notices = "Third-party notices for the bundled devtunnel host\n\n";
for (const path of [...packages].sort()) {
  const directory = resolve(root, path);
  const pkg = JSON.parse(await readFile(resolve(directory, "package.json"), "utf8"));
  const entries = await readdir(directory);
  const names = entries.filter(name => /^(licen[cs]e|copying|notice)([._-]|$)/i.test(name)).sort();
  let texts = await Promise.all(names.map(name => readFile(resolve(directory, name), "utf8")));
  if (!texts.length) {
    const readme = entries.find(name => /^readme(\.md|\.txt)?$/i.test(name));
    if (readme) {
      const body = await readFile(resolve(directory, readme), "utf8");
      const sections = [...body.matchAll(/^(?:#{1,4} +licen[cs]e.*|\(The MIT License\))$/gim)];
      if (sections.length) {
        const notice = body.slice(sections.at(-1).index);
        if (/Permission is hereby granted|Permission to use, copy|Apache License/.test(notice)) texts = [notice];
      }
    }
  }
  if (!texts.length && pkg.name.startsWith("@microsoft/dev-tunnels-")) {
    // The published SDK carries the MIT header but no root LICENSE file.
    texts = [await readFile(resolve(root, "microsoft-LICENSE.txt"), "utf8")];
  }
  if (!texts.length) throw new Error("missing third-party license: " + pkg.name);
  notices += `${pkg.name}@${pkg.version} (${typeof pkg.license === "string" ? pkg.license : JSON.stringify(pkg.license)})\n${texts.join("\n").trim()}\n\n`;
}
// Normalize notice presentation whitespace to LF before hashing so rebuilds
// agree even when published notices contain mixed line endings.
notices = notices.replace(/\r\n?/g, "\n").split("\n").map(line => line.trimEnd()).join("\n").trimEnd() + "\n";
const files = { "devtunnel-host.cjs": bundle, "devtunnel-host.NOTICES.txt": Buffer.from(notices) };
files["devtunnel-host.sha256"] = Buffer.from(Object.entries(files).map(([name, bytes]) => `${createHash("sha256").update(bytes).digest("hex")}  ${name}\n`).join(""));
await mkdir(out, { recursive: true });
for (const [name, bytes] of Object.entries(files)) {
  const path = resolve(out, name);
  if (check) {
    if (!(await readFile(path)).equals(bytes)) throw new Error("embedded asset is stale: " + relative(root, path));
  } else await writeFile(path, bytes);
}
console.log(check ? "Embedded devtunnel assets match locked build" : "Built embedded devtunnel assets");
