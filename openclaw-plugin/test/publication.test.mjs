import {test} from "node:test";
import assert from "node:assert/strict";
import {mkdtempSync,mkdirSync,writeFileSync,readdirSync,symlinkSync,rmSync} from "node:fs";
import os from "node:os";
import path from "node:path";
import {publicationFolder,registerPublicationBridge} from "../publication.js";

function fixture(){
 const stateDir=mkdtempSync(path.join(os.tmpdir(),"taba-publication-")),worktree=path.join(stateDir,"worktrees","widget","task");
 mkdirSync(path.join(worktree,".git"),{recursive:true});
 const folder=path.join(stateDir,"wtc","ad-hoc","task");mkdirSync(folder,{recursive:true});symlinkSync(worktree,path.join(folder,"widget"));
 const entry={workspaceDir:worktree,worktree:{branch:"openclaw/task"}},handlers=new Map(),calls=[];
 let code=0,result={status:"published",url:"https://github.com/example/widget/pull/7",branch:"feature"};
 const bridge=registerPublicationBridge({registerGatewayMethod:(name,handler,options)=>handlers.set(name,{handler,options})},{stateDir,getSessionEntry:async()=>entry,run:async argv=>{calls.push(argv);return {code};},dispatch:async(method,params)=>{calls.push({method,params});return {ok:true,payload:method==="plugins.sessionAction"?{ok:true,result}:result};}});
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
