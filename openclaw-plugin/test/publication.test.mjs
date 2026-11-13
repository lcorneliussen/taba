import {test} from "node:test";
import assert from "node:assert/strict";
import {mkdtempSync,mkdirSync,writeFileSync,readdirSync,symlinkSync,rmSync} from "node:fs";
import os from "node:os";
import path from "node:path";
import {publicationFolder,registerPublicationBridge} from "../publication.js";

function fixture({beforeEnlist=async()=>{}}={}){
 const stateDir=mkdtempSync(path.join(os.tmpdir(),"taba-publication-")),worktree=path.join(stateDir,"worktrees","widget","task");
 mkdirSync(path.join(worktree,".git"),{recursive:true});
 const folder=path.join(stateDir,"wtc","ad-hoc","task");mkdirSync(folder,{recursive:true});symlinkSync(worktree,path.join(folder,"widget"));
 const entry={workspaceDir:worktree,worktree:{branch:"openclaw/task"}},handlers=new Map(),calls=[];
 let code=0,result={requestId:"request",status:"published",url:"https://github.com/example/widget/pull/7",repository:"example/widget",branch:"feature",headCommit:"1".repeat(40)};
 const bridge=registerPublicationBridge({registerGatewayMethod:(name,handler,options)=>handlers.set(name,{handler,options})},{stateDir,getSessionEntry:async()=>entry,run:async argv=>{calls.push(argv);await beforeEnlist(argv);return {code};},dispatch:async(method,params)=>{calls.push({method,params});return {ok:true,payload:method==="plugins.sessionAction"?{ok:true,result}:result};}});
 const invoke=async(route,params)=>{let response;await handlers.get("wtc."+route).handler({params,respond:(ok,payload,error)=>response={ok,payload,error}});return response;};
 return {stateDir,worktree,folder,entry,handlers,calls,bridge,invoke,setCode:n=>code=n,setResult:r=>result=r,dispose:()=>rmSync(stateDir,{recursive:true,force:true})};
}
test("only fixed native publishers run under session write admission, then enlist the captured branch",async()=>{
 const f=fixture();try{
  for(const {options} of f.handlers.values())assert.deepEqual(options,{scope:"operator.write",profileAccess:"required",sessionAccess:{mode:"write"}});
  const params={sessionKey:"owner:widget",agentId:"main",requestId:"request",generation:"generation",account:{login:"person"},requestDigest:"digest"};
  const out=await f.invoke("github.confirm",params);
  assert.equal(out.payload.tracking.state,"enlisted");assert.deepEqual(f.calls[0],{method:"sessions.github.confirm",params});
  assert.deepEqual(f.calls[1],["wtc","openclaw","enlist-publication","--collection",f.folder,"--worktree",f.worktree,"--url","https://github.com/example/widget/pull/7","--branch","feature"]);
  assert.equal(readdirSync(path.join(f.stateDir,"plugins/wtc/publication-receipts")).length,0);
 }finally{f.dispose();}
});

function queueReceipt(f,index,override={}){
 const queue=path.join(f.stateDir,"plugins/wtc/publication-receipts");mkdirSync(queue,{recursive:true});
 const file=path.join(queue,index.toString(16).padStart(64,"0")+".json");
 writeFileSync(file,JSON.stringify({sessionKey:"owner:widget",worktree:f.worktree,collection:"task",url:`https://github.com/example/widget/pull/${index+1}`,branch:`topic/${index}`,...override}));
 return file;
}

test("retry budget ignores corrupt, unrelated and changed-checkout receipts",async()=>{
 const f=fixture();try{
  for(let i=0;i<100;i++)writeFileSync(queueReceipt(f,i),"not-json");
  for(let i=100;i<200;i++)queueReceipt(f,i,{sessionKey:"unrelated"});
  for(let i=200;i<300;i++)queueReceipt(f,i,{worktree:path.join(f.stateDir,"missing-checkout")});
  queueReceipt(f,300);
  await f.bridge.flush([{sessionKey:"owner:widget",entry:f.entry}]);
  assert.equal(f.calls.length,1,"ineligible receipts cannot consume the attempt budget");
  assert.equal(f.calls[0].at(-1),"topic/300");
  assert.ok(f.calls.every(Array.isArray),"refresh never invokes a native publisher");
  assert.equal(readdirSync(path.join(f.stateDir,"plugins/wtc/publication-receipts")).length,300);
 }finally{f.dispose();}
});

test("retry budget prioritizes never attempted receipts over persistent failures",async t=>{
 let now=0;t.mock.method(Date,"now",()=>now);
 const f=fixture();try{
  f.setCode(1);for(let i=0;i<101;i++)queueReceipt(f,i);
  const children=[{sessionKey:"owner:widget",entry:f.entry}];
  await f.bridge.flush(children);assert.equal(f.calls.length,100);
  now+=30001;await f.bridge.flush(children);
  assert.equal(f.calls.length,200,"each refresh attempts at most 100 eligible receipts");
  assert.equal(f.calls[100].at(-1),"topic/100","a new receipt takes priority over previously failed attempts");
  assert.ok(f.calls.every(Array.isArray),"only local tracking is retried");
  assert.equal(readdirSync(path.join(f.stateDir,"plugins/wtc/publication-receipts")).length,101);
 }finally{f.dispose();}
});

test("concurrent refreshes share one tracking attempt and completed receipts stay removed",{timeout:5000},async()=>{
 let enter,release;const entered=new Promise(r=>enter=r),released=new Promise(r=>release=r);
 const f=fixture({beforeEnlist:()=>{enter();return released;}});let first,second;
 try{
  queueReceipt(f,1);const children=[{sessionKey:"owner:widget",entry:f.entry}];
  first=f.bridge.flush(children);await entered;
  await f.bridge.flush([{sessionKey:"unrelated",entry:f.entry}]);
  let secondDone=false;second=f.bridge.flush(children).then(()=>secondDone=true);
  await new Promise(setImmediate);assert.equal(secondDone,false,"an overlapping reader joins relevant in-flight metadata");
  assert.equal(f.calls.length,1);
  release();await Promise.all([first,second]);await f.bridge.flush(children);
  assert.equal(f.calls.length,1);assert.deepEqual(readdirSync(path.join(f.stateDir,"plugins/wtc/publication-receipts")),[]);
 }finally{release();await Promise.allSettled([first,second]);f.dispose();}
});
test("refresh joins a native publication's in-flight enlist without publishing again",{timeout:5000},async()=>{
 let enter,release;const entered=new Promise(r=>enter=r),released=new Promise(r=>release=r);
 const f=fixture({beforeEnlist:()=>{enter();return released;}});let publication,refresh;
 try{
  const native={requestId:"request",status:"published",url:"https://github.com/example/widget/pull/7",repository:"example/widget",branch:"captured/topic",headCommit:"1".repeat(40)};
  f.setResult(native);publication=f.invoke("github.confirm",{sessionKey:"owner:widget",requestId:"request",requestDigest:"digest"});await entered;
  let refreshed=false;refresh=f.bridge.flush([{sessionKey:"owner:widget",entry:f.entry}]).then(()=>refreshed=true);
  await new Promise(setImmediate);assert.equal(refreshed,false,"refresh cannot finish before the captured receipt is recorded");
  release();const out=await publication;await refresh;
  assert.deepEqual(out.payload,{...native,tracking:{state:"enlisted"}});
  assert.equal(f.calls.filter(c=>!Array.isArray(c)).length,1,"only the original native publication runs");
  assert.equal(f.calls.filter(Array.isArray).length,1,"the same metadata attempt is shared");
  assert.equal(f.calls.find(Array.isArray).at(-1),"captured/topic","branch comes from the native result, not the current checkout");
 }finally{release();await Promise.allSettled([publication,refresh]);f.dispose();}
});
test("published success survives tracking failure; verified refresh retries only tracking",async()=>{
 const f=fixture();try{
  f.setCode(1);const out=await f.invoke("github.publish",{sessionKey:"owner:widget",idempotencyKey:"same",selection:{source:"personal"}});
  assert.equal(out.payload.status,"published");assert.equal(out.payload.tracking.state,"pending");
  f.calls.length=0;f.setCode(0);await f.bridge.flush([{sessionKey:"unrelated",entry:f.entry}]);assert.equal(f.calls.length,0);
  await f.bridge.flush([{sessionKey:"owner:widget",entry:f.entry}]);assert.equal(f.calls.length,1);assert.ok(Array.isArray(f.calls[0]),"no publisher retry");
  assert.deepEqual(readdirSync(path.join(f.stateDir,"plugins/wtc/publication-receipts")),[]);
  await f.bridge.flush([{sessionKey:"owner:widget",entry:f.entry}]);assert.equal(f.calls.length,1,"successful retry does not repeat on refresh");
 }finally{f.dispose();}
});
test("Bitbucket confirmation remains native and a prepare response never enlists",async()=>{
 const f=fixture();try{
  f.setResult({status:"needs_confirmation",requestId:"request"});await f.invoke("github.publish",{sessionKey:"owner:widget",selection:{source:"personal"}});assert.equal(f.calls.length,1);
  f.setResult({status:"published",url:"https://bitbucket.org/example/widget/pull-requests/7",branch:"topic"});
  const out=await f.invoke("bitbucket.confirm",{sessionKey:"owner:widget",requestId:"request",requestDigest:"digest",method:"arbitrary"});assert.equal(out.payload.tracking.state,"enlisted");
  assert.deepEqual(f.calls[1],{method:"plugins.sessionAction",params:{pluginId:"bitbucket",actionId:"publish-confirm",sessionKey:"owner:widget",payload:{requestId:"request",requestDigest:"digest"}}});
 }finally{f.dispose();}
});
test("duplicate collection names require actual membership and ambiguous membership is refused",()=>{
 const f=fixture();try{
  const other=path.join(f.stateDir,"wtc","other","task");mkdirSync(other,{recursive:true});
  assert.equal(publicationFolder(f.stateDir,"task",f.worktree),f.folder);
  symlinkSync(f.worktree,path.join(other,"widget"));assert.equal(publicationFolder(f.stateDir,"task",f.worktree),null);
  assert.equal(publicationFolder(f.stateDir,"../task",f.worktree),null);
 }finally{f.dispose();}
});

test("corrupt queued receipts cannot break a collection refresh",async()=>{
 const f=fixture();try{
  const queue=path.join(f.stateDir,"plugins/wtc/publication-receipts");mkdirSync(queue,{recursive:true});
  for(const [i,value]of ["null","{}","not-json"].entries())writeFileSync(path.join(queue,String(i).repeat(64)+".json"),value);
  await f.bridge.flush([{sessionKey:"owner:widget",entry:f.entry}]);assert.equal(f.calls.length,0);
 }finally{f.dispose();}
});
