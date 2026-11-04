// The gateway side of the plugin, where it needs no gateway.
import { test } from "node:test";
import assert from "node:assert/strict";
import plugin, { agentGitIdentity, collectionFolder, decideProjects, matchProjects, openaiDecisions, pullRequestChanges, pullRequestWatch, versionWarning, wtcVersionOf } from "../index.js";
import { mkdirSync, mkdtempSync } from "node:fs";
import os from "node:os";
import path from "node:path";

for (const host of ["legacy", "async-only", "both"]) test(`next actions recheck membership, state and approvals on ${host} hosts`, async () => {
  const actions = new Map();
  const owner = "agent:main:repo:api";
  const entry = { parentSessionKey: "agent:main:c", projectId: "api", label: "test api", worktree: { branch: "openclaw/test" }, workspaceDir: "/worktrees/api" };
  const commands = [];
  const reads = {
    listSessionEntries: () => [{ sessionKey: "agent:main:c", entry: {} }, { sessionKey: owner, entry }],
    getSessionEntry: ({ sessionKey }) => sessionKey === owner ? entry : undefined,
  };
  if (host !== "legacy") for (const name of ["listSessionEntries", "getSessionEntry"]) {
    const read = reads[name];
    reads[`${name}Async`] = async (params) => { await new Promise(setImmediate); return read(params); };
    if (host === "async-only") delete reads[name];
    else reads[name] = () => { throw new Error("deprecated read must not be used"); };
  }

  const step = { step: "return-to-tip", by: "wtc", key: "api:tip:abc", reason: "merged", command: ["wtc", "catch-up"] };
  let row = { path: entry.workspaceDir, repo: "api", next: step };
  let release;
  let block = false;
  plugin.register({
    pluginConfig: {},
    runtime: {
      state: { resolveStateDir: () => mkdtempSync(path.join(os.tmpdir(), "wtc-next-")) },
      agent: { session: reads },
      system: { runCommandWithTimeout: async (argv, opts) => {
        if (argv[1] === "status") return { code: 0, stdout: JSON.stringify({ test: { repos: [row] } }) };
        if (argv[1] === "--version") return { code: 0, stdout: "wtc version dev" };
        commands.push({ argv, opts });
        if (block) await new Promise((resolve) => { release = resolve; });
        return { code: 0, stdout: JSON.stringify({ summary: "returned to tip" }), stderr: "" };
      } },
    },
    session: { controls: { registerSessionAction: (a) => actions.set(a.id, a), registerControlUiDescriptor() {} } },
    registerHttpRoute() {}, on() {}, logger: {},
  });
  const act = (payload = {}, sessionKey = "agent:main:c") => actions.get("next").handler({ agentId: "main", sessionKey, payload: { ownerSessionKey: owner, key: step.key, ...payload } });
  assert.deepEqual(actions.get("next").requiredScopes, ["operator.write"]);
  assert.equal((await act({}, "agent:main:other")).ok, false);
  assert.equal((await act({ key: "old" })).ok, false);
  row = { ...row, next: null };
  assert.equal((await act()).ok, false, "a newly dirty checkout has no next step");
  row = { ...row, next: { ...step, approve: "agent" } };
  assert.equal((await act()).result.performed, false);
  row.next = { ...step, approve: "person", gateway: "github_publish" };
  assert.equal((await act()).result.performed, false);
  row.next = { ...step, command: ["sh", "-c", "untrusted"] };
  assert.equal((await act()).result.performed, false);
  assert.equal(commands.length, 0);
  const alternative = { step: "review", by: "agent", skill: "wtc-local-review", key: "review:exact-head", available: true };
  row.choices = { actions: [alternative, { ...alternative, key: step.key, available: false, blocked: "Head changed" }] };
  assert.equal((await act()).ok, false, "blocked choices cannot fall back to legacy next");
  const selected = await act({ key: alternative.key });
  assert.equal(selected.result.repo.next.key, alternative.key);
  assert.equal(selected.result.performed, false);
  assert.equal(commands.length, 0, "an alternative skill is a handoff, not a shell command");
  delete row.choices;
  row.next = step;
  block = true;
  const first = act({ command: ["sh", "-c", "ignored"] });
  while (!release) await new Promise((resolve) => setImmediate(resolve));
  assert.equal((await act()).ok, false, "a double click cannot start another execution");
  release();
  const result = await first;
  assert.equal(result.result.performed, true);
  assert.deepEqual(commands[0].argv, ["wtc", "catch-up", "--json"]);
  assert.equal(commands[0].opts.cwd, entry.workspaceDir);
  assert.equal(commands[0].opts.env.OPENCLAW_SHELL, "exec");
  block = false;
  row.next = { ...step, step: "catch-up", approve: "agent" };
  assert.equal((await act()).result.performed, false, "automatic callers still leave the decision to the agent");
  assert.equal((await act({ approveCatchUp: true })).result.performed, true, "a person's explicit catch-up click is sufficient");
  row.next = { ...step, approve: "person", gateway: "github_publish" };
  assert.equal((await act({ approveCatchUp: true })).result.performed, false, "catch-up permission cannot authorize publication");

});

test("the panel warns when the gateway's wtc is not the release the plugin came from", () => {
  assert.equal(versionWarning("0.1.42", "wtc version 0.1.42\n"), null);
  assert.match(versionWarning("0.1.42", "wtc version 0.1.43\n"), /the taba 0\.1\.42 plugin.*runs wtc 0\.1\.43.*wtc openclaw install/);
  assert.match(versionWarning("0.1.0", "wtc version v0.1.43"), /0\.1\.43/);
  // Dev builds on either side, a missing wtc and an unknown plugin version
  // say nothing.
  assert.equal(versionWarning("0.1.42", "wtc version dev"), null);
  assert.equal(versionWarning("0.1.42", "wtc version 0.1.43 (dev build, in place of 0.1.42)"), null);
  assert.equal(versionWarning("0.0.0-dev", "wtc version 0.1.43"), null);
  assert.equal(versionWarning("0.1.42", ""), null);
  assert.equal(versionWarning(null, "wtc version 0.1.43"), null);
});

test("the collection names the gateway's wtc version", () => {
  assert.equal(wtcVersionOf("wtc version 0.1.49\n"), "0.1.49");
  assert.equal(wtcVersionOf("wtc version 0.1.50-dev+gb8e3804.20261023.0644"), "0.1.50-dev+gb8e3804.20261023.0644");
  assert.equal(wtcVersionOf(""), null);
  assert.equal(wtcVersionOf(null), null);
});

test("overlapping collection requests share one wtc --version", async () => {
  const actions = new Map();
  let asked = 0;
  plugin.register({
    pluginConfig: {},
    runtime: {
      state: { resolveStateDir: () => "/nonexistent" },
      agent: { session: { listSessionEntries: () => [], getSessionEntry: async () => undefined } },
      system: {
        runCommandWithTimeout: async (argv) => {
          if (argv[1] === "--version") asked += 1;
          await new Promise((resolve) => setTimeout(resolve, 20));
          return { code: 0, stdout: "wtc version 99.0.0\n", stderr: "" };
        },
      },
    },
    session: { controls: { registerSessionAction: (a) => actions.set(a.id, a), registerControlUiDescriptor() {} } },
    registerHttpRoute() {},
    on() {},
    logger: {},
  });
  const collection = () => actions.get("collection").handler({ sessionKey: "s", agentId: "main", payload: {} });
  const results = await Promise.all([collection(), collection(), collection()]);
  assert.equal(asked, 1);
  for (const r of results) assert.match(r.result.warnings[0], /runs wtc 99\.0\.0/);
  assert.equal(results[0].result.version.wtc, "99.0.0");
});

test("a new conversation is a collection of its own, not its parent's", async () => {
  const actions = new Map();
  const entries = [
    { sessionKey: "agent:main:main", entry: { label: "[OpenClaw heartbeat poll]" } },
    { sessionKey: "agent:main:repo:api", entry: { parentSessionKey: "agent:main:main", projectId: "api", worktree: { id: "w1", branch: "openclaw/old" }, workspaceDir: "/worktrees/w1" } },
    { sessionKey: "agent:main:dashboard:new", entry: { parentSessionKey: "agent:main:main" } },
  ];
  plugin.register({
    pluginConfig: {},
    runtime: {
      state: { resolveStateDir: () => "/nonexistent" },
      agent: { session: { listSessionEntries: () => entries, getSessionEntry: async () => undefined } },
      system: { runCommandWithTimeout: async () => ({ code: 1, stdout: "", stderr: "" }) },
    },
    session: { controls: { registerSessionAction: (a) => actions.set(a.id, a), registerControlUiDescriptor() {} } },
    registerHttpRoute() {},
    on() {},
    logger: {},
  });
  const collection = async (sessionKey) => (await actions.get("collection").handler({ sessionKey, agentId: "main", payload: {} })).result;
  const fresh = await collection("agent:main:dashboard:new");
  assert.deepEqual(fresh.collection, { sessionKey: "agent:main:dashboard:new", label: null });
  assert.equal(fresh.repos.length, 0);
  // A repository's own session still shows the collection it belongs to.
  assert.equal((await collection("agent:main:repo:api")).collection.sessionKey, "agent:main:main");
});

test("the branches of a gateway project are read outside its clone", async () => {
  const { mkdtempSync, mkdirSync } = await import("node:fs");
  const { tmpdir } = await import("node:os");
  const path = await import("node:path");
  const state = mkdtempSync(path.join(tmpdir(), "wtc-plugin-"));
  mkdirSync(path.join(state, "repos", "docs", ".git"), { recursive: true });
  const actions = new Map();
  const calls = [];
  let origin = "https://github.com/example-org/docs.git";
  plugin.register({
    pluginConfig: {},
    runtime: {
      state: { resolveStateDir: () => state },
      agent: { session: { listSessionEntries: () => [], getSessionEntry: async () => undefined } },
      system: {
        runCommandWithTimeout: async (argv) => {
          calls.push(argv);
          if (argv.includes("--get")) return { code: 0, stdout: origin + "\n", stderr: "" };
          return { code: 0, stdout: "ref: refs/heads/main\tHEAD\n" + "a".repeat(40) + "\trefs/heads/main\n", stderr: "" };
        },
      },
    },
    session: { controls: { registerSessionAction: (a) => actions.set(a.id, a), registerControlUiDescriptor() {} } },
    registerHttpRoute() {},
    on() {},
    logger: {},
  });
  const branches = (payload) => actions.get("repo-branches").handler({ sessionKey: "s", agentId: "main", payload });
  const r = await branches({ project: "docs" });
  assert.deepEqual(r.result, { defaultBranch: "main", branches: ["main"] });
  const lsRemote = calls.find((argv) => argv.includes("ls-remote"));
  assert.ok(!lsRemote.includes("-C"), "ls-remote runs in no repository");
  assert.ok(lsRemote.includes(origin), "with the URL read from the clone");
  // A clone whose origin was pointed elsewhere is not read.
  origin = "ext::sh -c touch% /tmp/pwned";
  calls.length = 0;
  const refused = await branches({ project: "docs" });
  assert.equal(refused.ok, false);
  assert.ok(!calls.some((argv) => argv.includes("ls-remote")));
});

test("a collection's folder is where wtc made it", () => {
  const state = mkdtempSync(path.join(os.tmpdir(), "wtc-state-"));
  assert.equal(collectionFolder(state, "1-x"), null);
  for (const dir of ["wtc/platform/1-x", "wtc/ad-hoc/2-x", "wtc-collections/3-x"]) mkdirSync(path.join(state, dir), { recursive: true });
  assert.equal(collectionFolder(state, "1-x"), path.join(state, "wtc/platform/1-x"));
  assert.equal(collectionFolder(state, "2-x"), path.join(state, "wtc/ad-hoc/2-x"));
  assert.equal(collectionFolder(state, "3-x"), path.join(state, "wtc-collections/3-x"));
});

test("a service action runs in the collection's folder, and says when there is none yet", async () => {
  const state = mkdtempSync(path.join(os.tmpdir(), "wtc-state-"));
  const actions = new Map();
  const cwds = [];
  const entries = [
    { sessionKey: "agent:main:c", entry: {} },
    { sessionKey: "agent:main:repo:api", entry: { parentSessionKey: "agent:main:c", projectId: "api", worktree: { id: "w1", branch: "openclaw/9-x" }, workspaceDir: "/worktrees/w1" } },
  ];
  plugin.register({
    pluginConfig: {},
    runtime: {
      state: { resolveStateDir: () => state },
      agent: { session: { listSessionEntries: () => entries, getSessionEntry: async () => undefined } },
      system: { runCommandWithTimeout: async (argv, options) => { cwds.push(options.cwd); return { code: 0, stdout: "", stderr: "" }; } },
    },
    session: { controls: { registerSessionAction: (a) => actions.set(a.id, a), registerControlUiDescriptor() {} } },
    registerHttpRoute() {},
    on() {},
    logger: {},
  });
  const up = () => actions.get("svc").handler({ sessionKey: "agent:main:c", agentId: "main", payload: { op: "up" } });
  assert.match((await up()).error, /no folder yet/);
  mkdirSync(path.join(state, "wtc/platform/9-x"), { recursive: true });
  assert.equal((await up()).ok, true);
  assert.equal(cwds.at(-1), path.join(state, "wtc/platform/9-x"));
});

test("service logs are read-only, collection-scoped, bounded and require an explicit group", async () => {
  const state=mkdtempSync(path.join(os.tmpdir(),"wtc-log-read-"));
  mkdirSync(path.join(state,"wtc/platform/log-test"),{recursive:true});
  const actions=new Map(),commands=[];
  const entries=[{sessionKey:"agent:main:repo:api",entry:{parentSessionKey:"agent:main:c",projectId:"api",worktree:{branch:"openclaw/log-test"},workspaceDir:"/worktrees/api"}}];
  // Put a worker first: log reads must use the collection shown in Services.
  entries[0].entry.label="log-test api";
  entries.unshift({sessionKey:"agent:main:worker:api",entry:{parentSessionKey:"agent:main:c",projectId:"api",worktree:{branch:"openclaw/log-test--worker"},workspaceDir:"/worktrees/worker"}});
  mkdirSync(path.join(state,"wtc/platform/log-test--worker"),{recursive:true});
  let result={code:0,stdout:"x".repeat(70000)+"recent output",stderr:""};
  plugin.register({pluginConfig:{},runtime:{state:{resolveStateDir:()=>state},agent:{session:{listSessionEntries:()=>entries,getSessionEntry:async()=>undefined}},system:{runCommandWithTimeout:async(argv,options)=>{commands.push({argv,options});return result;}}},session:{controls:{registerSessionAction:a=>actions.set(a.id,a),registerControlUiDescriptor(){}}},registerHttpRoute(){},on(){},logger:{}});
  const action=actions.get("svc-logs");assert.deepEqual(action.requiredScopes,["operator.read"]);
  const request=(group,more={})=>action.handler({sessionKey:"agent:main:c",agentId:"main",payload:{group,op:"up"},...more});
  assert.equal((await request("")).ok,false);
  assert.equal((await request("../web")).ok,false);
  assert.equal((await request("api/web;touch marker")).ok,false);
  assert.equal((await request("api/web",{sessionKey:"agent:main:other"})).ok,false);
  assert.equal(commands.length,0);
  const out=await request("api/web");assert.equal(out.ok,true);assert.equal(out.result.logs.length,65536);assert.ok(out.result.logs.endsWith("recent output"));assert.equal(out.result.truncated,true);
  assert.deepEqual(commands[0].argv,["wtc","svc","logs","api/web"]);assert.equal(commands[0].options.cwd,path.join(state,"wtc/platform/log-test"));
  await request("api/web/server");assert.deepEqual(commands.at(-1).argv,["wtc","svc","logs","api/web/server"]);
  assert.equal((await request("api/web/..")).ok,false);
  for (const error of ["wtc: runtime is not configured", "wtc: lstat /synthetic/logs/web.log: no such file or directory"]) {
    result={code:1,stdout:"",stderr:error};assert.deepEqual((await request("api/web")).result,{logs:"",truncated:false});
  }
  result={code:1,stdout:"",stderr:"Logs are unavailable"};assert.match((await request("api/web")).error,/Logs are unavailable/);
});

test("what changed on a pull request, one line each", () => {
  const pr = { number: 2, url: "https://github.com/o/site/pull/2", state: "open", draft: true, checks: "pending", failed: 0, review: "", mergeable: "mergeable" };
  const changes = (prev, cur) => pullRequestChanges("site", prev, cur);
  // The first look has nothing to compare with; a look without one is no news.
  assert.deepEqual(changes(undefined, pr), []);
  assert.deepEqual(changes(null, null), []);
  assert.deepEqual(changes(null, pr), ["site: pull request #2 (https://github.com/o/site/pull/2) opened as a draft."]);
  assert.deepEqual(changes(pr, pr), []);
  assert.deepEqual(changes(pr, { ...pr, draft: false, checks: "failure", failed: 2, review: "changes_requested", mergeable: "conflicting" }), [
    "site: pull request #2 (https://github.com/o/site/pull/2) is ready for review.",
    "site: checks failed on #2 (https://github.com/o/site/pull/2) (2 failed).",
    "site: a review requested changes on #2 (https://github.com/o/site/pull/2).",
    "site: pull request #2 (https://github.com/o/site/pull/2) conflicts with its base.",
  ]);
  const ready = { ...pr, draft: false };
  assert.deepEqual(changes(ready, pr), ["site: pull request #2 (https://github.com/o/site/pull/2) went back to draft."]);
  assert.deepEqual(changes(pr, { ...pr, review: "approved" }), ["site: pull request #2 (https://github.com/o/site/pull/2) was approved."]);
  assert.deepEqual(changes({ ...pr, state: "closed" }, pr), ["site: pull request #2 (https://github.com/o/site/pull/2) was reopened."]);
  assert.deepEqual(changes({ ...pr, checks: "failure" }, { ...pr, checks: "success" }), ["site: checks passed on #2 (https://github.com/o/site/pull/2) again."]);
  // Merged, the rest of it is no longer news.
  assert.deepEqual(changes(pr, { ...pr, state: "merged", checks: "failure" }), ["site: pull request #2 (https://github.com/o/site/pull/2) was merged."]);
  assert.deepEqual(changes(pr, { ...pr, state: "closed" }), ["site: pull request #2 (https://github.com/o/site/pull/2) was closed without merging."]);
  assert.deepEqual(changes(pr, { ...pr, number: 3, url: "u3", draft: false }), ["site: pull request #3 (u3) opened."]);
});

test("the pull requests seen are kept across restarts, and a failed look is no news", () => {
  const file = path.join(mkdtempSync(path.join(os.tmpdir(), "wtc-watch-")), "pull-requests.json");
  const pr = { number: 2, url: "u", state: "open", draft: true, checks: "none" };
  const watch = pullRequestWatch(file);
  assert.deepEqual(watch.observe("main", "agent:main:c", [{ path: "/w/site", repo: "site", pullRequest: null }]), []);
  assert.deepEqual(pullRequestWatch(file).observe("main", "agent:main:c", [{ path: "/w/site", repo: "site", pullRequest: pr }]), ["site: pull request #2 (u) opened as a draft."]);
  const again = pullRequestWatch(file);
  assert.deepEqual(again.observe("main", "agent:main:c", [{ path: "/w/site", repo: "site", pullRequest: null }]), []);
  assert.deepEqual(again.open().map((c) => c.sessionKey), ["agent:main:c"]);
  assert.deepEqual(again.observe("main", "agent:main:c", [{ path: "/w/site", repo: "site", pullRequest: { ...pr, state: "merged" } }]), ["site: pull request #2 (u) was merged."]);
  assert.deepEqual(again.open(), []);
});

test("the collection's conversation hears what changed, and is woken for it", async () => {
  const state = mkdtempSync(path.join(os.tmpdir(), "wtc-state-"));
  const actions = new Map();
  const services = [];
  const events = [];
  const wakes = [];
  let pr = null;
  let readFailure = false;
  const warnings = [];
  const entries = [
    { sessionKey: "agent:main:c", entry: {} },
    { sessionKey: "agent:main:repo:site", entry: { parentSessionKey: "agent:main:c", projectId: "site", worktree: { id: "w1", branch: "openclaw/9-x" }, workspaceDir: "/worktrees/w1" } },
  ];
  const register = (pluginConfig) => plugin.register({
    pluginConfig,
    runtime: {
      state: { resolveStateDir: () => state },
      agent: { session: { listSessionEntries: () => { throw new Error("deprecated list called"); }, listSessionEntriesAsync: async () => {
        await new Promise(setImmediate);
        if (readFailure) throw new Error("session store unavailable");
        return entries;
      }, getSessionEntry: async () => undefined } },
      system: {
        runCommandWithTimeout: async (argv) => argv[1] === "status"
          ? { code: 0, stdout: JSON.stringify({ "9-x": { repos: [{ path: "/worktrees/w1", repo: "site", pullRequest: pr }] } }), stderr: "" }
          : { code: 0, stdout: "", stderr: "" },
        enqueueSystemEvent: (text, options) => events.push({ text, ...options }),
        requestHeartbeat: (options) => wakes.push(options),
      },
    },
    session: { controls: { registerSessionAction: (a) => actions.set(a.id, a), registerControlUiDescriptor() {} } },
    registerService: (s) => services.push(s),
    registerHttpRoute() {},
    on() {},
    logger: { warn: (message) => warnings.push(message) },
  });
  register({});
  const look = () => actions.get("collection").handler({ sessionKey: "agent:main:repo:site", agentId: "main", payload: {} });
  await look();
  pr = { number: 2, url: "u", state: "open", draft: true, checks: "none" };
  await look();
  assert.deepEqual(events, [{ text: "wtc: pull request news for this collection\n- site: pull request #2 (u) opened as a draft.\nAct on it with the wtc-follow skill, and bring the task's progress up to date.\nTell the person only what they need to act on.", agentId: "main", sessionKey: "agent:main:c" }]);
  assert.deepEqual(wakes, [{ source: "other", intent: "immediate", reason: "wtc-pull-request", agentId: "main", sessionKey: "agent:main:c", heartbeat: { target: "last" } }]);
  // Merged while nobody looks: the service's next round finds it.
  assert.equal(services.at(-1).id, "pull-request-watch");
  pr = { ...pr, state: "merged" };
  const { mock } = await import("node:test");
  mock.timers.enable({ apis: ["setInterval"] });
  services.at(-1).start();
  readFailure = true;
  const priorEvents = events.length;
  mock.timers.tick(5 * 60000);
  await new Promise(setImmediate);
  await new Promise(setImmediate);
  assert.equal(events.length, priorEvents, "failed reads do not publish or forget the watched collection");
  assert.equal(warnings.length, 1);
  readFailure = false;
  mock.timers.tick(5 * 60000);
  await new Promise((resolve) => setImmediate(resolve));
  await new Promise((resolve) => setImmediate(resolve));
  services.at(-1).stop();
  mock.timers.reset();
  assert.match(events.at(-1).text, /#2 \(u\) was merged\.\nAct on it with the wtc-follow skill/);
  // First seen already merged: the wtc-follow hint too.
  pr = { ...pr, number: 5, url: "u5", state: "merged" };
  await look();
  assert.match(events.at(-1).text, /#5 \(u5\) was opened and merged\.\nAct on it with the wtc-follow skill/);
  // queue tells without waking; off says nothing.
  register({ pullRequestEvents: "queue" });
  pr = { ...pr, number: 3, state: "open" };
  await look();
  assert.match(events.at(-1).text, /#3/);
  assert.equal(wakes.length, 3);
  register({ pullRequestEvents: "off" });
  pr = { ...pr, number: 4 };
  const before = events.length;
  await look();
  assert.equal(events.length, before);
});

test("the collection names its wtc project: its harness, or ad hoc by its folder", async () => {
  for (const [col, want] of [
    [{ harness: { name: "wtc", path: "/w" }, folder: "/state/wtc/wtc/9-x" }, "wtc"],
    [{ folder: "/state/wtc/ad-hoc/9-x" }, "ad-hoc"],
    [{ folder: "/state/wtc-collections/9-x" }, undefined],
  ]) {
    const actions = new Map();
    const entries = [
      { sessionKey: "agent:main:c", entry: {} },
      { sessionKey: "agent:main:repo:site", entry: { parentSessionKey: "agent:main:c", projectId: "site", worktree: { id: "w1", branch: "openclaw/9-x" }, workspaceDir: "/worktrees/w1" } },
    ];
    plugin.register({
      pluginConfig: {},
      runtime: {
        state: { resolveStateDir: () => "/nonexistent" },
        agent: { session: { listSessionEntries: () => entries, getSessionEntry: async () => undefined } },
        system: { runCommandWithTimeout: async (argv) => ({ code: 0, stdout: argv[1] === "status" ? JSON.stringify({ "9-x": { repos: [], ...col } }) : "", stderr: "" }) },
      },
      session: { controls: { registerSessionAction: (a) => actions.set(a.id, a), registerControlUiDescriptor() {} } },
      registerHttpRoute() {},
      on() {},
      logger: {},
    });
    const r = await actions.get("collection").handler({ sessionKey: "agent:main:c", agentId: "main", payload: {} });
    assert.equal(r.result.collection.project, want, JSON.stringify(col));
  }
});

const PROJECTS = [
  { name: "wtc", repos: [
    { name: "wtc-site", role: "The official wtc site; what it says about wtc comes from wtc-cli." },
    { name: "wtc-cli", role: "The wtc CLI and OpenClaw plugin, what the site describes." },
  ] },
  { name: "shop", repos: [{ name: "storefront", role: "The web shop" }, { name: "billing", role: "Invoices and payments" }] },
];

test("a message's words suggest a project and its repositories", () => {
  assert.deepEqual(matchProjects("lets work on the site", PROJECTS), { project: "wtc", repos: ["wtc-site"], source: "match" });
  assert.deepEqual(matchProjects("the invoices are wrong", PROJECTS), { project: "shop", repos: ["billing"], source: "match" });
  assert.deepEqual(matchProjects("fix the cli", PROJECTS).repos, ["wtc-cli"]);
  // The project's own name sets no repository of it apart.
  assert.deepEqual(matchProjects("fix the wtc cli", PROJECTS), { project: "wtc", repos: ["wtc-cli"], source: "match" });
  // A repository named like its project; a project named without a match.
  assert.deepEqual(matchProjects("fix the shop", [{ name: "shop", repos: [{ name: "shop" }] }]).repos, ["shop"]);
  assert.deepEqual(matchProjects("wtc payments", PROJECTS), { project: "shop", repos: ["billing"], source: "match" });
  assert.equal(matchProjects("hello there", PROJECTS), null);
  assert.equal(matchProjects("", PROJECTS), null);
});

test("the decision model chooses when the gateway selected one", async () => {
  const asked = [];
  const decisions = {
    evaluate: async (batch, opts) => {
      asked.push({ batch, opts });
      return { status: "ok", answers: { where: { choice: "shop/storefront", probabilities: { none: 0.05, "wtc/wtc-site": 0.02, "wtc/wtc-cli": 0.01, "shop/storefront": 0.6, "shop/billing": 0.32 } } } };
    },
  };
  assert.deepEqual(await decideProjects(decisions, "main", "the checkout page breaks", PROJECTS), { project: "shop", repos: ["storefront", "billing"], source: "decision" });
  const { batch, opts } = asked[0];
  assert.equal(batch.questions.where.type, "choice");
  assert.ok("none" in batch.questions.where.criteria && "wtc/wtc-site" in batch.questions.where.criteria);
  assert.equal(opts.agentId, "main");
  assert.equal(batch.state, "the checkout page breaks");
  // Contract 2: typed evidence.
  let v2;
  await decideProjects({ evaluateV2: async (b) => { v2 = b; return { status: "ok", result: { answers: { where: { type: "choice", choice: "wtc/wtc-site", probabilities: { "wtc/wtc-site": 0.9 } } } } }; }, evaluate: async () => { throw new Error("v1 used"); } }, "main", "the site", PROJECTS)
    .then((r) => assert.deepEqual(r, { project: "wtc", repos: ["wtc-site"], source: "decision" }));
  assert.deepEqual(v2.state, { type: "text", text: "the site" });
  // None chosen, unavailable, failing or absent: no suggestion from it.
  assert.deepEqual(await decideProjects({ evaluate: async () => ({ status: "ok", answers: { where: { choice: "none", probabilities: {} } } }) }, "main", "hi", PROJECTS), { none: true });
  assert.equal(await decideProjects({ evaluate: async () => ({ status: "unavailable", reason: "no model" }) }, "main", "site", PROJECTS), null);
  assert.equal(await decideProjects({ evaluate: async () => { throw new Error("down"); } }, "main", "site", PROJECTS), null);
  assert.equal(await decideProjects(undefined, "main", "site", PROJECTS), null);
});

test("a draft not sent yet never reaches the gateway's decision runtime", async () => {
  const actions = new Map();
  let asked = 0;
  plugin.register({
    pluginConfig: {},
    runtime: {
      decisions: { evaluateV2: async () => { asked += 1; return { status: "ok", result: { answers: { where: { choice: "none", probabilities: {} } } } }; } },
      state: { resolveStateDir: () => "/nonexistent" },
      agent: { session: { listSessionEntries: () => [], getSessionEntry: async () => undefined } },
      system: { runCommandWithTimeout: async (argv) => ({ code: 0, stdout: argv[1] === "projects" ? JSON.stringify({ ok: true, data: { projects: PROJECTS } }) : "", stderr: "" }) },
    },
    session: { controls: { registerSessionAction: (a) => actions.set(a.id, a), registerControlUiDescriptor() {} } },
    registerHttpRoute() {},
    on() {},
    logger: {},
  });
  const r = await actions.get("suggest").handler({ sessionKey: "s", agentId: "main", payload: { text: "work on the site", draft: true } });
  assert.equal(r.result.source, "match");
  assert.equal(asked, 0);
  await actions.get("suggest").handler({ sessionKey: "s", agentId: "main", payload: { text: "work on the site" } });
  assert.equal(asked, 1);
});

test("the suggest action asks wtc projects once a minute, the decision model first", async () => {
  const actions = new Map();
  let projectRuns = 0;
  let decide = null;
  const agents = [];
  plugin.register({
    pluginConfig: {},
    runtime: {
      decisions: { evaluate: async (batch, opts) => { agents.push(opts.agentId); return decide; } },
      state: { resolveStateDir: () => "/nonexistent" },
      agent: { session: { listSessionEntries: () => [], getSessionEntry: async () => undefined } },
      system: { runCommandWithTimeout: async (argv) => {
        if (argv[1] === "projects") projectRuns += 1;
        return { code: 0, stdout: argv[1] === "projects" ? JSON.stringify({ ok: true, data: { projects: PROJECTS } }) : "", stderr: "" };
      } },
    },
    session: { controls: { registerSessionAction: (a) => actions.set(a.id, a), registerControlUiDescriptor() {} } },
    registerHttpRoute() {},
    on() {},
    logger: {},
  });
  const suggest = (text) => actions.get("suggest").handler({ sessionKey: "s", agentId: "main", payload: { text } });
  // The model's choice wins over the words, and its "none" stands.
  decide = { status: "ok", answers: { where: { choice: "shop/billing", probabilities: { "shop/billing": 0.9 } } } };
  assert.deepEqual((await suggest("work on the site")).result, { project: "shop", repos: ["billing"], source: "decision" });
  decide = { status: "ok", answers: { where: { choice: "none", probabilities: {} } } };
  assert.equal((await suggest("work on the site")).result, null);
  assert.deepEqual(agents, ["main", "main"]);
  // Unavailable: the words.
  decide = { status: "unavailable", reason: "no model" };
  assert.deepEqual((await suggest("work on the site")).result, { project: "wtc", repos: ["wtc-site"], source: "match" });
  assert.equal((await suggest("hello")).result, null);
  assert.equal((await suggest("")).result, null);
  assert.equal(projectRuns, 1);
});

test("without a runtime (CLI metadata, setup) the plugin registers nothing", () => {
  for (const registrationMode of ["cli-metadata", "setup-only"]) {
    plugin.register({ registrationMode, get runtime() { throw new Error("runtime is intentionally unavailable"); }, session: { controls: { registerSessionAction() { throw new Error("registered"); } } } });
  }
});

test("OpenAI's Decisions API called directly answers like the gateway's runtime", async () => {
  const sent = [];
  const reply = (status, body) => async (url, init) => { sent.push({ url, init }); return { ok: status === 200, status, json: async () => body }; };
  const ok = openaiDecisions({ apiKey: "sk-test", fetchImpl: reply(200, { model: "gpt-6-luna", answers: [{ name: "where", type: "choice", choice: "shop/billing", confidence: 0.9, probabilities: [{ value: "none", probability: 0.05 }, { value: "shop/billing", probability: 0.8 }, { value: "shop/storefront", probability: 0.15 }] }] }) });
  assert.deepEqual(await decideProjects(ok, "main", "the invoices are wrong", PROJECTS), { project: "shop", repos: ["billing"], source: "decision" });
  const { url, init } = sent[0];
  assert.equal(url, "https://api.openai.com/v1/decisions");
  assert.equal(init.headers.authorization, "Bearer sk-test");
  const body = JSON.parse(init.body);
  assert.equal(body.model, "gpt-6-luna");
  assert.equal(body.input, "the invoices are wrong");
  assert.equal(body.questions[0].type, "choice");
  assert.ok(body.questions[0].choices.some((c) => c.value === "none") && body.questions[0].choices.some((c) => c.value === "wtc/wtc-site"));
  // Refused, failing or malformed: unavailable, so the words decide.
  for (const [status, body] of [[401, {}], [500, {}], [200, { answers: [] }]]) {
    assert.equal(await decideProjects(openaiDecisions({ apiKey: "k", fetchImpl: reply(status, body) }), "main", "site", PROJECTS), null);
  }
  assert.equal(await decideProjects(openaiDecisions({ apiKey: "k", fetchImpl: async () => { throw new Error("offline"); } }), "main", "site", PROJECTS), null);
});

test("the suggest action calls OpenAI itself with WTC_DECISIONS_OPENAI_API_KEY, and only then", async () => {
  const actions = new Map();
  plugin.register({
    pluginConfig: { decisionModel: "gpt-6-test" },
    runtime: {
      state: { resolveStateDir: () => "/nonexistent" },
      agent: { session: { listSessionEntries: () => [], getSessionEntry: async () => undefined } },
      system: { runCommandWithTimeout: async (argv) => ({ code: 0, stdout: argv[1] === "projects" ? JSON.stringify({ ok: true, data: { projects: PROJECTS } }) : "", stderr: "" }) },
    },
    session: { controls: { registerSessionAction: (a) => actions.set(a.id, a), registerControlUiDescriptor() {} } },
    registerHttpRoute() {},
    on() {},
    logger: {},
  });
  const realFetch = globalThis.fetch;
  const models = [];
  const authorizations = [];
  globalThis.fetch = async (url, init) => {
    models.push(JSON.parse(init.body).model);
    authorizations.push(init.headers.authorization);
    return { ok: true, status: 200, json: async () => ({ model: "gpt-6-test", answers: [{ name: "where", type: "choice", choice: "none", probabilities: [{ value: "none", probability: 0.9 }] }] }) };
  };
  const was = process.env.WTC_DECISIONS_OPENAI_API_KEY;
  const wasCurrent = process.env.TABA_DECISIONS_OPENAI_API_KEY;
  try {
    const suggest = () => actions.get("suggest").handler({ sessionKey: "s", agentId: "main", payload: { text: "work on the site" } });
    delete process.env.WTC_DECISIONS_OPENAI_API_KEY;
    delete process.env.TABA_DECISIONS_OPENAI_API_KEY;
    assert.equal((await suggest()).result.source, "match");
    assert.deepEqual(models, []);
    process.env.WTC_DECISIONS_OPENAI_API_KEY = "sk-test";
    // Its "none" stands over the words.
    assert.equal((await suggest()).result, null);
    assert.deepEqual(models, ["gpt-6-test"]);
    // A draft not sent yet never leaves the gateway: the words only.
    const fromDraft = await actions.get("suggest").handler({ sessionKey: "s", agentId: "main", payload: { text: "work on the site", draft: true } });
    assert.equal(fromDraft.result.source, "match");
    assert.deepEqual(models, ["gpt-6-test"]);
    process.env.TABA_DECISIONS_OPENAI_API_KEY = "sk-current-test";
    assert.equal((await suggest()).result, null);
    assert.equal(authorizations.at(-1), "Bearer sk-current-test");
    process.env.TABA_DECISIONS_OPENAI_API_KEY = "";
    const requests = models.length;
    assert.equal((await suggest()).result.source, "match");
    assert.equal(models.length, requests);
  } finally {
    globalThis.fetch = realFetch;
    if (was === undefined) delete process.env.WTC_DECISIONS_OPENAI_API_KEY; else process.env.WTC_DECISIONS_OPENAI_API_KEY = was;
    if (wasCurrent === undefined) delete process.env.TABA_DECISIONS_OPENAI_API_KEY; else process.env.TABA_DECISIONS_OPENAI_API_KEY = wasCurrent;
  }
});

test("a catch-up merges as the agent commits: its gitAuthor, else its name, else OpenClaw", () => {
  const who = (cfg, id = "main") => { const e = agentGitIdentity(cfg, id); return `${e.GIT_COMMITTER_NAME} <${e.GIT_COMMITTER_EMAIL}>`; };
  assert.equal(who({ agents: { entries: { main: { name: "Team Claw", identity: { name: "Team Claw" } } } } }), "Team Claw <team-claw@localhost>");
  assert.equal(who({ tools: { github: { gitAuthor: { name: "Automation User", email: "automation@example.com" } } }, agents: { entries: { main: { name: "Team Claw" } } } }), "Automation User <automation@example.com>");
  // An agent's own managed identity replaces the system's.
  assert.equal(who({ tools: { github: { gitAuthor: { name: "System", email: "s@example.com" } } }, agents: { entries: { main: { tools: { github: { gitAuthor: { name: "Review Agent" } } }, name: "Reviewer" } } } }), "Review Agent <reviewer@localhost>");
  assert.equal(who({}), "OpenClaw <openclaw@localhost>");
  const e = agentGitIdentity({}, "main");
  assert.equal(e.GIT_AUTHOR_NAME, e.GIT_COMMITTER_NAME);
});

test("the card's catch-up runs wtc catch-up with the agent's git identity", async () => {
  const actions = new Map();
  let env = null;
  plugin.register({
    pluginConfig: {},
    config: { agents: { entries: { main: { name: "Team Claw" } } } },
    runtime: {
      state: { resolveStateDir: () => "/nonexistent" },
      agent: { session: { listSessionEntries: () => [], getSessionEntry: () => ({ worktree: { branch: "openclaw/9-x" }, workspaceDir: "/worktrees/w1" }) } },
      system: { runCommandWithTimeout: async (argv, options) => { if (argv[1] === "catch-up") env = options.env; return { code: 0, stdout: "{}", stderr: "" }; } },
    },
    session: { controls: { registerSessionAction: (a) => actions.set(a.id, a), registerControlUiDescriptor() {} } },
    registerHttpRoute() {},
    on() {},
    logger: {},
  });
  await actions.get("catch-up").handler({ sessionKey: "agent:main:repo:site", agentId: "main", payload: {} });
  assert.equal(env.OPENCLAW_SHELL, "exec");
  assert.equal(env.GIT_COMMITTER_EMAIL, "team-claw@localhost");
  assert.equal(env.GIT_AUTHOR_NAME, "Team Claw");
});

test("a sub-session's checkouts are its own, not a second row of the collection's repository, whatever its name", async () => {
  const actions = new Map();
  const child = (key, repo, name, dir) => ({ sessionKey: key, entry: { parentSessionKey: "agent:main:c", projectId: repo, worktree: { id: dir, branch: `openclaw/${name}` }, workspaceDir: `/worktrees/${dir}` } });
  const entries = [
    { sessionKey: "agent:main:c", entry: {} },
    child("agent:main:subagent:site", "site", "9-x", "w1"),
    child("agent:main:subagent:api", "api", "9-x", "w2"),
    // A wtc worker, named after the collection...
    child("agent:main:subagent:site-docs", "site", "9-x--docs", "w3"),
    // ...whose harness the collection has no checkout of...
    child("agent:main:subagent:harness-docs", "harness", "9-x--docs", "w4"),
    // ...and a session spawned with a worktree named for its change.
    child("agent:main:subagent:nav", "site", "13-docs-nav", "w5"),
  ];
  const repo = (repo, dir, branch, more = {}) => ({ repo, path: `/worktrees/${dir}`, branch, ...more });
  const status = {
    "9-x": { repos: [repo("site", "w1", "openclaw/9-x"), repo("api", "w2", "openclaw/9-x")],
      subSessions: [
        { repo: "site", path: "/worktrees/w3", branch: "openclaw/9-x--docs", toMerge: 2, behind: 1, uncommitted: [{ path: "a.md", state: "only" }] },
        { repo: "site", path: "/worktrees/w5", branch: "openclaw/13-docs-nav", toMerge: 1, behind: 0 },
      ] },
    "9-x--docs": { repos: [repo("site", "w3", "openclaw/9-x--docs", { dirty: 13 }), repo("harness", "w4", "openclaw/9-x--docs")] },
    "13-docs-nav": { repos: [repo("site", "w5", "openclaw/13-docs-nav")] },
  };
  const argvs = [];
  plugin.register({
    pluginConfig: {},
    runtime: {
      state: { resolveStateDir: () => "/nonexistent" },
      agent: { session: { listSessionEntries: () => entries, getSessionEntry: ({ sessionKey }) => entries.find((e) => e.sessionKey === sessionKey)?.entry } },
      system: { runCommandWithTimeout: async (argv, options) => {
        argvs.push({ argv, cwd: options?.cwd });
        return { code: 0, stdout: argv[1] === "status" ? JSON.stringify({ [argv[2]]: status[argv[2]] }) : "{}", stderr: "" };
      } },
    },
    session: { controls: { registerSessionAction: (a) => actions.set(a.id, a), registerControlUiDescriptor() {} } },
    registerHttpRoute() {},
    on() {},
    logger: {},
  });
  const { result } = await actions.get("collection").handler({ sessionKey: "agent:main:c", agentId: "main", payload: {} });
  // The collection's status compares every sub-session's worktree.
  const collectionStatus = argvs.find((a) => a.argv[1] === "status" && a.argv[2] === "9-x").argv;
  assert.deepEqual(collectionStatus.filter((a, i) => collectionStatus[i - 1] === "--sub").sort(), ["/worktrees/w3", "/worktrees/w4", "/worktrees/w5"]);
  assert.deepEqual(result.repos.map((r) => `${r.repo} ${r.branch}`), ["api openclaw/9-x", "site openclaw/9-x"]);
  assert.deepEqual(result.subSessions.map((w) => w.name), ["13-docs-nav", "docs"]);
  const docs = result.subSessions.find((w) => w.name === "docs");
  assert.equal(docs.collection, "9-x--docs");
  assert.deepEqual(docs.repos.map((r) => [r.repo, r.dirty, r.toMerge, r.taskSessionKey]).sort(), [["harness", undefined, null, null], ["site", 13, 2, "agent:main:subagent:site"]]);
  // ↓ and the files are the collection's view, not origin's.
  const site = docs.repos.find((r) => r.repo === "site");
  assert.equal(site.behind, 1);
  assert.deepEqual(site.uncommitted, [{ path: "a.md", state: "only" }]);
  const nav = result.subSessions.find((w) => w.name === "13-docs-nav").repos[0];
  assert.deepEqual([nav.branch, nav.toMerge, nav.taskSessionKey], ["openclaw/13-docs-nav", 1, "agent:main:subagent:site"]);

  // Merging one is a catch-up from its branch, by its name, in the collection's checkout.
  await actions.get("catch-up").handler({ sessionKey: "agent:main:subagent:site", agentId: "main", payload: { from: "openclaw/13-docs-nav" } });
  assert.deepEqual(argvs.at(-1), { argv: ["wtc", "catch-up", "--from", "openclaw/13-docs-nav", "--json"], cwd: "/worktrees/w1" });
  for (const bad of ["--all", "a..b", "x/", "a//b"]) {
    const refused = await actions.get("catch-up").handler({ sessionKey: "agent:main:subagent:site", agentId: "main", payload: { from: bad } });
    assert.equal(refused.ok, false, bad);
  }
});

test("the collection is the one wtc made, also when a sub-session has as many checkouts", async () => {
  const actions = new Map();
  // One checkout of the collection, one of a sub-session spawned with a
  // worktree: a tie by count. wtc labels the owners it spawns.
  const entries = [
    { sessionKey: "agent:main:c", entry: {} },
    { sessionKey: "agent:main:subagent:site", entry: { parentSessionKey: "agent:main:c", label: "194-subrows site", projectId: "site", worktree: { branch: "openclaw/194-subrows" }, workspaceDir: "/worktrees/w1" } },
    { sessionKey: "agent:main:subagent:x", entry: { parentSessionKey: "agent:main:c", label: "docs helper", projectId: "site", worktree: { branch: "openclaw/docs" }, workspaceDir: "/worktrees/w2" } },
  ];
  plugin.register({
    pluginConfig: {},
    runtime: {
      state: { resolveStateDir: () => "/nonexistent" },
      agent: { session: { listSessionEntries: () => entries, getSessionEntry: () => undefined } },
      system: { runCommandWithTimeout: async (argv) => ({ code: 0, stdout: argv[1] === "status" ? JSON.stringify({ [argv[2]]: { repos: [{ repo: "site", path: argv[2] === "docs" ? "/worktrees/w2" : "/worktrees/w1", branch: `openclaw/${argv[2]}` }] } }) : "", stderr: "" }) },
    },
    session: { controls: { registerSessionAction: (a) => actions.set(a.id, a), registerControlUiDescriptor() {} } },
    registerHttpRoute() {},
    on() {},
    logger: {},
  });
  const { result } = await actions.get("collection").handler({ sessionKey: "agent:main:c", agentId: "main", payload: {} });
  assert.deepEqual(result.repos.map((r) => r.branch), ["openclaw/194-subrows"]);
  assert.deepEqual(result.subSessions.map((w) => [w.name, w.repos[0].taskBranch]), [["docs", "openclaw/194-subrows"]]);
});

test("without wtc's labels the collection is the name others extend, then the most used", async () => {
  const pick = async (entries) => {
    const actions = new Map();
    plugin.register({
      pluginConfig: {},
      runtime: {
        state: { resolveStateDir: () => "/nonexistent" },
        agent: { session: { listSessionEntries: () => [{ sessionKey: "agent:main:c", entry: {} }, ...entries], getSessionEntry: () => undefined } },
        system: { runCommandWithTimeout: async (argv) => ({ code: 0, stdout: argv[1] === "status" ? JSON.stringify({ [argv[2]]: { repos: [] } }) : "", stderr: "" }) },
      },
      session: { controls: { registerSessionAction: (a) => actions.set(a.id, a), registerControlUiDescriptor() {} } },
      registerHttpRoute() {},
      on() {},
      logger: {},
    });
    const { result } = await actions.get("collection").handler({ sessionKey: "agent:main:c", agentId: "main", payload: {} });
    return result.subSessions.map((w) => w.name);
  };
  const child = (key, name, label) => ({ sessionKey: key, entry: { parentSessionKey: "agent:main:c", ...(label ? { label } : {}), worktree: { branch: `openclaw/${name}` }, workspaceDir: `/worktrees/${key}` } });
  // No labels: a worker's name extends the collection's, so it is never the collection.
  assert.deepEqual(await pick([child("a", "9-x--docs"), child("b", "9-x--docs"), child("c", "9-x")]), ["docs"]);
  // A label without a project: the one that starts with its name.
  assert.deepEqual(await pick([child("a", "9-x", "9-x site"), child("b", "nav", "helper")]), ["nav"]);
});

test("failed async session reads never retry legacy reads or execute an action", async () => {
  for (const method of ["getSessionEntry", "listSessionEntries"]) {
    const actions = new Map();
    const error = new Error("session store unavailable");
    let commands = 0, legacy = 0;
    plugin.register({
      runtime: {
        state: { resolveStateDir: () => "/nonexistent" },
        agent: { session: {
          [method]: () => { legacy++; return []; },
          [`${method}Async`]: async () => { throw error; },
        } },
        system: { runCommandWithTimeout: async () => { commands++; return {}; } },
      },
      session: { controls: { registerSessionAction: a => actions.set(a.id, a), registerControlUiDescriptor() {} } },
      registerHttpRoute() {}, on() {}, logger: {},
    });
    const action = method === "getSessionEntry" ? "catch-up" : "collection";
    await assert.rejects(actions.get(action).handler({ agentId: "main", sessionKey: "agent:main:c" }), e => e === error);
    assert.equal(legacy, 0);
    assert.equal(commands, 0);
  }
});

test("unavailable checkouts retain canonical identity without fetching or inventing status", async () => {
  const root = mkdtempSync(path.join(os.tmpdir(), "wtc-identity-"));
  const actions = new Map(), calls = [];
  const entries = ["api", "web", "other", "api-copy"].map((repo, i) => ({
    sessionKey: `agent:main:repo:${repo}`,
    entry: { parentSessionKey: "agent:main:c", projectId: repo, worktree: { branch: "openclaw/test", repoRoot: `/clones/${i === 3 ? "api" : repo}` }, workspaceDir: path.join(root, repo) },
  }));
  mkdirSync(path.join(root, "web", ".git"), { recursive: true });
  let fail = false;
  plugin.register({
    pluginConfig: {},
    runtime: {
      state: { resolveStateDir: () => root },
      agent: { session: { listSessionEntries: () => entries, getSessionEntry: () => undefined } },
      system: { runCommandWithTimeout: async argv => {
        calls.push(argv);
        if (argv[1] === "--version") return { code: 0, stdout: "wtc version dev" };
        if (argv[1] === "status") {
          if (fail) throw new Error("status timed out");
          return { code: 0, stdout: JSON.stringify({ test: { repos: [] } }) };
        }
        assert.deepEqual(argv.slice(0, 4), ["git", "config", "--no-includes", "--file"]);
        assert.equal(argv.at(-1), "remote.origin.url");
        const remote = argv[4].includes("/api/") ? "git@github.com:example-org/api.git" : argv[4].includes("/web/") ? "https://user:secret@bitbucket.org/example-team/web.git" : "https://elsewhere.invalid/other.git";
        return { code: 0, stdout: remote };
      } },
    },
    session: { controls: { registerSessionAction: a => actions.set(a.id, a), registerControlUiDescriptor() {} } },
    registerHttpRoute() {}, on() {}, logger: {},
  });
  const load = async () => (await actions.get("collection").handler({ sessionKey: "agent:main:c", agentId: "main", payload: {} })).result;
  const first = await load();
  const api = first.repos.find(r => r.project === "api"), web = first.repos.find(r => r.project === "web"), other = first.repos.find(r => r.project === "other");
  assert.deepEqual([api.forge, api.owner, api.repo, api.error], ["github", "example-org", "api", "checkout unavailable"]);
  assert.deepEqual([web.forge, web.owner, web.repo, web.error], ["bitbucket", "example-team", "web", "status unavailable"]);
  assert.equal(other.forge, undefined, "unknown origins are never guessed from project names");
  assert.equal(JSON.stringify(first).includes("secret"), false);
  assert(first.repos.every(r => !r.next && !r.choices && !r.pullRequest && r.ahead === undefined));
  assert.equal(calls.filter(a => a[0] === "git").length, 3, "shared canonical roots coalesce local config reads");
  fail = true;
  const second = await load();
  assert.match(second.warnings[0], /status timed out/);
  assert.equal(second.repos.find(r => r.project === "api").forge, "github");
  assert.equal(calls.filter(a => a[0] === "git").length, 3, "cached identity avoids repeated reads during failures");
});
