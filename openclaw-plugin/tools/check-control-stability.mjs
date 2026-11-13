import { loadControlUIFixtureAssets, serveControlUIFixtureAsset } from "./control-ui-fixture-assets.mjs";
import {readFile,writeFile} from 'node:fs/promises';
import assert from 'node:assert/strict';
import {resolve} from 'node:path';
import {pathToFileURL,fileURLToPath} from 'node:url';
// Run against a separately installed Playwright/Chromium; all data is synthetic.
// Usage: node openclaw-plugin/tools/check-control-stability.mjs <playwright module> <output directory>
const [playwrightPath,outputPath]=process.argv.slice(2);
if(!playwrightPath||!outputPath)throw new Error('Pass a Playwright module path and output directory');
const {chromium}=await import(pathToFileURL(resolve(playwrightPath)).href);
const {mkdir}=await import('node:fs/promises');
await mkdir(resolve(outputPath),{recursive:true});
const out=pathToFileURL(resolve(outputPath)+'/'),assets=await loadControlUIFixtureAssets();
const fixture=await readFile(new URL('tunnel-menu-fixture.html',import.meta.url),'utf8');
const browser=await chromium.launch({headless:true}),results=[];
try{for(const width of [390,1100])for(const state of ['suggestion','collapsed','expanded','sidebar']){
 let html=fixture.replace("mounts.card(document.querySelector('#card'),ctx);","window.cardView=mounts.card(document.querySelector('#card'),ctx);window.demoCtx=ctx;").replace("mounts.panel(document.querySelector('#panel'),ctx);","window.panelView=mounts.panel(document.querySelector('#panel'),ctx);").replace("const request = async (method, params) => {","window.statusCalls=0;const request = async (method, params) => {if(method==='plugins.sessionAction'&&params.actionId==='collection')window.statusCalls++;");
 if(state==='suggestion')html=html.replace(/ repos:\[.*\],\n services:\[.*\]/,' repos:[],\n services:[]').replace(" if(method==='chat.history') return {messages:[]};"," if(method==='chat.history') return {messages:[{role:'user',content:'Work on the api search endpoint'}]};\n if(method==='plugins.sessionAction'&&params.actionId==='suggest')return {ok:true,result:{project:'example',repos:['api'],source:'words'}};");
 const page=await browser.newPage({viewport:{width,height:1000},deviceScaleFactor:2}),errors=[];page.on('pageerror',e=>errors.push(e.message));
 await page.addInitScript(()=>{window.routineTimers=[];const interval=window.setInterval;window.setInterval=(fn,ms,...args)=>{if(ms===15000||ms===30000)window.routineTimers.push({fn,ms});return interval(fn,ms,...args);};});
 await page.route('**/*',r=>r.request().url()==='https://demo.invalid/'?r.fulfill({contentType:'text/html',body:html}):serveControlUIFixtureAsset(r,assets));await page.goto('https://demo.invalid/');await page.locator('#card details').waitFor();await page.waitForTimeout(100);
 if(state==='expanded')await page.locator('#card details').evaluate(n=>n.open=true);
 if(state==='sidebar'){await page.getByRole('button',{name:'Open the taba Collection panel',exact:true}).click();await page.locator('#panel input[placeholder="Find or add a repository"]').press('Enter');}
 const root=state==='sidebar'?page.locator('#panel'):page.locator('#card');
 const target=state==='suggestion'?root.getByTitle('Ask the conversation to start this collection',{exact:true}):root.locator('.wtc-refresh-control').first();
 await target.hover();await target.focus();await target.evaluate(n=>{window.originalTarget=n;window.originalTitle=n.title;window.originalAge=document.querySelector('.wtc-time-label');window.leaves=0;n.addEventListener('pointerleave',()=>window.leaves++);});
 const before=await target.boundingBox();
 for(let i=0;i<30;i++)await page.evaluate(s=>window[s==='sidebar'?'panelView':'cardView'].update({...window.demoCtx,props:{...window.demoCtx.props}}),state);
 const tick=await page.evaluate(()=>{const now=Date.now();Date.now=()=>now+61000;for(const t of window.routineTimers)if(t.ms===15000)t.fn();const ages=[...document.querySelectorAll('.wtc-time-label')].map(n=>n.textContent),title=window.originalTarget.title;for(const t of window.routineTimers)if(t.ms===30000)t.fn();return {ages,title,originalTitle:window.originalTitle,busyAtStart:window.originalTarget.getAttribute('aria-busy')};});await page.waitForTimeout(150);
 const fact=await target.evaluate(n=>({connected:window.originalTarget.isConnected,same:n===window.originalTarget,focused:document.activeElement===n,leaves:window.leaves,statusCalls:window.statusCalls}));
 assert(fact.connected&&fact.same&&fact.focused);assert.equal(fact.leaves,0);assert.equal(fact.statusCalls,2,'one initial load and one explicit simulated status tick');
 if(state!=='suggestion'){assert.equal(tick.busyAtStart,'true');assert.equal(await target.getAttribute('aria-busy'),'false');}if(['collapsed','expanded'].includes(state))assert.equal(tick.title,tick.originalTitle,'hovered refresh tooltip must not be rewritten');if(state==='sidebar'){assert(await page.evaluate(()=>window.originalAge.isConnected&&window.originalAge===document.querySelector('.wtc-time-label')));assert(tick.ages.includes('Updated 1m ago'));assert.equal(await root.locator('.wtc-time-label').first().innerText(),'Updated just now');}assert.deepEqual(await target.boundingBox(),before);assert.deepEqual(errors,[]);assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);
 await page.screenshot({path:fileURLToPath(new URL(`${state}-${width}.png`,out))});results.push({state,width,...fact,ageTick:tick.ages,refreshBusyStart:tick.busyAtStart,errors});await page.close();
}await writeFile(new URL('browser-stability.json',out),JSON.stringify(results,null,2));console.log(`${results.length} hover/focus/update/tick cases passed`);}finally{await browser.close()}
