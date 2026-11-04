// Real Chromium checks with a synthetic host; no gateway or forge is contacted.
// Usage: node openclaw-plugin/tools/check-tunnel-menu.mjs /path/to/playwright/index.mjs /path/to/output
// Install Playwright/Chromium separately, or point at an existing Playwright installation.
import {readFile,writeFile,mkdir} from "node:fs/promises";
import {resolve} from "node:path";
import {pathToFileURL} from "node:url";
import assert from "node:assert/strict";
const [playwrightPath,outputPath]=process.argv.slice(2);
if (!playwrightPath || !outputPath) throw new Error("Usage: check-tunnel-menu.mjs <playwright module> <output directory>");
const {chromium}=await import(pathToFileURL(resolve(playwrightPath)).href);
const out=pathToFileURL(resolve(outputPath)+"/");
await mkdir(out,{recursive:true});
const module=await readFile(new URL("../dist/control-ui/v1/index.js",import.meta.url),"utf8");
const original=await readFile(new URL("tunnel-menu-fixture.html",import.meta.url),"utf8");
const browser=await chromium.launch({headless:true}),results=[];
try{
for(const topology of ['empty','existing','loose'])for(const theme of ['dark','light'])for(const width of [320,390,760,1100])for(const view of ['card','panel']){
 const height=width<500?740:850;
 let html=original.replace('plugin.activate(host);','window.demoCtx=ctx;plugin.activate(host);').replace("mounts.card(document.querySelector('#card'),ctx);","window.cardView=mounts.card(document.querySelector('#card'),ctx);").replace(" mounts.panel(document.querySelector('#panel'),ctx);"," window.panelView=mounts.panel(document.querySelector('#panel'),ctx);").replace('const request = async (method, params) => {',`window.calls=[];window.fixture=data;const request = async (method, params) => {
 if(method==='plugins.sessionAction'&&params.actionId==='svc'){window.calls.push(params.payload);return {ok:true,result:{message:'done'}};}
 if(method==='sessions.send'){window.calls.push({handoff:params.message});return {};}
 `);
 if(topology!=='empty')html=html.replace("url:'http://127.0.0.1:4321'}]", "url:'http://127.0.0.1:4321'},{group:'web/preview',path:'web/preview/cloudflare',repo:'web',kind:'endpoint',provider:'cloudflare',state:'idle'},{group:'web/preview',path:'web/preview/ngrok',repo:'web',kind:'endpoint',provider:'ngrok',state:'ready',url:'https://a-long-synthetic-preview-name.ngrok.app'}]");
 if(topology==='loose')html=html.replace("url:'https://a-long-synthetic-preview-name.ngrok.app'}]", "url:'https://a-long-synthetic-preview-name.ngrok.app'},{group:'web/standalone',path:'web/standalone',repo:'web',kind:'endpoint',provider:'cloudflare',state:'ready',url:'https://standalone-example.trycloudflare.com'}]");
 const colors=theme==='dark'?'--bg:#0e1015;--card:#161920;--text:#bcbcc0;--text-strong:#f4f4f5;--muted:#8b8b94;--border:#1e2028;--border-strong:#2e3040;--bg-hover:#242832':'--bg:#f4f1eb;--card:#fff;--text:#333;--text-strong:#222;--muted:#71717a;--border:#e4e4e7;--border-strong:#d4d4d8;--bg-hover:#efefef';
 html=html.replace('</style>',`:root{color-scheme:${theme};${colors}}body{padding:16px}#stage>h1,#stage>p,#stage>footer{display:none}${view==='card'?'#stage{position:fixed;bottom:16px;left:16px;right:16px}':''}</style>`);
 const page=await browser.newPage({viewport:{width,height},deviceScaleFactor:2});const errors=[];page.on('pageerror',e=>errors.push(e.message));
 await page.route('**/*',r=>r.request().url()==='https://demo.invalid/'?r.fulfill({contentType:'text/html',body:html}):r.request().url()==='https://demo.invalid/plugin.js'?r.fulfill({contentType:'text/javascript',body:module}):r.abort());
 await page.goto('https://demo.invalid/');await page.locator('#card details').waitFor();await page.waitForTimeout(200);await page.locator('#card details').evaluate(n=>{n.open=true});
 if(view==='panel'){await page.getByRole('button',{name:'Open the taba Collection panel',exact:true}).click();await page.getByText('Repositories',{exact:true}).waitFor();}
 const root=view==='card'?page.locator('#card'):page.locator('#panel');
 const trigger=root.getByRole('button',{name:'Add a tunnel to preview',exact:true});await trigger.waitFor();await page.waitForTimeout(100);
 await trigger.scrollIntoViewIfNeeded();const stable=await root.boundingBox();
 await trigger.click();const menu=page.getByRole('menu',{name:'Tunnel for preview',exact:true});await menu.waitFor();
 const afterOpen=await root.boundingBox();for(const k of ['x','y','width','height'])assert(Math.abs(stable[k]-afterOpen[k])<.5,'menu opening must not move the view');
 const box=await menu.boundingBox();if(box.x+box.width>width){console.log(await menu.evaluate(n=>({style:n.style.cssText,computed:{left:getComputedStyle(n).left,top:getComputedStyle(n).top,position:getComputedStyle(n).position,visibility:getComputedStyle(n).visibility},handler:typeof n.onbeforetoggle,innerWidth,innerHeight,viewport:visualViewport?.width})));await page.mouse.move(1,1);await page.screenshot({path:new URL('failure.png',out).pathname})};assert(box.x>=11&&box.y>=11&&box.x+box.width<=width-11&&box.y+box.height<=height-11,JSON.stringify({width,view,box}));
 const colorsActual=await menu.evaluate(n=>({background:getComputedStyle(n).backgroundColor,color:getComputedStyle(n).color}));assert.equal(colorsActual.background,theme==='dark'?'rgb(22, 25, 32)':'rgb(255, 255, 255)');
 assert.equal(await trigger.getAttribute('aria-expanded'),'true');assert.equal(await page.evaluate(()=>window.calls.length),0);
 if(view==='card')assert.equal(await page.locator('#card details').evaluate(n=>n.open),true,'opening a tunnel menu must not collapse the card');
 await page.screenshot({path:new URL(`${topology}-${theme}-${width}-${view}-open.png`,out).pathname,fullPage:false});
 const gap=await trigger.evaluate(n=>{const cell=n.closest('.wtc-tunnel-control').parentElement.parentElement;return cell.getBoundingClientRect().right-n.getBoundingClientRect().right;});assert(gap>=3,'plus retains breathing room');
 await trigger.click();await page.locator('.wtc-tunnel-menu:popover-open').waitFor({state:'hidden'});assert.equal(await trigger.getAttribute('aria-expanded'),'false','trigger toggles menu closed');
 await trigger.press('ArrowUp');await menu.waitFor();assert.equal(await page.evaluate(()=>document.activeElement.value),'devtunnel');
 await page.keyboard.press('ArrowDown');assert.equal(await page.evaluate(()=>document.activeElement.value),'gateway');
 await page.keyboard.press('Escape');assert(await trigger.evaluate(n=>document.activeElement===n));assert.equal(await trigger.getAttribute('aria-expanded'),'false');
 await trigger.press('ArrowDown');await menu.waitFor();await page.keyboard.press('Tab');await page.locator('.wtc-tunnel-menu:popover-open').waitFor({state:'hidden'});
 await trigger.click();await menu.waitFor();await page.mouse.click(1,1);await page.locator('.wtc-tunnel-menu:popover-open').waitFor({state:'hidden'});
 await trigger.click();await menu.waitFor();await menu.evaluate(n=>window.openMenu=n);await page.evaluate(v=>window[v==='card'?'cardView':'panelView'].update(window.demoCtx),view);assert(await menu.evaluate(n=>n===window.openMenu),'unchanged context preserves the open menu');await page.evaluate(v=>window[v==='card'?'cardView':'panelView'].update({...window.demoCtx,props:{...window.demoCtx.props,sessionKey:'demo:other'}}),view);await page.locator('.wtc-tunnel-menu:popover-open').waitFor({state:'hidden'});await page.evaluate(v=>window[v==='card'?'cardView':'panelView'].update(window.demoCtx),view);await trigger.waitFor();
 await trigger.click();await menu.waitFor();await page.setViewportSize({width:width+1,height});await page.locator('.wtc-tunnel-menu:popover-open').waitFor({state:'hidden'});
 await trigger.click();await menu.waitFor();await menu.getByRole('menuitem',{name:topology!=='empty'?/^Dev Tunnel/:/^Cloudflare/}).focus();await page.keyboard.press('Enter');
 await page.waitForTimeout(100);const calls=await page.evaluate(()=>window.calls);assert.deepEqual(calls,[{op:'tunnel',group:'web/preview',provider:topology!=='empty'?'devtunnel':'cloudflare',public:topology==='empty',add:true}]);
 if(topology==='loose') {
  const repair=root.getByTitle('Ask the conversation to open it with wtc svc tunnel instead');
  const repairBox=await repair.boundingBox(),closeBox=await page.getByRole('button',{name:'Close the cloudflare tunnel',exact:true}).boundingBox();
  assert(repairBox.x>=0&&repairBox.x+repairBox.width<=width,'Consolidate stays visible');
  assert(repairBox.x+repairBox.width<=closeBox.x||closeBox.x+closeBox.width<=repairBox.x||repairBox.y+repairBox.height<=closeBox.y||closeBox.y+closeBox.height<=repairBox.y,'Consolidate must not cover close');
 }
 assert.equal(await page.locator('.wtc-tunnel-menu:popover-open').count(),0);assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);assert.deepEqual(errors,[]);
 results.push({topology,theme,width,view,menu:box,padding:gap,colors:colorsActual,checks:{theme:true,viewport:true,stableLayout:true,paddedTrigger:true,toggle:true,arrowKeys:true,escapeFocus:true,tabDismiss:true,outsideDismiss:true,unchangedUpdateStable:true,sessionSwitchDismiss:true,resizeDismiss:true,explicitProvider:true,noOverflow:true,noErrors:true,looseControl:topology==='loose'?true:null},errors});await page.close();
}
await writeFile(new URL('browser-results.json',out),JSON.stringify(results,null,2));console.log(`${results.length} browser menu cases passed.`);
}finally{await browser.close()}
