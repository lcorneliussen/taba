// Build-time dependency acquisition only. The shipped helper never invokes npm.
import { mkdtemp, mkdir, writeFile, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { resolve, dirname, delimiter } from "node:path";
import { fileURLToPath } from "node:url";
import { spawnSync } from "node:child_process";

if (Number(process.versions.node.split(".")[0]) < 22) throw new Error("Node 22+ required for helper build verification");
const root = dirname(fileURLToPath(import.meta.url));
const lock = JSON.parse(await readFile(resolve(root, "package-lock.json"), "utf8"));
for (const [name, pkg] of Object.entries(lock.packages)) {
  if (name && (!pkg.resolved?.startsWith("https://registry.npmjs.org/") || !pkg.integrity?.startsWith("sha512-"))) throw new Error("dependency is not public/integrity-pinned");
}
const work = await mkdtemp(resolve(tmpdir(), "taba-host-build-"));
try {
  for (const name of ["home", "cache", "tmp"]) await mkdir(resolve(work, name));
  for (const name of ["user.npmrc", "global.npmrc"]) await writeFile(resolve(work, name), "registry=https://registry.npmjs.org/\nignore-scripts=true\n");
  const env = {
    HOME: resolve(work, "home"), TMPDIR: resolve(work, "tmp"),
    PATH: [dirname(process.execPath), "/usr/bin", "/bin"].join(delimiter),
    NPM_CONFIG_USERCONFIG: resolve(work, "user.npmrc"), NPM_CONFIG_GLOBALCONFIG: resolve(work, "global.npmrc"),
    NPM_CONFIG_CACHE: resolve(work, "cache"), NPM_CONFIG_REGISTRY: "https://registry.npmjs.org/",
    NPM_CONFIG_IGNORE_SCRIPTS: "true", NPM_CONFIG_AUDIT: "false", NPM_CONFIG_FUND: "false", NPM_CONFIG_UPDATE_NOTIFIER: "false",
  };
  // Use the distribution's CLI file directly, avoiding shell/profile wrappers.
  const npm = resolve(dirname(process.execPath), "../lib/node_modules/npm/bin/npm-cli.js");
  for (const args of [[npm, "ci", "--ignore-scripts", "--no-audit", "--no-fund"], ["build.mjs", "--check"], ["--test", "host.test.mjs"], ["build.mjs", "--check"]]) {
    const result = spawnSync(process.execPath, args, { cwd: root, env, stdio: "inherit", timeout: 180000 });
    if (result.status !== 0 || result.error) throw new Error("confined helper dependency/build/test check failed");
  }
} finally { await rm(work, { recursive: true, force: true }); }
