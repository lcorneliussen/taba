import { loadControlUIFixtureAssets, serveControlUIFixtureAsset } from "./control-ui-fixture-assets.mjs";
import { resolve } from "node:path";
import { pathToFileURL } from "node:url";
import { readFile, writeFile, mkdir } from "node:fs/promises";
import assert from "node:assert/strict";

// Synthetic card/sidebar version and baseline checks; no gateway is contacted.
// Usage: node openclaw-plugin/tools/check-version-labels.mjs <playwright module> <output directory>
const [playwrightPath, outputPath] = process.argv.slice(2);
if (!playwrightPath || !outputPath) throw Error("Pass a Playwright module and output directory");
const { chromium } = await import(pathToFileURL(resolve(playwrightPath)).href);
const out = resolve(outputPath);
await mkdir(out, { recursive: true });
const assets = await loadControlUIFixtureAssets();
const template = await readFile(new URL("tunnel-menu-fixture.html", import.meta.url), "utf8");
const versions = {
  release: { plugin: "0.2.4", wtc: "0.2.5" },
  development: { plugin: "0.2.5-dev.18+dev.g1234567", wtc: "0.2.5-dev.19+dev.gabcdef0" },
  unknown: { plugin: "0.2.5", wtc: null },
  failed: { plugin: "0.2.5", wtc: "0.2.5" },
};
const browser = await chromium.launch({ headless: true }), results = [];
try {
  for (const [scenario, version] of Object.entries(versions)) for (const theme of ["dark", "light"]) for (const width of [320, 390, 760, 1100]) for (const view of ["collapsed", "expanded", "panel"]) {
    const palette = theme === "light"
      ? "--bg:#fff;--card:#f8f9fb;--text:#333;--text-strong:#111;--muted:#63636d;--border:#ddd;--border-strong:#aaa"
      : "--bg:#0e1015;--card:#161920;--text:#bcbcc0;--text-strong:#f4f4f5;--muted:#8b8b94;--border:#252830;--border-strong:#404450";
    let html = template.replace(" collection:", ` version:${JSON.stringify(version)},\n collection:`)
      .replace("</style>", `:root{color-scheme:${theme};${palette}}body{padding:16px;font:14px/1.55 system-ui,sans-serif}#stage>h1,#stage>p,#stage>footer,.composer{display:none}#panel{min-height:0}</style>`);
    if (scenario === "failed") html = html.replace(/ repos:\[.*\],/, ' repos:[repo("api",{error:"checkout unavailable",recovery:{state:"restorable",key:"demo-api"}}),repo("web",{error:"checkout unavailable",recovery:{state:"restorable",key:"demo-web"}})],')
      .replace(/ services:\[.*\]/, ' services:[],subSessions:[{name:"retained-task",collection:"retained-task",repos:[repo("api",{mergeError:"Comparison checkout unavailable",toMerge:null})]}]')
      .replace("const request = async (method, params) => {", 'const request = async (method, params) => {\n if(method==="plugins.sessionAction" && params.actionId==="restore-checkout") return {ok:false,error:"Synthetic restore blocked for inspection"};');
    const page = await browser.newPage({ viewport: { width, height: 1000 }, deviceScaleFactor: 2 }), errors = [];
    page.on("pageerror", e => errors.push(e.message));
    await page.route("**/*", route => route.request().url() === "https://demo.invalid/"
      ? route.fulfill({ contentType: "text/html", body: html }) : serveControlUIFixtureAsset(route, assets));
    await page.goto("https://demo.invalid/");
    await page.locator("#card details").waitFor();
    if (scenario === "failed") {
      await page.locator("#card summary .wtc-restore-control").click();
      await page.waitForFunction(() => document.querySelector("#card summary")?.textContent.includes("Failed"));
    }
    if (view === "panel") await page.getByRole("button", { name: /Open (the|Activity in the) taba Collection panel/ }).click();
    else await page.locator("#card details").evaluate((n, open) => { n.open = open; }, view === "expanded");
    await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
    const root = page.locator(view === "panel" ? "#panel" : "#card details");
    const metrics = await root.evaluate(root => {
      const badge = root.querySelector(".wtc-brand-version"), brand = badge.closest(".wtc-brand"), anchor = brand.querySelector("a");
      const baseline = n => { const p = document.createElement("span"); p.style.cssText = "display:inline-block;width:0;height:0;vertical-align:baseline"; n.append(p); const y = p.getBoundingClientRect().top; p.remove(); return y; };
      const box = badge.getBoundingClientRect(), bounds = root.getBoundingClientRect(), controls = root.querySelector(".wtc-header-controls")?.getBoundingClientRect();
      const slot = root.querySelector(".wtc-toolbar-primary"), action = slot?.querySelector(".wtc-action-control");
      const primary = action ? { slotWidth: slot.getBoundingClientRect().width, buttonWidth: action.getBoundingClientRect().width, title: action.title, text: action.textContent } : null;
      const word = anchor.querySelector(".wtc-brand-word");
      const baselineError = getComputedStyle(word).display === "none" ? null : Math.abs(baseline(badge.firstElementChild) - baseline(word));
      return { text: badge.textContent, title: badge.title, baselineError, primary, headerHeight: root.querySelector("summary")?.getBoundingClientRect().height, wordVisible: getComputedStyle(word).display !== "none", headerChildren: [...(root.querySelector("summary")?.children ?? [])].map(n => ({className:n.className,display:getComputedStyle(n).display,width:n.getBoundingClientRect().width})), brandWidth:brand.getBoundingClientRect().width, badge: box.toJSON(), root: bounds.toJSON(), controls: controls?.toJSON(), clipped: badge.firstElementChild.scrollWidth > badge.firstElementChild.clientWidth };
    });
    assert.deepEqual(errors, []);
    assert.equal(metrics.text, version.wtc ? `v${version.wtc}` : "version ?");
    assert.ok(metrics.title.includes(`Taba ${version.wtc || "unknown"}`));
    assert.ok(metrics.title.includes(`plugin: ${version.plugin}`));
    assert.ok(metrics.badge.width > 0 && metrics.badge.height > 0);
    assert.equal(metrics.wordVisible, view === "panel" || width > 320, "only the narrowest card uses logo and version without the wordmark");
    assert.ok(metrics.baselineError === null || metrics.baselineError <= .5, `version baseline differs: ${JSON.stringify({scenario,theme,width,view,...metrics})}`);
    assert.ok(metrics.badge.x >= metrics.root.x && metrics.badge.right <= metrics.root.right + .5);
    if (scenario === "release" || scenario === "failed") assert.equal(metrics.clipped, false, "release number must remain readable");
    if (metrics.controls) {
      assert.ok(metrics.controls.right <= metrics.root.right + .5, "version displaced controls outside the card");
      assert.ok(metrics.badge.right <= metrics.controls.x + .5, `version overlaps controls: ${JSON.stringify({scenario,theme,width,view,...metrics})}`);
      assert.ok(metrics.controls.y < metrics.badge.bottom && metrics.badge.y < metrics.controls.bottom, `version wrapped controls: ${JSON.stringify({scenario,theme,width,view,...metrics})}`);
    }
    assert.ok(metrics.primary?.title?.trim(), "primary action details must remain available on hover");
    assert.ok(metrics.primary.buttonWidth <= metrics.primary.slotWidth + .5, "primary action exceeds its reserved slot");
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
    await root.screenshot({ path: resolve(out, `${scenario}-${theme}-${width}-${view}.png`), animations: "disabled" });
    results.push({ scenario, theme, width, view, ...metrics });
    await page.close();
  }
  for (const scenario of Object.keys(versions)) for (const theme of ["dark", "light"]) {
    const card = width => results.find(r => r.scenario === scenario && r.theme === theme && r.width === width && r.view === "collapsed");
    assert.ok(card(320).primary.slotWidth < card(390).primary.slotWidth && card(390).primary.slotWidth < card(760).primary.slotWidth, "primary slots must compact across the narrow layout bands");
    for (const width of [320, 390, 760, 1100]) {
      const expanded = results.find(r => r.scenario === scenario && r.theme === theme && r.width === width && r.view === "expanded");
      assert.equal(expanded.primary.slotWidth, card(width).primary.slotWidth, "expanding the card must not resize its primary slot");
      assert.equal(expanded.headerHeight, card(width).headerHeight, "expanding the card must not change the header height");
    }
  }
  await writeFile(resolve(out, "results.json"), JSON.stringify(results, null, 2));
  console.log(`${results.length} version visibility/baseline cases passed`);
} finally { await browser.close(); }
