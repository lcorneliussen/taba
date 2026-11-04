import {resolve} from 'node:path';
import {pathToFileURL} from 'node:url';
import {readFile,writeFile,mkdir} from 'node:fs/promises';
import assert from 'node:assert/strict';
// Synthetic screenshots and measured alignment rails; no gateway is contacted.
// Usage: node openclaw-plugin/tools/check-service-alignment.mjs <playwright module> <output directory>
const [playwrightPath,outputPath]=process.argv.slice(2);
if(!playwrightPath||!outputPath)throw new Error('Pass a Playwright module and output directory');
const {chromium}=await import(pathToFileURL(resolve(playwrightPath)).href);
const out=pathToFileURL(resolve(outputPath)+'/');await mkdir(out,{recursive:true});
const module=await readFile(new URL('../dist/control-ui/v1/index.js',import.meta.url),'utf8');
let html=await readFile(new URL('tunnel-menu-fixture.html',import.meta.url),'utf8');
html=html.replace(/ repos:\[.*\],/,' repos:[repo("api"),repo("web"),repo("docs")],').replace(/ services:\[.*\]/,' services:[{group:"api/worker",path:"api/worker",repo:"api",kind:"service",label:"Worker",state:"idle"},{group:"web/server",path:"web/server",repo:"web",kind:"service",label:"Demo server",state:"idle",url:"http://127.0.0.1:42001"}]');
html=html.replace('</style>',':root{--bg:#0e1015;--card:#161920;--text:#bcbcc0;--text-strong:#f4f4f5;--muted:#8b8b94;--border:#1e2028;--border-strong:#2e3040;--bg-hover:#242832}body{padding:16px;font:14px/1.55 system-ui,sans-serif}#stage>h1,#stage>p,#stage>footer,.composer{display:none}#panel{min-height:0}</style>');
const browser=await chromium.launch({headless:true}),results=[];
try{for(const view of ['card','panel'])for(const width of [320,390,760,1100]){
 const page=await browser.newPage({viewport:{width,height:1200},deviceScaleFactor:2}),errors=[];page.on('pageerror',e=>errors.push(e.message));
 await page.route('**/*',r=>r.request().url()==='https://demo.invalid/'?r.fulfill({contentType:'text/html',body:html}):r.request().url()==='https://demo.invalid/plugin.js'?r.fulfill({contentType:'text/javascript',body:module}):r.abort());await page.goto('https://demo.invalid/');await page.locator('#card details').waitFor();await page.waitForTimeout(80);
 if(view==='card')await page.locator('#card details').evaluate(n=>n.open=true);else await page.getByRole('button',{name:'Open the taba Collection panel',exact:true}).click();
 const root=page.locator(view==='card'?'#card details':'#panel');
 const metrics=await root.evaluate(root=>{
  const baseline=n=>{const p=document.createElement('span');p.style.cssText='display:inline-block;width:0;height:0;vertical-align:baseline';n.append(p);const y=p.getBoundingClientRect().top;p.remove();return y;};
  const labels=[...root.querySelectorAll('.wtc-service-label')], plays=[...root.querySelectorAll('.wtc-service-play')].filter(n=>!/all services/.test(n.title));
  const rows=labels.map((n,i)=>{const b=n.getBoundingClientRect(),y=baseline(n),play=plays[i]?.querySelector('svg')?.getBoundingClientRect();const cell=n.parentElement,grid=cell.parentElement,index=[...grid.children].indexOf(cell),cols=Number(grid.getAttribute('data-columns')),plus=grid.children[index+(cols-2)]?.querySelector('.wtc-tunnel-trigger')?.querySelector('svg')?.getBoundingClientRect();return {label:n.textContent,rect:b.toJSON(),baseline:y,playCenter:play?play.y+play.height/2:null,plusCenter:plus?plus.y+plus.height/2:null,iconCenterError:play&&plus?Math.abs(play.y+play.height/2-plus.y-plus.height/2):null};});
  return {rows,logo:root.querySelector('.wtc-brand-icon')?.tagName,headerPlay:[...root.querySelectorAll('.wtc-table')].some(t=>[...t.children].slice(0,Number(t.getAttribute('data-columns'))).some(n=>n.querySelector('.wtc-service-play'))),servicesHeight:root.querySelector('.wtc-base-services')?.getBoundingClientRect().height};
 });
 assert.deepEqual(errors,[]);assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);{assert.equal(metrics.logo,'svg');assert.equal(metrics.headerPlay,false);for(const r of metrics.rows)if(r.iconCenterError!==null)assert(r.iconCenterError<=.5,`controls differ by ${r.iconCenterError}`);}
 await root.screenshot({path:new URL(`${view}-${width}.png`,out).pathname,animations:'disabled'});
 await root.evaluate((root,metrics)=>{const box=root.getBoundingClientRect();const overlay=document.createElement('div');overlay.style.cssText='position:absolute;inset:0;pointer-events:none;z-index:9999';root.style.position='relative';for(const row of metrics.rows){for(const [y,color]of [[row.baseline,'#ff4078'],[row.playCenter,'#4ed7de']]){if(y===null)continue;const line=document.createElement('div');line.style.cssText=`position:absolute;left:0;right:0;top:${y-box.top}px;border-top:1px dashed ${color};opacity:.8`;overlay.append(line);}for(const x of [row.rect.x]){const line=document.createElement('div');line.style.cssText=`position:absolute;top:0;bottom:0;left:${x-box.left}px;border-left:1px dashed #d6ce77;opacity:.65`;overlay.append(line);}}root.append(overlay);},metrics);
 await root.screenshot({path:new URL(`${view}-${width}-rails.png`,out).pathname,animations:'disabled'});results.push({view,width,...metrics});await page.close();
}await writeFile(new URL('results.json',out),JSON.stringify(results,null,2));console.log(`${results.length} synthetic screenshots/rail checks passed`);}finally{await browser.close();}
