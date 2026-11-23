// Pull request writes a session asks for, carried out with the login of the
// person who confirms them.
//
// An agent cannot hold a person's Bitbucket login on a gateway, so tools such
// as a review poster hand their writes over as a request: a JSON file in the
// session's managed worktree. The person sees exactly what would be written,
// to which pull request and with which account, and confirms. Then the gateway
// writes, as them, and leaves a receipt next to the request for the tool to
// read back.
//
//   pr-writes         → needs_confirmation {requestId, confirmation: {account, requestDigest, operations, …}}
//   pr-writes-confirm {requestId, requestDigest} → done {receipt}
//
// The operations are a closed set, each with its own Bitbucket endpoint:
//
//   comment.create   {text, inline?: {path, to | from}, parent?}
//   comment.update   {comment, text}      only the confirming account's own comment
//   comment.resolve  {comment}
//   pr.ready         {}                   a draft becomes ready for review
//   pr.update        {title?, description?}
//
// The repository is the worktree's origin, never the request's say; the pull
// request must be in it and still have the head the request names. Nothing is
// retried: the first failure stops the run, and a write whose outcome is not
// known (sent, no answer) is recorded as unknown, for the tool to reconcile by
// reading the pull request. An intent id is started at most once: a claim is
// recorded before its first write, so a run interrupted before its receipt is
// refused rather than repeated, and is reconciled by reading the pull request.
import { createHash, randomUUID } from "node:crypto";
import { execFile } from "node:child_process";
import { mkdir, readFile, rename, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { promisify } from "node:util";

import { readLogin } from "./logins.js";
import { parseBitbucketRemote } from "./publish.js";

const execFileAsync = promisify(execFile);
const API = "https://api.bitbucket.org/2.0";
const REQUEST_TTL_MS = 10 * 60_000;
export const REQUEST_FILE = "bitbucket-pr-writes.json";
export const RECEIPTS_DIR = "bitbucket-pr-writes";
const MAX_REQUEST_BYTES = 512 * 1024;
const MAX_OPERATIONS = 100;
const MAX_TEXT = 32 * 1024;
const INTENT = /^[A-Za-z0-9][A-Za-z0-9._-]{7,79}$/;
const SHA = /^[0-9a-f]{12,40}$/;
const PATH = /^[^\0\n\r]{1,1024}$/;

export class WriteRefused extends Error {}

const worktreeOf = (entry) => (entry?.worktree ? entry.workspaceDir || entry.spawnedCwd || entry.worktree.path || null : null);
const positive = (n) => Number.isSafeInteger(n) && n > 0;
const text = (value, what) => {
  if (typeof value !== "string" || !value.trim() || value.length > MAX_TEXT || value.includes("\0")) throw new WriteRefused(`${what} must be non-empty text up to ${MAX_TEXT} characters`);
  return value;
};

// A request as the agent wrote it, reduced to the closed shape. Anything else
// is refused rather than ignored.
export function parseRequest(raw) {
  let request;
  try { request = JSON.parse(raw); } catch { throw new WriteRefused("the write request is not JSON"); }
  const only = (object, keys, what) => {
    if (!object || typeof object !== "object" || Array.isArray(object)) throw new WriteRefused(`${what} must be an object`);
    for (const key of Object.keys(object)) if (!keys.includes(key)) throw new WriteRefused(`${what} has an unsupported field ${JSON.stringify(key).slice(0, 40)}`);
  };
  only(request, ["version", "intentId", "owner", "repo", "pr", "head", "operations"], "the write request");
  if (request.version !== 1) throw new WriteRefused("unsupported write request version");
  if (typeof request.intentId !== "string" || !INTENT.test(request.intentId)) throw new WriteRefused("the write request needs an intentId of 8-80 letters, digits, dots, dashes or underscores");
  if (!positive(request.pr)) throw new WriteRefused("the write request needs a pull request number");
  if (typeof request.head !== "string" || !SHA.test(request.head)) throw new WriteRefused("the write request needs the pull request head it was made for");
  if (!Array.isArray(request.operations) || request.operations.length === 0 || request.operations.length > MAX_OPERATIONS) {
    throw new WriteRefused(`the write request needs 1-${MAX_OPERATIONS} operations`);
  }
  const operations = request.operations.map((op, index) => {
    const what = `operation ${index + 1}`;
    switch (op?.op) {
      case "comment.create": {
        only(op, ["op", "text", "inline", "parent"], what);
        const out = { op: op.op, text: text(op.text, `${what} text`) };
        if (op.inline !== undefined) {
          only(op.inline, ["path", "to", "from"], `${what} inline`);
          if (typeof op.inline.path !== "string" || !PATH.test(op.inline.path)) throw new WriteRefused(`${what} inline needs a file path`);
          const to = op.inline.to, from = op.inline.from;
          if ((to === undefined) === (from === undefined) || !positive(to ?? from)) throw new WriteRefused(`${what} inline needs exactly one line: to or from`);
          out.inline = { path: op.inline.path, ...(to !== undefined ? { to } : { from }) };
        }
        if (op.parent !== undefined) {
          if (!positive(op.parent)) throw new WriteRefused(`${what} parent must be a comment id`);
          out.parent = op.parent;
        }
        return out;
      }
      case "comment.update":
        only(op, ["op", "comment", "text"], what);
        if (!positive(op.comment)) throw new WriteRefused(`${what} needs a comment id`);
        return { op: op.op, comment: op.comment, text: text(op.text, `${what} text`) };
      case "comment.resolve":
        only(op, ["op", "comment"], what);
        if (!positive(op.comment)) throw new WriteRefused(`${what} needs a comment id`);
        return { op: op.op, comment: op.comment };
      case "pr.ready":
        only(op, ["op"], what);
        return { op: op.op };
      case "pr.update": {
        only(op, ["op", "title", "description"], what);
        if (op.title === undefined && op.description === undefined) throw new WriteRefused(`${what} changes nothing`);
        const out = { op: op.op };
        if (op.title !== undefined) {
          if (typeof op.title !== "string" || !op.title.trim() || op.title.length > 255 || /[\0\n\r]/.test(op.title)) throw new WriteRefused(`${what} title must be one line up to 255 characters`);
          out.title = op.title;
        }
        if (op.description !== undefined) {
          if (typeof op.description !== "string" || op.description.length > MAX_TEXT || op.description.includes("\0")) throw new WriteRefused(`${what} description must be text up to ${MAX_TEXT} characters`);
          out.description = op.description;
        }
        return out;
      }
      default:
        throw new WriteRefused(`${what} is not a supported pull request write`);
    }
  });
  return { version: 1, intentId: request.intentId, owner: request.owner, repo: request.repo, pr: request.pr, head: request.head, operations };
}

export function digestOf(request) {
  return createHash("sha256").update(JSON.stringify([
    request.sessionKey, request.cwd, request.account, request.owner, request.repo,
    request.intentId, request.pr, request.head, request.operations,
  ])).digest("hex");
}

// A receipt's file name: the intent id is validated, never a path.
export const receiptName = (intentId) => `${intentId}.receipt.json`;
export const claimName = (intentId) => `${intentId}.claim`;

export function createPullRequestWriter({ stateDir, getSessionEntry, currentProfile, fetchImpl = fetch, gitEnv = {}, apiUrl = API, now = Date.now }) {
  const requests = new Map();
  const running = new Set();
  const SAFE = [
    "-c", "core.fsmonitor=false", "-c", "core.hooksPath=" + os.devNull,
    "-c", "log.showSignature=false", "-c", "gpg.program=false",
  ];
  const wt = async (cwd, args) => {
    const { stdout } = await execFileAsync("git", ["-C", cwd, ...SAFE, ...args], {
      env: { ...process.env, ...gitEnv, GIT_TERMINAL_PROMPT: "0" }, timeout: 30_000, maxBuffer: 1024 * 1024,
    });
    return stdout.trim();
  };

  async function requester() {
    const profileId = currentProfile()?.profileId;
    if (!profileId) throw new WriteRefused("sign in as a person: pull request writes use your own Bitbucket login, and this connection has none");
    return { profileId, login: await readLogin(stateDir, profileId) };
  }

  // One Bitbucket call. A refused or failed answer throws WriteRefused. A
  // write that was sent but whose answer could not be read throws a
  // WriteRefused marked unknown: it may or may not have happened.
  async function bitbucket(login, method, route, payload) {
    let res;
    try {
      res = await fetchImpl(`${apiUrl}${route}`, {
        method,
        headers: {
          authorization: "Basic " + Buffer.from(`${login.user}:${login.token}`).toString("base64"),
          accept: "application/json",
          ...(payload ? { "content-type": "application/json" } : {}),
        },
        body: payload ? JSON.stringify(payload) : undefined,
        redirect: "error",
        signal: AbortSignal.timeout(30_000),
      });
    } catch {
      if (method === "GET") throw new WriteRefused(`Bitbucket did not answer ${method} ${route.split("?")[0]}`);
      const unknown = new WriteRefused(`Bitbucket did not answer ${method} ${route.split("?")[0]}; the write may or may not have happened`);
      unknown.unknown = true;
      throw unknown;
    }
    let body;
    try {
      body = await res.text();
    } catch {
      if (method === "GET") throw new WriteRefused(`Bitbucket's answer to ${method} ${route.split("?")[0]} could not be read`);
      const unknown = new WriteRefused(`Bitbucket's answer to ${method} ${route.split("?")[0]} could not be read; the write may or may not have happened`);
      unknown.unknown = true;
      throw unknown;
    }
    // The message names only the status: the response body is never echoed.
    // For a write, only a 4xx is a refusal; a 5xx or a 2xx Bitbucket answered
    // with something other than JSON may have been applied: unknown.
    const where = `${method} ${route.split("?")[0]}`;
    const answered = (message, applied) => {
      const error = new WriteRefused(message);
      if (method !== "GET" && applied) error.unknown = true;
      return error;
    };
    if (!res.ok) throw answered(`Bitbucket answered ${res.status} to ${where}`, res.status >= 500);
    try { return body ? JSON.parse(body) : null; } catch { throw answered(`Bitbucket answered ${where} with something other than JSON`, true); }
  }

  // The session's worktree, its Bitbucket origin and the write request in it.
  async function inspect({ sessionKey, agentId }) {
    if (!sessionKey) throw new WriteRefused("name the session whose writes to carry out");
    const entry = await getSessionEntry({ sessionKey, ...(agentId ? { agentId } : {}) });
    const cwd = worktreeOf(entry);
    if (!cwd) throw new WriteRefused("this session owns no managed worktree");
    const remote = parseBitbucketRemote(await wt(cwd, ["config", "--get", "remote.origin.url"]).catch(() => ""));
    if (!remote) throw new WriteRefused("this worktree's origin is not a Bitbucket repository");
    const gitDir = path.resolve(cwd, await wt(cwd, ["rev-parse", "--git-dir"]));
    let raw;
    try { raw = await readFile(path.join(gitDir, REQUEST_FILE)); } catch { throw new WriteRefused("this session has asked for no pull request writes"); }
    if (raw.length > MAX_REQUEST_BYTES) throw new WriteRefused("the write request is too large");
    const request = parseRequest(raw.toString("utf8"));
    if (request.owner !== undefined && request.owner !== remote.owner || request.repo !== undefined && request.repo !== remote.repo) {
      throw new WriteRefused("the write request names another repository than this worktree's origin");
    }
    return { ...request, ...remote, cwd, gitDir };
  }

  // What Bitbucket says about the pull request and the comments the
  // operations touch, checked against the request.
  async function verify(target, login) {
    const base = `/repositories/${encodeURIComponent(target.owner)}/${encodeURIComponent(target.repo)}/pullrequests/${target.pr}`;
    const pr = await bitbucket(login, "GET", base);
    if (pr?.id !== target.pr || String(pr?.destination?.repository?.full_name ?? "").toLowerCase() !== `${target.owner}/${target.repo}`.toLowerCase()) {
      throw new WriteRefused("Bitbucket returned another pull request than the one asked for");
    }
    if (pr.state !== "OPEN") throw new WriteRefused(`pull request #${target.pr} is ${String(pr.state ?? "unknown").toLowerCase()}, not open`);
    const head = String(pr?.source?.commit?.hash ?? "");
    if (!head || !(head.startsWith(target.head) || target.head.startsWith(head))) {
      throw new WriteRefused(`pull request #${target.pr} moved on since these writes were prepared: prepare them again for its current head`);
    }
    let me;
    for (const op of target.operations) {
      if (op.op === "pr.ready" && pr.draft !== true) throw new WriteRefused(`pull request #${target.pr} is not a draft`);
      if (op.op !== "comment.update" && op.op !== "comment.resolve") continue;
      const comment = await bitbucket(login, "GET", `${base}/comments/${op.comment}`);
      if (comment?.id !== op.comment || (comment?.pullrequest?.id !== undefined && comment.pullrequest.id !== target.pr)) {
        throw new WriteRefused(`comment ${op.comment} is not on pull request #${target.pr}`);
      }
      if (op.op === "comment.update") {
        me ??= await bitbucket(login, "GET", "/user");
        if (!me?.uuid || comment?.user?.uuid !== me.uuid) throw new WriteRefused(`comment ${op.comment} is not yours to edit`);
      }
    }
    return { base, head };
  }

  // A receipt, or a claim made before the first write of an intent.
  async function receiptExists(gitDir, intentId) {
    for (const name of [receiptName(intentId), claimName(intentId)]) {
      try { await readFile(path.join(gitDir, RECEIPTS_DIR, name)); return true; } catch {}
    }
    return false;
  }

  // Records that an intent's writes are starting. Exclusive: of two confirms
  // that both got this far, only one claims the intent.
  async function claim(gitDir, intentId) {
    const dir = path.join(gitDir, RECEIPTS_DIR);
    await mkdir(dir, { recursive: true, mode: 0o700 });
    try {
      await writeFile(path.join(dir, claimName(intentId)), new Date(now()).toISOString() + "\n", { mode: 0o600, flag: "wx" });
    } catch {
      throw new WriteRefused("these writes were started already: the tool reads their receipt or reconciles by reading the pull request");
    }
  }

  async function writeReceipt(gitDir, receipt) {
    const dir = path.join(gitDir, RECEIPTS_DIR);
    await mkdir(dir, { recursive: true, mode: 0o700 });
    const file = path.join(dir, receiptName(receipt.intentId));
    const temporary = `${file}.${randomUUID()}`;
    await writeFile(temporary, JSON.stringify(receipt, null, 2) + "\n", { mode: 0o600, flag: "wx" });
    await rename(temporary, file);
  }

  async function ask(action) {
    const { profileId, login } = await requester();
    if (!login) return { status: "not_connected" };
    const target = await inspect(action);
    if (await receiptExists(target.gitDir, target.intentId)) throw new WriteRefused("these writes were carried out already: the tool reads their receipt");
    await verify(target, login);
    for (const [id, r] of requests) if (r.expires <= now()) requests.delete(id);
    const request = { ...target, sessionKey: action.sessionKey, agentId: action.agentId, profileId, account: login.user, expires: now() + REQUEST_TTL_MS };
    request.requestDigest = digestOf(request);
    const requestId = randomUUID();
    requests.set(requestId, request);
    return {
      status: "needs_confirmation",
      requestId,
      confirmation: {
        account: login.user, name: login.name, requestDigest: request.requestDigest,
        owner: request.owner, repo: request.repo, pr: request.pr, head: request.head, intentId: request.intentId,
        operations: request.operations,
      },
    };
  }

  async function confirm(action) {
    const { profileId, login } = await requester();
    const requestId = String(action.payload?.requestId ?? "");
    const request = requests.get(requestId);
    if (!request || request.expires <= now()) {
      requests.delete(requestId);
      throw new WriteRefused("this write request has expired: ask again");
    }
    if (request.profileId !== profileId || request.sessionKey !== action.sessionKey) throw new WriteRefused("only the person who asked can confirm these writes");
    if (action.payload?.requestDigest !== request.requestDigest) throw new WriteRefused("the confirmation does not match the request: ask again");
    if (!login || login.user !== request.account) throw new WriteRefused("your Bitbucket login changed since you asked: ask again");
    requests.delete(requestId);
    // The request file must still say exactly what was confirmed.
    const current = await inspect(request);
    if (digestOf({ ...current, sessionKey: request.sessionKey, account: request.account }) !== request.requestDigest) {
      throw new WriteRefused("the write request changed since you asked: ask again");
    }
    const key = `${current.gitDir}\0${current.intentId}`;
    // The check and the reservation are synchronous, after the last await
    // that precedes any write, so two confirms close together cannot both
    // run; the exclusive claim below is the durable guard.
    if (running.has(key)) throw new WriteRefused("these writes are being carried out already");
    running.add(key);
    try {
      if (await receiptExists(current.gitDir, current.intentId)) throw new WriteRefused("these writes were carried out already: the tool reads their receipt");
      const { base, head } = await verify(current, login);
      await claim(current.gitDir, current.intentId);
      const results = [];
      let stopped = false;
      for (const [index, op] of current.operations.entries()) {
        if (stopped) { results.push({ index, op: op.op, status: "skipped" }); continue; }
        try {
          results.push({ index, op: op.op, status: "done", ...(await perform(login, base, op)) });
        } catch (error) {
          // A refusal (a 4xx, or a check before sending) is failed. An answer
          // that may follow an applied write, and anything unexpected after
          // sending, is unknown, never retried.
          const refused = error instanceof WriteRefused;
          results.push({ index, op: op.op, status: !refused || error.unknown ? "unknown" : "failed", error: refused ? error.message : "the write failed unexpectedly; it may or may not have happened" });
          stopped = true;
        }
      }
      const receipt = {
        version: 1, intentId: current.intentId, account: login.user, owner: current.owner, repo: current.repo,
        pr: current.pr, head, requestDigest: request.requestDigest, completed: !stopped, results,
      };
      await writeReceipt(current.gitDir, receipt);
      return { status: stopped ? "stopped" : "done", receipt };
    } finally {
      running.delete(key);
    }
  }

  async function perform(login, base, op) {
    switch (op.op) {
      case "comment.create": {
        const created = await bitbucket(login, "POST", `${base}/comments`, {
          content: { raw: op.text },
          ...(op.inline ? { inline: op.inline } : {}),
          ...(op.parent ? { parent: { id: op.parent } } : {}),
        });
        if (!positive(created?.id)) {
          const unknown = new WriteRefused("Bitbucket accepted the comment but returned no id");
          unknown.unknown = true;
          throw unknown;
        }
        return { comment: created.id };
      }
      case "comment.update":
        await bitbucket(login, "PUT", `${base}/comments/${op.comment}`, { content: { raw: op.text } });
        return { comment: op.comment };
      case "comment.resolve":
        await bitbucket(login, "POST", `${base}/comments/${op.comment}/resolve`);
        return { comment: op.comment };
      case "pr.ready":
        await bitbucket(login, "PUT", base, { draft: false });
        return {};
      case "pr.update":
        await bitbucket(login, "PUT", base, {
          ...(op.title !== undefined ? { title: op.title } : {}),
          ...(op.description !== undefined ? { description: op.description } : {}),
        });
        return {};
    }
    throw new WriteRefused("unsupported operation");
  }

  const handle = (fn) => async (action) => {
    try {
      return { ok: true, result: await fn(action) };
    } catch (error) {
      if (error instanceof WriteRefused) return { ok: false, error: error.message, code: "refused" };
      throw error;
    }
  };
  return { ask: handle(ask), confirm: handle(confirm), requests };
}
