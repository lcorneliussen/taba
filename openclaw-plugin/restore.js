// Restore an evicted collection checkout through OpenClaw's native
// worktrees.restore under the admitted request's own identity (#494).
//
// A session action cannot dispatch Gateway methods, and the `openclaw
// worktrees restore` CLI authenticates with whatever credential the gateway
// host's CLI holds, not the person's. So restoration is a plugin Gateway
// method that requires operator.admin and dispatches through the SDK, which
// refuses synthetic clients; the host checks the caller's own scopes again.
import { existsSync } from "node:fs";
import path from "node:path";

export const RESTORE_METHOD = "wtc.checkout.restore";

const nativeDispatch = async (...args) => (await import("openclaw/plugin-sdk/gateway-method-runtime")).dispatchGatewayMethod(...args);
const probeNativeDispatch = async () => typeof (await import("openclaw/plugin-sdk/gateway-method-runtime")).dispatchGatewayMethod === "function";

const UNAVAILABLE = "This gateway cannot restore checkouts from Collection";
const text = (error) => [error?.code, error?.details?.code, error?.details?.reason, error?.message ?? error].filter(Boolean).join(" ");

// What a failed native call means for the person. Only refusals that happen
// before the native operation can take effect say that nothing changed.
export function classifyRestoreFailure(error) {
  const t = text(error);
  if (/unknown method|method not found|METHOD_NOT_FOUND|dispatch is reserved|Cannot find (?:module|package)|ERR_MODULE_NOT_FOUND|ERR_PACKAGE_PATH_NOT_EXPORTED/i.test(t))
    return { kind: "capability", message: `${UNAVAILABLE}: update OpenClaw and the taba plugin together, or restore it from OpenClaw's Worktrees settings`, mutation: "none" };
  if (/MISSING_SCOPE|missing scope|FORBIDDEN|not authorized|permission denied/i.test(t))
    return { kind: "permission", message: "Permission denied: restoring a checkout requires gateway admin permission", mutation: "none" };
  if (/unauthori[sz]ed|AUTH_|authenticat|requires an authenticated plugin request scope|ECONNREFUSED|connect failed/i.test(t))
    return { kind: "authentication", message: "Gateway authentication failed; reconnect and refresh before restoring", mutation: "none" };
  if (/not restorable|snapshot/i.test(t))
    return { kind: "snapshot", message: "The saved snapshot is no longer available; the checkout was not restored", mutation: "unknown" };
  return { kind: "unknown", message: "Native restore did not confirm a result", mutation: "unknown" };
}

// Refusals the host makes before the native handler runs, recognised by
// structure rather than wording: the native operation cannot have started.
export function refusedBeforeEffects(error) {
  const code = String(error?.details?.code ?? "");
  if (error?.details?.mutationAccepted === false || code === "MISSING_SCOPE" || code.startsWith("AUTH_")) return true;
  if (error?.code === "INVALID_REQUEST" && /^unknown method: /.test(String(error?.message ?? ""))) return true;
  // The SDK refuses before dispatching; these carry no gateway error code.
  return error?.code === undefined && /^(?:Gateway method dispatch is reserved|In-process gateway dispatch requires an authenticated plugin request scope)/.test(String(error?.message ?? ""))
    || error?.code === "ERR_MODULE_NOT_FOUND" || error?.code === "ERR_PACKAGE_PATH_NOT_EXPORTED";
}

const sanitize = (error) => String(error?.message ?? error ?? "").replace(/\s+/g, " ").trim().slice(-600);

export function registerCheckoutRestore(api, { collectionOf, collectionIdentity, getSessionEntry, worktreeOf, worktreeRecovery, run, onSettled = () => {}, dispatch = nativeDispatch, probe = probeNativeDispatch }) {
  // Execution capability, separate from whether a snapshot is recorded.
  let capability = typeof api.registerGatewayMethod === "function"
    ? { state: "unknown" } : { state: "unavailable", reason: `${UNAVAILABLE}: this OpenClaw has no plugin Gateway methods` };
  if (capability.state === "unknown") {
    Promise.resolve().then(probe).then(
      (ok) => { capability = ok ? { state: "available" } : { state: "unavailable", reason: `${UNAVAILABLE}: the native dispatcher is missing` }; },
      () => { capability = { state: "unavailable", reason: `${UNAVAILABLE}: the native dispatcher is missing` }; });
  }
  const restoring = new Set();
  const native = async (method, params, options) => {
    const res = await dispatch(method, params, { expectFinal: true, ...options });
    if (!res?.ok) throw Object.assign(new Error(res?.error?.message ?? `${method} failed`), { code: res?.error?.code, details: res?.error?.details });
    return res.payload;
  };
  const records = async () => {
    const out = await native("worktrees.list", {}, { timeoutMs: 30000 });
    if (!Array.isArray(out?.worktrees)) throw Error("Native worktree records unavailable");
    return out.worktrees;
  };
  // Read-only: does the recorded snapshot still resolve in its repository?
  const snapshotPresent = async (record) => {
    if (!record?.snapshotRef || !record.repoRoot) return false;
    const r = await run(["git", "-C", record.repoRoot, "rev-parse", "--verify", "--quiet", `${record.snapshotRef}^{commit}`], { timeoutMs: 10000 }).catch(() => null);
    return r ? r.code === 0 : null;
  };

  // Validates everything the person saw before anything is dispatched.
  async function restore(params, assertCurrent) {
    const { agentId, sessionKey, ownerSessionKey, key } = params ?? {};
    if (typeof sessionKey !== "string" || !sessionKey || typeof agentId !== "string" || !agentId || typeof ownerSessionKey !== "string" || typeof key !== "string")
      return { status: "refused", kind: "invalid", message: "A collection checkout and recovery key are required", mutation: "none" };
    const { children } = await collectionOf(agentId, sessionKey);
    const { collectionName, task } = collectionIdentity(children);
    const child = children.find(c => c.sessionKey === ownerSessionKey && collectionName(c.entry) === task);
    if (!child?.entry.worktree?.id) return { status: "refused", kind: "changed", message: "The checkout is no longer in this collection", mutation: "none" };
    const entry = child.entry, id = entry.worktree.id, destination = worktreeOf(entry);
    if (restoring.has(id)) return { status: "refused", kind: "busy", message: "This checkout is already being restored", mutation: "none" };
    restoring.add(id);
    try {
      if (existsSync(path.join(destination, ".git"))) return { status: "refused", kind: "changed", message: "The checkout is already present; refresh the collection", mutation: "none" };
      let before;
      try { before = await records(); } catch (error) { return { status: "failed", ...classifyRestoreFailure(error), mutation: "none", detail: sanitize(error) }; }
      const current = worktreeRecovery(entry, ownerSessionKey, before);
      if (current.state !== "restorable" || current.key !== key) return { status: "refused", kind: "changed", message: "Recovery changed; refresh the collection before restoring", mutation: "none" };
      const fresh = await getSessionEntry({ agentId, sessionKey: ownerSessionKey });
      if (!fresh || fresh.parentSessionKey !== entry.parentSessionKey || fresh.worktree?.id !== id || worktreeOf(fresh) !== destination || fresh.worktree?.repoRoot !== entry.worktree.repoRoot || fresh.worktree?.branch !== entry.worktree.branch)
        return { status: "refused", kind: "changed", message: "The checkout owner changed; refresh before restoring", mutation: "none" };
      if (fresh.hasActiveRun || fresh.hasActiveSubagentRun || fresh.activeRunIds?.length) return { status: "refused", kind: "busy", message: "Wait for this checkout's active run to finish before restoring", mutation: "none" };
      const record = before.find(r => r.id === id);
      if (await snapshotPresent(record) === false) return { status: "failed", kind: "snapshot", message: "The saved snapshot is no longer available; the checkout was not restored", mutation: "none" };
      assertCurrent();
      let restored, failure;
      try { restored = await native("worktrees.restore", { id }, { timeoutMs: 300000 }); } catch (error) { failure = error; }
      const verified = (r) => r && r.id === id && r.path === destination && r.ownerId === ownerSessionKey && r.branch === entry.worktree.branch && r.removedAt == null && existsSync(path.join(destination, ".git"));
      if (!failure) return verified(restored)
        ? { status: "restored", message: "Checkout restored from its saved snapshot", mutation: "applied" }
        : { status: "failed", kind: "unknown", message: "Native restore returned an unexpected result; inspect the checkout before retrying", mutation: "unknown" };
      const classified = classifyRestoreFailure(failure);
      // A refusal before effects changed nothing; anything else is reconciled
      // against what the native service now records, and never replayed.
      if (refusedBeforeEffects(failure)) return { status: "failed", ...classified, mutation: "none", detail: sanitize(failure) };
      let after;
      try { after = (await records()).find(r => r.id === id); } catch { after = undefined; }
      if (verified(after)) return { status: "restored", message: "Checkout restored from its saved snapshot (confirmed after an unclear reply)", mutation: "applied", detail: sanitize(failure) };
      if (after && Number.isFinite(after.removedAt) && after.snapshotRef && !existsSync(destination)) {
        const present = await snapshotPresent(after);
        if (present === false) return { status: "failed", kind: "snapshot", message: "The saved snapshot is no longer available; the checkout was not restored", mutation: "none", detail: sanitize(failure) };
        return { status: "failed", ...classified, message: `${classified.kind === "snapshot" ? classified.message : "Native restore failed"}; the checkout is still evicted and its snapshot is recorded`, mutation: "none", detail: sanitize(failure) };
      }
      return { status: "failed", kind: "unknown", message: "Restore outcome unknown; inspect the checkout and refresh before retrying", mutation: "unknown", detail: sanitize(failure) };
    } finally { restoring.delete(id); onSettled(); }
  }

  if (capability.state !== "unavailable") {
    api.registerGatewayMethod(RESTORE_METHOD, async ({ params = {}, respond, sessionAccessAuthority }) => {
      const assertCurrent = () => sessionAccessAuthority?.assertCurrent?.();
      try {
        assertCurrent();
        respond(true, await restore(params, assertCurrent));
      } catch (error) {
        // restore() handles every failure after dispatch itself: this one came first.
        respond(true, { status: "failed", ...classifyRestoreFailure(error), mutation: "none", detail: sanitize(error) });
      }
    // Admin only. OpenClaw admits session-scoped plugin methods (sessionAccess)
    // only at operator.write, so membership is checked here instead.
    }, { scope: "operator.admin", profileAccess: "required" });
  }
  return { restore, capability: () => capability };
}
