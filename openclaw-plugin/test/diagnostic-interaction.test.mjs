// Optional real Chromium regression, sharing the existing synthetic host.
// WTC_TEST_PLAYWRIGHT=/path/to/playwright/index.mjs node --test openclaw-plugin/test/diagnostic-interaction.test.mjs
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFile, mkdir } from "node:fs/promises";
import { resolve } from "node:path";
import { pathToFileURL } from "node:url";
import { loadControlUIFixtureAssets, serveControlUIFixtureAsset } from "../tools/control-ui-fixture-assets.mjs";

const playwrightPath = process.env.WTC_TEST_PLAYWRIGHT;
const error = "Synthetic diagnostic: <img src=x onerror=alert(1)>\n" + "full error detail ".repeat(300) + "END";
for (const width of [320, 390, 1440]) test(`diagnostic browser interaction at ${width}px`, { skip: !playwrightPath, timeout:20000 }, async () => {
  const { chromium } = await import(pathToFileURL(resolve(playwrightPath)).href);
  const browser = await chromium.launch({ headless:true });
  try {
    const assets = await loadControlUIFixtureAssets(process.env.WTC_TEST_RENDERER);
    let html = await readFile(new URL("../tools/tunnel-menu-fixture.html", import.meta.url), "utf8");
    html = html.replace("const request = async (method, params) => {", `
      window.polls=[];const interval=window.setInterval;
      window.setInterval=(fn,ms)=>{if(ms===30000)window.polls.push(fn);return interval(fn,ms)};
      window.copied=[];window.denied=false;
      Object.defineProperty(navigator,'clipboard',{value:{writeText:async text=>{if(window.denied)throw Error('denied');window.copied.push(text)}},configurable:true});
      window.fixture=data;window.fixtureError=null;window.delayed=false;window.suggest=false;
      const request = async (method, params) => {
        if(method==='plugins.sessionAction'&&params.actionId==='suggest')return {ok:true,result:{project:'example',repos:['api'],source:'match'}};
        if(method==='chat.history'&&window.suggest)return {messages:[{role:'user',content:'Synthetic request to work on api'}]};
        if(method==='sessions.send')return {};
        if(method==='plugins.sessionAction'&&params.actionId==='collection'&&window.fixtureError){
          if(window.delayed)await new Promise(resolve=>window.releaseRefresh=resolve);
          return {ok:false,error:window.fixtureError};
        }
    `).replace("plugin.activate(host);", "window.demoCtx=ctx;plugin.activate(host);")
      .replace("mounts.card(document.querySelector('#card'),ctx);", "window.cardView=mounts.card(document.querySelector('#card'),ctx);")
      .replace("</style>", "body{padding:12px}#stage>h1,#stage>p,#stage>footer{display:none}</style>");
    const page = await browser.newPage({ viewport:{ width, height:850 } });
    page.setDefaultTimeout(5000);
    const errors = []; page.on("pageerror", e => errors.push(e.message));
    await page.route("**/*", route => route.request().url() === "https://demo.invalid/"
      ? route.fulfill({ contentType:"text/html", body:html }) : serveControlUIFixtureAsset(route, assets));
    await page.goto("https://demo.invalid/");
    await page.locator(".wtc-refresh-control").waitFor();
    await page.evaluate(error => { window.fixtureError=error; }, error);
    await page.locator(".wtc-refresh-control").click();
    const diagnostic = page.locator(".wtc-header-feedback");
    await diagnostic.waitFor({ state:"attached" });
    const output = process.env.WTC_TEST_SCREENSHOTS;
    if (output) { await mkdir(output,{recursive:true});await page.screenshot({path:resolve(output,`diagnostic-${width}.png`)}); }
    const card = page.locator("#card details");
    assert.equal(await card.evaluate(n => n.open), false, "fixture starts collapsed");
    assert(await diagnostic.isVisible(), `diagnostic is available on a collapsed card at ${width}px`);
    await diagnostic.hover(); await diagnostic.focus();
    await diagnostic.evaluate(n => { window.originalDiagnostic=n;window.originalTitle=n.title; });
    await page.evaluate(() => { window.delayed=true;window.polls[0](); });
    await page.waitForFunction(() => !!window.releaseRefresh);
    assert(await diagnostic.evaluate(n => n===window.originalDiagnostic && n.matches(":hover") && document.activeElement===n));
    await page.evaluate(() => window.releaseRefresh());
    await page.waitForFunction(() => document.querySelector('.wtc-refresh-control')?.getAttribute('aria-busy')==='false');
    assert(await diagnostic.evaluate(n => n===window.originalDiagnostic && n.title===window.originalTitle && n.matches(":hover") && document.activeElement===n));
    await page.evaluate(() => {
      for(let i=0;i<5;i++)window.cardView.update(window.demoCtx);
      window.fixture.updatedAt=Date.now();window.fixtureError=null;window.delayed=false;window.polls[0]();
    });
    await page.waitForFunction(() => document.querySelector('.wtc-refresh-control')?.getAttribute('aria-busy')==='false');
    assert(await diagnostic.evaluate(n => n===window.originalDiagnostic && document.activeElement===n));
    await diagnostic.hover(); await diagnostic.focus();
    await diagnostic.evaluate(n => { window.originalTitle=n.title; });
    await page.evaluate(() => {window.fixture.updatedAt++;window.fixture.repos[1].dirty=7;window.polls[0]();});
    await page.waitForFunction(() => document.querySelector('.wtc-refresh-control')?.getAttribute('aria-busy')==='false');
    assert(await diagnostic.evaluate(n => n===window.originalDiagnostic && n.title===window.originalTitle && n.matches(":hover") && document.activeElement===n), "irrelevant timestamps and repository updates preserve diagnostic interaction");
    assert.match(await page.locator("#card .wtc-local-change").last().textContent(), /7/, "repository facts still update");
    assert.deepEqual(await page.evaluate(() => window.copied), [], "refresh does not copy");
    // The last manual refresh note remains after status recovers; restore the error.
    await page.evaluate(error => {window.fixtureError=error;window.polls[0]();}, error);
    await page.waitForFunction(error => document.querySelector('.wtc-header-feedback').title.startsWith('Refresh failed: '+error), error);
    await diagnostic.focus(); await page.keyboard.press("Enter");
    await page.waitForFunction(() => window.copied.length===1);
    await page.keyboard.press("Space"); await page.waitForFunction(() => window.copied.length===2);
    assert.deepEqual(await page.evaluate(() => window.copied), Array(2).fill("Refresh failed: " + error));
    assert(await diagnostic.evaluate(n => document.activeElement===n));
    assert.equal(await page.locator("#card img").count(), 0, "diagnostic payload is text only");
    await page.evaluate(error=>{window.fixtureError=error+" Updated synthetic context";window.polls[0]();},error);
    await page.waitForFunction(()=>document.querySelector('.wtc-header-feedback').title.includes('Updated synthetic context'));
    await page.evaluate(error=>{window.fixtureError=error;window.polls[0]();},error);
    await page.waitForFunction(error=>document.querySelector('.wtc-header-feedback').title==='Refresh failed: '+error+' — Copy full diagnostic',error);
    await page.evaluate(() => {
      const status=document.querySelector('[role="status"][aria-live="polite"]');
      const exposed=()=>{for(let n=status;n;n=n.parentElement){const s=getComputedStyle(n);if(n.hidden||n.getAttribute('aria-hidden')==='true'||s.display==='none'||s.visibility==='hidden')return false;}return status.isConnected;};
      window.statusInitiallyExposed=exposed();window.statusMutations=[];
      const text=Object.getOwnPropertyDescriptor(Node.prototype,'textContent');
      Object.defineProperty(status,'textContent',{get(){return text.get.call(this)},set(value){if(value)window.statusMutations.push({text:value,exposed:exposed()});text.set.call(this,value)}});
      window.denied=true;
    });
    await diagnostic.focus(); await page.keyboard.press("Enter");
    const manual = page.getByRole("textbox",{name:"Full diagnostic for manual copy"});
    await manual.waitFor(); assert.equal(await manual.inputValue(), "Refresh failed: " + error);
    assert.deepEqual(await page.evaluate(()=>window.statusMutations), [{text:"Could not copy. Select the full diagnostic below and copy it manually.",exposed:true}], "denial updates an already exposed status");
    assert(await page.evaluate(()=>window.statusInitiallyExposed), "live status was exposed before activation");
    assert(await diagnostic.evaluate(n=>document.activeElement===n), "failure preserves keyboard focus");
    assert.equal(await page.getByRole("status").filter({hasText:"Could not copy"}).count(),1);
    await page.evaluate(()=>Object.defineProperty(navigator,'clipboard',{value:undefined,configurable:true}));
    await page.keyboard.press("Space");
    assert.deepEqual(await page.evaluate(()=>window.statusMutations), Array(2).fill({text:"Could not copy. Select the full diagnostic below and copy it manually.",exposed:true}), "unavailable clipboard uses the same exposed status");
    assert.equal(await manual.inputValue(), "Refresh failed: " + error);
    for(let i=0;i<12 && !await manual.evaluate(n=>document.activeElement===n);i++) await page.keyboard.press("Tab");
    assert(await manual.evaluate(n=>document.activeElement===n), "manual fallback is reachable by keyboard");
    await page.evaluate(()=>Object.defineProperty(navigator,'clipboard',{value:{writeText:async text=>window.copied.push(text)},configurable:true}));
    assert.equal(await card.evaluate(n => n.open), false, "copy does not expand the card");
    await card.evaluate(n => { n.open=true; });
    assert(await manual.isVisible(), "manual copy remains visible on an expanded card");
    await card.evaluate(n => { n.open=false; });
    assert(await manual.isVisible(), "manual copy remains visible on a collapsed card");
    assert.equal(await manual.getAttribute("readonly"), "");
    if (output) await page.screenshot({path:resolve(output,`diagnostic-${width}-fallback.png`)});
    await manual.focus(); await page.keyboard.press("ControlOrMeta+A");
    assert.equal(await manual.evaluate(n => n.selectionEnd-n.selectionStart), ("Refresh failed: " + error).length);
    assert.equal(await page.getByText("Copied",{exact:true}).count(),0);
    const box = await diagnostic.boundingBox(); assert(box.x>=0 && box.x+box.width<=width, "copy target is within viewport");
    await page.getByRole("button",{name:"More collection actions",exact:true}).click();
    await page.getByRole("menuitem",{name:"Activity",exact:true}).click();
    await page.locator("#panel").waitFor({state:"visible"});
    await page.evaluate(() => {
      document.querySelector('#card').hidden=false;document.querySelector('#panel').hidden=true;
      window.fixtureError=null;window.fixture.repos=[];window.suggest=true;
      window.cardView.update({...window.demoCtx,props:{...window.demoCtx.props,sessionKey:'demo:suggestion'}});
    });
    await page.getByRole('button',{name:'Start',exact:true}).waitFor();
    await page.getByRole('button',{name:'Start',exact:true}).click();
    await page.evaluate(() => {
      window.fixture.repos=[{repo:'api',owner:'example',forge:'github',sessionKey:'demo:api',branch:'main',base:'origin/main',ahead:0,behind:0}];
      window.suggest=false;window.polls.at(-1)();
    });
    try { await page.locator('#card .wtc-refresh-control').waitFor(); }
    catch (err) { assert.deepEqual(errors, [], 'suggestion-to-collection must not throw'); throw err; }
    assert(await page.getByRole('button',{name:'Open the taba Collection panel',exact:true}).isVisible(), "suggestion-to-collection restores header controls");
    await page.evaluate(error => {window.fixtureError=error;window.denied=false;window.copied=[];window.polls.at(-1)();}, error);
    await diagnostic.waitFor(); await diagnostic.focus(); await page.keyboard.press('Enter');
    await page.waitForFunction(() => window.copied.length===1);
    assert.deepEqual(await page.evaluate(() => window.copied), ["Refresh failed: " + error]);
    assert.deepEqual(errors, []);
  } finally { await browser.close(); }
});
