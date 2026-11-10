// Renders the Control UI's collection panel and composer card against a
// minimal DOM, so that a view which throws while rendering fails here rather
// than in someone's browser. Run with `node --test openclaw-plugin/test/`.
import { test, afterEach } from "node:test";
import assert from "node:assert/strict";

// Just enough of the DOM for el(), css() and the views: elements with
// children, text, attributes and style; nothing is laid out.
class Node {
  constructor(tag) {
    this.tagName = tag.toUpperCase();
    this.children = [];
    this.parentNode = null;
    this.attributes = {};
    this.style = { cssText: "" };
    this.listeners = {};
  }
  get childNodes() { return this.children; }
  get scrollHeight() { return this.tagName === "PRE" ? 360 : 0; }
  get textContent() { return this.children.map((c) => (typeof c === "string" ? c : c.textContent)).join(""); }
  set textContent(text) { this.replaceChildren(String(text ?? "")); }
  get isConnected() {
    let n = this;
    while (n.parentNode) n = n.parentNode;
    return n.root === true;
  }
  #adopt(kids) {
    return kids.map((k) => {
      if (typeof k !== "object") return String(k);
      k.remove();
      k.parentNode = this;
      return k;
    });
  }
  append(...kids) { this.children.push(...this.#adopt(kids)); }
  insertBefore(node, next) { node.remove(); const at = next ? this.children.indexOf(next) : this.children.length; node.parentNode = this; this.children.splice(at, 0, node); return node; }
  prepend(...kids) { this.children.unshift(...this.#adopt(kids)); }
  replaceChildren(...kids) {
    for (const c of this.children) if (typeof c === "object") c.parentNode = null;
    this.children = [];
    this.append(...kids);
  }
  remove() {
    if (!this.parentNode) return;
    const siblings = this.parentNode.children;
    siblings.splice(siblings.indexOf(this), 1);
    this.parentNode = null;
  }
  focus() { document.activeElement = this; }
  getBoundingClientRect() { return this.role === "menu" ? { width:this.popoverShown ? 252 : 0,height:this.popoverShown ? 180 : 0 } : { left:280,top:700,right:304,bottom:724,width:24,height:24 }; }
  showPopover() { this.onbeforetoggle?.({ newState: "open" }); this.popoverShown = true; this.ontoggle?.({ newState: "open" }); }
  hidePopover() { this.onbeforetoggle?.({ newState: "closed" }); this.popoverShown = false; this.ontoggle?.({ newState: "closed" }); }
  click() {
    this.onclick?.({ preventDefault() {}, stopPropagation() {} });
    const target = this.popoverTargetElement;
    if (target) target.popoverShown ? target.hidePopover() : target.showPopover();
  }
  scrollIntoView() { this.scrolled = true; }
  setAttribute(name, value) { this.attributes[name] = String(value); }
  getAttribute(name) { return this.attributes[name]; }
  querySelectorAll(selector) { return selector === "[data-repo-session]" ? [...this.all()].filter(n => n.attributes["data-repo-session"]) : []; }
  addEventListener(type, fn) { (this.listeners[type] ??= []).push(fn); }
  removeEventListener(type, fn) { this.listeners[type] = (this.listeners[type] ?? []).filter((f) => f !== fn); }
  // Every element below this one, depth first.
  *all() {
    for (const c of this.children) if (typeof c === "object") { yield c; yield* c.all(); }
  }
}

// A view whose test failed is never disposed; its refresh must not keep the
// run alive.
const setIntervalOf = globalThis.setInterval;
globalThis.setInterval = (fn, ms) => setIntervalOf(fn, ms).unref();

globalThis.document = {
  hidden: false,
  createElement: (tag) => new Node(tag),
  createElementNS: (_ns, tag) => new Node(tag),
  addEventListener() {},
  removeEventListener() {},
};

const { default: plugin } = await import("../dist/control-ui/v1/index.js");

// The plugin's panel and composer mounts, registered the way the Control UI
// registers them.
const mounts = {};
plugin.activate({
  request: async () => ({}),
  ui: {
    registerPanel: (p) => { mounts.panel = p.mount; },
    registerReplacement: (r) => { mounts.composer = r.mount; },
    registerAction: () => {},
    selectReplacement: () => {},
    openPanel: () => {},
  },
});

const settle = () => new Promise((resolve) => setTimeout(resolve, 0));
const container = () => Object.assign(new Node("div"), { root: true });
const texts = (node) => [...node.all()].map((n) => n.textContent);
const buttons = (node) => [...node.all()].filter((n) => n.tagName === "BUTTON").map((n) => n.textContent);
const titled = (node, title) => [...node.all()].find((n) => n.tagName === "BUTTON" && n.title === title);

// Every collection request is answered from `collections`, by session;
// wtc svc requests are recorded in `calls` and answered with `svcResult`.
const collections = new Map();
let controlledCollection;
// What each session's chat.history answers.
const histories = new Map();
const calls = [];
let svcResult = { ok: true, result: { message: "done" } };
let serviceLogsResult = { ok: true, result: { logs: "" } };
// What sessions.delete says it kept, by session.
let preserved = {};
let catchUpResult = { ok: true, result: { code: 0, json: { summary: "merged" } } };
let nextResult;
let restoreResult;
let githubPublishResult = { status: "needs_confirmation", requestId: "req-1" };
let githubConfirmResult;
let githubStatusResult;
let controlledSend;
let portals = [];
// The person's own GitHub connection, when a test gives them one.
let personal = null;
let controlledOptions;
// Methods a test makes fail.
const failing = new Set();
// The Bitbucket plugin: absent unless a test installs it with a login (or null).
let bitbucket;
let bitbucketRefusedAction;
let bitbucketConfirmResult;
let bitbucketPublishError;
let bitbucketDropDescription = false;
const hostRequest = async (method, params) => {
  if (failing.has(method)) throw new Error(`${method} failed`);
  if (method === "plugins.sessionAction" && params.pluginId === "bitbucket") {
    if (bitbucket === undefined) throw new Error(`unknown plugin session action: bitbucket/${params.actionId}`);
    if (params.actionId === "publish-options") return { ok: true, result: { account: bitbucket } };
    calls.push({ method, ...params });
    if (params.actionId === bitbucketRefusedAction) return { ok: true, result: { status: "blocked", message: "The requested publication is no longer valid." } };
    if (params.actionId === "publish") {
      if (bitbucketPublishError) throw bitbucketPublishError;
      return { ok: true, result: bitbucket ? { status: "needs_confirmation", requestId: "bb-1", confirmation: { account: bitbucket.user, name: bitbucket.name, requestDigest: "bd1", branch: "openclaw/test", head: "abcdef0123", dest: "main", uncommitted: 0, title: params.payload?.title, description: bitbucketDropDescription ? undefined : params.payload?.description } } : { status: "not_connected" } };
    }
    if (params.actionId === "publish-confirm") return { ok: true, result: bitbucketConfirmResult ?? { status: "published", created: true, url: "https://bitbucket.org/ws/hooks/pull-requests/3" } };
  }
  if (method === "sessions.github.options" && controlledOptions) return controlledOptions;
  if (method === "sessions.github.options") return { personal, shared: { source: "system-detected", login: "octo-bot" } };
  if (method === "sessions.github.status") return githubStatusResult ?? { confirmation: { generation: personal?.generation, account: personal?.account, requestDigest: "d1" } };
  if (method === "plugins.sessionAction" && params.actionId === "collection") {
    assert.equal(params.pluginId, "wtc", "visible branding must retain the installed plugin's action routing");
    if (controlledCollection) return controlledCollection(params);
    const data = collections.get(params.sessionKey);
    return data instanceof Error ? { ok: false, error: data.message } : { ok: true, result: data };
  }
  if (method === "portal.list") return { portals };
  if (method === "chat.history") return { messages: histories.get(params.sessionKey) ?? [] };
  if (method === "plugins.sessionAction" && params.actionId === "suggest") {
    calls.push({ method, ...params });
    return { ok: true, result: /site/.test(params.payload.text) ? { project: "wtc", repos: ["wtc-site"], source: "match" } : null };
  }
  calls.push({ method, ...params });
  if (method === "sessions.send" && controlledSend) return controlledSend;
  if (method === "plugins.sessionAction" && params.actionId === "svc") return svcResult;
  if (method === "plugins.sessionAction" && params.actionId === "svc-logs") return typeof serviceLogsResult === "function" ? serviceLogsResult(params) : serviceLogsResult;
  if (method === "plugins.sessionAction" && params.actionId === "catch-up") return catchUpResult;
  if (method === "plugins.sessionAction" && params.actionId === "next") return typeof nextResult === "function" ? nextResult(params) : nextResult;
  if (method === "plugins.sessionAction" && params.actionId === "restore-checkout") return typeof restoreResult === "function" ? restoreResult(params) : restoreResult;
  if (method === "sessions.delete") return { ok: true, key: params.key, deleted: true, archived: [], ...(preserved[params.key] ? { worktreePreserved: preserved[params.key] } : {}) };
  if (method === "sessions.github.publish") return typeof githubPublishResult === "function" ? githubPublishResult(params) : githubPublishResult;
  if (method === "sessions.github.confirm") return typeof githubConfirmResult === "function" ? githubConfirmResult(params) : githubConfirmResult ?? { status: "published", url: "https://github.com/octo/api/pull/7" };
  return {};
};

let sessions = 0;
const openedPanels = [];
const contexts = new WeakMap();
const detailViews = [];
afterEach(() => { for (const view of detailViews.splice(0)) view.dispose(); });
// Include full outcome text from accessible status/details labels in feedback assertions.
const readable = node => [node.textContent, ...[...node.all()].filter(n => n.className?.includes("wtc-header-feedback") || n.className?.includes("wtc-batch-result") || n.className?.split(" ").includes("wtc-step-status")).map(n => n.ariaLabel ?? n.title ?? "")].join("\n");
async function reviewAction(host) {
  let action = [...host.all()].find(n => n.tagName === "BUTTON" && n.textContent === "Review PR text");
  if (action) return action;
  const trigger = [...host.all()].find(n => n.className?.includes("wtc-header-feedback"));
  assert.ok(trigger, "publication outcome opens its sidebar details");
  trigger.onclick({ preventDefault() {}, stopPropagation() {} });
  const [, props] = openedPanels.at(-1);
  assert.equal(props.focus, "activity");
  const ctx = contexts.get(host);
  const details = await mount("panel", collections.get(ctx.props.sessionKey), undefined, ctx.props.sessionKey, { focus: "activity" });
  detailViews.push(details.view);
  action = [...details.host.all()].find(n => n.tagName === "BUTTON" && n.textContent === "Review PR text");
  assert.ok(action, "the full result retains its explicit follow-up action");
  return action;
}

async function mount(kind, data, history, key, props = {}) {
  const sessionKey = key ?? `agent:main:test:${++sessions}`;
  if (history) histories.set(sessionKey, history);
  collections.set(sessionKey, data);
  const host = container();
  const controller = new AbortController();
  const ctx = { props: { sessionKey, agentId: "main", ...props }, signal: controller.signal, presented: true, mountDefault: () => () => {} };
  ctx.host = { request: hostRequest, ui: { openPanel(...args) { openedPanels.push(args); } } };
  contexts.set(host, ctx);
  const view = mounts[kind](host, ctx);
  await settle();
  return { host, view, context: ctx, abort: () => controller.abort() };
}

const collection = (repos, services = []) => ({
  collection: { label: "test collection", folder: "/state/wtc-collections/test" },
  repos,
  services,
});
const repo = (name, fields = {}) => ({
  repo: name, owner: "octo", forge: "github", sessionKey: `agent:main:owner:${name}`,
  branch: "openclaw/test", base: "origin/main", ahead: 0, behind: 0, ...fields,
});
const busy = collection([
  repo("api", {
    ahead: 2, unpublished: 2, behind: 3,
    pullRequest: { number: 7, state: "open", title: "Add paging", url: "https://github.com/octo/api/pull/7",
      checks: "failure", passed: 1, failed: 1, running: 0, mergeable: "conflicting", review: "review_required" },
  }),
  repo("web", { dirty: 1 }),
  repo("hooks", { forge: "bitbucket", ahead: 1, unpublished: 1 }),
], [
  { group: "api/web", path: "api/web/main", repo: "api", kind: "service", label: "preview", state: "ready", url: "http://127.0.0.1:42201" },
  { group: "api/web", path: "api/web/cloudflare", repo: "api", kind: "endpoint", provider: "cloudflare", state: "ready", url: "https://calm-river.trycloudflare.com" },
  { group: "api/worker", path: "api/worker", repo: "api", kind: "service", state: "exited", exit_code: 1 },
]);

// The card's cells, row by row: a table is a grid of a header and rows of
// as many cells.
const tables = (node) => [...node.all()].filter((n) => n.className === "wtc-table").map((t) => {
  const cells = t.children.filter(n => !n.attributes["data-service-tunnels"] && n.className !== "wtc-action-preview-wash");
  const width = Number(t.attributes["data-columns"]);
  const rows = [];
  for (let i = 0; i < cells.length; i += width) rows.push(cells.slice(i, i + width).map((c) => {
    // A choice reads as its prompt, not its options.
    const pick = [...c.all()].find((n) => n.tagName === "SELECT");
    if (pick) return pick.children[0].textContent;
    const action = [...c.all()].find(n => n.className?.split(" ").includes("wtc-next-step"));
    if (action) return action.children.find(n=>n.className?.includes("wtc-primary-action"))?.textContent ?? "";
    const tunnel = [...c.all()].find(n => n.className === "wtc-tunnel-trigger");
    return tunnel ? "+" : c.textContent;
  }));
  return rows;
});

test("panel: no collection yet says what one is, not the conversation's label", async () => {
  const { host, view } = await mount("panel", { ...collection([]), collection: { label: "[OpenClaw heartbeat poll]", sessionKey: "agent:main:dashboard:new" }, version: { plugin: "0.1.50-dev+g317ca2a.20261023.0912", wtc: "0.1.50-dev+g317ca2a.20261023.0912" } });
  const all = texts(host);
  assert.ok(all.includes("No collection yet"));
  assert.ok(all.some((t) => /^A collection is this conversation's worktrees of a taba project's repositories/.test(t ?? "")));
  assert.ok(all.includes("Add a repository"));
  assert.ok(!all.some((t) => /heartbeat poll|^null$|0 repositories|None running/.test(t ?? "")));
  assert.ok(all.includes("plugin 0.1.50-dev+g317ca2a.20261023.0912, wtc 0.1.50-dev+g317ca2a.20261023.0912"));
  view.dispose();
  // Without versions, no footer, and no "null" for it.
  const bare = await mount("panel", collection([]));
  assert.ok(!texts(bare.host).some((t) => t === "null" || /^plugin /.test(t ?? "")));
  bare.view.dispose();
});

test("service hover hints retain the readable stopped label in both views", async () => {
  for (const kind of ["composer", "panel"]) for (const state of ["idle", "absent"]) {
    const { host, view } = await mount(kind, collection([repo("api")], [{ group: "api/worker", path: "api/worker", repo: "api", kind: "service", label: "Worker", state }]));
    try { assert.ok([...host.all()].some(n => n.tagName === "SPAN" && n.textContent === "stopped" && n.title === "stopped")); }
    finally { view.dispose(); }
  }
});

test("the branded link names the plugin's and gateway's versions in both views", async () => {
  for (const kind of ["composer", "panel"]) {
    const { host, view } = await mount(kind, { ...busy, version: { plugin: "1.2.3", wtc: "1.2.4-dev+gabc1234" } });
    await settle();
    const brandLink = [...host.all()].find((n) => n.tagName === "A" && n.ariaLabel === "taba home");
    assert.equal(brandLink.href, "https://taba.sh/");
    assert.match(brandLink.title, /\nplugin 1\.2\.3, wtc 1\.2\.4-dev\+gabc1234$/);
    view.dispose();
  }
});

test("panel: the collection's taba project", async () => {
  for (const [project, want] of [["wtc", "taba project wtc"], ["ad-hoc", "ad-hoc taba project"]]) {
    const { host, view } = await mount("panel", { ...busy, collection: { ...busy.collection, project } });
    assert.ok(texts(host).includes(want), want);
    view.dispose();
  }
});

test("panel: a busy collection", async () => {
  const { host, view } = await mount("panel", busy);
  const all = texts(host);
  // The shared branch once in the header, every repository's in its row.
  assert.ok(all.includes("3 repositories · 1/2 services running"));
  const [repos, services, prs] = tables(host);
  assert.deepEqual(repos[1], ["api", "openclaw/test", "↑ 2↓ 3", "#7✗✗○", ""]);
  // wtc status's PRs: number, repository, state, C M R, title.
  assert.deepEqual(prs, [["PR", "Repo", "State", "C", "M", "R", "Title"], ["#7", "api", "open", "✗", "✗", "○", "Add paging"]]);
  assert.deepEqual(services[1].slice(1), ["preview", "api", "ready", ":42201", "calm-river✕", "+"]);
  assert.ok(titled(host, "preview is running. Stop it"));
  assert.ok(titled(host, "worker: exited (exit 1). Restart it"));
  assert.ok(titled(host, "Start all services"));
  assert.ok(titled(host, "Stop all services"));
  assert.ok(titled(host, "Close the cloudflare tunnel"));
  view.dispose();
});

test("panel: compact PR columns retain full names and numbers as hover text", async () => {
  const current={number:12345,state:"open",url:"https://example.invalid/pull/12345",title:"Preview changes"};
  const {host,view}=await mount("panel",collection([repo("extended-api-service",{pullRequest:current,pullRequests:[current]})]));
  try {
    const number=[...host.all()].find(n=>n.title === "Pull request #12345");
    assert.ok(number);
    assert.ok([...number.all()].some(n=>n.tagName === "A" && n.href === current.url));
    assert.ok([...host.all()].some(n=>n.tagName === "SPAN" && n.textContent === "extended-api-service" && n.title === "extended-api-service"));
  } finally {view.dispose();}
});

test("service names open read-only logs with refresh, empty, error and close states", async () => {
  const previous=serviceLogsResult;
  serviceLogsResult={ok:true,result:{logs:'missing PORT\n<script>untrusted</script>',truncated:true}};
  const {host,view}=await mount("panel",collection([repo("api")],[{group:"api/web",repo:"api",label:"Preview",kind:"service",state:"exited",exit_code:2}]));
  try {
    calls.length=0;await titled(host,"View logs for Preview").onclick();
    const log=[...host.all()].find(n=>n.className==="wtc-service-log");assert.ok(log);
    assert.ok(log.textContent.includes('<script>untrusted</script>'));assert.equal([...log.all()].some(n=>n.tagName==="SCRIPT"),false);
    assert.ok(log.textContent.includes("showing recent output only"));
    assert.deepEqual(calls.filter(c=>c.actionId==="svc-logs").map(c=>c.payload),[{group:"api/web"}]);
    assert.equal(calls.some(c=>c.actionId==="svc"),false);
    const services=[...host.all()].find(n=>n.className==="wtc-base-services");
    assert.ok(log.parentNode.children.indexOf(log)>services.parentNode.children.indexOf(services));
    serviceLogsResult={ok:false,error:"read refused"};await titled(host,"Refresh service logs").onclick();
    assert.match(host.textContent,/Could not read logs: read refused/);
    serviceLogsResult={ok:true,result:{logs:""}};await titled(host,"Refresh service logs").onclick();assert.match(host.textContent,/No logs recorded yet/);
    titled(host,"Close service logs").onclick();assert.equal([...host.all()].some(n=>n.className==="wtc-service-log"),false);
  } finally {view.dispose();serviceLogsResult=previous;}
});

test("service logs retain the same connected reading node across status rerenders and can select tasks", async () => {
  const previous=serviceLogsResult;serviceLogsResult={ok:true,result:{logs:"older\nlatest startup failure"}};
  const {host,view,context}=await mount("panel",collection([repo("api")],[
    {group:"api/web",path:"api/web/server",label:"Preview",kind:"service",state:"exited"},
    {group:"api/web",path:"api/web/job",label:"Prepare",kind:"job",state:"exited"},
    {group:"api/web",path:"api/web/tunnel",kind:"endpoint",state:"ready"},
  ]));
  try {
    await titled(host,"View logs for Preview").onclick();await settle();
    const pane=[...host.all()].find(n=>n.className==="wtc-service-log"),pre=[...pane.all()].find(n=>n.tagName==="PRE");
    assert.equal(calls.at(-1).payload.group,"api/web/server");
    assert.equal(pre.scrollTop,360,"new output opens at the tail");
    pre.scrollTop=78;let detached=false;const remove=pre.parentNode.remove.bind(pre.parentNode);pre.parentNode.remove=()=>{detached=true;remove();};
    view.update(context);
    assert.equal([...host.all()].find(n=>n.className==="wtc-service-log"),pane);assert.equal(pre.scrollTop,78);assert.equal(detached,false);
    assert.match(pane.textContent,/last 100 lines/);
    await titled(host,"Read logs for api/web/job").onclick();assert.equal(calls.at(-1).payload.group,"api/web/job");await settle();
    let latest=[...host.all()].find(n=>n.tagName==="PRE");assert.equal(latest.scrollTop,360);
    await titled(host,"Refresh service logs").onclick();await settle();latest=[...host.all()].find(n=>n.tagName==="PRE");assert.equal(latest.scrollTop,360,"Refresh opens new output at the tail");
    assert.equal(titled(host,"Read logs for api/web/tunnel"),undefined);
  } finally {view.dispose();serviceLogsResult=previous;}
});

test("flat service logs remain reachable and rows without a configured process do not offer log reads", async () => {
  const previous=serviceLogsResult;serviceLogsResult={ok:true,result:{logs:"main process output"}};
  const {host,view}=await mount("panel",collection([repo("api")],[
    {group:"api/web",path:"api/web",label:"Preview",kind:"service",state:"ready"},
    {group:"api/web",path:"api/web/migrate",label:"Migrate",kind:"job",state:"exited"},
    {group:"api/db",path:"api/@resources/db",label:"Database",kind:"resources",state:"ready"},
    {group:"api/new",path:"api/new/server",label:"New service",kind:"service",state:"unconfigured"},
    {group:"api",path:"api/@declarations",kind:"configuration",state:"unknown"},
  ]));
  try {
    await titled(host,"View logs for Preview").onclick();assert.equal(calls.at(-1).payload.group,"api/web");
    const main=titled(host,"Read logs for api/web");assert.ok(main);assert.equal(main.textContent,"main");
    await titled(host,"Read logs for api/web/migrate").onclick();await titled(host,"Read logs for api/web").onclick();assert.equal(calls.at(-1).payload.group,"api/web");
    const unavailable=[...host.all()].filter(n=>n.tagName==="BUTTON"&&n.title==="No process logs available yet");assert.equal(unavailable.length,3);
    const before=calls.length;for(const button of unavailable){assert.equal(button.disabled,true);await button.onclick();}assert.equal(calls.length,before);
  } finally {view.dispose();serviceLogsResult=previous;}
});

test("composer service names open logs in the sidebar, including an already mounted panel", async () => {
  const previous=serviceLogsResult;
  const data=collection([repo("api")],[{group:"api/web",label:"Preview",kind:"service",state:"ready"}]);
  serviceLogsResult={ok:true,result:{logs:"first log"}};
  const composer=await mount("composer",data);let panel;
  try {
    await titled(composer.host,"View logs for Preview").onclick();
    assert.equal(openedPanels.at(-1)[0],"collection");assert.equal(openedPanels.at(-1)[1].sessionKey,composer.context.props.sessionKey);
    assert.equal([...composer.host.all()].some(n=>n.className==="wtc-service-log"),false);
    panel=await mount("panel",data,undefined,composer.context.props.sessionKey);await settle();
    assert.match(panel.host.textContent,/first log/);
    serviceLogsResult={ok:true,result:{logs:"refreshed from composer"}};
    await titled(composer.host,"View logs for Preview").onclick();await settle();
    assert.match(panel.host.textContent,/refreshed from composer/);
    assert.equal([...composer.host.all()].some(n=>n.className==="wtc-service-log"),false);
  } finally {panel?.view.dispose();composer.view.dispose();serviceLogsResult=previous;}
});

test("closing service logs prevents an in-flight read from reopening them", async () => {
  const previous=serviceLogsResult;let finish;
  serviceLogsResult=new Promise(resolve=>{finish=resolve;});
  const {host,view}=await mount("panel",collection([repo("api")],[{group:"api/web",label:"Preview",kind:"service",state:"ready"}]));
  try {
    const pending=titled(host,"View logs for Preview").onclick();
    assert.match(host.textContent,/Loading logs/);assert.equal(titled(host,"Refresh service logs").disabled,true);
    titled(host,"Close service logs").onclick();finish({ok:true,result:{logs:"late output"}});await pending;
    assert.equal([...host.all()].some(n=>n.className==="wtc-service-log"),false);
  } finally {finish?.({ok:true,result:{logs:""}});view.dispose();serviceLogsResult=previous;}
});

test("service logs cannot arrive in another conversation or reopen after a round trip", async () => {
  const previous=serviceLogsResult;let finish;
  serviceLogsResult=new Promise(resolve=>{finish=resolve;});
  const data=collection([repo("api")],[{group:"api/web",label:"Preview",kind:"service",state:"ready"}]);
  const {host,view,context}=await mount("panel",data);const other="agent:main:logs-other";collections.set(other,data);
  try {
    const pending=titled(host,"View logs for Preview").onclick();
    view.update({...context,props:{sessionKey:other,agentId:"main"}});await settle();
    assert.equal([...host.all()].some(n=>n.className==="wtc-service-log"),false);
    view.update(context);await settle();finish({ok:true,result:{logs:"old conversation output"}});await pending;
    assert.equal([...host.all()].some(n=>n.className==="wtc-service-log"),false);
    assert.ok(!host.textContent.includes("old conversation output"));
  } finally {finish?.({ok:true,result:{logs:""}});view.dispose();collections.delete(other);serviceLogsResult=previous;}
});

test("panel: repositories on different branches", async () => {
  const { host, view } = await mount("panel", collection([
    repo("api", { ahead: 1 }), repo("web", { ahead: 1 }), repo("docs", { branch: "release", ahead: 1 }), repo("site"),
  ]));
  // One with nothing on its branch yet shows its base.
  assert.deepEqual(tables(host)[0].slice(1).map((r) => r[1]), ["openclaw/test", "openclaw/test", "release", "at main"]);
  view.dispose();
});

test("panel: a collection that cannot be loaded", async () => {
  const { host, view } = await mount("panel", new Error("wtc status failed"));
  assert.ok(texts(host).includes("The collection could not be loaded: wtc status failed"));
  view.dispose();
});

test("panel: a tunnel started as a service of its own", async () => {
  const { host, view } = await mount("panel", collection([repo("api")], [
    { group: "api/web", path: "api/web/main", repo: "api", kind: "service", label: "preview", state: "ready", url: "http://127.0.0.1:42201" },
    { group: "api/share", path: "api/share/main", repo: "api", kind: "service", state: "ready", url: "https://calm-river.trycloudflare.com" },
  ]));
  const all = texts(host);
  assert.equal([...host.all()].filter((n) => n.title === "Started apart from the service it exposes").length, 1);
  assert.ok(buttons(host).includes("Consolidate"));
  view.dispose();
});

test("composer: no card for a session without repositories", async () => {
  const { host, view } = await mount("composer", collection([]));
  assert.equal([...host.all()].some((n) => n.className === "wtc-collection-card"), false);
  view.dispose();
});

test("next actions: run the rechecked step and hand judgment to the conversation", async () => {
  for (const kind of ["composer", "panel"]) {
    const step = { step: "return-to-tip", by: "wtc", reason: "The pull request merged", key: "tip:1", command: ["wtc", "catch-up"] };
    const r = repo("api", { next: step });
    const { host, view } = await mount(kind, collection([r]));
    const action = () => [...host.all()].find((n) => n.tagName === "BUTTON" && n.title === step.reason);
    assert.ok(action());
    nextResult = { ok: true, result: { performed: true, repo: r, code: 0, json: { summary: "returned to tip" } } };
    calls.length = 0;
    action().onclick({ preventDefault() {}, stopPropagation() {} });
    await settle();
    assert.deepEqual(calls.map((c) => c.actionId), ["next"]);
    assert.deepEqual(calls[0].payload, { ownerSessionKey: r.sessionKey, key: step.key });
    assert.match(readable(host), /returned to tip/);
    nextResult = { ok: false, error: "the next step changed" };
    action().onclick({ preventDefault() {}, stopPropagation() {} });
    await settle();
    assert.match(readable(host), /the next step changed/);
    const approved = { ...step, step: "catch-up", approve: "agent" };
    nextResult = { ok: true, result: { performed: false, repo: { ...r, next: approved } } };
    calls.length = 0;
    action().onclick({ preventDefault() {}, stopPropagation() {} });
    await settle();
    assert.deepEqual(calls.map((c) => c.actionId ?? c.method), ["next"]);
    assert.match(readable(host), /did not execute this deterministic catch-up/);
    view.dispose();
  }
});

test("pending files offer a commit handoff and publication refusal is a visible failure for the selected repository", async () => {
  for (const kind of ["composer", "panel"]) {
    const { host, view } = await mount(kind, collection([repo("harness"), repo("site", { dirty: 14, unpublished: 1 })]));
    const commit = [...host.all()].find((n) => n.tagName === "BUTTON" && n.textContent === "Commit changes");
    assert.ok(commit, "a commit action is visible for pending files");
    calls.length = 0;
    commit.onclick({ preventDefault() {}, stopPropagation() {} });
    await settle();
    assert.deepEqual(calls.map((c) => c.method), ["sessions.send"]);
    assert.match(calls[0].message, /^\/skill wtc site: review and commit/);
    assert.match(readable(host), /site: asked the conversation to save intended changes as local commits/);
    assert.match(readable(host), /Pushing and opening a PR are separate/);
    personal = { state: "connected", account: { id: 1, login: "octo" }, generation: "g1" };
    githubPublishResult = { status: "blocked", message: "The managed branch cannot publish yet. Review and commit its intended changes first." };
    const push = [...host.all()].find((n) => n.tagName === "BUTTON" && /^14 uncommitted files and 1 commit to publish/.test(n.title));
    assert.ok(push);
    calls.length = 0;
    await push.onclick();
    await settle();
    assert.equal(calls.find((c) => c.method === "sessions.github.publish").sessionKey, "agent:main:owner:site");
    assert.ok([...host.all()].some((n) => (n.role ?? n.attributes.role) === "alert" && /site: blocked/.test(n.ariaLabel ?? n.textContent)));
    assert.match(readable(host), /Review and commit its intended changes first/);
    personal = null;
    githubPublishResult = { status: "needs_confirmation", requestId: "req-1" };
    view.dispose();
  }
});

test("Bitbucket publication and confirmation refusals show a full alert without reporting a push", async () => {
  for (const kind of ["composer", "panel"]) for (const stage of ["publish", "publish-confirm"]) {
    bitbucket = { user: "ada@example.com", name: "Ada" };
    bitbucketRefusedAction = stage;
    const { host, view } = await mount(kind, collection([repo("hooks", { forge: "bitbucket", unpublished: 1 })]));
    try {
      const push = [...host.all()].find((n) => n.tagName === "BUTTON" && /^1 commit to push/.test(n.title));
      await push.onclick();
      await settle();
      if (stage === "publish-confirm") {
        const confirm = [...host.all()].find((n) => n.tagName === "BUTTON" && n.textContent === "Confirm push");
        assert.ok(confirm);
        await confirm.onclick();
        await settle();
      }
      assert.ok([...host.all()].some((n) => (n.role ?? n.attributes.role) === "alert" && /hooks: blocked.*no longer valid/.test(n.ariaLabel ?? n.textContent)));
      assert.doesNotMatch(readable(host), /hooks: pushed/);
    } finally {
      view.dispose();
      bitbucket = undefined;
      bitbucketRefusedAction = undefined;
    }
  }
});

test("composer: the card for a busy collection", async () => {
  const { host, view } = await mount("composer", busy);
  const card = [...host.all()].find((n) => n.className === "wtc-collection-card");
  assert.ok(card, "the card is shown");
  assert.equal(host.children[0], card, "above the message box");
  const [repos, services] = tables(card);
  // The browser groups related facts in stable places, with the branch below its repository.
  assert.deepEqual(repos, [
    ["Repository", "Branch", "Changes", "Pull request", "Next"],
    ["api", "openclaw/test", "↑ 2↓ 3", "#7✗✗○", ""],
    ["web", "openclaw/test", "± 1", "", ""],
    ["hooks", "openclaw/test", "↑ 1", "", ""],
  ]);
  // A service and its tunnel are one row; the tunnel has its column.
  assert.deepEqual(services.map((r) => r.slice(1)), [
    ["Service", "Repo", "State", "URL", "cloudflare", ""],
    ["preview", "api", "ready", ":42201", "calm-river✕", "+"],
    ["worker", "api", "exited (exit 1)", "·", "·", "+"],
  ]);
  assert.ok(titled(card, "preview is running. Stop it"));
  assert.ok(titled(card, "Start all services"));
  assert.ok(titled(card, "Close the cloudflare tunnel"));
  view.dispose();
});

test("composer: the actions are on the facts they change", async () => {
  const { host, view } = await mount("composer", busy);
  const card = [...host.all()].find((n) => n.className === "wtc-collection-card");
  const named = (re) => [...card.all()].find((n) => n.tagName === "BUTTON" && re.test(n.title));
  calls.length = 0;
  await named(/^3 new on main\. Catch up/).onclick();
  assert.deepEqual(calls.map((c) => c.actionId), ["catch-up"]);
  // A catch-up that stops on a conflict goes to the conversation too.
  catchUpResult = { ok: true, result: { code: 1, json: null, message: "wtc: merging origin/main into openclaw/test conflicts in: README.md, go.mod. The merge was aborted and the checkout is as before. Merge by hand with `git merge origin/main`, resolve, commit — do not rebase\n" } };
  calls.length = 0;
  await named(/^3 new on main\. Catch up/).onclick();
  assert.deepEqual(calls.map((c) => c.actionId ?? c.method), ["catch-up", "sessions.send"]);
  assert.equal(calls[1].message, "/skill wtc-catch-up api: catching up with origin/main conflicts in README.md, go.mod.");
  catchUpResult = { ok: true, result: { code: 0, json: { summary: "merged" } } };
  await named(/^#7 conflicts with main\. Resolve/).onclick();
  assert.equal(calls.at(-1).message, "/skill wtc-catch-up api: pull request #7 conflicts with main.");
  // Without the Bitbucket plugin's publish, Bitbucket is not pushed from
  // here: pushing is never handed to the agent.
  assert.equal(named(/^1 commit to push/), undefined);
  view.dispose();
});

test("composer: Bitbucket is pushed by the Bitbucket plugin, with the person's own login, after they confirm", async () => {
  bitbucket = null;
  const { host, view } = await mount("composer", busy);
  await settle();
  const card = () => [...host.all()].find((n) => n.className === "wtc-collection-card");
  const push = () => [...card().all()].find((n) => n.tagName === "BUTTON" && /^1 commit to push/.test(n.title));
  calls.length = 0;
  await push().onclick();
  await settle();
  // Not connected: the card says where to connect, and nothing is pushed.
  const howTo = [...host.all()].find((n) => n.className === "wtc-connect");
  assert.match(howTo.textContent, /Pushing needs your own Bitbucket login/);
  assert.ok([...howTo.all()].some((n) => n.tagName === "A" && n.href === "/plugins/bitbucket/connect"));
  assert.equal(calls.some((c) => c.actionId === "publish-confirm"), false);
  // Connected: asking names the commit and account, and confirming pushes.
  bitbucket = { user: "ada@example.com", name: "Ada" };
  await push().onclick();
  await settle();
  assert.deepEqual(calls.at(-1).payload, { draft: true, dest: "main" });
  const confirm = [...host.all()].find((n) => n.tagName === "BUTTON" && n.textContent === "Confirm push");
  assert.equal(confirm.title, "Confirm the captured push of hooks as Ada (ada@example.com)");
  assert.match(readable(host), /1 commit/);
  await confirm.onclick();
  await settle();
  assert.deepEqual(calls.at(-1), { method: "plugins.sessionAction", pluginId: "bitbucket", actionId: "publish-confirm", sessionKey: "agent:main:owner:hooks", agentId: "main", payload: { requestId: "bb-1", requestDigest: "bd1" } });
  bitbucket = undefined;
  view.dispose();
});

test("publication results distinguish new and existing Bitbucket PRs and offer text review in both views", async () => {
  for (const kind of ["composer", "panel"]) for (const created of [true, false]) {
    bitbucket = { user: "ada@example.com", name: "Ada" };
    bitbucketConfirmResult = { status: "published", created, url: "https://bitbucket.org/example/docs/pull-requests/8" };
    const { host, view } = await mount(kind, collection([repo("docs", { forge: "bitbucket", unpublished: 2, dirty: 1 })]));
    try {
      calls.length = 0;
      const push = [...host.all()].find((n) => n.tagName === "BUTTON" && /^2 commits to push/.test(n.title));
      assert.ok(push);
      assert.doesNotMatch(push.title, /Commit pending changes/);
      assert.match(push.title, /Uncommitted files stay behind/);
      await push.onclick();
      await settle();
      await [...host.all()].find((n) => n.tagName === "BUTTON" && n.textContent === "Confirm push").onclick();
      await settle();
      assert.match(readable(host), created ? /pushed commits and created draft PR #8/ : /pushed commits to existing PR #8\. Its title and description are unchanged/);
      assert.equal(calls.some((c) => c.method === "sessions.send"), false, "publishing alone does not start text review");
      const review = await reviewAction(host);
      assert.ok(review, `${kind}: result keeps its follow-up after refresh`);
      review.onclick({ preventDefault() {}, stopPropagation() {} });
      await settle();
      const request = calls.find((c) => c.method === "sessions.send");
      assert.ok(request);
      assert.ok(request.key, "the handoff names the task conversation");
      assert.notEqual(request.key, "agent:main:owner:docs");
      assert.match(request.message, /\/skill wtc docs: review the title and description of https:\/\/bitbucket.org\/example\/docs\/pull-requests\/8/);
      assert.match(request.message, /full current PR diff/);
      assert.match(request.message, /Preserve useful human edits/);
      assert.match(request.message, /propose a revised title and description/);
      assert.match(request.message, /not a request for a code review/);
      assert.match(readable(host), /asked the conversation to review the PR title and description/);
      assert.equal([...host.all()].some((n) => n.tagName === "BUTTON" && n.textContent === "Review PR text"), false);
    } finally {
      view.dispose();
      bitbucket = undefined;
      bitbucketConfirmResult = undefined;
    }
  }
});

test("publication keeps the successful PR result and shows a local-status warning", async () => {
  for (const kind of ["composer", "panel"]) {
    bitbucket = { user: "ada@example.com", name: "Ada" };
    bitbucketConfirmResult = { status: "published", created: false, url: "https://bitbucket.org/example/docs/pull-requests/8", warning: "Local status could not be updated; fetch and prune origin, then refresh." };
    const { host, view } = await mount(kind, collection([repo("docs", { forge: "bitbucket", unpublished: 2 })]));
    try {
      await [...host.all()].find((n) => n.tagName === "BUTTON" && /^2 commits to push/.test(n.title)).onclick();
      await settle();
      await [...host.all()].find((n) => n.tagName === "BUTTON" && n.textContent === "Confirm push").onclick();
      await settle();
      assert.match(readable(host), /pushed commits to existing PR #8/);
      assert.match(readable(host), /Warning: Local status could not be updated/);
      assert.doesNotMatch(readable(host), /Action failed/);
      assert.match(readable(host), /fetch and prune origin/);
      assert.ok(await reviewAction(host));
    } finally {
      view.dispose();
      bitbucket = undefined;
      bitbucketConfirmResult = undefined;
    }
  }
});

test("Bitbucket dirty files offer commits; push appears only as a disabled alternative", async () => {
  bitbucket = { user: "ada@example.com", name: "Ada" };
  for (const kind of ["composer", "panel"]) {
    const { host, view } = await mount(kind, collection([repo("docs", { forge: "bitbucket", dirty: 2, ahead: 3, unpublished: 0, published: true })]));
    try {
      assert.ok([...host.all()].some((n) => n.tagName === "BUTTON" && n.textContent === "Commit changes"));
      const pushes=[...host.all()].filter(n=>n.tagName==="BUTTON"&&/to push/.test(n.title));
      assert.ok(pushes.every(n=>n.disabled&&n.role==="menuitem"),"only disabled menu explanations may mention a push; no executable empty push");
    } finally { view.dispose(); }
  }
  bitbucket = undefined;
});

test("a PR text review handoff failure is visible and never claims the text was updated", async () => {
  for (const kind of ["composer", "panel"]) {
    bitbucket = { user: "ada@example.com", name: "Ada" };
    bitbucketConfirmResult = { status: "published", created: false, url: "https://bitbucket.org/example/docs/pull-requests/8" };
    const { host, view } = await mount(kind, collection([repo("docs", { forge: "bitbucket", unpublished: 1 })]));
    try {
      await [...host.all()].find((n) => n.tagName === "BUTTON" && /^1 commit to push/.test(n.title)).onclick();
      await settle();
      await [...host.all()].find((n) => n.tagName === "BUTTON" && n.textContent === "Confirm push").onclick();
      await settle();
      failing.add("sessions.send");
      (await reviewAction(host)).onclick({ preventDefault() {}, stopPropagation() {} });
      await settle();
      assert.ok([...host.all()].some((n) => (n.role ?? n.attributes.role) === "alert" && /sessions.send failed/.test(n.ariaLabel ?? n.textContent)));
      assert.doesNotMatch(readable(host), /asked the conversation|updated.*description/);
    } finally { failing.delete("sessions.send"); view.dispose(); bitbucket = undefined; bitbucketConfirmResult = undefined; }
  }
});

test("a GitHub URL alone does not claim a new PR was created", async () => {
  personal = { state: "connected", account: { id: 1, login: "octo" }, generation: "g1" };
  const previous = githubPublishResult;
  githubPublishResult = { status: "published", url: "https://github.com/example/docs/pull/8" };
  const { host, view } = await mount("composer", collection([repo("docs", { unpublished: 1 })]));
  try {
    await [...host.all()].find((n) => n.tagName === "BUTTON" && /^1 commit to push/.test(n.title)).onclick();
    await settle();
    assert.match(readable(host), /pushed branch commits\. Pull request: PR #8/);
    assert.doesNotMatch(readable(host), /created (draft )?PR/);
    assert.ok(await reviewAction(host));
  } finally { view.dispose(); personal = null; githubPublishResult = previous; }
});

test("PR text review never follows a publication into another conversation", async () => {
  for (const kind of ["composer", "panel"]) for (const delayed of [false, true]) {
    bitbucket = { user: "ada@example.com", name: "Ada" };
    let complete;
    const result = { status: "published", created: false, url: "https://bitbucket.org/example/docs/pull-requests/8" };
    bitbucketConfirmResult = delayed ? new Promise((resolve) => { complete = resolve; }) : result;
    const { host, view } = await mount(kind, collection([repo("docs", { forge: "bitbucket", unpublished: 1 })]));
    try {
      await [...host.all()].find((n) => n.tagName === "BUTTON" && /^1 commit to push/.test(n.title)).onclick();
      await settle();
      const pending = [...host.all()].find((n) => n.tagName === "BUTTON" && n.textContent === "Confirm push").onclick();
      await settle();
      const oldAction = delayed ? undefined : await reviewAction(host);
      if (!delayed) assert.ok(oldAction);
      const nextKey = `agent:main:other:${++sessions}`;
      collections.set(nextKey, { ...collection([repo("other")]), collection: { sessionKey: nextKey } });
      view.update({ props: { sessionKey: nextKey, agentId: "main" }, host: { request: hostRequest, ui: { openPanel() {} } }, signal: new AbortController().signal, presented: true, mountDefault: () => () => {} });
      if (delayed) complete(result);
      await pending;
      await settle();
      assert.equal([...host.all()].some((n) => n.tagName === "BUTTON" && n.textContent === "Review PR text"), false);
      assert.doesNotMatch(readable(host), /pushed commits to existing PR #8/);
      calls.length = 0;
      oldAction?.onclick({ preventDefault() {}, stopPropagation() {} });
      await settle();
      assert.equal(calls.some((c) => c.method === "sessions.send"), false);
    } finally { view.dispose(); bitbucket = undefined; bitbucketConfirmResult = undefined; }
  }
});

for (const kind of ["composer", "panel"]) test(`${kind}: a delayed PR text review failure stays in its original conversation`, async () => {
  bitbucket = { user: "ada@example.com", name: "Ada" };
  bitbucketConfirmResult = { status: "published", created: false, url: "https://bitbucket.org/example/docs/pull-requests/8" };
  const { host, view } = await mount(kind, collection([repo("docs", { forge: "bitbucket", unpublished: 1 })]));
  try {
    await [...host.all()].find((n) => n.tagName === "BUTTON" && /^1 commit to push/.test(n.title)).onclick();
    await settle();
    await [...host.all()].find((n) => n.tagName === "BUTTON" && n.textContent === "Confirm push").onclick();
    await settle();
    let reject;
    controlledSend = new Promise((resolve, fail) => { reject = fail; });
    (await reviewAction(host)).onclick({ preventDefault() {}, stopPropagation() {} });
    await settle();
    const nextKey = `agent:main:other:${++sessions}`;
    collections.set(nextKey, { ...collection([repo("other")]), collection: { sessionKey: nextKey } });
    view.update({ props: { sessionKey: nextKey, agentId: "main" }, host: { request: hostRequest, ui: { openPanel() {} } }, signal: new AbortController().signal, presented: true, mountDefault: () => () => {} });
    await settle();
    reject(new Error("The old PR text review failed"));
    await settle();
    assert.doesNotMatch(readable(host), /The old PR text review failed/);
    assert.equal([...host.all()].some((n) => (n.role ?? n.attributes.role) === "alert"), false);
  } finally { view.dispose(); controlledSend = undefined; bitbucket = undefined; bitbucketConfirmResult = undefined; }
});

for (const kind of ["composer", "panel"]) test(`${kind}: a delayed next-action check cannot publish into another conversation`, async () => {
  personal = { state: "connected", account: { id: 1, login: "octo" }, generation: "g1" };
  const step = { step: "push", by: "wtc", approve: "person", gateway: "github_publish", key: "docs:push:head1", reason: "One commit is ready to push" };
  const r = repo("docs", { unpublished: 1, next: step });
  const { host, view } = await mount(kind, collection([r]));
  try {
    let finish;
    nextResult = new Promise((resolve) => { finish = resolve; });
    calls.length = 0;
    titled(host, step.reason).onclick({ preventDefault() {}, stopPropagation() {} });
    await settle();
    const nextKey = `agent:main:other:${++sessions}`;
    collections.set(nextKey, { ...collection([repo("other")]), collection: { sessionKey: nextKey } });
    view.update({ props: { sessionKey: nextKey, agentId: "main" }, host: { request: hostRequest, ui: { openPanel() {} } }, signal: new AbortController().signal, presented: true, mountDefault: () => () => {} });
    finish({ ok: true, result: { performed: false, repo: r } });
    await settle();
    assert.equal(calls.some((c) => c.method === "sessions.github.publish" || c.method === "sessions.send"), false);
    assert.doesNotMatch(readable(host), /Preparing to push docs|confirm to push/);
  } finally { view.dispose(); personal = null; nextResult = undefined; }
});

for (const phase of ["prepare", "confirm"]) for (const switchConversation of [false, true]) test(`composer: ${phase} batch ${switchConversation ? "stops after switching conversations" : "completes in its original conversation"}`, async () => {
  personal = { state: "connected", account: { id: 1, login: "octo" }, generation: "g1" };
  const previous = githubPublishResult;
  const prepared = { status: "needs_confirmation", requestId: "req-1" };
  githubPublishResult = prepared;
  const { host, view } = await mount("composer", collection([repo("api", { unpublished: 1 }), repo("docs", { unpublished: 1 })]));
  try {
    const push = () => titled(host, "Push all 2 eligible repositories; prepare each personal confirmation");
    if (phase === "confirm") { await push().onclick(); await settle(); }
    let finish;
    const delayed = new Promise((resolve) => { finish = resolve; });
    const published = (params) => ({ status: "published", url: `https://github.com/example/${params.sessionKey.endsWith(":api") ? "api" : "docs"}/pull/8` });
    if (phase === "prepare") githubPublishResult = (params) => params.sessionKey.endsWith(":api") ? delayed : prepared;
    else githubConfirmResult = (params) => params.sessionKey.endsWith(":api") ? delayed : published(params);
    calls.length = 0;
    const pending = phase === "prepare" ? push().onclick() : [...host.all()].find(n => n.tagName === "BUTTON" && n.title === "Confirm the captured push of api as @octo").onclick();
    await settle();
    if (switchConversation) {
      const nextKey = `agent:main:other:${++sessions}`;
      collections.set(nextKey, { ...collection([repo("other")]), collection: { sessionKey: nextKey } });
      view.update({ props: { sessionKey: nextKey, agentId: "main" }, host: { request: hostRequest, ui: { openPanel() {} } }, signal: new AbortController().signal, presented: true, mountDefault: () => () => {} });
    }
    finish(phase === "prepare" ? prepared : published({ sessionKey: "agent:main:owner:api" }));
    await pending;
    await settle();
    const method = phase === "prepare" ? "sessions.github.publish" : "sessions.github.confirm";
    assert.equal(calls.filter((c) => c.method === method).length, phase === "confirm" ? 1 : switchConversation ? 1 : 2);
    const review = phase === "confirm" && !switchConversation ? await reviewAction(host) : undefined;
    if (switchConversation) {
      assert.equal(review, undefined);
      assert.doesNotMatch(readable(host), /Preparing to push docs|docs: pushed/);
    } else if (phase === "confirm") {
      assert.ok(review);
      review.onclick({ preventDefault() {}, stopPropagation() {} });
      await settle();
      assert.match(calls.find((c) => c.method === "sessions.send").message, /api: review.*example\/api\/pull\/8/);
    }
  } finally { view.dispose(); personal = null; githubPublishResult = previous; githubConfirmResult = undefined; }
});

test("composer: a push is the person's, with their own account, after they confirm", async () => {
  const { host, view } = await mount("composer", busy);
  const card = () => [...host.all()].find((n) => n.className === "wtc-collection-card");
  const push = () => [...card().all()].find((n) => n.tagName === "BUTTON" && /^2 commits to push/.test(n.title));
  // Without their own connection, nothing is pushed: the shared account is
  // never used.
  calls.length = 0;
  personal = null;
  await push().onclick();
  assert.equal(calls.some((c) => c.method === "sessions.github.publish"), false);
  // The card says how to connect it, linking to the profile page.
  const howTo = [...host.all()].find((n) => n.className === "wtc-connect");
  assert.ok(howTo, "how to connect is shown");
  assert.ok([...howTo.all()].some((n) => n.tagName === "A" && n.href === "/settings/profile"));
  assert.match(howTo.textContent, /For me \(not For the system\)/);
  // With it, publishing asks for a confirmation, which the ↑ turns into.
  personal = { state: "connected", account: { id: 1, login: "octo" }, generation: "g1" };
  await push().onclick();
  await settle();
  assert.deepEqual(calls.find((c) => c.method === "sessions.github.publish").selection, { source: "personal", account: { id: 1, login: "octo" }, generation: "g1" });
  const confirm = [...host.all()].find((n) => n.tagName === "BUTTON" && n.textContent === "Confirm push");
  assert.equal(confirm.title, "Confirm the captured push of api as @octo");
  assert.match(readable(host), /2 commits/);
  await confirm.onclick();
  await settle();
  assert.deepEqual(calls.find((c) => c.method === "sessions.github.confirm"),
    { method: "sessions.github.confirm", sessionKey: "agent:main:owner:api", agentId: "main", requestId: "req-1", generation: "g1", account: { id: 1, login: "octo" }, requestDigest: "d1" });
  personal = null;
  view.dispose();
});

test("composer: a new pull request opens with the title and description the agent proposed", async () => {
  personal = { state: "connected", account: { id: 1, login: "octo" }, generation: "g1" };
  bitbucket = { user: "ada@example.com", name: "Ada" };
  const proposal = { branch: "openclaw/test", title: "Add a web preview", body: "What, why, and how it was checked." };
  const data = collection([
    repo("web", { dirty: 1, unpublished: 1, proposal }),
    repo("hooks", { forge: "bitbucket", ahead: 1, unpublished: 1, proposal }),
    repo("docs", { ahead: 1 }),
    // An open pull request keeps its text: the gateway takes it only when it opens one.
    repo("api", { ahead: 1, unpublished: 1, proposal, pullRequest: { number: 7, state: "open", url: "https://github.com/octo/api/pull/7", checks: "none" } }),
  ]);
  const { host, view } = await mount("composer", data);
  await settle();
  const card = () => [...host.all()].find((n) => n.className === "wtc-collection-card");
  const push = (what) => [...card().all()].find((n) => n.tagName === "BUTTON" && n.title.startsWith(what));
  assert.match(push("1 uncommitted file and 1 commit to publish").title, /Title: "Add a web preview", with the agent's description\./);
  assert.match([...card().all()].find((n) => n.tagName === "BUTTON" && /Bitbucket/.test(n.title)).title, /Title: "Add a web preview", with the agent's description\.$/);
  assert.match(push("1 commit to push. Commit pending changes and push the branch; create a draft pull request if none exists, with your own GitHub").title, /proposed no title or description/);
  assert.doesNotMatch(push("1 commit to push. Commit pending changes and push the branch to PR #7").title, /Title|proposed/);
  calls.length = 0;
  await push("1 uncommitted file and 1 commit to publish").onclick();
  await settle();
  const published = calls.find((c) => c.method === "sessions.github.publish");
  assert.equal(published.title, "Add a web preview");
  assert.equal(published.body, "What, why, and how it was checked.");
  await push("1 commit to push. Commit pending changes and push the branch to PR #7").onclick();
  await settle();
  const again = calls.filter((c) => c.method === "sessions.github.publish").at(-1);
  assert.equal("title" in again || "body" in again, false);
  const bb = [...card().all()].find((n) => n.tagName === "BUTTON" && /^1 commit to push.*Bitbucket/.test(n.title));
  await bb.onclick();
  await settle();
  assert.deepEqual(calls.at(-1).payload, { draft: true, dest: "main", title: "Add a web preview", description: proposal.body });
  bitbucket = undefined;
  personal = null;
  view.dispose();
});

test("Bitbucket descriptions reach the publisher and the captured confirmation in both views", async () => {
  for (const kind of ["composer", "panel"]) for (const title of ["Proposed title", undefined]) {
    bitbucket = { user: "ada@example.com", name: "Ada" };
    const body = "# Summary\n\n**Markdown** 🗂️\n\n<em>literal text</em>\n";
    const proposal = { branch: "openclaw/test", body, ...(title ? { title } : {}) };
    const { host, view } = await mount(kind, collection([repo("docs", { forge: "bitbucket", unpublished: 1, proposal })]));
    try {
      calls.length = 0;
      const push = [...host.all()].find((n) => n.tagName === "BUTTON" && /^1 commit to push.*Bitbucket/.test(n.title));
      assert.match(push.title, /agent's description/);
      await push.onclick();
      await settle();
      const request = calls.find((c) => c.actionId === "publish");
      assert.equal(request.payload.description, body);
      assert.equal(request.payload.title, title);
      const confirm = [...host.all()].find((n) => n.tagName === "BUTTON" && n.textContent === "Confirm push");
      assert.match(readable(host), new RegExp(body.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")), "confirmation previews the exact captured description");
      assert.match(readable(host), /existing PR text is kept/);
      assert.equal([...host.all()].some((n) => n.tagName === "EM"), false, "description is plain text in the tooltip");
      await confirm.onclick();
      await settle();
      assert.equal(calls.find((c) => c.actionId === "publish-confirm").payload.requestDigest, "bd1");
    } finally { view.dispose(); bitbucket = undefined; }
  }
});

test("an older Bitbucket publisher requests an upgrade instead of dropping the description", async () => {
  for (const kind of ["composer", "panel"]) for (const drops of [false, true]) {
    bitbucket = { user: "ada@example.com", name: "Ada" };
    bitbucketDropDescription = drops;
    bitbucketPublishError = drops ? undefined : new Error("plugin session action payload does not match schema: must NOT have additional properties");
    const { host, view } = await mount(kind, collection([repo("docs", { forge: "bitbucket", unpublished: 1, proposal: { branch: "openclaw/test", body: "A useful description." } })]));
    try {
      calls.length = 0;
      await [...host.all()].find((n) => n.tagName === "BUTTON" && /^1 commit to push.*Bitbucket/.test(n.title)).onclick();
      await settle();
      assert.match(readable(host), /Bitbucket plugin 0\.3\.4 or later/);
      assert.match(readable(host), /nothing was published/);
      assert.equal(calls.filter((c) => c.actionId === "publish").length, 1, "no retry discards the body");
      assert.equal(calls.some((c) => c.actionId === "publish-confirm" || c.method === "sessions.send"), false);
    } finally { view.dispose(); bitbucket = undefined; bitbucketPublishError = undefined; bitbucketDropDescription = false; }
  }
});

test("a title-only Bitbucket proposal says the description is absent and remains compatible", async () => {
  bitbucket = { user: "ada@example.com", name: "Ada" };
  const { host, view } = await mount("composer", collection([repo("docs", { forge: "bitbucket", unpublished: 1, proposal: { branch: "openclaw/test", title: "Title only" } })]));
  try {
    calls.length = 0;
    const push = [...host.all()].find((n) => n.tagName === "BUTTON" && /^1 commit to push.*Bitbucket/.test(n.title));
    assert.match(push.title, /without a proposed description/);
    await push.onclick();
    await settle();
    assert.deepEqual(calls.find((c) => c.actionId === "publish").payload, { draft: true, dest: "main", title: "Title only" });
    assert.ok([...host.all()].find((n) => n.tagName === "BUTTON" && n.textContent === "Confirm push"));
  } finally { view.dispose(); bitbucket = undefined; }
});

test("composer: a repository without a branch yet", async () => {
  const { host, view } = await mount("composer", collection([repo("api", { branch: null })]));
  assert.ok(texts(host).includes("at main"));
  view.dispose();
});

test("panel: play, stop and start all run wtc svc", async () => {
  const { host, view } = await mount("panel", collection([repo("api")], [
    { group: "api/web", path: "api/web/main", repo: "api", kind: "service", label: "preview", state: "absent" },
    { group: "api/docs", path: "api/docs/main", repo: "api", kind: "service", label: "docs", state: "ready", url: "http://127.0.0.1:42202" },
  ]));
  calls.length = 0;
  titled(host, "preview: absent. Start it").onclick({ preventDefault() {}, stopPropagation() {} });
  await settle();
  titled(host, "docs is running. Stop it").onclick({ preventDefault() {}, stopPropagation() {} });
  await settle();
  titled(host, "Start all services").onclick({ preventDefault() {}, stopPropagation() {} });
  await settle();
  assert.deepEqual(calls.filter((c) => c.actionId === "svc").map((c) => c.payload),
    [{ op: "up", group: "api/web" }, { op: "down", group: "api/docs" }, { op: "up" }]);
  view.dispose();
});

test("panel: a new tunnel, and one whose client is missing", async () => {
  const { host, view } = await mount("panel", collection([repo("api")], [
    { group: "api/web", path: "api/web/main", repo: "api", kind: "service", label: "preview", state: "ready", url: "http://127.0.0.1:42201" },
  ]));
  const pick = [...host.all()].find((n) => n.className === "wtc-tunnel-menu");
  // The gateway's portal first; without tunnel clients, the others are
  // installed first, and Tailscale is not offered on a personal tailnet.
  assert.deepEqual(pick.children.map((o) => o.value), ["gateway", "cloudflare", "ngrok", "devtunnel"]);
  assert.equal(pick.children[0].ariaLabel, "Gateway · OpenClaw portal");
  assert.equal(pick.children[1].ariaLabel, "Cloudflare · public link · install first");
  // Only reachable on the gateway: the port, not a link.
  assert.ok(texts(host).includes(":42201"));
  calls.length = 0;
  svcResult = { ok: true, result: { missing: "cloudflared" } };
  await pick.children.find(o => o.value === "cloudflare").onclick({ preventDefault() {}, stopPropagation() {} });
  svcResult = { ok: true, result: { message: "done" } };
  assert.deepEqual(calls[0].payload, { op: "tunnel", group: "api/web", provider: "cloudflare", public: true, add: true });
  const asked = calls.find((c) => c.method === "sessions.send");
  assert.match(asked.message, /cloudflare tunnel to api\/web\. cloudflared is not on the gateway's PATH/);
  view.dispose();
});

test("composer: start and stop all only with more than one service", async () => {
  const one = await mount("composer", collection([repo("api")], [
    { group: "api/web", path: "api/web/main", repo: "api", kind: "service", label: "preview", state: "absent" },
  ]));
  assert.ok(titled(one.host, "preview: absent. Start it"));
  assert.equal(titled(one.host, "Start all services"), undefined);
  one.view.dispose();
});

test("panel: the ways to a service", async () => {
  const web = { group: "api/web", path: "api/web/main", repo: "api", kind: "service", label: "preview", state: "ready", url: "http://127.0.0.1:42201" };
  const team = await mount("panel", { ...collection([repo("api")], [web]), tunnelProviders: ["tailscale", "cloudflare"], teamTailnet: true });
  const pick = [...team.host.all()].find((n) => n.className === "wtc-tunnel-menu");
  assert.deepEqual(pick.children.map((o) => o.ariaLabel),
    ["Gateway · OpenClaw portal", "Cloudflare · public link", "ngrok · public link · install first", "Dev Tunnel · your account · install first", "Tailscale · team tailnet"]);
  team.view.dispose();
  // Without portal ingress, the gateway's portal comes last, marked.
  const bare = await mount("panel", { ...collection([repo("api")], [web]), tunnelProviders: ["cloudflare"], portalIngress: false });
  const choice = [...bare.host.all()].find((n) => n.className === "wtc-tunnel-menu");
  assert.deepEqual(choice.children.map((o) => o.ariaLabel),
    ["Cloudflare · public link", "ngrok · public link · install first", "Dev Tunnel · your account · install first", "Gateway · OpenClaw portal · needs ingress"]);
  bare.view.dispose();
  // With every other way taken, the one left is named.
  const tunnels = ["cloudflare", "ngrok", "devtunnel"].map((p) => ({ group: "api/web", path: `api/web/${p}`, repo: "api", kind: "endpoint", provider: p, state: "idle" }));
  const last = await mount("panel", { ...collection([repo("api")], [web, ...tunnels]), tunnelProviders: [] });
  assert.ok([...last.host.all()].some((n) => n.tagName === "BUTTON" && n.title.startsWith("Open Gateway (OpenClaw portal)")), "one way left: no choice to make");
  last.view.dispose();
});

test("panel: a portal of the gateway beside its service", async () => {
  const web = { group: "api/web", path: "api/web/main", repo: "api", kind: "service", label: "preview", state: "ready", url: "http://127.0.0.1:42201" };
  portals = [{ id: "p42201", title: "preview", port: 42201, listenPort: 41553, url: "http://172.30.0.2:41553/?openclaw_portal=t", publicUrl: "http://172.30.0.2:41553/", createdAtMs: 0 }];
  const open = await mount("panel", collection([repo("api")], [web]));
  const links = [...open.host.all()].filter((n) => n.tagName === "A").map((n) => n.textContent);
  // In the gateway column; a container's address, which a browser elsewhere
  // cannot reach, is marked.
  assert.ok(links.includes("open ⚠"));
  assert.ok(tables(open.host).at(-1)[0].includes("gateway"));
  // Listed without its URL, it opens again on a click.
  portals = [{ ...portals[0], url: undefined, id: "p42202" }];
  const listed = await mount("panel", collection([repo("api")], [web]));
  assert.ok(titled(listed.host, "Open the portal"));
  listed.view.dispose();
  portals = [{ ...portals[0], id: "p42201", url: "http://172.30.0.2:41553/?openclaw_portal=t" }];
  // Open already: not offered again.
  assert.equal([...open.host.all()].find((n) => n.className === "wtc-tunnel-menu").children.some((o) => o.value === "gateway"), false);
  calls.length = 0;
  titled(open.host, "Close the portal").onclick({ preventDefault() {}, stopPropagation() {} });
  await settle();
  assert.deepEqual(calls.find((c) => c.method === "portal.close"), { method: "portal.close", id: "p42201" });
  portals = [];
  open.view.dispose();
});

test("composer: collapsed, the same facts summed", async () => {
  const { host, view } = await mount("composer", busy);
  const card = [...host.all()].find((n) => n.className === "wtc-collection-card");
  const summary = card.children.find((n) => n.tagName === "SUMMARY");
  assert.equal([...summary.all()].find(n => n.className === "wtc-summary-facts").textContent, "±1↑ 2↓ 3PR1 ✗svc1/2");
  // ↑ pushes every repository, ↓ catches every one up.
  assert.ok(titled(summary, "Push api, each to its pull request or a new draft one"));
  assert.ok([...summary.all()].some((n) => n.tagName === "BUTTON" && /^Catch up api \(3\)/.test(n.title)));
  // Open, the tables have them and the summary does not repeat them.
  card.open = true;
  for (const fn of card.listeners.toggle) fn();
  assert.equal([...summary.all()].find(n => n.className === "wtc-summary-facts").textContent, "");
  view.dispose();
});

test("composer: after a merge, ↓ leaves the finished branch even at the tip", async () => {
  const merged = (mergedIn) => ({ number: 2, state: "merged", url: "https://github.com/octo/site/pull/2", checks: "success", mergedIn });
  const { host, view } = await mount("composer", collection([repo("site", { pullRequest: merged(false) }), repo("docs", { pullRequest: merged(true) })]));
  await settle();
  const card = [...host.all()].find((n) => n.className === "wtc-collection-card");
  const downs = [...card.all()].filter((n) => n.tagName === "BUTTON" && n.textContent === "↓");
  assert.ok(downs.some((n) => n.title === "#2 was merged. Return to main's tip, detached; keep it in the pull request list. The next change starts a new branch"));
  // Collapsed, both completed branches still need to be left.
  assert.ok(downs.some((n) => n.title.startsWith("Catch up site (#2 merged)")));
  assert.ok(downs.some((n) => /docs/.test(n.title)));
  calls.length = 0;
  await downs.find((n) => n.title.startsWith("#2 was merged")).onclick();
  await settle();
  assert.ok(calls.some((c) => c.actionId === "catch-up" && c.sessionKey === "agent:main:owner:site"));
  view.dispose();
});

test("completed rounds leave the repository row and remain in the PR list", async () => {
  const old = { number: 2, state: "merged", title: "First round", url: "https://github.com/octo/site/pull/2", checks: "success" };
  const current = repo("site", { branch: undefined, pullRequests: [old] });
  for (const kind of ["composer", "panel"]) {
    const { host, view } = await mount(kind, collection([current]));
    const [repos, prs] = tables(host);
    assert.equal(repos[1][0], "site");
    assert.equal(repos[1][1], "at main");
    assert.equal(repos[1][3], "1 archived PR");
    if (kind === "panel") assert.deepEqual(prs[1].slice(0, 3), ["#2", "site", "merged"]);
    else assert.equal(prs, undefined, "the conversation card keeps history in the sidebar");
    assert.ok(![...host.all()].some(n => n.tagName === "BUTTON" && !n.disabled && /^[↑↓]/.test(n.textContent)), "finished repositories keep disabled bulk slots without an executable push/catch-up");
    view.dispose();
  }
  // A following round keeps the earlier one; the same PR recorded during a
  // retry appears only once.
  const next = { ...old, number: 3, state: "open", title: "Next round" };
  const { host, view } = await mount("panel", collection([repo("site", { pullRequest: next, pullRequests: [old, next] })]));
  assert.deepEqual(tables(host)[1].slice(1).map((r) => r[0]), ["#2", "#3"]);
  view.dispose();
});

test("narrow: repository facts fold together and next steps retain their position", async () => {
  // A ResizeObserver whose width the test sets.
  const observers = [];
  globalThis.ResizeObserver = class { constructor(fn) { this.fn = fn; observers.push(this); } observe() {} disconnect() {} };
  try {
    const { host, view } = await mount("panel", busy);
    for (const o of observers) o.fn([{ contentRect: { width: 360 } }]);
    const [repos, services] = tables(host);
    assert.deepEqual(repos[0], ["Repository", "Next"]);
    assert.deepEqual(repos[1], ["apiopenclaw/test↑ 2↓ 3#7✗✗○", ""]);
    assert.deepEqual(services[1].slice(1), ["previewapi", "ready", ":42201", "calm-river✕", "+"]);
    view.dispose();
  } finally {
    delete globalThis.ResizeObserver;
  }
});

test("composer: without a collection, the card suggests where the latest message asks to work", async () => {
  const { latestUserText, startMessage } = await import("../dist/control-ui/v1/index.js");
  assert.equal(latestUserText([{ role: "user", content: "lets work on the site" }, { role: "assistant", content: "ok" }, { role: "user", content: [{ type: "text", text: "[OpenClaw heartbeat poll]" }] }]), "lets work on the site");
  assert.equal(latestUserText([]), "");
  // What Start sent was acted on: nothing to suggest until the person writes again.
  assert.equal(latestUserText([{ role: "user", content: "the site" }, { role: "user", content: "(wtc) Start this conversation's collection in taba project wtc with wtc-site: …" }]), "");
  assert.equal(latestUserText([{ role: "user", content: "lets work on the site\n\n(wtc) Start a new conversation …" }, { role: "user", content: "and the footer" }]), "and the footer");
  const { host, view } = await mount("composer", collection([]), [{ role: "user", content: [{ type: "text", text: "lets work on the site" }] }]);
  await settle();
  const card = () => [...host.all()].find((n) => n.className === "wtc-collection-card");
  assert.ok(card(), "a suggestion card");
  assert.ok(texts(card()).some((t) => /^Work in taba project /.test(t ?? "")));
  const ask = calls.find((c) => c.actionId === "suggest");
  assert.deepEqual(ask.payload, { text: "lets work on the site" });
  calls.length = 0;
  await [...card().all()].find((n) => n.tagName === "BUTTON" && n.textContent === "Start").onclick({ preventDefault() {}, stopPropagation() {} });
  await settle();
  assert.deepEqual(calls.find((c) => c.method === "sessions.send"), { method: "sessions.send", key: ask.sessionKey, message: `(wtc) ${startMessage({ project: "wtc", repos: ["wtc-site"] })}` });
  assert.match(startMessage({ project: "wtc", repos: ["wtc-site"] }), /wtc add-repo wtc-site --project wtc --worktree/);
  assert.equal(card(), undefined, "gone once started");
  view.dispose();
  // ✕ dismisses it until the card is mounted again; Change… is offered.
  const again = await mount("composer", collection([]), [{ role: "user", content: "the site" }]);
  await settle();
  const card2 = () => [...again.host.all()].find((n) => n.className === "wtc-collection-card");
  assert.ok(card2());
  const change = [...card2().all()].find((n) => n.tagName === "BUTTON" && n.textContent === "Change…");
  assert.ok(change, "Change… is offered");
  await [...card2().all()].find((n) => n.tagName === "BUTTON" && n.title === "Not now").onclick({ preventDefault() {}, stopPropagation() {} });
  assert.equal(card2(), undefined);
  again.view.dispose();
  // A Start that fails puts the card back, saying why.
  failing.add("sessions.send");
  const retry = await mount("composer", collection([]), [{ role: "user", content: "the site" }]);
  await settle();
  const card3 = () => [...retry.host.all()].find((n) => n.className === "wtc-collection-card");
  await [...card3().all()].find((n) => n.tagName === "BUTTON" && n.textContent === "Start").onclick({ preventDefault() {}, stopPropagation() {} });
  await settle();
  failing.clear();
  assert.ok(card3(), "back after a failed Start");
  // A failure reads as one.
  assert.ok(texts(card3()).includes("⚠ sessions.send failed"));
  retry.view.dispose();
  // A failed load is no empty collection: nothing is suggested.
  const broken = await mount("composer", new Error("down"), [{ role: "user", content: "the site" }]);
  await settle();
  assert.equal([...broken.host.all()].find((n) => n.className === "wtc-collection-card"), undefined);
  broken.view.dispose();
  // Nothing to suggest: no card.
  const quiet = await mount("composer", collection([]), [{ role: "user", content: "hello" }]);
  await settle();
  assert.equal([...quiet.host.all()].find((n) => n.className === "wtc-collection-card"), undefined);
  quiet.view.dispose();
});

test("composer: the suggestion is asked again only for a new message, and not more than every ten seconds", async () => {
  const { mock } = await import("node:test");
  mock.timers.enable({ apis: ["Date"], now: 1_000_000 });
  try {
    const history = [{ role: "user", content: "lets work on the site" }];
    const { host, view } = await mount("composer", collection([]), history);
    await settle();
    const asked = () => calls.filter((c) => c.actionId === "suggest").length;
    const before = asked();
    const sessionKey = [...collections.keys()].at(-1);
    const again = async () => {
      view.update({ props: { sessionKey, agentId: "main" }, host: { request: hostRequest, ui: { openPanel() {} } }, signal: new AbortController().signal, presented: true, mountDefault: () => () => {} });
      await settle();
    };
    // Within ten seconds: not asked again, whatever the text.
    mock.timers.tick(5_000);
    history.push({ role: "user", content: "the invoices" });
    await again();
    assert.equal(asked(), before);
    // Later, with the same text as last time asked: not asked again either.
    history.pop();
    mock.timers.tick(11_000);
    await again();
    assert.equal(asked(), before);
    // A new message after ten seconds: asked.
    history.push({ role: "user", content: "and the site footer" });
    mock.timers.tick(11_000);
    await again();
    assert.equal(asked(), before + 1);
    view.dispose();
  } finally {
    mock.timers.reset();
  }
});

test("composer: the draft is suggested about once typing pauses, and Start sends it along", async () => {
  const { isMainSession, startMessage } = await import("../dist/control-ui/v1/index.js");
  assert.equal(isMainSession("agent:main:main"), true);
  assert.equal(isMainSession("agent:main:dashboard:abc"), false);
  assert.match(startMessage({ project: "wtc", repos: ["wtc-site"] }, true), /^Start a new conversation .*sessions_spawn with visible: true.*wtc add-repo wtc-site --project wtc/);
  const { mock } = await import("node:test");
  // settle() waits on setTimeout, which this test fakes.
  const flush = async () => { for (let i = 0; i < 5; i++) await new Promise((r) => setImmediate(r)); };
  const { host, view } = await mount("composer", collection([]));
  await settle();
  mock.timers.enable({ apis: ["setTimeout"] });
  try {
    const sessionKey = [...collections.keys()].at(-1);
    let draft = "";
    const sent = [];
    const props = () => ({ sessionKey, agentId: "main", draft, setDraft: (d) => { draft = d; }, send: async () => { sent.push(draft); return true; } });
    const type = async (text) => {
      draft = text;
      view.update({ props: props(), host: { request: hostRequest, ui: { openPanel() {} } }, signal: new AbortController().signal, presented: true, mountDefault: () => () => {} });
      await flush();
    };
    calls.length = 0;
    await type("lets work on the site");
    assert.equal(calls.filter((c) => c.actionId === "suggest").length, 0, "not while typing");
    mock.timers.tick(800);
    await flush();
    await flush();
    assert.deepEqual(calls.find((c) => c.actionId === "suggest")?.payload, { text: "lets work on the site", draft: true });
    const card = () => [...host.all()].find((n) => n.className === "wtc-collection-card");
    assert.ok(card(), "suggested from the draft");
    await [...card().all()].find((n) => n.tagName === "BUTTON" && n.textContent === "Start").onclick({ preventDefault() {}, stopPropagation() {} });
    await flush();
    assert.equal(sent.length, 1);
    assert.ok(sent[0].startsWith("lets work on the site\n\n(wtc) Start this conversation's collection in taba project wtc"));
    assert.equal(calls.some((c) => c.method === "sessions.send"), false, "through the composer's own send");
    view.dispose();
  } finally {
    mock.timers.reset();
  }
});

test("composer: in Home, Start asks for a conversation of its own; a refused send gives the draft back", async () => {
  const { startMessage } = await import("../dist/control-ui/v1/index.js");
  const { mock } = await import("node:test");
  const flush = async () => { for (let i = 0; i < 5; i++) await new Promise((r) => setImmediate(r)); };
  const { host, view } = await mount("composer", collection([]), [], "agent:main:main");
  await settle();
  mock.timers.enable({ apis: ["setTimeout"] });
  try {
    let draft = "";
    let accept = false;
    const sent = [];
    const props = () => ({ sessionKey: "agent:main:main", agentId: "main", draft, setDraft: (d) => { draft = d; }, send: async () => { sent.push(draft); return accept; } });
    const type = async (text) => { draft = text; view.update({ props: props(), host: { request: hostRequest, ui: { openPanel() {} } }, signal: new AbortController().signal, presented: true, mountDefault: () => () => {} }); await flush(); };
    await type("lets work on the site");
    mock.timers.tick(800);
    await flush();
    const card = () => [...host.all()].find((n) => n.className === "wtc-collection-card");
    assert.ok(texts(card()).some((t) => /^Start a conversation in taba project /.test(t ?? "")));
    // Refused: the draft as written comes back, and the card with it.
    await [...card().all()].find((n) => n.tagName === "BUTTON" && n.textContent === "Start").onclick({ preventDefault() {}, stopPropagation() {} });
    await flush();
    assert.equal(sent[0], `lets work on the site\n\n(wtc) ${startMessage({ project: "wtc", repos: ["wtc-site"] }, true)}`);
    assert.equal(draft, "lets work on the site");
    assert.ok(card(), "the card is back after a refused send");
    // Sent: Home stays open to further suggestions.
    accept = true;
    await type("lets work on the site footer");
    mock.timers.tick(800);
    await flush();
    await [...card().all()].find((n) => n.tagName === "BUTTON" && n.textContent === "Start").onclick({ preventDefault() {}, stopPropagation() {} });
    await flush();
    await type("and the site header too");
    mock.timers.tick(800);
    await flush();
    assert.ok(card(), "Home is suggested to again");
    view.dispose();
  } finally {
    mock.timers.reset();
  }
});

test("the plugin registers the panel, the composer and a session-menu action, and no header bar", async () => {
  const { default: ui } = await import("../dist/control-ui/v1/index.js");
  const actions = [];
  const noop = () => () => {};
  ui.activate({ ui: { registerPanel: noop, registerReplacement: noop, registerAction: (a) => { actions.push(a); return () => {}; }, selectReplacement() {} } });
  assert.deepEqual(actions.map((a) => [a.id, a.placement]), [["open-collection-session", "session"]]);
});

test("panel: a ready service that doesn't answer, and a tunnel whose provider needs a login", async () => {
  const web = { group: "api/web", path: "api/web/main", repo: "api", kind: "service", label: "preview", state: "ready", url: "http://127.0.0.1:42201", answering: "no", answer_detail: "HTTP 502" };
  const tunnel = { group: "api/web", path: "api/web/tailscale", repo: "api", kind: "endpoint", provider: "tailscale", state: "ready", url: "https://host.example.ts.net:42201", login: "Tailscale needs a login: tailscale up" };
  const { host, view } = await mount("panel", { ...collection([repo("api")], [web, tunnel]), tunnelProviders: ["tailscale"] });
  const all = [...host.all()];
  assert.ok(all.some((n) => n.textContent !== "⚠" && /^ready, not answering now \(HTTP 502\)/.test(n.title ?? n.textContent ?? "")), "the service says it isn't answering");
  assert.ok(all.some((n) => n.textContent === "⚠" && /Tailscale needs a login: tailscale up/.test(n.title)), "the tunnel says it needs a login");
  view.dispose();
  // A closed tunnel's provider login leaves a healthy service healthy; the
  // button that opens it again says what to do first.
  const fine = { ...web, answering: "yes", answer_detail: undefined };
  const closed = { ...tunnel, state: "idle" };
  const quiet = await mount("panel", { ...collection([repo("api")], [fine, closed]), tunnelProviders: ["tailscale"] });
  const nodes = [...quiet.host.all()];
  assert.ok(!nodes.some((n) => /not answering|⚠/.test(n.textContent ?? "") || /^ready: /.test(n.title ?? "")), "nothing marked as failing");
  assert.ok(nodes.some((n) => n.tagName === "BUTTON" && /Open the tailscale tunnel again .*First: Tailscale needs a login: tailscale up/.test(n.title)));
  quiet.view.dispose();
});

test("composer: a repository with nothing on its branch shows its base", async () => {
  const { host, view } = await mount("composer", collection([
    repo("wtc-site"),
    repo("wtc-cli", { ahead: 1, unpublished: 1 }),
  ]));
  await settle();
  const card = [...host.all()].find((n) => n.className === "wtc-collection-card");
  card.open = true;
  for (const fn of card.listeners.toggle) fn();
  const codes = [...card.all()].filter((n) => n.tagName === "CODE");
  const site = codes.find((n) => n.textContent === "at main");
  assert.ok(site, "the untouched one shows its base");
  assert.match(site.title, /Checked out on branch openclaw\/test/);
  assert.ok(codes.some((n) => n.textContent === "openclaw/test"), "the one with a commit shows its branch");
  view.dispose();
});

test("composer: a sub-session has its own rows, merges into the task's checkout, and is discarded after asking again", async () => {
  const data = {
    ...collection([repo("site")]),
    subSessions: [{ name: "docs", collection: "test--docs", repos: [
      repo("site", { sessionKey: "agent:main:owner:site-docs", branch: "openclaw/test--docs", dirty: 13, toMerge: 2, taskSessionKey: "agent:main:owner:site" }),
    ] }],
  };
  const { host, view } = await mount("composer", data);
  const card = [...host.all()].find((n) => n.className === "wtc-collection-card");
  const [repos] = tables(card);
  // site once for the task; the worker's site is a sub-row under it, with the
  // same columns against the task: ± its files, ↑ into the task, no PR.
  assert.deepEqual(repos.slice(1), [
    ["site", "at main", "", ""],
    ["↳docs✕", "openclaw/test--docs", "± 13↑ 2", ""],
  ]);
  const named = (re) => [...card.all()].find((n) => n.tagName === "BUTTON" && re.test(n.title));

  calls.length = 0;
  await named(/^2 commits on openclaw\/test--docs not in the collection's site/).onclick();
  assert.deepEqual(calls.map((c) => [c.actionId, c.sessionKey, c.payload?.from]), [["catch-up", "agent:main:owner:site", "openclaw/test--docs"]]);

  // ✕ asks again, naming what would go; the second click deletes the sessions.
  calls.length = 0;
  named(/^Discard sub-session docs/).onclick({ preventDefault() {}, stopPropagation() {} });
  await settle();
  assert.equal(calls.length, 0);
  const again = named(/^Click again to discard sub-session docs\. It has 13 uncommitted files and 2 commits not merged\.$/);
  assert.ok(again);
  await again.onclick();
  assert.deepEqual(calls.map((c) => [c.method, c.key]), [["sessions.delete", "agent:main:owner:site-docs"]]);
  view.dispose();
});

test("composer: a sub-session's merge conflict goes to the conversation; an unknown merge state is said before discarding", async () => {
  const data = {
    ...collection([repo("site")]),
    subSessions: [{ name: "docs", collection: "test--docs", repos: [
      repo("site", { sessionKey: "agent:main:owner:site-docs", branch: "openclaw/test--docs", toMerge: 1, taskSessionKey: "agent:main:owner:site" }),
      repo("harness", { sessionKey: "agent:main:owner:harness-docs", branch: "openclaw/test--docs", toMerge: null, taskSessionKey: null }),
    ] }],
  };
  const { host, view } = await mount("composer", data);
  const card = [...host.all()].find((n) => n.className === "wtc-collection-card");
  const named = (re) => [...card.all()].find((n) => n.tagName === "BUTTON" && re.test(n.title));
  const stop = { preventDefault() {}, stopPropagation() {} };

  catchUpResult = { ok: true, result: { code: 1, json: null, message: "wtc: merging openclaw/test--docs into openclaw/test conflicts in: index.md. The merge was aborted and the checkout is as before. Merge by hand with `git merge openclaw/test--docs`, resolve, commit — do not rebase\n" } };
  calls.length = 0;
  await named(/^1 commit on openclaw\/test--docs not in the collection's site/).onclick();
  assert.deepEqual(calls.map((c) => c.actionId ?? c.method), ["catch-up", "sessions.send"]);
  assert.equal(calls[1].message, "/skill wtc-catch-up site: merging sub-session docs's openclaw/test--docs (wtc catch-up --from openclaw/test--docs) conflicts in index.md.");
  catchUpResult = { ok: true, result: { code: 0, json: { summary: "merged" } } };

  // The task has no checkout of the harness: nothing says what is merged.
  assert.ok(named(/^Discard sub-session docs, in all its 2 repositories: .*Whether its commits in harness are merged is not known\.$/));
  named(/^Discard sub-session docs/).onclick(stop);
  await settle();
  calls.length = 0;
  preserved = { "agent:main:owner:harness-docs": { id: "w9", branch: "openclaw/test--docs", path: "/worktrees/w9", reason: "busy" } };
  await named(/^Click again to discard sub-session docs/).onclick();
  preserved = {};
  assert.deepEqual(calls.map((c) => c.key), ["agent:main:owner:site-docs", "agent:main:owner:harness-docs"]);
  await settle();
  assert.match(readable(card), /Sub-session docs discarded; OpenClaw kept the worktree of harness \(busy, at \/worktrees\/w9\)\./);
  view.dispose();
});

test("composer: a catch-up that fails reads as a failure, without wtc's prefix", async () => {
  const { host, view } = await mount("composer", busy);
  const card = [...host.all()].find((n) => n.className === "wtc-collection-card");
  catchUpResult = { ok: true, result: { code: 1, json: null, message: "wtc: cannot fetch main from origin: exit status 128\n" } };
  await [...card.all()].find((n) => n.tagName === "BUTTON" && /^3 new on main\. Catch up/.test(n.title)).onclick();
  catchUpResult = { ok: true, result: { code: 0, json: { summary: "merged" } } };
  const note = [...card.all()].find(n => n.className?.includes("wtc-header-feedback"));
  assert.equal(note.role, "alert");
  assert.match(note.ariaLabel, /api: cannot fetch main from origin: exit status 128/);
  assert.match(note.style.cssText, /color:var\(--danger/);
  assert.match([...host.all()].find(n => n.className?.startsWith("wtc-toolbar-primary")).textContent, /Failed/, "failure remains visible in the fixed primary slot at narrow widths");
  view.dispose();
});

test("panel: a failure stays one across a refresh; a sub-session merge that fails reads as one too", async () => {
  const data = {
    ...busy,
    subSessions: [{ name: "docs", collection: "test--docs", repos: [
      repo("web", { sessionKey: "agent:main:owner:web-docs", branch: "openclaw/test--docs", toMerge: 1, taskSessionKey: "agent:main:owner:web" }),
    ] }],
  };
  const { host, view } = await mount("panel", data);
  const button = (re) => [...host.all()].find((n) => n.tagName === "BUTTON" && re.test(n.title));
  const log = () => [...host.all()].find(n => n.className === "wtc-activity-item" && n.role === "alert");
  catchUpResult = { ok: true, result: { code: 1, json: null, message: "wtc: cannot fetch main from origin\n" } };
  await button(/^3 new on main\. Catch up/).onclick();
  assert.match(readable(host), /api: cannot fetch main from origin/);
  assert.equal(log().role, "alert");
  // The refresh after it renders the panel again; the failure stays.
  await settle();
  assert.match(readable(host), /api: cannot fetch main from origin/);

  catchUpResult = { ok: true, result: { code: 1, json: null, message: "wtc: web has no branch openclaw/test--docs\n" } };
  await button(/^1 commit on openclaw\/test--docs not in the collection's web/).onclick();
  assert.match(readable(host), /web: web has no branch openclaw\/test--docs/);
  catchUpResult = { ok: true, result: { code: 0, json: { summary: "merged" } } };
  view.dispose();
});

test("composer: a sub-session with uncommitted files is reviewed by the conversation; a clean one's ↓ merges the collection's branch", async () => {
  const sub = (fields) => ({
    ...collection([repo("site", { branch: "openclaw/test", ahead: 1 })]),
    subSessions: [{ name: "nav", collection: "13-docs-nav", repos: [
      repo("site", { sessionKey: "agent:main:owner:site-nav", branch: "openclaw/13-docs-nav", path: "/worktrees/w5", toMerge: 0, behind: 2,
        taskSessionKey: "agent:main:owner:site", taskBranch: "openclaw/test", ...fields }),
    ] }],
  });
  const dirty = await mount("composer", sub({ dirty: 3,
    uncommitted: [{ path: "a.md", state: "same" }, { path: "b.md", state: "differs" }, { path: "c.mjs", state: "only" }] }));
  let card = [...dirty.host.all()].find((n) => n.className === "wtc-collection-card");
  assert.deepEqual(tables(card)[0][2], ["↳nav✕", "openclaw/13-docs-nav", "± 3↓ 2", ""]);
  const buttons = () => [...card.all()].filter((n) => n.tagName === "BUTTON");
  // With uncommitted files there is no catch-up to click: it would refuse.
  assert.equal(buttons().find((n) => /^2 commits of the collection's site/.test(n.title)), undefined);
  const review = buttons().find((n) => /^3 uncommitted files, compared with the collection's site:/.test(n.title));
  assert.match(review.title, /^3 uncommitted files, compared with the collection's site:\ndiffers: b.md\nonly here: c.mjs\nthe same in the collection: a.md\n\nAsk the conversation/);
  calls.length = 0;
  await review.onclick();
  assert.equal(calls.at(-1).method, "sessions.send");
  assert.match(calls.at(-1).message, /^Sub-session nav left 3 uncommitted files in its checkout of site \(\/worktrees\/w5\)\. These differ from yours or only it has them: b\.md, c\.mjs\..*Don't commit in its checkout/);
  dirty.view.dispose();

  // Clean, ↓ merges the collection's branch by its name, in the sub-session's checkout.
  const clean = await mount("composer", sub({}));
  card = [...clean.host.all()].find((n) => n.className === "wtc-collection-card");
  calls.length = 0;
  await buttons().find((n) => /^2 commits of the collection's site not on openclaw\/13-docs-nav/.test(n.title)).onclick();
  assert.deepEqual(calls.map((c) => [c.actionId, c.sessionKey, c.payload]), [["catch-up", "agent:main:owner:site-nav", { from: "openclaw/test" }]]);
  clean.view.dispose();
});


const withChoices = (name, kind = "fix-checks") => {
  const step = { repo: name, step: kind, by: "agent", skill: "wtc-follow", key: `${name}:${kind}:head1`, reason: `${name} needs ${kind}` };
  const alternative = { ...step, step: "follow-pr", key: `${name}:follow-pr:head1`, reason: `Check ${name}'s current PR` };
  return repo(name, { next: step, choices: { recommended: step.key, actions: [{ ...step, available: true }, { ...alternative, available: true }, { ...step, step: "review", key: `${name}:review`, available: false, blocked: "No review adapter" }] } });
};
const clickText = async (host, text) => {
  const b = [...host.all()].find((n) => n.tagName === "BUTTON" && (n.textContent === text || text === "Catch up 2 repos →" && n.textContent === "Catch up 2" || text === "Choose steps…" && n.ariaLabel === "Choose steps…" || text === "Cancel queued" && n.title === "Stop queued dispatches; active work continues" || text === "Allow retry" && n.title === "Check the conversation before retrying unchanged work"));
  assert.ok(b, text); b.onclick({ preventDefault() {}, stopPropagation() {} });
  for (let i = 0; i < 6; i++) await settle();
};

const chooseSteps = async (host) => {
  if (!buttons(host).includes("Exit selection")) await clickText(host, "Choose steps…");
};

test("selected mixed actions retain alternatives and combine author handoffs", async () => {
  for (const kind of ["composer", "panel"]) {
    const api = withChoices("api"), web = withChoices("web", "address-review");
    const { host, view } = await mount(kind, collection([api, web]));
    try {
      await chooseSteps(host);
      const select = [...host.all()].find((n) => n.tagName === "SELECT" && n.ariaLabel === "Action for web");
      select.value = web.choices.actions[1].key; select.onchange();
      await chooseSteps(host); await clickText(host, "Select suggested");
      calls.length = 0;
      assert.equal(calls.length, 0, "selection must not dispatch");
      nextResult = ({ payload }) => {
        const r = payload.ownerSessionKey === api.sessionKey ? api : web;
        return { ok: true, result: { performed: false, repo: { ...r, next: r.choices.actions.find((a) => a.key === payload.key) } } };
      };
      await clickText(host, "Run selected (2)");
      const sends = calls.filter((c) => c.method === "sessions.send");
      assert.equal(sends.length, 1, "one author conversation gets one prompt");
      assert.match(sends[0].message, /api needs fix-checks/);
      assert.match(sends[0].message, /Check web's current PR/);
      assert.doesNotMatch(sends[0].message, /web needs address-review/);
      assert.match(readable(host), /Sent to agent|completion pending/);
      await chooseSteps(host); await clickText(host, "Select suggested");
      assert.ok(buttons(host).includes("Run selected"), "unchanged handoffs cannot be immediately resent");
      await clickText(host, "Allow retry");
      const rowActions = [...host.all()].filter(n => n.className === "wtc-next-step");
      assert.ok(rowActions.some(n => [...n.all()].some(b => b.tagName === "SELECT" && !b.disabled)), "retry restores selectable row alternatives");
      await chooseSteps(host); await clickText(host, "Select suggested");
      assert.ok(buttons(host).includes("Run selected (2)"), "explicit retry recovers an unchanged failed handoff");
    } finally { view.dispose(); nextResult = undefined; }
  }
});

test("batch stale targets fail independently and an unselected new repository stays excluded", async () => {
  for (const kind of ["composer", "panel"]) {
    const api = withChoices("api"), web = withChoices("web");
    const sessionKey = `agent:main:batch:${++sessions}`;
    const { host, view } = await mount(kind, collection([api, web]), undefined, sessionKey);
    try {
      await chooseSteps(host); await clickText(host, "Select suggested");
      const tools = withChoices("tools");
      collections.set(sessionKey, collection([api, web, tools]));
      await [...host.all()].find(n => n.tagName === "BUTTON" && (n.title === "Refresh" || /\. Refresh$/.test(n.title))).onclick({ preventDefault() {}, stopPropagation() {} });
      assert.match(readable(host), /tools/);
      calls.length = 0;
      nextResult = ({ payload }) => payload.ownerSessionKey === api.sessionKey ? { ok: false, error: "Head changed" } : { ok: true, result: { performed: false, repo: web } };
      await clickText(host, "Run selected (2)");
      assert.ok([...host.all()].some(n => n.title === "Failed or stale: Head changed"));
      const sends = calls.filter((c) => c.method === "sessions.send");
      assert.equal(sends.length, 1);
      assert.match(sends[0].message, /web needs/); assert.doesNotMatch(sends[0].message, /api needs|tools/);
      assert.equal(calls.filter((c) => c.actionId === "next" && c.payload.ownerSessionKey === tools.sessionKey).length, 0);
    } finally { view.dispose(); nextResult = undefined; }
  }
});

test("cancelling a batch or switching conversations prevents queued handoffs", async () => {
  for (const kind of ["composer", "panel"]) for (const switchView of [false, true]) {
    const r = withChoices("api"); const { host, view } = await mount(kind, collection([r]));
    let release;
    try {
      await chooseSteps(host); await clickText(host, "Select suggested");
      nextResult = () => new Promise((resolve) => { release = resolve; }); calls.length = 0;
      await clickText(host, "Run selected (1)");
      assert.ok(release);
      if (switchView) {
        const key = `agent:main:other:${++sessions}`; collections.set(key, collection([repo("other")]));
        view.update({ props: { sessionKey: key, agentId: "main" }, host: { request: hostRequest, ui: { openPanel() {} } }, signal: new AbortController().signal, presented: true, mountDefault: () => () => {} }); await settle();
      } else await clickText(host, "Cancel queued");
      release({ ok: true, result: { performed: false, repo: r } });
      for (let i=0;i<6;i++) await settle();
      assert.equal(calls.filter((c) => c.method === "sessions.send").length, 0);
      if (switchView) assert.doesNotMatch(readable(host), /api needs|completion pending/);
      else assert.match(readable(host), /Cancelled|Stopped/);
    } finally { view.dispose(); nextResult = undefined; }
  }
});


test("closing or aborting a view cancels selected work before handoff", async () => {
 for(const kind of ["composer","panel"])for(const lifecycle of ["dispose","abort"]){
  const r=withChoices("api");const {host,view,abort}=await mount(kind,collection([r]));let release;
  try{
   await chooseSteps(host); await clickText(host,"Select suggested");
   nextResult=()=>new Promise(resolve=>{release=resolve});calls.length=0;
   await clickText(host,"Run selected (1)");assert.ok(release);
   if(lifecycle==="dispose")view.dispose();else abort();
   release({ok:true,result:{performed:false,repo:r}});
   for(let i=0;i<6;i++)await settle();
   assert.equal(calls.filter(c=>c.method==="sessions.send").length,0,`${kind}/${lifecycle}`);
   assert.equal(calls.filter(c=>c.actionId==="next").length,1,"no queued recheck after exit");
  }finally{view.dispose();nextResult=undefined;}
 }
});

test("a captured batch cannot duplicate an in-flight individual author handoff", async () => {
 for(const kind of ["composer","panel"]){
  const r=withChoices("api");const {host,view}=await mount(kind,collection([r]));let finish;
  try{
   nextResult={ok:true,result:{performed:false,repo:r}};
   await chooseSteps(host); await clickText(host,"Select suggested");
   controlledSend=new Promise(resolve=>{finish=resolve});calls.length=0;
   await clickText(host,"Exit selection"); await clickText(host,"Fix checks");
   assert.equal(calls.filter(c=>c.method==="sessions.send").length,1);
   await clickText(host,"Run chosen (1) →");
   assert.equal(calls.filter(c=>c.method==="sessions.send").length,1,"batch reused an in-flight reservation");
   finish({});for(let i=0;i<6;i++)await settle();
  }finally{finish?.({});view.dispose();nextResult=undefined;controlledSend=undefined;}
 }
});


test("publication preparation checks cancellation after revalidation and account lookup", async () => {
 for(const kind of ["composer","panel"])for(const boundary of ["revalidation","options"]){
  const step={step:"push",by:"wtc",approve:"person",gateway:"github_publish",key:"push:h1",reason:"Push exact commits"};
  const r=repo("api",{ahead:1,unpublished:1,next:step,choices:{recommended:step.key,actions:[{...step,available:true}]}});
  personal={state:"connected",account:{login:"demo"},generation:"g1"};
  const {host,view}=await mount(kind,collection([r]));let release;
  try{
   await chooseSteps(host); await clickText(host,"Select suggested");await clickText(host,"Run selected (1)");
   nextResult=boundary==="revalidation"?()=>new Promise(resolve=>{release=resolve}):{ok:true,result:{performed:false,repo:r}};
   if(boundary==="options")controlledOptions=new Promise(resolve=>{release=resolve});
   calls.length=0;await clickText(host,"Confirm 1 actions");assert.ok(release);
   await clickText(host,"Cancel queued");
   release(boundary==="revalidation"?{ok:true,result:{performed:false,repo:r}}:{personal});
   for(let i=0;i<6;i++)await settle();
   assert.equal(calls.filter(c=>c.method==="sessions.github.publish"||c.method==="sessions.github.confirm").length,0,`${kind}/${boundary}`);
   assert.match(readable(host),/Cancelled/);
  }finally{view.dispose();nextResult=undefined;controlledOptions=undefined;personal=null;}
 }
});

test("publication preparation stops after abort or disposal during account lookup", async () => {
 for(const kind of ["composer","panel"])for(const lifecycle of ["abort","dispose"]){
  const step={step:"push",by:"wtc",approve:"person",gateway:"github_publish",key:"push:h1",reason:"Push exact commits"};
  const r=repo("api",{ahead:1,unpublished:1,next:step,choices:{recommended:step.key,actions:[{...step,available:true}]}});
  personal={state:"connected",account:{login:"demo"},generation:"g1"};
  const {host,view,abort}=await mount(kind,collection([r]));let release;
  try{
   await chooseSteps(host); await clickText(host,"Select suggested");await clickText(host,"Run selected (1)");
   nextResult={ok:true,result:{performed:false,repo:r}};controlledOptions=new Promise(resolve=>{release=resolve});calls.length=0;
   await clickText(host,"Confirm 1 actions");assert.ok(release);
   if(lifecycle==="abort")abort();else view.dispose();
   release({personal});for(let i=0;i<6;i++)await settle();
   assert.equal(calls.filter(c=>c.method==="sessions.github.publish"||c.method==="sessions.github.confirm").length,0,`${kind}/${lifecycle}`);
  }finally{view.dispose();nextResult=undefined;controlledOptions=undefined;personal=null;}
 }
});

test("catch-up selections dispatch on one click without redundant single-choice menus", async () => {
 for(const kind of ["composer","panel"]){
  const rows=["api","docs"].map((name,i)=>{const next={step:"catch-up",by:"wtc",...(i===0?{approve:"agent"}:{}),command:["wtc","catch-up"],key:`${name}:catchup`,reason:`${i+1} new on main`};return repo(name,{next,choices:{recommended:next.key,actions:[{...next,available:true}]}})});
  const {host,view}=await mount(kind,collection(rows));
  try{
   assert.equal([...host.all()].filter(n=>n.tagName==="SELECT").length,0);
   nextResult=({payload})=>({ok:true,result:{performed:true,repo:rows.find(r=>r.sessionKey===payload.ownerSessionKey),code:0}});
   await chooseSteps(host); await clickText(host,"Select suggested");calls.length=0;await clickText(host,"Run selected (2)");
   const nextCalls=calls.filter(c=>c.actionId==="next");assert.equal(nextCalls.length,2);assert.equal(nextCalls[0].payload.approveCatchUp,true);
   assert.equal(buttons(host).some(b=>/^Confirm /.test(b)),false);
   assert.match(readable(host),/✓ Caught up/);
   assert.equal(calls.filter(c=>c.method==="sessions.send").length,0,"a person's catch-up click can run the mechanical action directly");
  }finally{view.dispose();nextResult=undefined;}
 }
});


test("manual catch-up with alternatives promises Run rather than an agent handoff", async () => {
 for(const kind of ["composer","panel"]){
  const step={step:"catch-up",by:"wtc",approve:"agent",command:["wtc","catch-up"],key:"catchup:h1",reason:"Merge the current base cleanly"};
  const r=repo("api",{next:step,choices:{recommended:step.key,actions:[{...step,available:true},{step:"follow-pr",by:"agent",skill:"wtc-follow",key:"follow:h1",available:true}]}});
  const {host,view}=await mount(kind,collection([r]));
  try{
   await chooseSteps(host); await clickText(host, "Select suggested");
   assert.ok(buttons(host).includes("Run selected (1)"));assert.equal(buttons(host).includes("Ask agent"),false);
   nextResult={ok:true,result:{performed:true,repo:r,code:0}};calls.length=0;await clickText(host,"Run selected (1)");
   assert.equal(calls.find(c=>c.actionId==="next").payload.approveCatchUp,true);
   assert.equal(calls.some(c=>c.method==="sessions.send"),false);
  }finally{view.dispose();nextResult=undefined;}
 }
});

test("compact recommendations run directly and keep selection controls on demand", async () => {
 for (const kind of ["composer", "panel"]) {
  const rows = ["api", "docs"].map(name => {
   const next = { step: "catch-up", by: "wtc", approve: "agent", command: ["wtc", "catch-up"], key: `${name}:catchup:h1`, reason: "Behind main; merges cleanly" };
   return repo(name, { behind: 3, next, choices: { recommended: next.key, actions: [{ ...next, available: true }] } });
  });
  const { host, view } = await mount(kind, collection(rows));
  let release;
  try {
   assert.equal([...host.all()].some(n => n.type === "checkbox" || n.tagName === "SELECT"), false);
   assert.equal(buttons(host).includes("Select suggested"), false);
   assert.equal(buttons(host).includes("Run selected"), false);
   assert.equal([...host.all()].filter(n => n.className === "wtc-next-step").length, 2);
   nextResult = () => new Promise(resolve => { release = resolve; }); calls.length = 0;
   await clickText(host, "Catch up 2 repos →");
   assert.match(readable(host), /Catching up…/); assert.match(readable(host), /Queued/);
   assert.equal(calls.filter(c => c.actionId === "next").length, 1);
   await clickText(host, "Cancel queued");
   release({ ok: true, result: { performed: true, repo: rows[0], code: 0 } });
   for (let i = 0; i < 6; i++) await settle();
   assert.equal(calls.filter(c => c.actionId === "next").length, 1);
   assert.equal(buttons(host).some(b => /^Confirm/.test(b)), false);
   assert.match(readable(host), /Cancelled|Stopped/);
  } finally { view.dispose(); nextResult = undefined; }
 }
});

test("compact mixed recommendations preserve publication preview and exact captured targets", async () => {
 for (const kind of ["composer", "panel"]) {
  const api = withChoices("api");
  const step = { step: "push", by: "wtc", approve: "person", gateway: "github_publish", key: "docs:push:h1", reason: "Push exact commits" };
  const docs = repo("docs", { next: step, choices: { recommended: step.key, actions: [{ ...step, available: true }] } });
  const key = `agent:main:compact:${++sessions}`;
  const { host, view } = await mount(kind, collection([api, docs]), undefined, key);
  try {
   calls.length = 0;
   await clickText(host, "Do next (2) →");
   assert.ok(buttons(host).includes("Confirm 2 actions"));
   assert.equal(calls.some(c => c.actionId === "next" || c.method === "sessions.send" || c.method === "sessions.github.publish"), false);
   collections.set(key, collection([api, docs, withChoices("extra")]));
   await [...host.all()].find(n => n.tagName === "BUTTON" && (n.title === "Refresh" || /\. Refresh$/.test(n.title))).onclick({ preventDefault() {}, stopPropagation() {} });
   assert.ok(buttons(host).includes("Confirm 2 actions"));
   await clickText(host, "Back");
   await clickText(host, "Exit selection");
   assert.ok(buttons(host).includes("Run chosen (2) →"), "closing the chooser retains the captured selection and excludes new repositories");
  } finally { view.dispose(); }
 }
});


test("repository folding has its own breakpoint on both surfaces", async () => {
 const observers = [];
 globalThis.ResizeObserver = class { constructor(fn) { this.fn = fn; observers.push(this); } observe() {} disconnect() {} };
 try {
  for (const kind of ["composer", "panel"]) {
   const start = observers.length;
   const { host, view } = await mount(kind, busy);
   try {
    for (const width of [560, 660, 360]) {
     for (const o of observers.slice(start)) o.fn([{ contentRect: { width } }]);
     const rows = tables(host), repos = rows[0], services = rows[1];
     assert.deepEqual(repos[0], width < 480 ? ["Repository", "Next"] : ["Repository", "Branch", "Changes", "Pull request", "Next"]);
     assert.equal(services[0].includes("Repo"), width >= 520, `${kind}/${width}: service layout follows its own breakpoint`);
    }
   } finally { view.dispose(); }
  }
 } finally { delete globalThis.ResizeObserver; }
});

test("running compact batch controls show disabled state while cancellation stays available", async () => {
 for (const kind of ["composer", "panel"]) {
  const api = withChoices("api");
  const local = ["docs", "tools"].map(name => {
   const next = { step: "catch-up", by: "wtc", command: ["wtc", "catch-up"], key: `${name}:catchup:h1`, reason: "Behind main" };
   return repo(name, { next, choices: { recommended: next.key, actions: [{ ...next, available: true }] } });
  });
  const { host, view } = await mount(kind, collection([api, ...local]));
  let release;
  try {
   nextResult = { ok: true, result: { performed: false, repo: api } };
   await clickText(host, "Fix checks");
   await chooseSteps(host); await clickText(host, "Select suggested"); await clickText(host, "Exit selection");
   nextResult = () => new Promise(resolve => { release = resolve; });
   await clickText(host, "Run chosen (2) →");
   const button = text => [...host.all()].find(n => n.tagName === "BUTTON" && (n.textContent === text || text === "Allow retry" && n.title === "Check the conversation before retrying unchanged work" || text === "Cancel queued" && n.title === "Stop queued dispatches; active work continues"));
   assert.equal([...host.all()].find(n => n.tagName === "BUTTON" && /0\/2$/.test(n.textContent)).disabled, true);
   assert.equal(button("Allow retry"), undefined, "retry is hidden while a batch is running");
   assert.equal(button("Cancel queued").disabled, false);
   await clickText(host, "Cancel queued"); release({ ok: true, result: { performed: true, repo: local[0], code: 0 } });
   for (let i = 0; i < 6; i++) await settle();
  } finally { view.dispose(); nextResult = undefined; }
 }
});

test("collapsed card exposes the top action without expanding for local work", async () => {
 const rows = ["api", "docs"].map(name => {
  const next = { step: "catch-up", by: "wtc", command: ["wtc", "catch-up"], key: `${name}:catchup:h1`, reason: "Behind main" };
  return repo(name, { next, choices: { recommended: next.key, actions: [{ ...next, available: true }] } });
 });
 const { host, view } = await mount("composer", collection(rows));
 let release;
 try {
  const card = [...host.all()].find(n => n.className === "wtc-collection-card");
  card.open = false; for (const fn of card.listeners.toggle) fn();
  const summary = card.children.find(n => n.tagName === "SUMMARY");
  assert.equal([...summary.all()].filter(n => n.className === "wtc-action-toolbar").length, 1);
  assert.equal([...host.all()].filter(n => n.className === "wtc-action-toolbar").length, 1, "the controls are not repeated below the table");
  nextResult = () => new Promise(resolve => { release = resolve; }); calls.length = 0;
  await clickText(summary, "Catch up 2");
  assert.equal(card.open, false, "local execution keeps the one-line card collapsed");
  assert.equal(calls.filter(c => c.actionId === "next").length, 1);
  await clickText(summary, "Cancel queued");
  release({ ok: true, result: { performed: true, repo: rows[0], code: 0 } });
  for (let i = 0; i < 6; i++) await settle();
  assert.equal(card.open, false);
  await clickText(summary, "Choose steps…");
  assert.equal(card.open, true, "opening the chooser reveals its controls");
 } finally { view.dispose(); nextResult = undefined; }
});

test("collapsed PR update uses the existing personal preparation and confirmation", async () => {
 const next = { step: "push", by: "wtc", approve: "person", gateway: "github_publish", key: "api:push:h1", reason: "Send local commits to the PR" };
 const r = repo("api", { ahead: 1, unpublished: 1, pullRequest: { number: 7, state: "open", url: "https://github.com/octo/api/pull/7" }, next, choices: { recommended: next.key, actions: [{ ...next, available: true }] } });
 personal = { state: "connected", account: { login: "demo" }, generation: "g1" };
 const { host, view } = await mount("composer", collection([r]));
 try {
  const card = [...host.all()].find(n => n.className === "wtc-collection-card"); card.open = false;
  for (const fn of card.listeners.toggle) fn();
  const toolbar = () => [...card.all()].find(n => n.className === "wtc-action-toolbar");
  nextResult = { ok: true, result: { performed: false, repo: r } }; calls.length = 0;
  await clickText(toolbar(), "Update PR");
  assert.equal(card.open, false, "publication keeps the collapsed bar in place");
  assert.ok([...host.all()].some(n => n.className === "wtc-publication-confirmations"), "the separate personal preview is visible above the collapsed bar");
  assert.equal(calls.filter(c => c.method === "sessions.github.publish").length, 1);
  assert.equal(calls.filter(c => c.method === "sessions.github.confirm").length, 0);
  await clickText(host, "Confirm push");
  assert.equal(calls.filter(c => c.method === "sessions.github.confirm").length, 1);
  assert.equal(card.open, false, "confirmed publication also preserves the collapsed bar");
 } finally { view.dispose(); nextResult = undefined; personal = null; }
});

test("a collapsed legacy snapshot exposes publication without clipping it into summary facts", async () => {
 personal = { state: "connected", account: { login: "demo" }, generation: "g1" };
 const { host, view } = await mount("composer", collection([repo("api", { ahead: 2, unpublished: 2 })]));
 try {
  const card = [...host.all()].find(n => n.className === "wtc-collection-card"); card.open = false;
  for (const fn of card.listeners.toggle) fn();
  const toolbar = [...card.all()].find(n => n.className === "wtc-action-toolbar");
  calls.length = 0;
  await clickText(toolbar, "Push");
  assert.equal(card.open, false);
  assert.equal(calls.filter(c => c.method === "sessions.github.publish").length, 1);
  assert.equal(calls.filter(c => c.method === "sessions.github.confirm").length, 0);
  assert.ok([...host.all()].some(n => n.className === "wtc-publication-confirmations"));
  await clickText(host, "Confirm push");
  assert.equal(calls.filter(c => c.method === "sessions.github.confirm").length, 1);
  assert.equal(card.open, false);
 } finally { view.dispose(); personal = null; }
});


test("PR hints distinguish current, other active and completed PRs and open sidebar history", async () => {
 const pr = (number, state) => ({ number, state, title: `Change ${number}`, url: `https://github.com/octo/api/pull/${number}` });
 const current = pr(7, "open");
 const r = repo("api", { pullRequest: current, pullRequests: [current, pr(8, "open"), pr(2, "merged"), pr(2, "merged"), pr(3, "closed"), pr(4, "merged")] });
 const { host, view } = await mount("composer", collection([r]));
 try {
  assert.equal(tables(host).length, 1, "no second PR list occupies the conversation");
  assert.equal(tables(host)[0][1][3], "#74 other PRs");
  const active = [...host.all()].find(n => n.tagName === "A" && n.textContent === "#8");
  assert.equal(active, undefined, "the fixed row summarizes additional PRs rather than growing another inline list");
  const before = openedPanels.length;
  const history = [...host.all()].find(n => n.tagName === "BUTTON" && n.textContent === "4 other PRs" && /1 open, 3 archived/.test(n.title));
  assert.ok(history, "the summary names open and archived counts accessibly");
  history.onclick({ preventDefault() {}, stopPropagation() {} });
  assert.equal(openedPanels.length, before + 1);
  assert.equal(openedPanels.at(-1)[0], "collection");
  assert.equal(openedPanels.at(-1)[1].focus, "pull-requests");
  const { host: panel, view: sidebar, context } = await mount("panel", collection([r]));
  try {
   assert.notEqual([...panel.all()].find(n => n.className === "wtc-pr-history").scrolled, true);
   sidebar.update({ ...context, props: { ...context.props, focus: "pull-requests" } });
   assert.equal([...panel.all()].find(n => n.className === "wtc-pr-history").scrolled, true);
   assert.equal(tables(panel)[1].length, 6, "sidebar retains all five distinct PRs");
   assert.equal([...panel.all()].find(n => n.tagName === "A" && n.textContent === "#8").href, "https://github.com/octo/api/pull/8");
  } finally { sidebar.dispose(); }
 } finally { view.dispose(); }
});


test("a fetched catch-up conflict hands only the fresh resolution step to its named skill", async () => {
 for (const kind of ["composer", "panel"]) {
  const key = `agent:main:conflict:${++sessions}`;
  const next = { step: "catch-up", by: "wtc", command: ["wtc", "catch-up"], key: "api:catchup:h1", reason: "Merges cleanly" };
  const r = repo("api", { next, choices: { recommended: next.key, actions: [{ ...next, available: true }] } });
  const merge = { step: "merge", by: "agent", skill: "wtc-catch-up", key: "api:merge:h2", reason: "Merge conflicts in README.md" };
  const fresh = repo("api", { next: merge, choices: { recommended: merge.key, actions: [{ ...merge, available: true }] } });
  const snapshot = (repos) => ({ ...collection(repos), collection: { ...collection(repos).collection, sessionKey: key } });
  const { host, view } = await mount(kind, snapshot([r]), undefined, key);
  try {
   await chooseSteps(host); await clickText(host, "Select suggested"); calls.length = 0;
   nextResult = ({ payload }) => {
    if (payload.key === next.key) {
     collections.set(key, snapshot([fresh]));
     return { ok: true, result: { performed: true, repo: r, code: 1, message: "merging origin/main conflicts in: README.md. The merge was aborted" } };
    }
    return { ok: true, result: { performed: false, repo: fresh } };
   };
   await clickText(host, "Run selected (1)");
   const sends = calls.filter(c => c.method === "sessions.send");
   assert.equal(sends.length, 1); assert.match(sends[0].message, /^\/skill wtc-catch-up api: Merge conflicts/);
   assert.doesNotMatch(sends[0].message, /Carry out only these selected single steps|Merges cleanly/);
   assert.equal(calls.filter(c => c.actionId === "next")[0].payload.key, next.key);
   assert.equal(calls.filter(c => c.actionId === "next")[1].payload.key, merge.key);
  } finally { view.dispose(); nextResult = undefined; }
 }
});


test("batch completion stays compact and its shared sidebar log retains the details", async () => {
 const key = `agent:main:activity:${++sessions}`;
 const rows = ["api", "docs"].map(name => {
  const next = { step: "catch-up", by: "wtc", command: ["wtc", "catch-up"], key: `${name}:catchup:h1`, reason: "Behind main" };
  return repo(name, { next, choices: { recommended: next.key, actions: [{ ...next, available: true }] } });
 });
 const { host, view } = await mount("composer", collection(rows), undefined, key);
 const releases = [];
 try {
  nextResult = () => new Promise(resolve => releases.push(resolve));
  await chooseSteps(host); await clickText(host, "Select suggested"); await clickText(host, "Run selected (2)");
  releases[0]({ ok: true, result: { performed: true, repo: rows[0], code: 0 } });
  for(let i=0;i<6;i++) await settle();
  assert.equal(releases.length, 2);
  assert.match(readable(host), /✓ Caught up/); assert.match(readable(host), /Catching up…/);
  assert.doesNotMatch(readable(host), /Working…Completed/);
  const fresh = collection([repo("api", { branch: null }), repo("docs", { branch: null })]);
  collections.set(key, fresh);
  releases[1]({ ok: true, result: { performed: true, repo: rows[1], code: 0 } });
  for(let i=0;i<6;i++) await settle();
  assert.ok(buttons(host).includes("✓ Caught up 2"));
  assert.equal(buttons(host).some(text => /^Results/.test(text) || text === "Done choosing"), false);
  assert.doesNotMatch(host.textContent, /api: Completed|docs: Completed/, "full details stay out of the visible card body");
  await clickText(host, "✓ Caught up 2");
  assert.equal(openedPanels.at(-1)[1].focus, "activity");
  const sidebar = await mount("panel", fresh, undefined, key, { focus: "activity" });
  try {
   const log = [...sidebar.host.all()].find(n => n.className === "wtc-activity-log");
   assert.ok(log?.scrolled); assert.match(log.textContent, /api · Catch up/); assert.match(log.textContent, /docs · Catch up/);
   assert.equal((log.textContent.match(/Completed/g) ?? []).length, 2);
   const services = [...sidebar.host.all()].find(n => n.tagName === "H3" && n.textContent === "Services").parentNode.parentNode;
   assert.ok(log.parentNode.children.indexOf(services) < log.parentNode.children.indexOf(log), "services precede the action log");
  } finally { sidebar.view.dispose(); }
  const unrelated = await mount("panel", collection([repo("other")]));
  try { assert.equal([...unrelated.host.all()].some(n => n.className === "wtc-activity-log"), false); }
  finally { unrelated.view.dispose(); }
 } finally { view.dispose(); nextResult = undefined; }
});


test("a fetched conflict never hands off a changed or unreadable resolution step", async () => {
 for (const failure of ["changed", "unreadable"]) {
  const key = `agent:main:conflict-refusal:${++sessions}`;
  const next = { step: "catch-up", by: "wtc", command: ["wtc", "catch-up"], key: "api:catchup:h1", reason: "Merges cleanly" };
  const r = repo("api", { next, choices: { recommended: next.key, actions: [{ ...next, available: true }] } });
  const { host, view } = await mount("composer", collection([r]), undefined, key);
  try {
   await chooseSteps(host); await clickText(host, "Select suggested"); calls.length = 0;
   nextResult = () => {
    collections.set(key, failure === "unreadable" ? new Error("Collection unavailable") : collection([repo("api")]));
    return { ok: true, result: { performed: true, repo: r, code: 1, message: "merging origin/main conflicts in: README.md. The merge was aborted" } };
   };
   await clickText(host, "Run selected (1)");
   assert.equal(calls.filter(c => c.method === "sessions.send").length, 0);
   await chooseSteps(host);
   assert.match(readable(host), failure === "unreadable" ? /Cannot recheck conflict resolution: Collection unavailable/ : /Catch-up stopped; its current resolution step changed/);
  } finally { view.dispose(); nextResult = undefined; }
 }
});


test("a finished mixed batch restores PR navigation instead of claiming it is running", async () => {
 for (const kind of ["composer", "panel"]) {
  const catchup = { step: "catch-up", by: "wtc", command: ["wtc", "catch-up"], key: "api:catchup:h1", reason: "Behind main" };
  const navigate = { step: "merge-pr", by: "person", approve: "person", key: "web:merge:h1", reason: "Review merge requirements" };
  const rows = [["api", catchup], ["web", navigate]].map(([name, next]) => repo(name, { next, choices: { recommended: next.key, actions: [{ ...next, available: true }] }, ...(name === "web" ? { pullRequest: { number: 7, state: "open", url: "https://github.com/octo/web/pull/7" } } : {}) }));
  const { host, view } = await mount(kind, collection(rows));
  try {
   nextResult = ({ payload }) => {
    const r = rows.find(r => r.sessionKey === payload.ownerSessionKey);
    return { ok: true, result: { performed: r === rows[0], repo: r, code: 0 } };
   };
   calls.length = 0;
   await clickText(host, "Do next (2) →");
   await clickText(host, "Confirm 2 actions");
   assert.equal(calls.filter(c => c.actionId === "next").length, 2, "the test must dispatch the batch rather than stop at its preview");
   const action = [...host.all()].find(n => n.className === "wtc-next-step" && [...n.all()].some(a => a.tagName === "A" && a.href === rows[1].pullRequest.url));
   assert.ok(action, "the repository retains its PR navigation action");
   assert.match(action.textContent, /Open pull request/);
   assert.doesNotMatch(readable(host), /Running…|Catching up…/);
   assert.equal(calls.filter(c => c.method === "sessions.send").length, 0);
  } finally { view.dispose(); nextResult = undefined; }
 }
});


test("baseline labels agree while hover text identifies actual branch versus detached HEAD", async () => {
 const { host, view } = await mount("composer", collection([
  repo("untouched", { branch: "openclaw/task", ahead: 0 }), repo("detached", { branch: null }), repo("working", { branch: "main", dirty: 1 }),
 ]));
 try {
  assert.deepEqual(tables(host)[0].slice(1).map(r => r[1]), ["at main", "at main", "main"]);
  assert.ok([...host.all()].some(n => n.title === "Checked out on branch openclaw/task; no task commits ahead of main"));
  assert.ok([...host.all()].some(n => n.title === "Detached HEAD at main; no branch is checked out"));
 } finally { view.dispose(); }
});


test("publication discards a pre-action refresh and stays busy until a fresh status returns", async () => {
  const r = repo("api", { unpublished: 1 });
  personal = { state: "connected", account: { login: "demo" }, generation: "g1" };
  const { host, view, context } = await mount("composer", collection([r]));
  let oldResponse, freshResponse;
  try {
    await [...host.all()].find(n => n.tagName === "BUTTON" && /^1 commit to push/.test(n.title)).onclick();
    const confirm = [...host.all()].find(n => n.tagName === "BUTTON" && n.textContent === "Confirm push");
    assert.ok(confirm);
    let reads = 0;
    controlledCollection = () => new Promise(resolve => { if (++reads === 1) oldResponse = resolve; else freshResponse = resolve; });
    [...host.all()].find(n => n.tagName === "BUTTON" && /Updated .*Refresh/.test(n.title)).onclick({ preventDefault() {}, stopPropagation() {} });
    await settle();
    const publication = confirm.onclick({ preventDefault() {}, stopPropagation() {} });
    await settle();
    assert.equal(reads, 1, "the post-publication refresh waits for the existing request");
    oldResponse({ ok: true, result: collection([repo("api", { unpublished: 1, branch: "stale-response" })]) });
    for (let i = 0; i < 4; i++) await settle();
    assert.equal(reads, 2, "a fresh request follows the invalidated one");
    assert.doesNotMatch(readable(host), /stale-response/, "the pre-action response was never applied");
    const counts = [...host.all()].filter(n => n.tagName === "BUTTON" && n.textContent === "↑ 1");
    assert.ok(counts.length, "the captured count remains visible during the fresh-status check");
    assert.ok(counts.every(n=>n.disabled), "retaining a count must not offer another publication");
    const fresh = collection([repo("api", { unpublished: 0, branch: "after-push", pullRequest: { number: 7, state: "open", url: "https://github.com/octo/api/pull/7" } })]);
    collections.set(context.props.sessionKey, fresh);
    freshResponse({ ok: true, result: fresh });
    await publication;
    assert.match(readable(host), /after-push/);
    assert.equal([...host.all()].some(n => n.tagName === "BUTTON" && n.textContent === "↑ 1"), false);
  } finally { controlledCollection = undefined; oldResponse?.({ ok: true, result: collection([r]) }); freshResponse?.({ ok: true, result: collection([r]) }); view.dispose(); personal = null; }
});

for (const kind of ["composer", "panel"]) test(`${kind}: repeated preparation targets never publish and Cancel invalidates their confirmation`, async () => {
  personal = { state: "connected", account: { login: "demo" }, generation: "g1" };
  const { host, view } = await mount(kind, collection([repo("api", { unpublished: 1 })]));
  try {
    calls.length = 0;
    await [...host.all()].find(n => n.tagName === "BUTTON" && /^1 commit to push/.test(n.title)).onclick();
    const confirm = [...host.all()].find(n => n.tagName === "BUTTON" && n.textContent === "Confirm push");
    const review = [...host.all()].find(n => n.tagName === "BUTTON" && /prepared publication before confirming/.test(n.title));
    assert.ok(confirm); assert.ok(review);
    await review.onclick({ preventDefault() {}, stopPropagation() {} });
    await review.onclick({ preventDefault() {}, stopPropagation() {} });
    assert.equal(calls.filter(c => c.method === "sessions.github.confirm").length, 0);
    assert.match(readable(host), /@demo/);
    await [...host.all()].find(n => n.tagName === "BUTTON" && /^Cancel preparation/.test(n.title)).onclick();
    assert.equal([...host.all()].some(n => n.tagName === "BUTTON" && n.textContent === "Confirm push"), false);
    await confirm.onclick();
    assert.equal(calls.filter(c => c.method === "sessions.github.confirm").length, 0, "a stale confirmation cannot execute after cancellation");
    await [...host.all()].find(n => n.tagName === "BUTTON" && /^1 commit to push/.test(n.title)).onclick();
    await confirm.onclick();
    assert.equal(calls.filter(c => c.method === "sessions.github.confirm").length, 0, "a stale button cannot confirm a newly prepared request");
  } finally { view.dispose(); personal = null; }
});

for (const kind of ["composer", "panel"]) test(`${kind}: old confirmation cannot publish after navigation to another conversation`, async () => {
 personal={state:"connected",account:{login:"demo"},generation:"g1"};
 const {host,view}=await mount(kind,collection([repo("api",{unpublished:1})]));
 try{
  await [...host.all()].find(n=>n.tagName==="BUTTON"&&/^1 commit to push/.test(n.title)).onclick();
  const confirm=[...host.all()].find(n=>n.tagName==="BUTTON"&&n.textContent==="Confirm push");assert.ok(confirm);
  const nextKey=`agent:main:other:${++sessions}`;collections.set(nextKey,collection([repo("other")]));
  view.update({props:{sessionKey:nextKey,agentId:"main"},host:{request:hostRequest,ui:{openPanel(){}}},signal:new AbortController().signal,presented:true,mountDefault:()=>()=>{}});
  await settle();calls.length=0;await confirm.onclick();
  assert.equal(calls.filter(c=>c.method==="sessions.github.confirm").length,0);
 }finally{view.dispose();personal=null;}
});

test("settled header feedback yields to the next action while its result remains in history", async t => {
  const next = { step: "catch-up", by: "wtc", command: ["wtc", "catch-up"], key: "api:catchup:h1", reason: "Behind main" };
  const { host, view } = await mount("composer", collection([repo("api", { behind: 2, next, choices: { recommended: next.key, actions: [{ ...next, available: true }] } })]));
  try {
    t.mock.timers.enable({ apis: ["setTimeout"] });
    await [...host.all()].find(n => n.tagName === "BUTTON" && /Updated .*Refresh/.test(n.title)).onclick({ preventDefault() {}, stopPropagation() {} });
    assert.ok([...host.all()].some(n => n.className?.includes("wtc-header-feedback") && n.textContent === "Refreshed"));
    t.mock.timers.tick(8001);
    assert.equal([...host.all()].some(n => n.className?.includes("wtc-header-feedback")), false);
    assert.ok([...host.all()].some(n => n.tagName === "BUTTON" && n.textContent === "Catch up" && n.title.includes("api:")), "the primary next action returns");
    t.mock.timers.reset();
    const panel = await mount("panel", collection([repo("api")]), undefined, contexts.get(host).props.sessionKey);
    try { assert.match(readable(panel.host), /Collection status refreshed/); } finally { panel.view.dispose(); }
  } finally { t.mock.timers.reset(); view.dispose(); }
});


test("panel: failed repository addition remains visible before a collection exists", async () => {
  const { host, view } = await mount("panel", collection([]));
  try {
    const input = [...host.all()].find(n => n.tagName === "INPUT" && n.placeholder === "Find or add a repository");
    input.value = "https://github.com/example/demo";
    input.onkeydown({ key: "Enter", preventDefault() {} });
    await settle();
    failing.add("projects.add");
    await [...host.all()].find(n => n.tagName === "BUTTON" && n.textContent === "Add").onclick();
    const alert = [...host.all()].find(n => n.attributes?.role === "alert" || n.role === "alert");
    assert.ok(alert, "failed addition exposes an alert in the empty panel");
    assert.match(alert.textContent, /projects.add failed/);
  } finally { failing.delete("projects.add"); view.dispose(); }
});

test("composer: repository-session service results appear in the owning collection history", async () => {
  const collectionKey = `agent:main:collection:${++sessions}`;
  const data = { ...collection([repo("api")], [{ group: "api/web", path: "api/web/main", repo: "api", kind: "service", label: "preview", state: "absent" }]), collection: { sessionKey: collectionKey, label: "Demo" } };
  const { host, view, context } = await mount("composer", data);
  try {
    calls.length = 0;
    await titled(host, "preview: absent. Start it").onclick({ preventDefault() {}, stopPropagation() {} });
    const rpc = calls.find(c => c.actionId === "svc");
    assert.equal(rpc.sessionKey, context.props.sessionKey, "service dispatch retains its original conversation");
    const panel = await mount("panel", data, undefined, collectionKey);
    try {
      const log = [...panel.host.all()].find(n => n.className === "wtc-activity-log");
      assert.ok(log); assert.match(log.textContent, /api\/web/); assert.match(log.textContent, /Start/);
    } finally { panel.view.dispose(); }
  } finally { view.dispose(); }
});

test("a preparation returned after navigation is cancelled in its original history", async () => {
  personal = { state: "connected", account: { login: "demo" }, generation: "g1" };
  const previous = githubPublishResult;
  let finish;
  githubPublishResult = new Promise(resolve => { finish = resolve; });
  const data = collection([repo("api", { unpublished: 1 })]);
  const { host, view, context } = await mount("composer", data);
  try {
    const pending = [...host.all()].find(n => n.tagName === "BUTTON" && /^1 commit to push/.test(n.title)).onclick();
    await settle();
    const nextKey = `agent:main:other:${++sessions}`;
    collections.set(nextKey, collection([repo("other")]));
    view.update({ ...context, props: { sessionKey: nextKey, agentId: "main" } });
    finish({ status: "needs_confirmation", requestId: "delayed" });
    await pending;
    const panel = await mount("panel", data, undefined, context.props.sessionKey);
    try {
      assert.match(readable(panel.host), /Cancelled/);
      assert.match(readable(panel.host), /nothing was pushed/);
    } finally { panel.view.dispose(); }
  } finally { finish?.({ status: "needs_confirmation", requestId: "delayed" }); view.dispose(); personal = null; githubPublishResult = previous; }
});

test("a failed push offers Retry only for a matching next action", async () => {
  const step = { step: "push", by: "wtc", approve: "person", gateway: "github_publish", key: "api:push:h1", reason: "Send local commits" };
  const r = repo("api", { unpublished: 1, next: step, choices: { actions: [{ ...step, available: true }] } });
  personal = { state: "connected", account: { login: "demo" }, generation: "g1" };
  const { host, view, context } = await mount("composer", collection([r]));
  try {
    failing.add("sessions.github.publish");
    await [...host.all()].find(n => n.tagName === "BUTTON" && /^1 commit to push/.test(n.title)).onclick();
    assert.ok([...host.all()].some(n => n.tagName === "BUTTON" && n.textContent === "Retry"));
    assert.ok([...host.all()].some(n => n.tagName === "BUTTON" && n.textContent === "⚠"));
    const next = { step: "catch-up", by: "wtc", command: ["wtc", "catch-up"], key: "api:catchup:h2", reason: "Behind main" };
    collections.set(context.props.sessionKey, collection([repo("api", { behind: 1, next, choices: { actions: [{ ...next, available: true }] } })]));
    failing.delete("sessions.github.publish");
    await [...host.all()].find(n => n.tagName === "BUTTON" && /Updated .*Refresh/.test(n.title)).onclick({ preventDefault() {}, stopPropagation() {} });
    assert.equal([...host.all()].some(n => n.className === "wtc-next-step" && n.textContent === "Retry"), false);
  } finally { failing.delete("sessions.github.publish"); view.dispose(); personal = null; }
});


test("direct catch-up is labelled Running in the action log until it completes", async () => {
  const previous = catchUpResult;
  let finish;
  catchUpResult = new Promise(resolve => { finish = resolve; });
  const { host, view } = await mount("panel", collection([repo("api", { behind: 2 })]));
  try {
    const pending = [...host.all()].find(n => n.tagName === "BUTTON" && /^2 new on main/.test(n.title)).onclick();
    const log = [...host.all()].find(n => n.className === "wtc-activity-log");
    assert.ok(log); assert.match(log.textContent, /Running/);
    finish({ ok: true, result: { code: 0, json: { summary: "merged" } } });
    await pending;
    assert.match([...host.all()].find(n => n.className === "wtc-activity-log").textContent, /Done/);
  } finally { finish?.(previous); catchUpResult = previous; view.dispose(); }
});


test("temporary controls extend above the composer base and below sidebar services", async () => {
  personal = { state: "connected", account: { login: "demo" }, generation: "g1" };
  for (const kind of ["composer", "panel"]) {
    const { host, view } = await mount(kind, collection([repo("api", { unpublished: 1 })]));
    try {
      await [...host.all()].find(n => n.tagName === "BUTTON" && /^1 commit to push/.test(n.title)).onclick();
      const extra = [...host.all()].find(n => n.className === "wtc-transient-actions");
      const base = [...host.all()].find(n => n.className === "wtc-collection-card");
      assert.ok(extra);assert.match(extra.textContent, /Confirm push/);
      if(kind === "composer") {
        assert.ok(host.children.indexOf(extra) < host.children.indexOf(base));
        assert.ok([...base.all()].some(n => n.className === "wtc-repositories"));
        assert.equal([...base.all()].some(n => n.textContent === "Confirm push"), false);
      } else {
        const services = [...host.all()].find(n => n.className === "wtc-base-services");
        const extraIndex = extra.parentNode.children.indexOf(extra), serviceIndex = extra.parentNode.children.indexOf(services);
        assert.ok(extraIndex >= 0 && serviceIndex >= 0);
        assert.ok(extraIndex > serviceIndex);
      }
    } finally {view.dispose();}
  }
  personal = null;
});

test("bulk changes control stays disabled and unused push/catch-up columns are absent", async () => {
  for(const kind of ["composer", "panel"]) {
    const {host, view}=await mount(kind, collection([repo("api")]));
    try {
      const bulk=[...host.all()].find(n=>n.className === "wtc-bulk-actions");assert.ok(bulk);
      const controls=[...bulk.all()].filter(n=>n.tagName === "BUTTON");assert.equal(controls.length,1);assert.ok(controls.every(n=>n.disabled));
    } finally {view.dispose();}
  }
});


test("sidebar Review push explicitly reveals the temporary section without automatic scrolling", async () => {
  personal = { state: "connected", account: { login: "demo" }, generation: "g1" };
  const previous = Node.prototype.scrollIntoView;
  const scrolled=[]; Node.prototype.scrollIntoView=function(options){scrolled.push({node:this,options});};
  const step={step:"push",by:"wtc",approve:"person",gateway:"github_publish",key:"api:push:h1",reason:"Push local commits"};
  const {host,view}=await mount("panel",collection([repo("api",{unpublished:1,next:step,choices:{actions:[{...step,available:true}]}})]));
  try {
    await [...host.all()].find(n=>n.tagName === "BUTTON" && /^1 commit to push/.test(n.title)).onclick();
    assert.equal(scrolled.length,0,"preparing does not move the fixed base out of view");
    await [...host.all()].find(n=>n.tagName === "BUTTON" && n.textContent === "Review push").onclick();
    assert.equal(scrolled.length,1);
    assert.equal(scrolled[0].node.className,"wtc-transient-actions");
    assert.equal(scrolled[0].options.block,"nearest");
    scrolled.length=0;
    await [...host.all()].find(n=>n.tagName === "BUTTON" && n.ariaLabel === "Choose steps…").onclick();
    assert.equal(scrolled.length,1,"explicit selection also reveals its controls");
    assert.equal(scrolled[0].node.className,"wtc-transient-actions");
  } finally {view.dispose();personal=null;if(previous)Node.prototype.scrollIntoView=previous;else delete Node.prototype.scrollIntoView;}
});

test("a settled single-repository push resolves the fixed header slot", async () => {
  personal={state:"connected",account:{login:"demo"},generation:"g1"};
  const {host,view,context}=await mount("composer",collection([repo("api",{unpublished:1})]));
  try {
    await [...host.all()].find(n=>n.tagName === "BUTTON" && /^1 commit to push/.test(n.title)).onclick();
    collections.set(context.props.sessionKey,collection([repo("api",{unpublished:0,pullRequest:{number:7,state:"open",url:"https://github.com/octo/api/pull/7"}})]));
    await [...host.all()].find(n=>n.tagName === "BUTTON" && n.textContent === "Confirm push").onclick();
    const slot=[...host.all()].find(n=>n.className?.startsWith("wtc-toolbar-primary"));
    assert.match(slot.textContent,/Pushed/);
    assert.doesNotMatch(readable(host),/Publishing…/);
  } finally {view.dispose();personal=null;}
});


test("a tracked push has one shared log row through confirmation, publication and completion", async () => {
  personal={state:"connected",account:{login:"demo"},generation:"g1"};
  const data=collection([repo("api",{unpublished:1})]);
  const first=await mount("composer",data),second=await mount("panel",data,undefined,first.context.props.sessionKey);
  let finish;
  const observerPush=[...second.host.all()].find(n=>n.tagName === "BUTTON" && /^1 commit to push/.test(n.title));
  try {
    await [...first.host.all()].find(n=>n.tagName === "BUTTON" && /^1 commit to push/.test(n.title)).onclick();
    const rows=()=>[...second.host.all()].filter(n=>n.className === "wtc-activity-item");
    assert.equal(rows().length,1);
    githubConfirmResult=new Promise(resolve=>{finish=resolve;});
    const pending=[...first.host.all()].find(n=>n.tagName === "BUTTON" && n.textContent === "Confirm push").onclick();
    await settle();
    assert.equal(rows().length,1,"publishing must not add a generic Latest action duplicate");
    assert.doesNotMatch(rows()[0].textContent,/Latest action/);
    assert.match(rows()[0].textContent,/Running/);
    assert.equal([...second.host.all()].filter(n=>n.tagName === "BUTTON" && /^1 commit to push/.test(n.title)).length,0,"observer arrow must show working while publishing");
    assert.ok([...second.host.all()].find(n=>n.tagName === "BUTTON" && /^Push all/.test(n.title)).disabled,"observer bulk push must be disabled");
    const prepares=calls.filter(c=>c.method === "sessions.github.publish").length;
    await observerPush.onclick();
    assert.equal(calls.filter(c=>c.method === "sessions.github.publish").length,prepares,"captured observer control cannot prepare another push");
    finish({status:"published",url:"https://github.com/octo/api/pull/7"});await pending;
    assert.equal(rows().length,1);assert.match(rows()[0].textContent,/Done/);
    assert.equal([...second.host.all()].find(n=>n.tagName === "BUTTON" && /^Push all/.test(n.title)).disabled,false,"observer reservation releases after refresh");
  } finally {finish?.({status:"published",url:"https://github.com/octo/api/pull/7"});githubConfirmResult=undefined;personal=null;first.view.dispose();second.view.dispose();}
});

test("an unreadable pending confirmation never reports a successful push", async () => {
  personal={state:"connected",account:{login:"demo"},generation:"g1"};
  failing.add("sessions.github.status");
  const {host,view}=await mount("panel",collection([repo("api",{unpublished:1})]));
  try {
    calls.length=0;
    await [...host.all()].find(n=>n.tagName === "BUTTON" && /^1 commit to push/.test(n.title)).onclick();
    const log=[...host.all()].find(n=>n.className === "wtc-activity-log");
    assert.ok(log);assert.match(log.textContent,/Needs attention/);
    assert.doesNotMatch(log.textContent,/Done|Pushed/);
    assert.match(log.textContent,/gateway waits for a confirmation/);
    assert.equal(calls.filter(c=>c.method === "sessions.github.confirm").length,0);
  } finally {failing.delete("sessions.github.status");personal=null;view.dispose();}
});

test("publication headers distinguish preparation from a personally confirmed push", async () => {
  const previous=githubPublishResult;
  personal={state:"connected",account:{login:"demo"},generation:"g1"};
  let releasePreparation,releasePublication,preparation,publication;
  githubPublishResult=()=>new Promise(resolve=>{releasePreparation=resolve;});
  const data=collection([repo("api",{unpublished:1})]);
  const first=await mount("composer",data),second=await mount("panel",data,undefined,first.context.props.sessionKey);
  const header=host=>[...host.all()].find(n=>n.className === "wtc-action-toolbar").textContent;
  try {
    calls.length=0;
    preparation=[...first.host.all()].find(n=>n.tagName === "BUTTON" && /^1 commit to push/.test(n.title)).onclick();
    await settle();
    for(const host of [first.host,second.host]) {assert.match(header(host),/Preparing…/);assert.doesNotMatch(header(host),/Publishing/);}
    assert.equal(calls.filter(c=>c.method === "sessions.github.confirm").length,0);
    releasePreparation({status:"needs_confirmation",requestId:"req-1"});await preparation;
    githubConfirmResult=()=>new Promise(resolve=>{releasePublication=resolve;});
    publication=[...first.host.all()].find(n=>n.tagName === "BUTTON" && n.textContent === "Confirm push").onclick();
    await settle();
    for(const host of [first.host,second.host])assert.match(header(host),/Publishing…/);
    releasePublication({status:"published",url:"https://github.com/octo/api/pull/7"});await publication;
  } finally {
    releasePreparation?.({status:"needs_confirmation",requestId:"req-1"});releasePublication?.({status:"published",url:"https://github.com/octo/api/pull/7"});
    await Promise.allSettled([preparation,publication].filter(Boolean));
    githubPublishResult=previous;githubConfirmResult=undefined;personal=null;first.view.dispose();second.view.dispose();
  }
});

test("a concurrent preparation is not named as the repository currently publishing", async () => {
  const previous=githubPublishResult;personal={state:"connected",account:{login:"demo"},generation:"g1"};
  let releasePreparation,releasePublication,preparation,publication;
  githubPublishResult=params=>params.sessionKey.endsWith(":web")?new Promise(resolve=>{releasePreparation=resolve;}):{status:"needs_confirmation",requestId:"req-1"};
  const {host,view}=await mount("composer",collection([repo("api",{unpublished:1}),repo("web",{unpublished:2})]));
  try {
    await [...host.all()].find(n=>n.tagName === "BUTTON"&&/^1 commit to push/.test(n.title)).onclick();
    githubConfirmResult=()=>new Promise(resolve=>{releasePublication=resolve;});
    publication=[...host.all()].find(n=>n.tagName === "BUTTON"&&n.textContent === "Confirm push").onclick();await settle();
    preparation=[...host.all()].find(n=>n.tagName === "BUTTON"&&/^2 commits to push/.test(n.title)).onclick();await settle();
    const context=[...host.all()].find(n=>n.className === "wtc-header-feedback wtc-action-control");
    assert.ok(context);assert.doesNotMatch(context.textContent,/web/,"the unconfirmed repository must not be paired with the collection's Publishing label");
    assert.match([...host.all()].find(n=>n.className === "wtc-action-toolbar").textContent,/Publishing…/);
  } finally {
    releasePreparation?.({status:"needs_confirmation",requestId:"req-web"});releasePublication?.({status:"published",url:"https://github.com/octo/api/pull/7"});
    await Promise.allSettled([preparation,publication].filter(Boolean));githubPublishResult=previous;githubConfirmResult=undefined;personal=null;view.dispose();
  }
});

test("compact rows omit empty facts and do not reserve a PR lane for catch-up only", async () => {
  const observers=[];
  globalThis.ResizeObserver=class { constructor(fn){this.fn=fn;observers.push(this);} observe(){} disconnect(){} };
  const {host,view}=await mount("panel",collection([repo("api"),repo("web",{behind:3})]));
  try {
    observers.forEach(o=>o.fn([{contentRect:{width:240}}]));
    const identities=[...host.all()].filter(n=>n.className==="wtc-repo-identity");
    assert.equal(identities.length,2);
    assert.equal([...identities[0].parentNode.all()].some(n=>n.className==="wtc-repo-facts"),false);
    const facts=[...identities[1].parentNode.all()].find(n=>n.className==="wtc-repo-facts");
    assert.ok(facts); assert.match(facts.textContent,/↓ 3/);
    assert.equal([...facts.all()].some(n=>n.className==="wtc-repo-pr-slots"),false);
  } finally {view.dispose();delete globalThis.ResizeObserver;}
});

test("an opened action-log result survives an unrelated refresh", async () => {
  const {host,view,context}=await mount("panel",collection([repo("api",{behind:3})]));
  const previous=catchUpResult;
  try {
    catchUpResult={ok:true,result:{code:1,message:"Cannot fetch the remote"}};
    await [...host.all()].find(n=>n.tagName==="BUTTON"&&/^3 new on main/.test(n.title)).onclick();
    const opened=[...host.all()].find(n=>n.className==="wtc-activity-item");
    opened.open=true;opened.ontoggle();
    view.update(context);await settle();
    const restored=[...host.all()].find(n=>n.className==="wtc-activity-item"&&n.ariaLabel===opened.ariaLabel);
    assert.equal(restored,opened,"unchanged updates keep the disclosure connected");assert.equal(restored.open,true);
  } finally {view.dispose();catchUpResult=previous;}
});

test("publication disclosures survive rerender and navigation cancels their preparation record", async () => {
  const previous=githubPublishResult;
  githubPublishResult={status:"needs_confirmation",requestId:"disclosure-demo"};
  githubStatusResult={confirmation:{account:{login:"demo"},commits:["Synthetic commit"]}};
  personal={state:"connected",account:{login:"demo"},generation:"g1"};
  const data=collection([repo("api",{unpublished:1,proposal:{title:"Synthetic change",body:"Synthetic description"}})]);
  const {host,view,context}=await mount("composer",data);
  try {
    await [...host.all()].find(n=>n.tagName==="BUTTON"&&/^1 commit to push/.test(n.title)).onclick();
    const disclosures=()=>[...host.all()].filter(n=>n.tagName==="DETAILS"&&/^Inspect|^Proposed PR text/.test(n.textContent));
    assert.equal(disclosures().length,2);
    for(const n of disclosures()){n.open=true;n.ontoggle();}
    view.update(context);await settle();
    assert.ok(disclosures().every(n=>n.open));
    const nextKey=`agent:main:other:${++sessions}`;collections.set(nextKey,collection([repo("other")]));
    view.update({...context,props:{sessionKey:nextKey,agentId:"main"}});await settle();
    const panel=await mount("panel",data,undefined,context.props.sessionKey);
    try {assert.match(readable(panel.host),/Cancelled/);assert.doesNotMatch(readable(panel.host),/Awaiting confirm/);assert.match(readable(panel.host),/nothing was pushed/);}
    finally {panel.view.dispose();}
  } finally {view.dispose();githubPublishResult=previous;githubStatusResult=undefined;personal=null;}
});


test("a new current PR keeps the archived-history wording and its trailing rail", async () => {
  const archived={number:2,state:"merged",title:"Earlier task",url:"https://example.invalid/pr/2"};
  for(const current of [null,{number:8,state:"open",url:"https://example.invalid/pr/8"}]) {
    const {host,view}=await mount("panel",collection([repo("web",{pullRequest:current,pullRequests:[archived]})]));
    try {const hint=[...host.all()].find(n=>n.tagName==="BUTTON"&&n.textContent==="1 archived PR");assert.ok(hint);assert.match(hint.style.cssText,/justify-content:flex-end/);}
    finally {view.dispose();}
  }
});

for (const kind of ["composer", "panel"]) test(`${kind}: Next publication has one outcome record through failure and Cancel`, async () => {
 const step={step:"push",by:"wtc",approve:"person",gateway:"github_publish",key:"api:push:owner-test",reason:"Publish the captured commits"};
 const r=repo("api",{unpublished:1,next:step,choices:{recommended:step.key,actions:[{...step,available:true}]}}),data=collection([r]);
 personal={state:"connected",account:{login:"demo"},generation:"g1"};
 nextResult={ok:true,result:{performed:false,repo:r}};
 const {host,view,context}=await mount(kind,data);
 let panel;
 try {
  failing.add("sessions.github.publish");
  await clickText(host,"Push");
  assert.ok([...host.all()].some(n=>n.tagName==="BUTTON"&&n.textContent==="Retry"),"failed Next preparation must leave a retry affordance");
  const failureDetails=await mount("panel",data,undefined,context.props.sessionKey);
  try {assert.match([...failureDetails.host.all()].find(n=>n.className==="wtc-activity-item").ariaLabel,/sessions.github.publish failed/,"the publisher error must replace generic route progress in details");}
  finally {failureDetails.view.dispose();}
  failing.delete("sessions.github.publish");
  await clickText(host,"Retry");
  await clickText(host,"Cancel");
  panel=await mount("panel",data,undefined,context.props.sessionKey);
  const records=[...panel.host.all()].filter(n=>n.className==="wtc-activity-item");
  assert.equal(records.length,2,"one record for each of the failed and cancelled attempts");
  assert.match(readable(panel.host),/Failed/);assert.match(readable(panel.host),/Cancelled/);
  assert.ok(records.some(n=>/publication cancelled/.test(n.ariaLabel)),"cancel details must not retain the initial Push progress");
  assert.doesNotMatch(readable(panel.host),/Awaiting confirm|Publication response received/);
 } finally {failing.delete("sessions.github.publish");panel?.view.dispose();view.dispose();personal=null;nextResult=undefined;}
});

for(const kind of ["composer","panel"]) test(`${kind}: closing a prepared view cancels its original confirmation record`,async()=>{
 personal={state:"connected",account:{login:"demo"},generation:"g1"};
 const data=collection([repo("api",{unpublished:1})]);
 const {host,view,context}=await mount(kind,data);
 let panel;
 try {
  await [...host.all()].find(n=>n.tagName==="BUTTON"&&/^1 commit to push/.test(n.title)).onclick();
  assert.ok([...host.all()].some(n=>n.className?.includes("wtc-confirm-publication")));
  view.dispose();
  panel=await mount("panel",data,undefined,context.props.sessionKey);
  assert.match(readable(panel.host),/Cancelled/);assert.match(readable(panel.host),/view closed/);
  assert.doesNotMatch(readable(panel.host),/Awaiting confirm|confirm to push as/);
 }finally{panel?.view.dispose();view.dispose();personal=null;}
});

test("a pending individual handoff keeps Sent and does not replace another repository's action",async()=>{
 const a={step:"fix-checks",by:"agent",skill:"wtc",key:"api:fix:h1",reason:"Repair failing checks"};
 const b={step:"push",by:"wtc",approve:"person",gateway:"github_publish",key:"web:push:h1",reason:"Publish web commits"};
 const api=repo("api",{next:a,choices:{actions:[{...a,available:true}]}}),web=repo("web",{unpublished:1,next:b,choices:{actions:[{...b,available:true}]}});
 const {host,view,context}=await mount("composer",collection([api]));
 const primary=()=>[...host.all()].find(n=>n.className?.startsWith("wtc-toolbar-primary"));
 try {
  nextResult={ok:true,result:{performed:false,repo:api}};
  await clickText(host,"Fix checks");
  assert.match(primary().textContent,/Sent/);assert.doesNotMatch(primary().textContent,/Retry/);
  collections.set(context.props.sessionKey,collection([api,web]));
  await [...host.all()].find(n=>n.tagName==="BUTTON"&&/Updated .*Refresh/.test(n.title)).onclick({preventDefault(){},stopPropagation(){}});
  assert.equal(primary().textContent,"Push","a real target keeps priority over a retry escape hatch");
  assert.equal([...host.all()].some(n=>n.tagName==="BUTTON"&&n.textContent==="Allow retry"),false);
  await clickText(host,"Choose steps…");assert.ok([...host.all()].some(n=>n.tagName==="BUTTON"&&n.textContent==="Allow retry"));
 }finally{view.dispose();nextResult=undefined;}
});


test("a selected publication shares its outcome record rather than leaving a batch confirmation duplicate",async()=>{
 const step={step:"push",by:"wtc",approve:"person",gateway:"github_publish",key:"api:push:batch-owner",reason:"Publish captured commits"};
 const r=repo("api",{unpublished:1,next:step,choices:{recommended:step.key,actions:[{...step,available:true}]}}),data=collection([r]);
 personal={state:"connected",account:{login:"demo"},generation:"g1"};nextResult={ok:true,result:{performed:false,repo:r}};
 const {host,view,context}=await mount("composer",data);let panel;
 try{
  await chooseSteps(host);await clickText(host,"Select suggested");await clickText(host,"Run selected (1)");await clickText(host,"Confirm 1 actions");
  for(let i=0;i<12;i++)await settle();
  await clickText(host,"Cancel");
  panel=await mount("panel",data,undefined,context.props.sessionKey);
  assert.equal([...panel.host.all()].filter(n=>n.className==="wtc-activity-item").length,1);
  assert.match(readable(panel.host),/Cancelled/);assert.doesNotMatch(readable(panel.host),/Awaiting confirm/);
 }finally{panel?.view.dispose();view.dispose();personal=null;nextResult=undefined;}
});

test("a Next publication keeps its single activity through a personally confirmed success",async()=>{
 const step={step:"push",by:"wtc",approve:"person",gateway:"github_publish",key:"api:push:confirm-owner",reason:"Publish captured commits"};
 const r=repo("api",{unpublished:1,next:step,choices:{actions:[{...step,available:true}]}}),data=collection([r]);
 const previous=githubConfirmResult;
 personal={state:"connected",account:{login:"demo"},generation:"g1"};nextResult={ok:true,result:{performed:false,repo:r}};
 const {host,view,context}=await mount("composer",data);let panel;
 try{
  githubConfirmResult=()=>{
   collections.set(context.props.sessionKey,collection([repo("api",{unpublished:0,pullRequest:{number:7,state:"open",url:"https://example.invalid/pr/7"}})]));
   return {status:"published",created:true,url:"https://example.invalid/pr/7"};
  };
  await clickText(host,"Push");await clickText(host,"Confirm push");
  panel=await mount("panel",collections.get(context.props.sessionKey),undefined,context.props.sessionKey);
  assert.equal([...panel.host.all()].filter(n=>n.className==="wtc-activity-item").length,1);
  assert.match(readable(panel.host),/Done/);assert.doesNotMatch(readable(panel.host),/Awaiting confirm|Publication response received/);
 }finally{panel?.view.dispose();view.dispose();githubConfirmResult=previous;personal=null;nextResult=undefined;}
});

test("tunnel menu keys navigate without starting a tunnel, and Escape restores its trigger", async () => {
  const {host,view}=await mount("composer", collection([repo("api")],[{group:"api/web",repo:"api",kind:"service",label:"preview",state:"ready",url:"http://127.0.0.1:42201"}]));
  const oldWidth=globalThis.innerWidth; globalThis.innerWidth=320;
  try {
    const trigger=[...host.all()].find(n=>n.className==="wtc-tunnel-trigger");
    const menu=[...host.all()].find(n=>n.className==="wtc-tunnel-menu");
    const key=key=>({key,preventDefault(){},stopPropagation(){}});
    calls.length=0;
    trigger.onkeydown(key("ArrowUp")); await settle();
    assert.equal(trigger.ariaExpanded,"true");
    assert.ok(Number.parseFloat(menu.style.left)+252<=308,"placement uses the shown menu width and fits the viewport");
    assert.ok(Number.parseFloat(menu.style.top)+180<=756,"the menu fits above a trigger near the bottom");
    assert.equal(document.activeElement.value,"devtunnel");
    menu.onkeydown(key("ArrowDown")); assert.equal(document.activeElement.value,"gateway");
    menu.onkeydown(key("End")); assert.equal(document.activeElement.value,"devtunnel");
    menu.onkeydown(key("Home")); assert.equal(document.activeElement.value,"gateway");
    menu.onkeydown(key("Escape"));
    assert.equal(trigger.ariaExpanded,"false"); assert.equal(document.activeElement,trigger);
    assert.equal(calls.length,0,"opening, navigation and dismissal never start a provider");
  } finally {globalThis.innerWidth=oldWidth;view.dispose();}
});

test("unchanged composer updates preserve the focused suggestion and its current action", async () => {
 const data=collection([]),key="agent:main:dashboard:hover-start";
 histories.set(key,[{role:"user",content:"Work on the site documentation"}]);
 const {host,view,context}=await mount("composer",data,undefined,key);
 try {
  await settle();
  const start=titled(host,"Ask the conversation to start this collection");assert.ok(start);start.focus();
  for(let i=0;i<30;i++)view.update({...context,props:{...context.props}});
  assert.equal(titled(host,"Ask the conversation to start this collection"),start);
  assert.equal(start.isConnected,true);assert.equal(document.activeElement,start);
  calls.length=0;await start.onclick({preventDefault(){},stopPropagation(){}});await settle();
  assert.equal(calls.filter(c=>c.method==="sessions.send").length,1);
  assert.equal(calls.find(c=>c.method==="sessions.send").key,key);
 }finally{view.dispose();histories.delete(key);}
});

for(const kind of ["composer","panel"])test(`${kind}: unchanged context preserves focused service controls but real status changes update them`,async()=>{
 const data=collection([repo("api")],[{group:"api/web",repo:"api",kind:"service",label:"Preview",state:"idle"}]);
 const {host,view,context}=await mount(kind,data);
 try{
  const starter=()=>[...host.all()].find(n=>n.tagName==="BUTTON"&&/^(Start Preview|Preview: idle\. Start it)$/.test(n.title));
  const start=starter();assert.ok(start);start.focus();
  for(let i=0;i<30;i++)view.update({...context,props:{...context.props}});
  assert.equal(starter(),start);assert.equal(document.activeElement,start);
  data.services[0].state="ready";view.update(context);
  assert.equal(starter(),undefined);assert.ok([...host.all()].some(n=>n.tagName==="BUTTON"&&/Preview.*Stop it/.test(n.title)));
 }finally{view.dispose();}
});

test("a panel focus intent retained in host props does not rebuild its controls",async()=>{
 const {host,view,context}=await mount("panel",collection([repo("api")]),undefined,undefined,{focus:"activity"});
 try{
  const refresh=titled(host,"Refresh");assert.ok(refresh);refresh.focus();
  for(let i=0;i<20;i++)view.update({...context,props:{...context.props}});
  assert.equal(titled(host,"Refresh"),refresh);assert.equal(document.activeElement,refresh);
 }finally{view.dispose();}
});


for(const kind of ["composer","panel"])test(`${kind}: unchanged status still rechecks a newly connected account on its existing cadence`,async()=>{
 const realNow=Date.now;let now=realNow();Date.now=()=>now;personal=null;
 const {view,context}=await mount(kind,collection([repo("api",{unpublished:1})]));
 let options=0;const request=context.host.request;
 context.host.request=(method,params)=>{if(method==="sessions.github.options")options++;return request(method,params);};
 try{
  await settle();personal={state:"connected",account:{login:"demo"},generation:"new"};now+=31000;
  view.update(context);await settle();assert.equal(options,1);
  for(let i=0;i<20;i++)view.update({...context,props:{...context.props}});
  await settle();assert.equal(options,1,"host updates must not bypass the account-check throttle");
 }finally{Date.now=realNow;personal=null;view.dispose();}
});

for(const mode of ["hidden","aborted","workers"])test(`unchanged ${mode} composer updates do not start suggestion reads`,async()=>{
 const realNow=Date.now;let now=realNow();Date.now=()=>now;
 const data=mode==="workers"?{...collection([]),subSessions:[{name:"helper",collection:"test--helper",repos:[repo("api")]}]}:collection([]);
 const {view,context,abort}=await mount("composer",data);
 let history=0;const request=context.host.request;
 context.host.request=(method,params)=>{if(method==="chat.history")history++;return request(method,params);};
 try{
  const next=mode==="hidden"?{...context,presented:false}:context;if(mode==="aborted")abort();
  view.update(next);now+=11000;
  for(let i=0;i<20;i++)view.update(next);
  await settle();assert.equal(history,0);
 }finally{Date.now=realNow;view.dispose();}
});


test("panel feedback renders once and keeps its refreshed stamp for later host updates",async()=>{
 const {host,view,context}=await mount("panel",collection([repo("api")]));
 try{
  const input=[...host.all()].find(n=>n.tagName==="INPUT"&&n.placeholder==="Find or add a repository");
  input.value="";input.onkeydown({key:"Enter",preventDefault(){}});
  assert.match(readable(host),/Pick a repository from the list/);
  const refresh=titled(host,"Refresh");refresh.focus();
  for(let i=0;i<20;i++)view.update({...context,props:{...context.props}});
  assert.equal(titled(host,"Refresh"),refresh);assert.equal(refresh.isConnected,true);assert.equal(document.activeElement,refresh);
 }finally{view.dispose();}
});


for (const kind of ["composer", "panel"]) test(`${kind}: an action refresh from a previous conversation keeps the new controls connected`, async () => {
  const data = collection([repo("api", { behind: 3 })]);
  const { host, view, context } = await mount(kind, data);
  let release;
  const pending = new Promise(resolve => { release = resolve; });
  controlledCollection = params => params.sessionKey === context.props.sessionKey
    ? pending : { ok: true, result: collections.get(params.sessionKey) };
  try {
    const action = [...host.all()].find(n => n.tagName === "BUTTON" && /^3 new on main/.test(n.title)).onclick();
    await settle();
    const nextKey = `agent:main:other:${++sessions}`;
    collections.set(nextKey, collection([repo("web")]));
    view.update({ ...context, props: { sessionKey: nextKey, agentId: "main" } });
    await settle();
    const refreshControl = () => [...host.all()].find(n => n.className === "wtc-refresh-control");
    const refresh = refreshControl();
    refresh.focus();
    release({ ok: true, result: data });
    await action;
    await settle();
    assert.equal(refreshControl(), refresh);
    assert.equal(refresh.isConnected, true);
    assert.equal(document.activeElement, refresh);
  } finally { release({ ok: true, result: data }); controlledCollection = undefined; view.dispose(); }
});

for (const kind of ["composer", "panel"]) test(`native checkout recovery from ${kind} logs errors and refreshes successes`, async () => {
  const r=repo("api",{error:"checkout unavailable",recovery:{state:"restorable",key:"recovery-key",removedAt:1000}});
  const key=`agent:main:restore-${kind}`;
  const {host,view}=await mount(kind,collection([r]),undefined,key);
  try {
    assert.ok(texts(host).some(t=>t.includes("Evicted")));
    const button=()=>[...host.all()].find(n=>n.tagName==="BUTTON"&&n.textContent==="Restore"&&n.title.startsWith("Restore api from"));
    assert.ok(button());
    let finish;restoreResult=new Promise(resolve=>{finish=resolve});
    button().click();await settle();
    assert.ok(buttons(host).includes("Restoring…"));
    assert.equal(calls.at(-1).actionId,"restore-checkout");
    assert.deepEqual(calls.at(-1).payload,{ownerSessionKey:r.sessionKey,key:"recovery-key"});
    finish({ok:false,error:"missing scope: operator.admin"});await settle();await settle();
    assert.ok(readable(host).includes("operator.admin"),"permission failure remains accessible in result details");
    restoreResult=()=>{collections.set(key,collection([repo("api")]));return {ok:true,result:{message:"Checkout restored from its saved snapshot"}}};
    button().click();await settle();await settle();
    assert.ok(!texts(host).some(t=>t==="Evicted"));
    assert.ok(!button(),"the refreshed row no longer offers recovery");
  } finally {view.dispose();restoreResult=undefined;}
});

test("recovery without a recorded snapshot never offers Restore", async () => {
  const {host,view}=await mount("panel",collection([repo("api",{error:"checkout unavailable",recovery:{state:"unavailable",reason:"No recovery snapshot is recorded"}})]));
  try{assert.ok(!buttons(host).includes("Restore"));assert.ok(texts(host).some(t=>t.includes("Snapshot unavailable")));}finally{view.dispose();}
});

test("collapsed restore-all stays sequential, records partial failure and cancels queued work on switch", async () => {
  const missing=name=>repo(name,{error:"checkout unavailable",recovery:{state:"restorable",key:name}});
  const key="agent:main:restore-queue";
  const {host,view}=await mount("composer",collection([missing("api"),missing("web")]),undefined,key);
  const requests=[];let finish;
  restoreResult=params=>{requests.push(params);return new Promise(resolve=>{finish=resolve})};
  try {
    const all=[...host.all()].find(n=>n.tagName==="BUTTON"&&n.textContent==="Restore all");assert.ok(all,"collapsed toolbar exposes recovery");
    all.click();await settle();assert.equal(requests.length,1);
    const primary=[...host.all()].find(n=>n.className?.startsWith("wtc-toolbar-primary"));
    assert.ok(primary.textContent.includes("Restoring 1/2"));
    const queued=[...host.all()].find(n=>n.tagName==="BUTTON"&&n.textContent==="Queued");
    assert.ok(queued?.disabled,"waiting target is visibly queued and disabled");
    await all.onclick();assert.equal(requests.length,1,"stale second click dispatches nothing");
    assert.ok(readable(host).includes("already running"));
    finish({ok:false,error:"Snapshot unavailable"});await settle();await settle();
    assert.equal(requests.length,2,"second target starts only after first result and refresh");
    assert.equal(requests[1].payload.ownerSessionKey,"agent:main:owner:web");
    finish({ok:true,result:{message:"restored"}});await settle();await settle();
    assert.ok(!readable(host).includes("Restoring…"));
    const log=await mount("panel",collections.get(key),undefined,key,{focus:"activity"});
    try{assert.ok(readable(log.host).includes("api: restore failed"));assert.ok(readable(log.host).includes("web: restored"));}finally{log.view.dispose();}
  }finally{view.dispose();restoreResult=undefined;}
  const other=await mount("composer",collection([missing("api"),missing("web")]),undefined,"agent:main:restore-switch");
  requests.length=0;
  restoreResult=params=>{requests.push(params);return new Promise(resolve=>{finish=resolve})};
  try{
    [...other.host.all()].find(n=>n.tagName==="BUTTON"&&n.textContent==="Restore all").click();await settle();
    collections.set("agent:main:replacement",collection([repo("docs")]));
    other.context.props.sessionKey="agent:main:replacement";other.view.update(other.context);await settle();
    finish({ok:true,result:{message:"restored"}});await settle();await settle();
    assert.equal(requests.length,1,"switch cancels the second captured target");
    assert.ok(!readable(other.host).includes("api: restored"),"old result cannot appear in new conversation");
  }finally{other.view.dispose();restoreResult=undefined;}
});

test("evicted rows do not hide a prepared publication in the header", async () => {
  personal={state:"connected",account:{login:"demo"},generation:"g1"};
  const step={step:"push",by:"wtc",approve:"person",gateway:"github_publish",key:"api:push:h1",reason:"Push local commits"};
  const {host,view}=await mount("composer",collection([repo("api",{unpublished:1,next:step,choices:{actions:[{...step,available:true}]}}),repo("web",{error:"checkout unavailable",recovery:{state:"restorable",key:"web"}})]));
  try{
    await [...host.all()].find(n=>n.tagName==="BUTTON"&&/^1 commit to push/.test(n.title)).onclick();
    const primary=[...host.all()].find(n=>n.className?.startsWith("wtc-toolbar-primary"));
    assert.ok(primary.textContent.includes("Review push"),"prepared confirmation takes precedence over recovery");
  }finally{view.dispose();personal=null;}
});

for (const kind of ["composer", "panel"]) test(`mixed recovery and checkout actions stay available in ${kind} menus`, async () => {
  const step={step:"catch-up",by:"wtc",approve:"agent",command:["wtc","catch-up"],key:"api:catch-up:head",reason:"12 new on main"};
  const follow={step:"follow-pr",by:"agent",skill:"wtc-follow",key:"api:follow:head",reason:"Inspect PR progress",available:true};
  const clean=repo("api",{behind:12,next:step,choices:{actions:[{...step,available:true},follow]}});
  const commit={step:"commit",by:"agent",skill:"wtc",key:"web:commit:head",reason:"Save local changes"};
  const dirty=repo("web",{dirty:2,behind:7,next:commit,choices:{actions:[{...commit,available:true}]}});
  const missing=repo("docs",{error:"checkout unavailable",recovery:{state:"restorable",key:"docs"}});
  const {host,view}=await mount(kind,collection([clean,dirty,missing]));
  try{
    const menus=[...host.all()].filter(n=>n.className==="wtc-context-menu");
    const header=menus.find(n=>n.ariaLabel==="Collection actions");
    const item=(menu,label)=>menu.children.find(n=>n.ariaLabel===label);
    assert.ok(!item(header,"Catch up (1)").disabled,"one clean checkout remains eligible beside recovery");
    assert.equal(item(header,"Restore (1)"),undefined,"primary recovery is not duplicated in overflow");
    assert.ok(buttons(host).includes("Restore"));
    const web=menus.find(n=>n.ariaLabel==="Actions for web");
    assert.ok(item(web,"Catch up").disabled);
    assert.match(item(web,"Catch up").textContent,/Save or stash/);
    const api=menus.find(n=>n.ariaLabel==="Actions for api");
    assert.ok(!item(api,"Check PR progress").disabled);
    const trigger=[...host.all()].find(n=>n.ariaLabel==="More actions for api");
    const event=key=>({key,preventDefault(){},stopPropagation(){}});
    calls.length=0;trigger.onkeydown(event("ArrowDown"));
    assert.equal(trigger.ariaExpanded,"true");
    assert.equal(document.activeElement,item(api,"Check PR progress"));
    api.onkeydown(event("Escape"));assert.equal(document.activeElement,trigger);
    assert.equal(calls.length,0,"opening and navigating menus dispatches nothing");
    nextResult={ok:true,result:{performed:true,repo:clean,code:0,json:{summary:"caught up"}}};
    await item(header,"Catch up (1)").onclick(event());for(let i=0;i<6;i++)await settle();
    const invoked=calls.filter(c=>c.actionId==="next");assert.equal(invoked.length,1);
    assert.equal(invoked[0].payload.ownerSessionKey,clean.sessionKey);
    assert.equal(invoked[0].payload.approveCatchUp,true);
    assert.ok(!calls.some(c=>c.actionId==="restore-checkout"),"choosing catch-up does not restore another row");
  }finally{view.dispose();nextResult=undefined;}
});

test("blocked repository alternatives remain visible and cannot dispatch", async()=>{
 const step={step:"follow-pr",by:"agent",skill:"wtc-follow",key:"api:follow:head",reason:"Inspect PR progress",available:true};
 const r=repo("api",{next:step,choices:{actions:[step,{step:"review",by:"agent",skill:"wtc-local-review",key:"api:review:head",available:false,blocked:"Independent review adapter unavailable"}]}});
 const {host,view}=await mount("panel",collection([r]));
 try{const menu=[...host.all()].find(n=>n.ariaLabel==="Actions for api");const blocked=menu.children.find(n=>n.ariaLabel==="Run independent review");assert.ok(blocked.disabled);assert.match(blocked.textContent,/adapter unavailable/);calls.length=0;await blocked.onclick();assert.equal(calls.length,0);}finally{view.dispose();}
});

test("already dispatched repository steps are disabled in the alternative menu", async()=>{
 const step={step:"follow-pr",by:"agent",skill:"wtc-follow",key:"api:follow:head",reason:"Inspect PR progress",available:true};
 const r=repo("api",{next:step,choices:{actions:[step]}});
 const {host,view}=await mount("panel",collection([r]));
 try{
   nextResult={ok:true,result:{performed:false,repo:r}};
   let menu=[...host.all()].find(n=>n.ariaLabel==="Actions for api");
   const main=[...host.all()].find(n=>n.className?.includes("wtc-primary-action")&&n.textContent==="Check PR progress");
   await main.onclick();for(let i=0;i<6;i++)await settle();
   menu=[...host.all()].find(n=>n.ariaLabel==="Actions for api");const sent=menu.children.find(n=>n.ariaLabel==="Check PR progress");
   assert.ok(sent.disabled);assert.match(sent.textContent,/Already sent/);
   calls.length=0;await sent.onclick();assert.equal(calls.length,0);
 }finally{view.dispose();nextResult=undefined;}
});

for(const kind of ["composer","panel"]) test(`responsive ${kind} actions surface available choices and keep overflow left`,async()=>{
 const observers=[];globalThis.ResizeObserver=class{constructor(fn){this.fn=fn;observers.push(this)}observe(){}disconnect(){}};
 const primary={step:"follow-pr",by:"agent",skill:"wtc-follow",key:"api:follow",reason:"Inspect current PR",available:true};
 const fix={step:"fix-checks",by:"agent",skill:"wtc",key:"api:fix",reason:"Fix checks",available:true};
 const review={step:"address-review",by:"agent",skill:"wtc",key:"api:review",reason:"Address review",available:true};
 const blocked={step:"merge-pr",by:"person",key:"api:merge",available:false,blocked:"Approval required"};
 let view;
 try{
  const mounted=await mount(kind,collection([repo("api",{next:primary,choices:{actions:[primary,fix,review,blocked]}})]));view=mounted.view;const host=mounted.host;
  const resize=width=>{for(const o of observers)o.fn([{contentRect:{width}}])};
  resize(320);let row=[...host.all()].find(n=>n.className==="wtc-next-step");assert.equal(row.children[0].className,"wtc-action-menu");assert.equal(row.children.at(-1).textContent,"Check PR progress");assert.equal(row.children.filter(n=>n.className?.includes("wtc-inline-action")).length,0);
  resize(520);row=[...host.all()].find(n=>n.className==="wtc-next-step");assert.equal(row.children.filter(n=>n.className?.includes("wtc-inline-action")).length,1);assert.equal(row.children.at(-2).textContent,"Fix checks");
  resize(1100);row=[...host.all()].find(n=>n.className==="wtc-next-step");assert.equal(row.children.filter(n=>n.className?.includes("wtc-inline-action")).length,2);assert.deepEqual(row.children.slice(1,-1).map(n=>n.textContent),["Address review","Fix checks"]);assert.equal(row.children.at(-1).textContent,"Check PR progress");
  const menu=row.children[0].children.find(n=>n.role==="menu");assert.deepEqual(menu.children.map(n=>n.ariaLabel),["Open pull request"]);assert.ok(menu.children[0].disabled);
  calls.length=0;resize(320);assert.equal(calls.length,0,"resizing never dispatches an action");
 }finally{view?.dispose();delete globalThis.ResizeObserver;}
});

const localChangeButton = host => [...host.all()].find(n => n.tagName === "BUTTON" && n.className?.includes("wtc-local-change") && !n.className.includes("wtc-column-commit"));
const commitColumnButton = host => [...host.all()].find(n => n.className?.includes("wtc-column-commit"));

test("changed-files cells share pending feedback, reject duplicates and require observed commits for success", async () => {
  const r = repo("site", {dirty:1,unpublished:0});
  const card = await mount("composer", collection([r]));
  const panel = await mount("panel", collection([r]), undefined, card.context.props.sessionKey);
  let release;
  try {
    controlledSend = new Promise(resolve => {release=resolve;});calls.length=0;
    const work = localChangeButton(card.host).onclick({preventDefault(){},stopPropagation(){}});
    await settle();
    assert.equal(calls.filter(c=>c.method==="sessions.send").length,1);
    assert.equal(localChangeButton(panel.host).attributes["aria-busy"],"true","other surface sees the same pending cell");
    await localChangeButton(panel.host).onclick({preventDefault(){},stopPropagation(){}});
    assert.equal(calls.filter(c=>c.method==="sessions.send").length,1,"pending click does not resend");
    release({runId:"commit-run"});await work;await settle();
    assert.match(localChangeButton(card.host).title,/Sent to conversation/);
    assert.doesNotMatch(localChangeButton(card.host).textContent,/✓/,"handoff receipt is not success");
    collections.set(card.context.props.sessionKey,collection([repo("site",{dirty:0,unpublished:1,ahead:1})]));
    await [...card.host.all()].find(n=>n.tagName==="BUTTON"&&(n.title==="Refresh"||/^Updated .*Refresh/.test(n.title))).onclick({preventDefault(){},stopPropagation(){}});await settle();
    assert.match(localChangeButton(card.host).textContent,/✓/);
    assert.match(localChangeButton(panel.host).textContent,/✓/);
    assert.equal(calls.some(c=>/publish|confirm/.test(c.method??"")),false,"commit does not publish");
  } finally {release?.({runId:"cleanup"});controlledSend=undefined;card.view.dispose();panel.view.dispose();}
});

test("a cleared working tree without new local commits does not earn a commit checkmark", async () => {
 const r=repo("site",{dirty:1,unpublished:0});const {host,view,context}=await mount("panel",collection([r]));
 try {
  await localChangeButton(host).onclick();
  collections.set(context.props.sessionKey,collection([repo("site",{dirty:0,unpublished:0})]));
  await [...host.all()].find(n=>n.tagName==="BUTTON"&&(n.title==="Refresh"||/^Updated .*Refresh/.test(n.title))).onclick({preventDefault(){},stopPropagation(){}});await settle();
  assert.equal(localChangeButton(host),undefined);
 }finally{view.dispose();}
});

test("the changes-column control sends one bounded group request and excludes clean repositories", async () => {
 const {host,view}=await mount("panel",collection([repo("api",{dirty:2}),repo("site",{dirty:1}),repo("clean")]));
 try {
  calls.length=0;await commitColumnButton(host).onclick();await settle();
  const sent=calls.filter(c=>c.method==="sessions.send");assert.equal(sent.length,1);
  assert.match(sent[0].message,/eligible repositories only: api, site/);
  assert.doesNotMatch(sent[0].message,/clean/);
  assert.match(sent[0].message,/Publication and follow-up actions are separate/);
  assert.equal(commitColumnButton(host).disabled,true);
 }finally{view.dispose();}
});

test("a blocked commit stays a read-only fact and a failed request offers a retry in the same cell", async () => {
 const step={step:"commit",by:"agent",key:"blocked-commit",reason:"Save changes",available:false,blocked:"Owner is busy"};
 const blocked=await mount("panel",collection([repo("blocked",{dirty:1,next:step,choices:{actions:[step]}})]));
 try {calls.length=0;assert.equal(localChangeButton(blocked.host).disabled,true);await localChangeButton(blocked.host).onclick();assert.equal(calls.length,0);assert.match(localChangeButton(blocked.host).title,/Owner is busy/);}finally{blocked.view.dispose();}
 const active=await mount("panel",collection([repo("site",{dirty:1})]));
 try {
  failing.add("sessions.send");await localChangeButton(active.host).onclick();await settle();
  assert.match(localChangeButton(active.host).title,/Commit request failed/);assert.equal(localChangeButton(active.host).disabled,false);
  failing.delete("sessions.send");calls.length=0;await localChangeButton(active.host).onclick();await settle();assert.equal(calls.filter(c=>c.method==="sessions.send").length,1);
 }finally{failing.delete("sessions.send");active.view.dispose();}
});

test("partial commits settle the fact feedback while preserving the remaining dirty count", async () => {
 const {host,view,context}=await mount("panel",collection([repo("site",{dirty:3,unpublished:0})]));
 try {
  await localChangeButton(host).onclick();
  collections.set(context.props.sessionKey,collection([repo("site",{dirty:1,unpublished:1})]));
  await titled(host,"Refresh").onclick({preventDefault(){},stopPropagation(){}});await settle();
  assert.doesNotMatch(localChangeButton(host).textContent,/✓/);
  assert.match(localChangeButton(host).textContent,/1/);
  assert.equal(localChangeButton(host).attributes["aria-busy"],undefined);
  assert.equal(localChangeButton(host).disabled,false);
 }finally{view.dispose();}
});

test("a pending commit times out to an honest unknown result instead of spinning forever", async () => {
 const set=globalThis.setTimeout;let expire;
 globalThis.setTimeout=(fn,delay,...args)=>delay===90000?(expire=fn,{unref(){}}):set(fn,delay,...args);
 const {host,view}=await mount("panel",collection([repo("site",{dirty:1})]));
 try {
  await localChangeButton(host).onclick();assert.ok(expire);
  expire();await settle();
  assert.match(localChangeButton(host).textContent,/\?/);
  assert.match(localChangeButton(host).title,/No commit observed yet/i);
  calls.length=0;await localChangeButton(host).onclick();await settle();
  assert.equal(calls.filter(c=>c.method==="sessions.send").length,0,"unknown result opens details rather than blindly resending");
 }finally{globalThis.setTimeout=set;view.dispose();}
});

test("sparse facts use one line at intermediate widths and observed columns stay reserved", async () => {
 const observers=[];globalThis.ResizeObserver=class{constructor(fn){this.fn=fn;observers.push(this)}observe(){}disconnect(){}};
 try { for(const kind of ["composer","panel"]) {
  const start=observers.length;const data=collection([repo("agent-harness"),repo("site",{dirty:1})]);
  const {host,view,context}=await mount(kind,data);
  const resize=width=>observers.slice(start).forEach(o=>o.fn([{contentRect:{width}}]));
  const refresh=async()=>{await [...host.all()].find(n=>n.tagName==="BUTTON"&&(n.title==="Refresh"||/^Updated .*Refresh/.test(n.title))).onclick({preventDefault(){},stopPropagation(){}});await settle();};
  try {
   resize(390);assert.deepEqual(tables(host)[0][0],["Repository","Branch","Changes","Next"]);
   assert.equal([...host.all()].some(n=>n.className==="wtc-repo-facts"),false);
   const tracks=[...host.all()].find(n=>n.className==="wtc-change-slots").style.cssText;
   collections.set(context.props.sessionKey,collection([repo("agent-harness"),repo("site",{dirty:0})]));await refresh();
   assert.equal([...host.all()].find(n=>n.className==="wtc-change-slots").style.cssText,tracks,"clearing facts does not collapse the observed columns");
   collections.set(context.props.sessionKey,busy);await refresh();
   assert.deepEqual(tables(host)[0][0],["Repository","Next"],"a PR and more facts need folding at the same width");
   resize(560);assert.deepEqual(tables(host)[0][0],["Repository","Branch","Changes","Pull request","Next"]);
   calls.length=0;resize(390);resize(560);assert.equal(calls.length,0,"layout changes never dispatch actions");
  }finally{view.dispose();}
 }}finally{delete globalThis.ResizeObserver;}
});

test("pending commit choices do not create a second disabled action menu", async () => {
 const step={step:"commit",by:"agent",skill:"wtc",key:"site:commit",reason:"Save changes",available:true};
 const r=repo("site",{dirty:1,next:step,choices:{actions:[step]}});const {host,view}=await mount("panel",collection([r]));
 let release;try {
  nextResult={ok:true,result:{performed:false,repo:r}};controlledSend=new Promise(resolve=>release=resolve);
  const running=localChangeButton(host).onclick();await settle();
  assert.equal([...host.all()].filter(n=>n.className==="wtc-action-menu"&&/Actions for site/.test(n.textContent)).length,0);
  release({runId:"commit"});await running;
 }finally{release?.({runId:"cleanup"});controlledSend=undefined;nextResult=undefined;view.dispose();}
});

test("a planner-routed commit can open its unconfirmed result even though redispatch is blocked", async () => {
 const set=globalThis.setTimeout;let expire;
 globalThis.setTimeout=(fn,delay,...args)=>delay===90000?(expire=fn,{unref(){}}):set(fn,delay,...args);
 const step={step:"commit",by:"agent",skill:"wtc",key:"site:commit",reason:"Save changes",available:true};
 const r=repo("site",{dirty:1,next:step,choices:{actions:[step]}});const {host,view}=await mount("panel",collection([r]));
 try {
  nextResult={ok:true,result:{performed:false,repo:r}};
  await localChangeButton(host).onclick();expire();await settle();
  assert.equal(localChangeButton(host).disabled,false,"reading an outcome is distinct from redispatching");
  calls.length=0;await localChangeButton(host).onclick();await settle();
  assert.equal(calls.some(c=>c.method==="sessions.send"||c.actionId==="next"),false);
 }finally{nextResult=undefined;globalThis.setTimeout=set;view.dispose();}
});

for (const kind of ["composer", "panel"]) test(`${kind} catch-up focus previews only eligible rows without dispatch`, async () => {
  const step = {step:"catch-up",by:"wtc",approve:"agent",command:["wtc","catch-up"],key:"preview-catch",reason:"Preserve edits",available:true};
  const data = collection([repo("api",{behind:2,next:step,choices:{actions:[step]}}),repo("site",{behind:5,dirty:1,next:step,choices:{actions:[step,{step:"commit",by:"agent",key:"preview-commit",available:true}]}}),repo("blocked",{behind:3,dirty:1,next:{step:"commit",by:"agent",key:"blocked-commit"},choices:{actions:[{step:"commit",by:"agent",key:"blocked-commit",available:true}]}})]);
  const {host,view} = await mount(kind,data);
  try {
    const button = [...host.all()].find(n=>n.tagName==="BUTTON"&&/^Catch up all 2 eligible/.test(n.title));
    assert.ok(button);calls.length=0;
    for(const fn of button.listeners.focus??[])fn();
    const cells=[...host.all()].filter(n=>n.attributes["data-repo-session"]);
    assert.ok(cells.length>0);
    for(const cell of cells)assert.equal(cell.attributes["data-action-preview"],String(cell.attributes["data-repo-session"]!==data.repos[2].sessionKey));
    assert.equal(calls.length,0,"preview is read-only");
    for(const fn of button.listeners.blur??[])fn();
    assert.ok(cells.every(n=>n.attributes["data-action-preview"]==="false"));
  }finally{view.dispose();}
});

test("unconfirmed action details offer one diagnostic handoff without retrying", async()=>{
  const set=globalThis.setTimeout;let expire;
  globalThis.setTimeout=(fn,delay,...args)=>delay===90000?(expire=fn,{unref(){}}):set(fn,delay,...args);
  const {host,view}=await mount("panel",collection([repo("site",{dirty:1})]));
  try {
    await localChangeButton(host).onclick();expire();await settle();calls.length=0;
    const help=[...host.all()].find(n=>n.tagName==="BUTTON"&&n.textContent==="Ask agent for help");assert.ok(help);
    await help.onclick();await settle();
    const sent=calls.filter(c=>c.method==="sessions.send");assert.equal(sent.length,1);assert.match(sent[0].message,/diagnosis, not a retry/);assert.match(sent[0].message,/gateway worktree or the sandbox/);
    assert.equal(calls.some(c=>c.actionId==="catch-up"||c.actionId==="next"),false);
    const requested=[...host.all()].find(n=>n.tagName==="BUTTON"&&n.textContent==="Help requested");assert.ok(requested?.disabled);
  }finally{globalThis.setTimeout=set;view.dispose();}
});
