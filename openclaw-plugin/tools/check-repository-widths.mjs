import { loadControlUIFixtureAssets, serveControlUIFixtureAsset } from "./control-ui-fixture-assets.mjs";
// Synthetic card/sidebar geometry checks; no gateway or account is contacted.
// Usage: node openclaw-plugin/tools/check-repository-widths.mjs <playwright module> <output directory> [renderer file]
import { resolve } from "node:path";
import { pathToFileURL } from "node:url";
import { readFile, writeFile, mkdir } from "node:fs/promises";
import assert from "node:assert/strict";

const [playwrightPath, outputPath, rendererPath] = process.argv.slice(2);
if (!playwrightPath || !outputPath) throw Error("Pass a Playwright module and output directory");
const { chromium } = await import(pathToFileURL(resolve(playwrightPath)).href);
const out = resolve(outputPath);
await mkdir(out, { recursive: true });
const assets = await loadControlUIFixtureAssets(rendererPath);
const template = await readFile(new URL("tunnel-menu-fixture.html", import.meta.url), "utf8");
const names = ["component-library-workbench", "system-overview-documentation"];
const scenarios = {
  idle: names.map(name => ({ repo: name, owner: "example-org", forge: "bitbucket", sessionKey: `demo:${name}`, branch: "main", base: "origin/main", ahead: 0, behind: 0 })),
  facts: names.map((name, i) => ({ repo: name, owner: "example-org", forge: "bitbucket", sessionKey: `demo:${name}`, branch: "feature/improve-search", base: "origin/main", ahead: 2, unpublished: 2, behind: i + 1, dirty: i + 1 })),
  catchup: names.map((name, i) => ({ repo: name, owner: "example-org", forge: "bitbucket", sessionKey: `demo:${name}`, branch: "feature/improve-search", base: "origin/main", ahead: 0, behind: i + 1 })),
  extreme: [{ repo: `component-${"x".repeat(70)}`, owner: "example-org", forge: "bitbucket", sessionKey: "demo:long", branch: "feature/improve-search", base: "origin/main", ahead: 0, behind: 0 }],
};
const browser = await chromium.launch({ headless: true });
const results = [];
try {
  for (const [scenario, repos] of Object.entries(scenarios)) for (const view of ["card", "panel"]) for (const width of [320, 390, 760, 1037, 1440]) {
    const html = template.replace(/ repos:\[.*\],/, ` repos:${JSON.stringify(repos)},`)
      .replace(/ services:\[.*\]/, " services:[]")
      .replace("</style>", ":root{--bg:#0e1015;--card:#161920;--text:#bcbcc0;--text-strong:#f4f4f5;--muted:#8b8b94;--border:#252830}body{padding:16px;font:14px/1.55 system-ui,sans-serif}#stage>h1,#stage>p,#stage>footer,.composer{display:none}#panel{min-height:0}</style>");
    const page = await browser.newPage({ viewport: { width, height: 1000 }, deviceScaleFactor: 2 });
    const errors = [];
    page.on("pageerror", error => errors.push(error.message));
    await page.route("**/*", route => route.request().url() === "https://demo.invalid/"
      ? route.fulfill({ contentType: "text/html", body: html })
      : serveControlUIFixtureAsset(route, assets));
    await page.goto("https://demo.invalid/");
    await page.locator("#card details").waitFor();
    if (view === "card") await page.locator("#card details").evaluate(node => node.open = true);
    else await page.getByRole("button", { name: "Open the taba Collection panel", exact: true }).click();
    const root = page.locator(view === "card" ? "#card details" : "#panel");
    const labels = root.locator(".wtc-repo-label");
    await labels.first().waitFor();
    await page.evaluate(() => document.fonts.ready);
    await page.waitForTimeout(100);
    const metrics = await labels.evaluateAll(nodes => nodes.map(node => {
      const text = document.createRange();
      text.selectNodeContents(node);
      return { text: node.textContent, title: node.title, width: node.getBoundingClientRect().width, textWidth: text.getBoundingClientRect().width, parentTitle: node.parentElement.title };
    }));
    await root.screenshot({ path: `${out}/${scenario}-${view}-${width}.png` });
    assert.deepEqual(metrics.map(row => row.text), repos.map(repo => repo.repo));
    if (width >= 1037 && scenario !== "extreme") for (const row of metrics) assert(row.width + 1 >= row.textWidth, `${scenario}/${view}/${width}: ${row.text} needlessly clipped (${row.width}/${row.textWidth})`);
    if (width >= 1037 && scenario === "extreme") {
      assert(metrics[0].width < metrics[0].textWidth, "extreme names truncate instead of taking the whole row");
      assert(await root.locator(".wtc-repo-branch").evaluate(node => node.getBoundingClientRect().width > 100), "extreme names leave usable branch space");
    }
    for (let i = 0; i < metrics.length; i++) {
      const label = labels.nth(i);
      await label.waitFor({ state: "visible" });
      const before = await label.boundingBox();
      await label.hover();
      assert.equal(await label.getAttribute("title"), `example-org/${repos[i].repo}`);
      assert.deepEqual(await label.boundingBox(), before, "hover must not change layout");
      await label.focus();
      assert.deepEqual(await label.boundingBox(), before, "focus must not change layout");
    }
    assert.equal(await root.evaluate(node => node.scrollWidth > node.clientWidth + 1), false, `${scenario}/${view}/${width}: view overflow`);
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
    if (scenario === "catchup") {
      const branches = root.locator(".wtc-repo-branch");
      const before = await branches.evaluateAll(nodes => nodes.map(node => node.getBoundingClientRect().x));
      await root.locator('button[title^="2 new on main."]').click();
      await root.locator('.wtc-repo-warning button').waitFor();
      const after = await branches.evaluateAll(nodes => nodes.map(node => node.getBoundingClientRect().x));
      if (width >= 760) assert.deepEqual(after, before, "an action error must not shift shared branch columns");
      await root.screenshot({ path: `${out}/${scenario}-${view}-${width}-failure.png` });
    }
    assert.deepEqual(errors, []);
    await page.evaluate(() => document.activeElement?.blur());
    await page.mouse.move(0, 0);
    results.push({ scenario, view, width, metrics });
    await page.close();
  }
  await writeFile(`${out}/results.json`, JSON.stringify(results, null, 2));
  console.log(`${results.length} repository width/hover scenarios passed`);
} finally { await browser.close(); }
