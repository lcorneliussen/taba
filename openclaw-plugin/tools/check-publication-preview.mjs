import { loadControlUIFixtureAssets, serveControlUIFixtureAsset } from "./control-ui-fixture-assets.mjs";
// Synthetic browser checks only: no account, gateway or real publication.
import {readFile,writeFile,mkdir} from "node:fs/promises";
import {resolve} from "node:path";
import {pathToFileURL} from "node:url";
import assert from "node:assert/strict";
const [playwrightPath,outputPath]=process.argv.slice(2);
if(!playwrightPath||!outputPath)throw Error("Usage: check-publication-preview.mjs <playwright module> <output directory>");
const {chromium}=await import(pathToFileURL(resolve(playwrightPath)).href),root=new URL("../",import.meta.url),out=pathToFileURL(resolve(outputPath)+"/");
await mkdir(out,{recursive:true});
const assets=await loadControlUIFixtureAssets();
const template=await readFile(new URL("tools/tunnel-menu-fixture.html",root),"utf8");
const body="# Summary\n\nKeep **confirmation controls** in place and make the proposal readable.\n\n- [x] Preserve the captured text\n\n- [ ] Format lists, `code` and headings\n\n## Validation\n\n```sh\nnode --test\n```\n\n| Check | Result |\n|---|---|\n| Unit tests | Passed |\n| Review | Pending |\n\n<img src='https://private.invalid/pixel' onerror='alert(1)'>\n\n![Explicit image link](https://example.invalid/diagram.png)\n";
const repo={repo:"web",forge:"github",owner:"example-org",sessionKey:"demo:web",branch:"openclaw/add-search",base:"origin/main",ahead:2,unpublished:2,behind:0,proposal:{branch:"openclaw/add-search",title:"Keep publication clear",body}};
const browser=await chromium.launch({headless:true}),results=[];
const scenarios=[{count:1,height:1000},{count:3,height:640},{count:3,height:1000}];
try{for(const view of ["card","panel"])for(const width of [320,390,760,1100])for(const {count,height} of scenarios){
 const repos=["web","api","docs"].slice(0,count).map(name=>({...repo,repo:name,sessionKey:`demo:${name}`}));
 let html=template.replace(/ repos:\[.*\],/," repos:"+JSON.stringify(repos)+",").replace(/ services:\[.*\]/," services:[]");
 html=html.replace("const request = async (method, params) => {",`window.requests=[];const request = async (method, params) => {window.requests.push({method,params});if(method==='wtc.github.publish')return {status:'needs_confirmation',requestId:'demo-'+params.sessionKey};if(method==='sessions.github.status')return {confirmation:{account:{login:'demo-user'},generation:1,requestDigest:'demo-digest',branch:'openclaw/add-search',head:'abcdef123456',dest:'main'}};`);
 html=html.replace("</style>","body{padding:10px;font:14px/1.55 system-ui,sans-serif}#stage>h1,#stage>p,#stage>footer{display:none}#panel{min-height:0;height:calc(100dvh - 20px);overflow:hidden}.composer{margin:8px 0 0;font-size:13px;padding:14px}#stage{max-height:100vh}"+(view==="card"?"#stage{position:fixed;bottom:10px;left:10px;right:10px}":"")+"</style>");
 const page=await browser.newPage({viewport:{width,height},deviceScaleFactor:2}),errors=[],external=[];
 page.on("pageerror",e=>errors.push(e.message));
 await page.route("**/*",route=>{const url=new URL(route.request().url());if(url.origin!=="https://demo.invalid"){external.push(url.href);return route.abort();}return url.pathname==="/"?route.fulfill({contentType:"text/html",body:html}):serveControlUIFixtureAsset(route,assets);});
 await page.goto("https://demo.invalid/");await page.locator("#card details").waitFor();
 if(view==="panel")await page.getByRole("button",{name:"Open the taba Collection panel",exact:true}).click();
 const selector=view==="card"?"#card":"#panel",host=page.locator(selector);
 if(view==="card")await host.locator("details").first().evaluate(n=>n.open=true);
 await host.locator(`button[title^="Push all ${count} eligible"]`).click();
 await page.waitForFunction(({selector,count})=>document.querySelector(selector).querySelectorAll(".wtc-publication-confirmation").length===count,{selector,count});
 const group=host.locator(".wtc-publication-confirmations"),preview=host.locator(".wtc-pr-preview").first(),controls=host.locator(".wtc-publication-controls").first();
 assert.equal(await preview.evaluate(n=>n.open),true);assert.equal(await host.locator("img,script,iframe").count(),0);
 assert.equal(await preview.locator("h2").first().textContent(),"Summary");
 assert.equal(await preview.getByRole("img",{name:"Completed task",exact:true}).count(),1);
 assert.equal(await preview.getByRole("img",{name:"Incomplete task",exact:true}).count(),1);
 const p=await preview.boundingBox(),c=await controls.boundingBox();assert(view==="card"?p.y+p.height<=c.y:c.y+c.height<=p.y,"preview placement");
 assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false,"no horizontal overflow");
 assert.equal(await host.evaluate(n=>n.scrollWidth>n.clientWidth+1),false,"host overflow");
 const region=await group.boundingBox();
 if(view==="card")assert(region.y>=0&&region.y+region.height<=height,"all prepared publications share a viewport bound");
 if(count===1)assert(c.x>=0&&c.x+c.width<=width+1&&c.y+c.height<height,"controls stay visible");
 if(count>1)await page.screenshot({path:new URL(`${view}-${width}-${height}-${count}-initial.png`,out).pathname});
 if(view==="card"&&count>1){
  assert(await host.getByText(`${count} prepared pushes · scroll to review each`,{exact:true}).isVisible(),"multiple preparations have a visible scroll cue");
  await group.focus();await page.keyboard.press("PageDown");
  await page.waitForFunction(selector=>document.querySelector(selector).querySelector(".wtc-publication-confirmations").scrollTop>0,selector);
  await group.evaluate(n=>n.scrollTop=0);
 }
 const buttons=await host.locator(".wtc-publication-controls button").elementHandles(),seen=new Set();
 if(view==="card")await group.focus();else await buttons[0].focus();
 for(let step=0;step<120&&seen.size<buttons.length;step++){
  const focused=await host.evaluate(n=>{
   const buttons=[...n.querySelectorAll(".wtc-publication-controls button")],index=buttons.indexOf(document.activeElement);
   if(index<0)return null;
   const r=document.activeElement.getBoundingClientRect();let top=0,bottom=innerHeight;
   for(let p=document.activeElement.parentElement;p;p=p.parentElement)if(/auto|scroll|hidden|clip/.test(getComputedStyle(p).overflowY)){const b=p.getBoundingClientRect();top=Math.max(top,b.top);bottom=Math.min(bottom,b.bottom);}
   return {index,visible:r.top>=top-1&&r.bottom<=bottom+1};
  });
  if(focused){assert(focused.visible,"keyboard-focused Confirm/Cancel is inside its scroll bounds");seen.add(focused.index);}
  if(seen.size<buttons.length)await page.keyboard.press("Tab");
 }
 assert.equal(seen.size,buttons.length,"every prepared Confirm and Cancel is keyboard reachable");
 assert.deepEqual(external,[],"preview must not load an image or resource");assert.deepEqual(errors,[]);
 const suffix=count===1?`${view}-${width}`:`${view}-${width}-${height}-${count}`,stage=page.locator(view==="card"?"#stage":"#panel");
 if(count===1){
  await stage.screenshot({path:new URL(`${suffix}-expanded.png`,out).pathname});
  await preview.locator("summary").first().click();const after=await controls.boundingBox();assert(Math.abs(c.y-after.y)<1,"collapse cannot move controls");
 }else{
  await page.screenshot({path:new URL(`${suffix}-expanded.png`,out).pathname});
  await preview.locator("summary").first().click();
  for(const button of buttons)assert(await button.evaluate(n=>n.isConnected),"disclosure does not replace or retarget confirmation buttons");
  if(view==="card")await host.locator("details").first().evaluate(n=>n.open=false);
 }
 await stage.screenshot({path:new URL(`${suffix}-collapsed.png`,out).pathname});
 const requests=await page.evaluate(()=>window.requests),sent=requests.filter(r=>r.method==='wtc.github.publish');
 assert.equal(sent.length,count);assert(sent.every(r=>r.params.body===body),"formatting never rewrites publication text");
 assert.equal(requests.some(r=>r.method==='wtc.github.confirm'),false,"preview, keyboard navigation and collapse never confirm");
 results.push({view,width,height,count,controls:c,region,keyboardTargets:seen.size,external});await page.close();
}await writeFile(new URL("results.json",out),JSON.stringify(results,null,2)+"\n");console.log(`${results.length} publication preview scenarios passed`);}finally{await browser.close();}
