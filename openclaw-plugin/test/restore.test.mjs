// Checkout restoration as an authenticated Gateway method (#494). The native
// dispatcher is replaced by a recording fake here. The contract with a real
// OpenClaw Gateway is exercised by internal/openclaw/restore_integration_test.go,
// an opt-in Docker test (build tag openclaw_integration) that CI does not run.
import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import { classifyRestoreFailure, refusedBeforeEffects, registerCheckoutRestore, RESTORE_METHOD } from "../restore.js";

const parent = "agent:main:dashboard:task", owner = "agent:main:subagent:api";

function setup({ dispatch, gitSnapshot = 0, entryChanges = {} } = {}) {
  const dir = mkdtempSync(path.join(os.tmpdir(), "taba-restore-method-"));
  const entry = { parentSessionKey: parent, label: "task api", projectId: "api", workspaceDir: path.join(dir, "api"), worktree: { id: "w-api", repoRoot: path.join(dir, "clone"), branch: "openclaw/task" } };
  const state = {
    record: { id: "w-api", path: entry.workspaceDir, repoRoot: entry.worktree.repoRoot, branch: entry.worktree.branch, ownerKind: "session", ownerId: owner, removedAt: 1000, snapshotRef: "refs/openclaw/snapshots/w-api" },
    calls: [], git: [], settled: 0, gitSnapshot,
  };
  const worktreeOf = (e) => (e?.worktree ? e.workspaceDir || e.spawnedCwd || null : null);
  // Same verification as index.js: the evicted record must match owner and destination.
  const worktreeRecovery = (e, sessionKey, records) => {
    const r = records.filter(x => x.id === e.worktree?.id)[0];
    if (!r || r.ownerId !== sessionKey || r.path !== worktreeOf(e) || r.repoRoot !== e.worktree.repoRoot || r.branch !== e.worktree.branch) return { state: "unknown" };
    if (!Number.isFinite(r.removedAt)) return { state: "unknown" };
    if (!r.snapshotRef) return { state: "unavailable" };
    return { state: "restorable", key: `${r.id}:${r.removedAt}:${r.snapshotRef}`, removedAt: r.removedAt };
  };
  const methods = new Map();
  const api = { registerGatewayMethod: (name, handler, opts) => methods.set(name, { handler, opts }) };
  const fresh = () => ({ ...entry, ...entryChanges, worktree: { ...entry.worktree, ...(entryChanges.worktree ?? {}) } });
  const deps = {
    collectionOf: async (agentId, sessionKey) => ({ children: sessionKey === parent ? [{ sessionKey: owner, entry }] : [] }),
    collectionIdentity: () => ({ collectionName: (e) => String(e.worktree?.branch ?? "").replace(/^openclaw\//, ""), task: "task" }),
    getSessionEntry: async () => fresh(),
    worktreeOf, worktreeRecovery,
    run: async (argv) => { state.git.push(argv); return { code: state.gitSnapshot, stdout: "", stderr: "" }; },
    onSettled: () => { state.settled++; },
    probe: async () => true,
    dispatch: async (method, params, options) => {
      state.calls.push({ method, params, options });
      return dispatch ? dispatch(method, params, state, entry) : { ok: true, payload: { worktrees: [state.record] } };
    },
  };
  const restore = registerCheckoutRestore(api, deps);
  const key = `w-api:1000:refs/openclaw/snapshots/w-api`;
  const call = async (params = {}, authority) => {
    let out;
    await methods.get(RESTORE_METHOD).handler({ params: { agentId: "main", sessionKey: parent, ownerSessionKey: owner, key, ...params }, respond: (ok, payload, error) => { out = { ok, payload, error }; }, sessionAccessAuthority: authority });
    return out;
  };
  return { dir, entry, state, methods, restore, call, key };
}
const restores = (state) => state.calls.filter(c => c.method === "worktrees.restore").length;
// The native service recreates the checkout at its recorded destination.
const succeed = (method, params, state, entry) => {
  if (method === "worktrees.list") return { ok: true, payload: { worktrees: [state.record] } };
  mkdirSync(path.join(entry.workspaceDir, ".git"), { recursive: true });
  state.record = { ...state.record, removedAt: undefined };
  return { ok: true, payload: state.record };
};

test("the restore method requires gateway admin and an authenticated profile", () => {
  const { methods, restore } = setup();
  const { opts } = methods.get(RESTORE_METHOD);
  assert.equal(opts.scope, "operator.admin");
  assert.equal(opts.sessionAccess, undefined, "the host admits session-scoped plugin methods only at operator.write");
  assert.equal(opts.profileAccess, "required");
  assert.notEqual(restore.capability().state, "unavailable");
});

test("authorized restoration dispatches native worktrees.restore once under the admitted request and verifies the result", async () => {
  const { state, call } = setup({ dispatch: succeed });
  let checks = 0;
  const out = await call({}, { assertCurrent: () => { checks++; } });
  assert.equal(out.ok, true);
  assert.equal(out.payload.status, "restored", JSON.stringify(out.payload));
  assert.equal(out.payload.mutation, "applied");
  const restoreCall = state.calls.find(c => c.method === "worktrees.restore");
  assert.deepEqual(restoreCall.params, { id: "w-api" }, "no credential, owner override or replacement clone is sent");
  assert.equal(restoreCall.options.expectFinal, true);
  assert.ok(checks >= 2, "session access is rechecked immediately before the native effect");
  assert.equal(state.settled, 1, "status cache is invalidated after the attempt");
});

test("membership, key, owner, destination, branch and snapshot changes refuse before any native restore", async () => {
  for (const [name, params, change] of [
    ["other conversation", { sessionKey: "agent:main:other" }],
    ["stale key", { key: "stale" }],
    ["foreign owner", {}, { ownerId: "foreign" }],
    ["moved destination", {}, { path: "/other" }],
    ["other repository", {}, { repoRoot: "/other" }],
    ["changed branch", {}, { branch: "openclaw/other" }],
    ["no snapshot", {}, { snapshotRef: null }],
    ["evicted again", {}, { removedAt: 2000 }],
    ["already live", {}, { removedAt: undefined }],
  ]) {
    const { state, call } = setup({ dispatch: succeed });
    if (change) state.record = { ...state.record, ...change };
    const out = await call(params);
    assert.notEqual(out.payload.status, "restored", name);
    assert.equal(out.payload.mutation, "none", name);
    assert.equal(restores(state), 0, `${name} must not dispatch restore`);
  }
  for (const [name, entryChanges] of [["owner rebound", { parentSessionKey: "elsewhere" }], ["branch rebound", { worktree: { branch: "openclaw/other" } }], ["active run", { hasActiveRun: true }], ["active subagent", { activeRunIds: ["r1"] }]]) {
    const { state, call } = setup({ dispatch: succeed, entryChanges });
    const out = await call();
    assert.equal(out.payload.status, "refused", name);
    assert.equal(restores(state), 0, name);
  }
});

test("an existing checkout and a concurrent restore are refused", async () => {
  const { state, call } = setup({ dispatch: async (method, params, s, e) => {
    if (method === "worktrees.list") return { ok: true, payload: { worktrees: [s.record] } };
    await new Promise(r => { s.release = r; });
    return succeed(method, params, s, e);
  } });
  const pending = call();
  while (!state.release) await new Promise(setImmediate);
  assert.equal((await call()).payload.kind, "busy", "a duplicate request is refused");
  state.release();
  assert.equal((await pending).payload.status, "restored");
  const again = await call();
  assert.equal(again.payload.status, "refused", "an existing checkout is never overwritten");
  assert.equal(restores(state), 1);
});

test("permission denial, connection authentication and missing capability are distinct and report no change", async () => {
  for (const [kind, failure] of [
    ["permission", { ok: false, error: { code: "INVALID_REQUEST", message: "missing scope: operator.admin", details: { code: "MISSING_SCOPE", missingScope: "operator.admin" } } }],
    ["authentication", new Error("In-process gateway dispatch requires an authenticated plugin request scope (method: worktrees.restore).")],
    ["authentication", { ok: false, error: { code: "INVALID_REQUEST", message: "unauthorized", details: { code: "AUTH_UNAUTHORIZED" } } }],
    ["capability", new Error('Gateway method dispatch is reserved for authenticated plugin HTTP routes or RPC handlers that declare contracts.gatewayMethodDispatch: ["authenticated-request"] for plugin "wtc".')],
    ["capability", Object.assign(new Error("Cannot find package 'openclaw' imported from restore.js"), { code: "ERR_MODULE_NOT_FOUND" })],
    ["capability", { ok: false, error: { code: "INVALID_REQUEST", message: "unknown method: worktrees.restore" } }],
  ]) {
    const { state, call } = setup({ dispatch: (method, params, s) => {
      if (method === "worktrees.list") return { ok: true, payload: { worktrees: [s.record] } };
      if (failure instanceof Error) throw failure;
      return failure;
    } });
    const out = await call();
    assert.equal(out.payload.status, "failed");
    assert.equal(out.payload.kind, kind, JSON.stringify(out.payload));
    assert.equal(out.payload.mutation, "none");
    assert.equal(restores(state), 1, "never retried");
    assert.equal(state.calls.filter(c => c.method === "worktrees.list").length, 1, "a refusal before effects needs no reconciliation");
  }
  // A refused listing stops before restore is attempted.
  const { state, call } = setup({ dispatch: () => ({ ok: false, error: { code: "INVALID_REQUEST", message: "missing scope: operator.read", details: { code: "MISSING_SCOPE" } } }) });
  const out = await call();
  assert.equal(out.payload.kind, "permission");
  assert.equal(restores(state), 0);
});

test("an expired snapshot is reported as unavailable without dispatching restore", async () => {
  const { state, call } = setup({ dispatch: succeed, gitSnapshot: 1 });
  const out = await call();
  assert.equal(out.payload.kind, "snapshot");
  assert.equal(out.payload.mutation, "none");
  assert.equal(restores(state), 0);
  assert.deepEqual(state.git[0].slice(-4), ["rev-parse", "--verify", "--quiet", "refs/openclaw/snapshots/w-api^{commit}"], "the snapshot check is a read-only lookup");
});

test("a native snapshot failure is reconciled against the record and never replayed", async () => {
  const { state, call } = setup({ dispatch: (method, params, s) => {
    if (method === "worktrees.list") return { ok: true, payload: { worktrees: [s.record] } };
    s.gitSnapshot = 1; // vanished between the check and the native restore
    return { ok: false, error: { code: "UNAVAILABLE", message: "Error: worktree w-api is not restorable" } };
  } });
  const out = await call();
  assert.equal(out.payload.kind, "snapshot");
  assert.equal(out.payload.mutation, "none", "the record and destination prove nothing changed");
  assert.equal(restores(state), 1);
});

test("an ambiguous reply is reconciled: verified restoration, still evicted, or unknown", async () => {
  // The reply was lost but the native service restored the checkout.
  let s1 = setup({ dispatch: (method, params, s, e) => {
    if (method === "worktrees.list") return { ok: true, payload: { worktrees: [s.record] } };
    succeed(method, params, s, e);
    throw new Error("gateway request timed out");
  } });
  let out = await s1.call();
  assert.equal(out.payload.status, "restored");
  assert.match(out.payload.message, /confirmed after an unclear reply/);
  assert.equal(restores(s1.state), 1);
  // Nothing changed: still evicted with its snapshot.
  const s2 = setup({ dispatch: (method, params, s) => method === "worktrees.list" ? { ok: true, payload: { worktrees: [s.record] } } : { ok: false, error: { code: "UNAVAILABLE", message: "git checkout failed" } } });
  out = await s2.call();
  assert.equal(out.payload.status, "failed");
  assert.equal(out.payload.mutation, "none");
  assert.match(out.payload.message, /still evicted and its snapshot is recorded/);
  // The state cannot be read back: say so, do not guess or retry.
  let listed = 0;
  const s3 = setup({ dispatch: (method) => {
    if (method === "worktrees.list") return ++listed === 1 ? { ok: true, payload: { worktrees: [s3.state.record] } } : { ok: false, error: { code: "UNAVAILABLE", message: "busy" } };
    throw new Error("socket closed");
  } });
  out = await s3.call();
  assert.equal(out.payload.kind, "unknown");
  assert.equal(out.payload.mutation, "unknown");
  assert.equal(restores(s3.state), 1);
});

test("a native result for another owner, destination or branch is not reported as restored", async () => {
  const { state, call } = setup({ dispatch: (method, params, s, e) => {
    if (method === "worktrees.list") return { ok: true, payload: { worktrees: [s.record] } };
    succeed(method, params, s, e);
    return { ok: true, payload: { ...s.record, ownerId: "foreign" } };
  } });
  const out = await call();
  assert.equal(out.payload.status, "failed");
  assert.equal(out.payload.mutation, "unknown");
  assert.equal(restores(state), 1);
});

test("revoked session access stops before the native restore", async () => {
  const { state, call } = setup({ dispatch: succeed });
  let n = 0;
  const out = await call({}, { assertCurrent: () => { if (++n > 1) throw new Error("session access revoked"); } });
  assert.equal(out.payload.status, "failed");
  assert.equal(out.payload.mutation, "none");
  assert.equal(restores(state), 0);
});

test("a host without plugin Gateway methods reports the missing capability instead of offering restore", async () => {
  const restore = registerCheckoutRestore({}, { collectionOf: async () => ({ children: [] }), collectionIdentity: () => ({}), getSessionEntry: async () => null, worktreeOf: () => null, worktreeRecovery: () => ({}), run: async () => ({ code: 0 }), probe: async () => true, dispatch: async () => assert.fail("no dispatch") });
  assert.equal(restore.capability().state, "unavailable");
  assert.match(restore.capability().reason, /cannot restore checkouts from Collection/);
  const missing = registerCheckoutRestore({ registerGatewayMethod() {} }, { collectionOf: async () => ({ children: [] }), collectionIdentity: () => ({}), getSessionEntry: async () => null, worktreeOf: () => null, worktreeRecovery: () => ({}), run: async () => ({ code: 0 }), probe: async () => { throw new Error("Cannot find package 'openclaw'"); } });
  await new Promise(setImmediate); await new Promise(setImmediate);
  assert.equal(missing.capability().state, "unavailable", "an unresolvable native dispatcher is a missing capability");
});

test("failure classification keeps the four person-facing causes apart", () => {
  assert.equal(classifyRestoreFailure({ code: "INVALID_REQUEST", message: "unknown method: worktrees.restore" }).kind, "capability");
  assert.equal(classifyRestoreFailure({ message: "missing scope: operator.admin", details: { code: "MISSING_SCOPE" } }).kind, "permission");
  assert.equal(classifyRestoreFailure(new Error("gateway connect failed: GatewayClientRequestError: unauthorized")).kind, "authentication");
  assert.equal(classifyRestoreFailure(new Error("worktree w is not restorable")).kind, "snapshot");
  assert.equal(classifyRestoreFailure(new Error("socket closed")).kind, "unknown");
});

test("only structured pre-handler refusals skip reconciliation, never matching words", async () => {
  assert.equal(refusedBeforeEffects({ code: "INVALID_REQUEST", message: "missing scope: operator.admin", details: { code: "MISSING_SCOPE" } }), true);
  assert.equal(refusedBeforeEffects({ code: "INVALID_REQUEST", message: "unauthorized", details: { code: "AUTH_UNAUTHORIZED" } }), true);
  assert.equal(refusedBeforeEffects({ code: "INVALID_REQUEST", message: "unknown method: worktrees.restore" }), true);
  assert.equal(refusedBeforeEffects({ code: "UNAVAILABLE", message: "x", details: { mutationAccepted: false } }), true);
  assert.equal(refusedBeforeEffects(new Error("In-process gateway dispatch requires an authenticated plugin request scope (method: worktrees.restore).")), true);
  for (const message of ["git checkout: permission denied", "Error: unauthorized write to index", "worktree snapshot copy failed: not authorized"])
    assert.equal(refusedBeforeEffects({ code: "UNAVAILABLE", message }), false, message);
  // A native failure worded like a permission problem is read back, not trusted.
  let listed = 0;
  const { state, call } = setup({ dispatch: (method, params, s) => {
    if (method === "worktrees.list") { listed++; return { ok: true, payload: { worktrees: [s.record] } }; }
    return { ok: false, error: { code: "UNAVAILABLE", message: "Error: git checkout-index: permission denied" } };
  } });
  const out = await call();
  assert.equal(listed, 2, "the native record is read again after an effectful failure");
  assert.equal(out.payload.status, "failed");
  assert.match(out.payload.message, /still evicted/);
  assert.equal(restores(state), 1);
});

test("a native failure that mentions the snapshot does not claim it is gone while it still resolves", async () => {
  const { state, call } = setup({ dispatch: (method, params, s) =>
    method === "worktrees.list" ? { ok: true, payload: { worktrees: [s.record] } } : { ok: false, error: { code: "UNAVAILABLE", message: "Error: snapshot copy failed: disk full" } } });
  const out = await call();
  assert.equal(out.payload.status, "failed");
  assert.equal(out.payload.kind, "unknown");
  assert.equal(out.payload.mutation, "none");
  assert.doesNotMatch(out.payload.message, /no longer available/);
  assert.match(out.payload.message, /still evicted and its snapshot is recorded/);
  assert.match(out.payload.detail, /snapshot copy failed/, "the native text stays in the details");
  assert.equal(restores(state), 1);
});
