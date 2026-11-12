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
const module = await readFile(rendererPath ? resolve(rendererPath) : new URL("../dist/control-ui/v1/index.js", import.meta.url), "utf8");
const template = await readFile(new URL("tunnel-menu-fixture.html", import.meta.url), "utf8");
const names = ["component-library-workbench", "system-overview-documentation"];
const scenarios = {
  idle: names.map(name => ({ repo: name, owner: "example-org", forge: "bitbucket", sessionKey: `demo:${name}`, branch: "main", base: "origin/main", ahead: 0, behind: 0 })),
  facts: names.map((name, i) => ({ repo: name, owner: "example-org", forge: "bitbucket", sessionKey: `demo:${name}`, branch: "feature/improve-search", base: "origin/main", ahead: 2, unpublished: 2, behind: i + 1, dirty: i + 1 })),
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
      : route.request().url() === "https://demo.invalid/plugin.js"
        ? route.fulfill({ contentType: "text/javascript", body: module }) : route.abort());
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
    assert.deepEqual(metrics.map(row => row.text), names);
    if (width >= 1037) for (const row of metrics) assert(row.width + 1 >= row.textWidth, `${scenario}/${view}/${width}: ${row.text} needlessly clipped (${row.width}/${row.textWidth})`);
    for (let i = 0; i < metrics.length; i++) {
      const label = labels.nth(i);
      await label.waitFor({ state: "visible" });
      const before = await label.boundingBox();
      await label.hover();
      assert.equal(await label.getAttribute("title"), `example-org/${names[i]}`);
      assert.deepEqual(await label.boundingBox(), before, "hover must not change layout");
      await label.focus();
      assert.deepEqual(await label.boundingBox(), before, "focus must not change layout");
    }
    assert.equal(await root.evaluate(node => node.scrollWidth > node.clientWidth + 1), false, `${scenario}/${view}/${width}: view overflow`);
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
    assert.deepEqual(errors, []);
    await page.evaluate(() => document.activeElement?.blur());
    await page.mouse.move(0, 0);
    await root.screenshot({ path: `${out}/${scenario}-${view}-${width}.png` });
    results.push({ scenario, view, width, metrics });
    await page.close();
  }
  await writeFile(`${out}/results.json`, JSON.stringify(results, null, 2));
  console.log(`${results.length} repository width/hover scenarios passed`);
} finally { await browser.close(); }
