// Synthetic Chromium geometry and action checks; no gateway or forge is contacted.
// Install Playwright/Chromium separately and pass its module path plus an output directory.
import {readFile,writeFile,mkdir} from 'node:fs/promises';
import assert from 'node:assert/strict';
import {resolve} from 'node:path';
import {pathToFileURL} from 'node:url';
const [playwrightPath, outputPath] = process.argv.slice(2);
if (!playwrightPath || !outputPath) throw Error('Usage: check-fact-cells.mjs <playwright module> <output directory>');
const {chromium} = await import(pathToFileURL(resolve(playwrightPath)).href);
const source=new URL('../',import.meta.url),out=pathToFileURL(resolve(outputPath)+'/');await mkdir(out,{recursive:true});
const module=await readFile(new URL('dist/control-ui/v1/index.js',source),'utf8');
const repo=(name,extra={})=>({repo:name,forge:'github',owner:'example-org',sessionKey:`demo:${name}`,branch:'main',base:'origin/main',ahead:0,behind:0,...extra});
const scenarios={sparse:[repo('agent-harness'),repo('cli'),repo('site',{branch:'openclaw/update-copy',dirty:1,unpublished:0})],dense:[repo('api',{ahead:2,unpublished:2,behind:3,pullRequest:{number:42,state:'open',checks:'success',review:'approved',url:'https://example.invalid/pr/42'}}),repo('site',{dirty:676,behind:6,branch:'openclaw/update-copy'})]};
const template=await readFile(new URL('tools/tunnel-menu-fixture.html',source),'utf8');
const browser=await chromium.launch({headless:true});const results=[];
try { for(const [scenario,repos] of Object.entries(scenarios)) for(const view of ['card','panel']) for(const width of [320,390,520,604,760,1100]) {
 let html=template.replace('const data = {','const data = window.fixtureData = {').replace(/ repos:\[.*\],/,' repos:'+JSON.stringify(repos)+',').replace('const request = async (method, params) => {',`window.sent=[];const request = async (method, params) => {if(method==='sessions.send'){window.sent.push(params);return await new Promise(resolve=>{window.releaseCommit=()=>resolve({runId:'demo-run'});});}`).replace('</style>','body{padding:10px;font:14px/1.55 system-ui,sans-serif}#stage>h1,#stage>p,#stage>footer,.composer{display:none}#panel{min-height:0}</style>');
 const page=await browser.newPage({viewport:{width,height:1100},deviceScaleFactor:2});const errors=[];page.on('pageerror',e=>errors.push(e.message));
 await page.route('**/*',r=>r.request().url()==='https://demo.invalid/'?r.fulfill({contentType:'text/html',body:html}):r.request().url()==='https://demo.invalid/plugin.js'?r.fulfill({contentType:'text/javascript',body:module}):r.abort());await page.goto('https://demo.invalid/');await page.locator('#card details').waitFor();
 if(view==='card') await page.locator('#card details').evaluate(n=>n.open=true);else await page.getByRole('button',{name:'Open the taba Collection panel',exact:true}).click();
 const root=page.locator(view==='card'?'#card details':'#panel');await page.waitForTimeout(100);
 const local=root.locator('.wtc-local-change:not(.wtc-column-commit)').first();await local.waitFor();
 const measure=()=>root.evaluate(n=>{const cells=[...n.querySelectorAll('.wtc-local-change')].map(x=>({text:x.textContent,box:x.getBoundingClientRect().toJSON()}));const bulk=n.querySelector('.wtc-column-commit').getBoundingClientRect();return {cells,headerX:bulk.x,folded:!!n.querySelector('.wtc-repo-facts'),overflow:n.scrollWidth>n.clientWidth+1};});
 const before=await measure();assert(await root.locator('.wtc-next-step>.wtc-action-menu').evaluateAll(ns=>ns.every(n=>{const r=n.getBoundingClientRect(),p=n.parentElement.parentElement.getBoundingClientRect();return r.left>=p.left-.5&&r.right<=p.right+.5})), 'overflow controls must fit their cells');assert(!before.overflow,`${scenario}/${view}/${width} overflow`);assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);
 if(scenario==='sparse'&&width>=390)assert.equal(before.folded,false,'Sparse rows should fit without an empty PR lane');
 for(const cell of before.cells) {assert.equal(cell.box.width,44);assert.equal(cell.box.height,24);assert(Math.abs(cell.box.x-before.headerX)<.5,'local changes align with group action');}
 const box=await local.boundingBox();await local.hover();assert.deepEqual(await local.boundingBox(),box,'hover cannot change geometry');await local.focus();assert.deepEqual(await local.boundingBox(),box,'focus cannot change geometry');
 await page.evaluate(()=>document.activeElement?.blur());await page.mouse.move(1,1);await root.screenshot({path:new URL(`${scenario}-${view}-${width}.png`,out).pathname});
 if(scenario==='sparse') {
  if(width===390) {
   await page.evaluate(()=>{window.fixtureData.repos.at(-1).choices={actions:[{step:'commit',by:'agent',skill:'wtc',key:'site:commit',reason:'Save changes',available:false,blocked:'Owner is busy'}]};});
   const refresh=()=>root.locator('button').evaluateAll(ns=>ns.find(n=>n.title==='Refresh'||/Updated .*Refresh/.test(n.title))?.click());
   await refresh();await page.waitForFunction(()=>document.querySelector('.wtc-local-change:not(.wtc-column-commit)')?.disabled===true);assert.deepEqual(await local.boundingBox(),box,'disabled fact keeps its geometry');
   await page.evaluate(()=>window.fixtureData.repos.at(-1).choices.actions[0].available=true);await refresh();await page.waitForFunction(()=>document.querySelector('.wtc-local-change:not(.wtc-column-commit)')?.disabled===false);assert.deepEqual(await local.boundingBox(),box,'action availability does not move the fact');
  }
  await local.click();await page.waitForFunction(()=>window.sent.length===1);await root.locator('.wtc-local-change[aria-busy="true"]').waitFor();assert.deepEqual(await root.locator('.wtc-local-change:not(.wtc-column-commit)').first().boundingBox(),box,'pending stays in same place');
  await root.screenshot({path:new URL(`${scenario}-${view}-${width}-pending.png`,out).pathname});
  await page.evaluate(()=>{Object.assign(window.fixtureData.repos.at(-1),{dirty:0,ahead:1,unpublished:1});window.releaseCommit();});await page.waitForTimeout(100);
  const done=root.locator('.wtc-local-change:not(.wtc-column-commit)').first();await done.waitFor();await page.waitForFunction(()=>[...document.querySelectorAll('.wtc-local-change:not(.wtc-column-commit)')].some(n=>n.textContent.includes('✓')),{},{timeout:10000});assert.match(await done.innerText(),/✓/);assert.deepEqual(await done.boundingBox(),box,'observed completion keeps the same cell');
  await root.screenshot({path:new URL(`${scenario}-${view}-${width}-done.png`,out).pathname});
  if(view==='card'&&width===390){await page.waitForTimeout(5300);assert.equal(await root.locator('.wtc-local-change:not(.wtc-column-commit)').count(),0,'success clears after its brief feedback');}
 }
 assert.deepEqual(errors,[]);results.push({scenario,view,width,...before});await page.close();
 }await writeFile(new URL('results.json',out),JSON.stringify(results,null,2));console.log(`${results.length} geometry/action scenarios passed`);
}finally{await browser.close();}
