// Compose only the native personal publishers under the admitted request's
// identity. The SDK refuses synthetic clients and undeclared dispatch scopes.
import { createHash, randomUUID } from "node:crypto";
import { existsSync, mkdirSync, readFileSync, readdirSync, realpathSync, renameSync, unlinkSync, writeFileSync } from "node:fs";
import path from "node:path";

const nativeDispatch = async (...args) => (await import("openclaw/plugin-sdk/gateway-method-runtime")).dispatchGatewayMethod(...args);
const samePath = (a,b) => { try { return realpathSync(a) === realpathSync(b); } catch { return false; } };

// Folder names alone are not identity: the same task can exist in two projects.
// Accept exactly one linked collection containing the actual managed checkout.
export function publicationFolder(stateDir, name, worktree) {
  if (!/^[a-zA-Z0-9][a-zA-Z0-9._-]*$/.test(name) || name.includes("..")) return null;
  let groups=[];
  try {groups=readdirSync(path.join(stateDir,"wtc"));} catch {}
  const candidates=[path.join(stateDir,"wtc-collections",name),...groups.map(g=>path.join(stateDir,"wtc",g,name))];
  const matches=candidates.filter(folder=>{
    try {return readdirSync(folder).some(repo=>existsSync(path.join(folder,repo,".git"))&&samePath(path.join(folder,repo),worktree));}catch{return false;}
  });
  return matches.length===1?matches[0]:null;
}

export function registerPublicationBridge(api, {run,getSessionEntry,stateDir,dispatch=nativeDispatch}) {
  const queue=path.join(stateDir,"plugins","wtc","publication-receipts");
  const running=new Map(), retried=new Map();
  const store=receipt=>{
    mkdirSync(queue,{recursive:true,mode:0o700});
    const id=createHash("sha256").update(JSON.stringify([receipt.sessionKey,receipt.worktree,receipt.url,receipt.branch])).digest("hex");
    const file=path.join(queue,id+".json"), temporary=file+"."+randomUUID();
    writeFileSync(temporary,JSON.stringify(receipt)+"\n",{mode:0o600,flag:"wx"});
    renameSync(temporary,file);
    return file;
  };
  const enlist=(file,receipt)=>{
    if(running.has(file))return running.get(file);
    const result=(async()=>{
      const folder=publicationFolder(stateDir,receipt.collection,receipt.worktree);
      if(!folder)throw Error("The published checkout is not linked to one collection. Refresh Collection and retry tracking; do not publish again.");
      const r=await run(["wtc","openclaw","enlist-publication","--collection",folder,"--worktree",receipt.worktree,"--url",receipt.url,"--branch",receipt.branch],{timeoutMs:10000});
      if(r.code!==0)throw Error("The PR was published, but collection tracking is awaiting repair. Refresh Collection or ask the conversation to inspect tracking; do not publish again.");
      unlinkSync(file);
      return {state:"enlisted"};
    })().finally(()=>running.delete(file));
    running.set(file,result);return result;
  };
  // Only receipts from a successful native publisher are queued. Refresh may
  // retry this local metadata write; it never retries a push or PR creation.
  const flush=async(children)=>{
    let files=[];try{files=readdirSync(queue).filter(f=>/^[a-f0-9]{64}\.json$/.test(f));}catch{return;}
    await Promise.all(files.slice(0,100).map(async name=>{
      const file=path.join(queue,name);
      if(Date.now()-(retried.get(file)??0)<30000)return;
      let receipt;try{receipt=JSON.parse(readFileSync(file,"utf8"));}catch{return;}
      if(!receipt || typeof receipt!=="object" || ["sessionKey","worktree","collection","url","branch"].some(key=>typeof receipt[key]!=="string"||!receipt[key]))return;
      const child=children.find(c=>c.sessionKey===receipt.sessionKey);
      const worktree=child?.entry?.workspaceDir||child?.entry?.spawnedCwd;
      if(!child?.entry?.worktree||!samePath(worktree,receipt.worktree))return;
      retried.set(file,Date.now());
      try{await enlist(file,receipt);}catch{/* Keep the receipt for a later verified refresh. */}
    }));
  };
  if(typeof api.registerGatewayMethod!=="function")return {flush};
  for(const route of ["github.publish","github.confirm","bitbucket.confirm"]){
    api.registerGatewayMethod(`wtc.${route}`,async({params={},respond})=>{
      try{
        if(typeof params.sessionKey!=="string"||!params.sessionKey||params.agentId!==undefined&&typeof params.agentId!=="string")throw Error("An explicit repository session is required.");
        const entry=await getSessionEntry({sessionKey:params.sessionKey,...(params.agentId?{agentId:params.agentId}:{})});
        const worktree=entry?.worktree&&(entry.workspaceDir||entry.spawnedCwd);
        if(!worktree)throw Error("This session owns no managed repository checkout.");
        const collection=String(entry.worktree.branch??"").replace(/^openclaw\//,"");
        if(route==="github.publish"&&params.selection?.source!=="personal")throw Error("Select your personal GitHub account before publishing.");
        const bitbucket=route==="bitbucket.confirm";
        const native=await dispatch(bitbucket?"plugins.sessionAction":`sessions.${route}`,bitbucket?{
          pluginId:"bitbucket",actionId:"publish-confirm",sessionKey:params.sessionKey,...(params.agentId?{agentId:params.agentId}:{}),
          payload:{requestId:params.requestId,requestDigest:params.requestDigest},
        }:params,{expectFinal:true,timeoutMs:120000});
        if(!native.ok){respond(false,native.payload,native.error);return;}
        const out=bitbucket?(native.payload?.result??native.payload):native.payload;
        if(out?.status!=="published"){respond(true,out);return;}
        let tracking;
        try{
          if(typeof out.url!=="string"||typeof out.branch!=="string"||!out.url||!out.branch)throw Error("The publisher returned no PR URL or captured branch; inspect its result before tracking. Do not publish again.");
          const receipt={sessionKey:params.sessionKey,worktree,collection,url:out.url,branch:out.branch};
          const file=store(receipt);
          tracking=await enlist(file,receipt);
        }catch(error){tracking={state:"pending",message:String(error.message??error)};}
        // Publication remains successful even if recording its metadata fails.
        respond(true,{...out,tracking});
      }catch(error){respond(false,undefined,{code:"UNAVAILABLE",message:String(error.message??error)});}
    },{scope:"operator.write",profileAccess:"required",sessionAccess:{mode:"write"}});
  }
  return {flush};
}
