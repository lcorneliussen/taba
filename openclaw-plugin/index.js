// wtc for OpenClaw: a collection is the conversation a task started in plus,
// per repository, a gateway-managed worktree held by an owner session (hidden
// unless someone wants a conversation per repository). The gateway knows the
// sessions; `wtc` knows the checkouts and services. This joins the two.
//
// Plain ESM, no build step and no SDK import. Needs `wtc` on the gateway's
// PATH, and `git`; pull requests also need `gh` with the gateway's login.
// Bitbucket repositories use the logins of the Bitbucket plugin
// (openclaw-bitbucket) when it is installed.
import { createHash } from "node:crypto";
import { existsSync, mkdirSync, readFileSync, readdirSync, renameSync, writeFileSync } from "node:fs";
import path from "node:path";
import { startSandboxGit } from "./sandbox-git.js";

const PLUGIN_ID = "wtc";
// A worker's collection is <task>--<worker> (openclaw.WorkerSeparator).
const WORKER_SEPARATOR = "--";

// The client each tunnel provider of wtc svc tunnel runs, and the providers
// whose tunnel anyone with the link can open.
const TUNNEL_PROGRAMS = { tailscale: "tailscale", devtunnel: "devtunnel", cloudflare: "cloudflared", ngrok: "ngrok" };
const PUBLIC_TUNNELS = new Set(["cloudflare", "ngrok"]);
// The wtc this plugin came from: wtc openclaw install stamps its version.
const PLUGIN_VERSION = (() => {
  try {
    return JSON.parse(readFileSync(new URL("./package.json", import.meta.url), "utf8")).version ?? null;
  } catch {
    return null;
  }
})();

// The version wtc --version names, or null.
export const wtcVersionOf = (output) => /^wtc version (\S+)/.exec(String(output ?? "").trim())?.[1] ?? null;

// What to say when the wtc on the gateway's PATH is a release other than the
// one this plugin came from, as after upgrading wtc without reinstalling the
// plugin. A dev build on either side says nothing: a wtc without a release
// version stamps the plugin with a -dev version (describeBuild in cmd/taba),
// or 0.0.0-dev.
export function versionWarning(pluginVersion, wtcVersionOutput) {
  const m = /^wtc version v?(\d+\.\d+\.\d+)$/.exec(String(wtcVersionOutput ?? "").trim());
  if (!m || !/^\d+\.\d+\.\d+$/.test(String(pluginVersion ?? "")) || m[1] === pluginVersion) return null;
  return `This card is the taba ${pluginVersion} plugin, but the gateway runs wtc ${m[1]}: run "wtc openclaw install" on the gateway host.`;
}

// Native records are authoritative; directory absence alone does not establish recovery.
function worktreeRecovery(entry, sessionKey, records) {
  const matches = records.filter(r => r.id === entry.worktree?.id);
  const r = matches.length === 1 ? matches[0] : null;
  if (!r || r.ownerKind !== "session" || r.ownerId !== sessionKey ||
      r.path !== worktreeOf(entry) || r.repoRoot !== entry.worktree?.repoRoot ||
      r.branch !== entry.worktree?.branch) return { state: "unknown", reason: "Native worktree ownership or location could not be verified" };
  if (!Number.isFinite(r.removedAt)) return { state: "unknown", reason: "The native record is not evicted; inspect the missing checkout" };
  if (!r.snapshotRef) return { state: "unavailable", reason: "No recovery snapshot is recorded" };
  const key = createHash("sha256").update(JSON.stringify([r.id, r.ownerId, r.path, r.repoRoot, r.branch, r.removedAt, r.snapshotRef])).digest("hex");
  return { state: "restorable", key, removedAt: r.removedAt };
}

const onPath = (program) => String(process.env.PATH ?? "").split(path.delimiter).some((dir) => dir && existsSync(path.join(dir, program)));

const warnedEnvConflicts = new Set();

// Read both spellings without exposing values, especially API keys, in warnings.
export function productEnv(name, env = process.env, warn = console.warn) {
  const suffix = /^(?:WTC|TABA)_(.+)$/.exec(name)?.[1];
  if (!suffix || /^(?:TEST_|DEV_)/.test(suffix)) return env[name];
  const current = `TABA_${suffix}`, legacy = `WTC_${suffix}`;
  if (!Object.hasOwn(env, current)) return env[legacy];
  if (Object.hasOwn(env, legacy) && env[current] !== env[legacy] && !warnedEnvConflicts.has(legacy)) {
    warnedEnvConflicts.add(legacy);
    warn(`wtc: ${current} and ${legacy} differ; using ${current}`);
  }
  return env[current];
}

// A Bitbucket repository as a person types it: a bitbucket.org URL in https
// or scp form, or bitbucket.org/<workspace>/<repo>.
export function parseBitbucketRepo(spec) {
  const m = String(spec ?? "").trim().match(/^(?:https:\/\/(?:[^@/]+@)?|git@)?bitbucket\.org[/:]([A-Za-z0-9_.-]+)\/([A-Za-z0-9_.-]+?)(?:\.git)?\/?$/);
  if (!m || [m[1], m[2]].some((part) => part === "." || part === "..")) return null;
  return { workspace: m[1], repo: m[2], cloneUrl: `https://bitbucket.org/${m[1]}/${m[2]}.git`, webUrl: `https://bitbucket.org/${m[1]}/${m[2]}` };
}

// Repository identity only: credentials, custom ports and access never leave this read.
export function repositoryOrigin(remote) {
  const text = String(remote ?? "").trim();
  const scp = text.match(/^(?:[^/@:\s]+@)?(github\.com|bitbucket\.org):(.+)$/i);
  try {
    const url = new URL(scp ? `ssh://${scp[1]}/${scp[2]}` : text);
    if (!["https:", "http:", "ssh:"].includes(url.protocol) || !["github.com", "bitbucket.org"].includes(url.hostname.toLowerCase())) return {};
    const parts = url.pathname.match(/^\/([A-Za-z0-9_.-]+)\/([A-Za-z0-9_.-]+?)(?:\.git)?\/?$/);
    return parts && ![parts[1], parts[2]].some(p => p === "." || p === "..") ? { forge: url.hostname.toLowerCase() === "github.com" ? "github" : "bitbucket", owner: parts[1], repo: parts[2] } : {};
  } catch { return {}; }
}

// The person a session belongs to, as the Bitbucket plugin decides it: the
// assigned human owner, otherwise the signed-in person who created it, for a
// spawned session that of its parent.
async function sessionOwnerProfileId(getSessionEntry, sessionKey, agentId) {
  let key = sessionKey;
  for (let depth = 0; key && depth <= 5; depth += 1) {
    const entry = await getSessionEntry({ sessionKey: key, ...(agentId ? { agentId } : {}) });
    if (!entry) return undefined;
    if (entry.owner?.actor?.type === "human") return entry.owner.actor.id || undefined;
    const creator = entry.createdActor;
    if (creator?.type === "human") return creator.source === "profile" ? creator.id || undefined : undefined;
    key = entry.parentSessionKey ?? entry.spawnedBy;
  }
  return undefined;
}

// That person's Bitbucket login, from the file the Bitbucket plugin keeps
// for its credential helper: e-mail and API token.
function bitbucketLogin(stateDir, profileId) {
  if (!profileId) return undefined;
  const digest = createHash("sha256").update(profileId).digest("hex").slice(0, 40);
  try {
    const login = {};
    for (const line of readFileSync(path.join(stateDir, "plugins", "bitbucket", "logins", `${digest}.login`), "utf8").split("\n")) {
      const at = line.indexOf("=");
      if (at > 0) login[line.slice(0, at)] = line.slice(at + 1);
    }
    return login.profile === profileId && login.user && login.token ? login : undefined;
  } catch {
    return undefined;
  }
}

// Bitbucket lists repositories per workspace only: the person's workspaces
// first, then the repositories they are a member of in each.
export async function bitbucketRepos(login, query) {
  const get = async (url) => {
    const response = await fetch(`https://api.bitbucket.org/2.0/${url}`, {
      headers: { accept: "application/json", authorization: `Basic ${Buffer.from(`${login.user}:${login.token}`).toString("base64")}` },
      redirect: "error",
      signal: AbortSignal.timeout(15000),
    });
    if (!response.ok) throw new Error(`Bitbucket answered ${response.status}`);
    return response.json();
  };
  const workspaces = ((await get("user/workspaces?pagelen=10")).values ?? []).map((w) => w.workspace?.slug).filter(Boolean);
  const params = new URLSearchParams({ role: "member", pagelen: "20", sort: "-updated_on" });
  const q = String(query ?? "").replace(/["\\]/g, "").trim();
  if (q) params.set("q", `full_name ~ "${q}"`);
  const pages = await Promise.all(workspaces.map((w) => get(`repositories/${encodeURIComponent(w)}?${params}`).catch(() => ({ values: [] }))));
  return pages.flatMap((page) => page.values ?? []).map((r) => ({
    forge: "bitbucket",
    name: r.name ?? r.slug,
    fullName: r.full_name,
    cloneUrl: `https://bitbucket.org/${r.full_name}.git`,
    webUrl: r.links?.html?.href ?? `https://bitbucket.org/${r.full_name}`,
    private: Boolean(r.is_private),
    description: r.description || "",
  })).slice(0, 20);
}

// The folder of the collection called name, as wtc makes it: wtc/<harness
// or ad-hoc>/<name>, or wtc-collections/<name> from before there was one per
// harness.
export function collectionFolder(stateDir, name) {
  const legacy = path.join(stateDir, "wtc-collections", name);
  if (existsSync(legacy)) return legacy;
  let groups = [];
  try {
    groups = readdirSync(path.join(stateDir, "wtc")).sort();
  } catch {}
  for (const group of groups) {
    const dir = path.join(stateDir, "wtc", group, name);
    if (existsSync(dir)) return dir;
  }
  return null;
}

// What the conversation is told about a pull request: the facts of it that
// change, as wtc status reports them.
const prFacts = (pr) => (pr ? {
  number: pr.number, url: pr.url, state: pr.state, draft: !!pr.draft, checks: pr.checks ?? "none",
  failed: pr.failed ?? 0, review: pr.review ?? "", mergeable: pr.mergeable ?? "",
} : null);

// What changed on a repository's pull request between two looks, one line
// each. prev is undefined before the first look, which has nothing to compare
// with, and null while there was no pull request.
export function pullRequestChanges(name, prev, cur) {
  if (prev === undefined || !cur) return [];
  const pr = `#${cur.number} (${cur.url})`;
  if (!prev || prev.number !== cur.number) {
    const what = cur.state === "merged" ? "was opened and merged" : cur.state === "closed" ? "was opened and closed" : cur.draft ? "opened as a draft" : "opened";
    return [`${name}: pull request ${pr} ${what}.`];
  }
  const lines = [];
  if (prev.state !== cur.state) {
    if (cur.state === "merged") lines.push(`${name}: pull request ${pr} was merged.`);
    else if (cur.state === "closed") lines.push(`${name}: pull request ${pr} was closed without merging.`);
    else if (cur.state === "open") lines.push(`${name}: pull request ${pr} was reopened.`);
  }
  if (cur.state !== "open") return lines;
  if (prev.draft !== cur.draft) lines.push(`${name}: pull request ${pr} ${cur.draft ? "went back to draft" : "is ready for review"}.`);
  if (prev.checks !== cur.checks) {
    if (cur.checks === "failure") lines.push(`${name}: checks failed on ${pr}${cur.failed ? ` (${cur.failed} failed)` : ""}.`);
    else if (cur.checks === "success") lines.push(`${name}: checks passed on ${pr}${prev.checks === "failure" ? " again" : ""}.`);
  }
  if (prev.review !== cur.review) {
    if (cur.review === "approved") lines.push(`${name}: pull request ${pr} was approved.`);
    else if (cur.review === "changes_requested") lines.push(`${name}: a review requested changes on ${pr}.`);
  }
  if (prev.mergeable !== cur.mergeable && cur.mergeable === "conflicting") lines.push(`${name}: pull request ${pr} conflicts with its base.`);
  return lines;
}

// The pull requests seen in each collection, by agent and the collection's
// session, kept in the gateway's state so that a restart does not tell the
// same news again. A collection not looked at for a while is forgotten.
const FORGET_MS = 14 * 24 * 3600 * 1000;
export function pullRequestWatch(file, now = Date.now) {
  let seen = {};
  try {
    seen = JSON.parse(readFileSync(file, "utf8")) ?? {};
  } catch {}
  const save = () => {
    for (const [key, c] of Object.entries(seen)) if (now() - (c.at ?? 0) > FORGET_MS) delete seen[key];
    try {
      mkdirSync(path.dirname(file), { recursive: true });
      writeFileSync(`${file}.tmp`, JSON.stringify(seen));
      renameSync(`${file}.tmp`, file);
    } catch {}
  };
  return {
    // Records a look at a collection's repositories; returns what changed.
    observe(agentId, sessionKey, repos) {
      const key = `${agentId} ${sessionKey}`;
      const c = (seen[key] ??= { agentId, sessionKey, repos: {} });
      c.at = now();
      const lines = [];
      for (const r of repos) {
        if (!r.path || r.error) continue;
        const prev = c.repos[r.path];
        const cur = prFacts(r.pullRequest);
        // A pull request does not go away: none after one was seen means
        // the forge could not be asked this time. (wtc status reports both
        // that and no pull request yet as none.)
        if (!cur && prev) continue;
        lines.push(...pullRequestChanges(r.repo ?? r.project ?? path.basename(r.path), prev, cur));
        c.repos[r.path] = cur;
      }
      save();
      return lines;
    },
    // The collections with a pull request still open, to look at again.
    open() {
      return Object.values(seen).filter((c) => Object.values(c.repos).some((pr) => pr?.state === "open"));
    },
    forget(agentId, sessionKey) {
      delete seen[`${agentId} ${sessionKey}`];
      save();
    },
  };
}

// Words that say nothing about where to work.
const STOPWORDS = new Set("a an and are as at be but by can do for from get go have help i in into is it its lets let's look make me my need of on or our please should so some start that the this to up us want we what when where which will with work working would you".split(" "));
const wordsOf = (text) => String(text ?? "").toLowerCase().split(/[^a-z0-9]+/).filter((w) => w.length > 1 && !STOPWORDS.has(w));

// Where a message asks to work, by its words: the wtc project whose
// repositories' names, then roles, it names, and those repositories. A
// repository's name counts three times its role; one that scores less than
// half of its project's best is left out. null when nothing matches.
export function matchProjects(text, projects) {
  const words = new Set(wordsOf(text));
  if (words.size === 0) return null;
  let best = null;
  for (const p of projects ?? []) {
    // A repository counts by what sets it apart in its project ("wtc" names
    // every repository of project wtc), each word once.
    const shared = new Set(wordsOf(p.name));
    const scored = (p.repos ?? []).map((r) => {
      const hits = (text) => new Set(wordsOf(text).filter((w) => words.has(w) && !shared.has(w))).size;
      // A repository named just like its project still has its name.
      const own = wordsOf(r.name).every((w) => shared.has(w));
      const name = own ? new Set(wordsOf(r.name).filter((w) => words.has(w))).size : hits(r.name);
      const role = hits(r.role);
      return { name: r.name, role: r.role ?? "", score: 3 * name + role };
    }).filter((r) => r.score > 0).sort((a, b) => b.score - a.score);
    // Naming the project itself counts too.
    const named = wordsOf(p.name).some((w) => words.has(w)) ? 1 : 0;
    const top = (scored[0]?.score ?? 0) + named;
    // Only a project with a repository to start in can be the answer.
    if (scored.length && (!best || top > best.score)) {
      best = { project: p.name, repos: scored.filter((r) => r.score * 2 >= scored[0].score).map((r) => r.name), score: top };
    }
  }
  return best ? { project: best.project, repos: best.repos, source: "match" } : null;
}

// The same through a decision model (the gateway's, or openaiDecisions): one
// choice among every project's repositories, and "none". Repositories within
// half of the chosen one's probability, in its project, come along. null when
// there is no decision model or it is unavailable; { none: true } when it
// found the message to be about none of them.
export async function decideProjects(decisions, agentId, text, projects) {
  if (!decisions?.evaluate && !decisions?.evaluateV2) return null;
  const criteria = { none: "The message is not about working on any of these repositories" };
  const where = {};
  for (const p of projects ?? []) {
    for (const r of p.repos ?? []) {
      const label = `${p.name}/${r.name}`;
      criteria[label] = `Work on ${r.name}${r.role ? `: ${r.role}` : ""} (wtc project ${p.name})`;
      where[label] = { project: p.name, repo: r.name };
    }
  }
  if (Object.keys(where).length === 0) return null;
  const message = String(text).slice(0, 2000);
  const questions = { where: { type: "choice", instructions: "Which repository does this message ask to work on, look into or change?", criteria } };
  const options = { agentId, purpose: "wtc.suggest-project", rubricVersion: "1", timeoutMs: 3000 };
  let outcome;
  try {
    // Contract 2 takes typed evidence; contract 1 any JSON, which a
    // classifier reads as text, so the message goes as plain text.
    outcome = decisions.evaluateV2
      ? await decisions.evaluateV2({ state: { type: "text", text: message }, questions }, options)
      : await decisions.evaluate({ state: message, questions }, options);
  } catch {
    return null;
  }
  const answer = (outcome?.answers ?? outcome?.result?.answers)?.where;
  const chosen = answer?.choice;
  if ((outcome?.status && outcome.status !== "ok") || !chosen) return null;
  if (chosen === "none") return { none: true };
  if (!where[chosen]) return null;
  const { project } = where[chosen];
  const p = answer.probabilities ?? {};
  const repos = Object.keys(where)
    .filter((l) => where[l].project === project && (l === chosen || (p[l] ?? 0) * 2 >= (p[chosen] ?? 1)))
    .sort((a, b) => (a === chosen ? -1 : b === chosen ? 1 : (p[b] ?? 0) - (p[a] ?? 0)))
    .map((l) => where[l].repo);
  return { project, repos, source: "decision" };
}

// OpenAI's Decisions API, called directly, in the shape of OpenClaw's
// decisions runtime (evaluateV2), for gateways whose OpenClaw has no decision
// models yet (#159). Only choice questions, which is all the suggestion asks.
export function openaiDecisions({ apiKey, model = "gpt-6-luna", baseUrl = "https://api.openai.com/v1", fetchImpl = fetch }) {
  return {
    async evaluateV2(batch, options = {}) {
      const questions = Object.entries(batch.questions).map(([name, q]) => ({
        type: "choice", name, instructions: q.instructions,
        choices: Object.entries(q.criteria).map(([value, description]) => ({ value, description })),
      }));
      let response;
      try {
        response = await fetchImpl(`${baseUrl}/decisions`, {
          method: "POST",
          headers: { authorization: `Bearer ${apiKey}`, "content-type": "application/json" },
          body: JSON.stringify({ model, input: batch.state?.text ?? "", questions }),
          signal: AbortSignal.timeout(options.timeoutMs ?? 3000),
          redirect: "error",
        });
      } catch {
        return { status: "unavailable", reason: "transport" };
      }
      if (!response.ok) return { status: "unavailable", reason: response.status === 401 || response.status === 403 ? "authentication" : `http-${response.status}` };
      const body = await response.json().catch(() => null);
      const answers = {};
      for (const [i, [name]] of Object.entries(batch.questions).entries()) {
        const a = body?.answers?.[i];
        if (a?.name !== name || a.type !== "choice" || !Array.isArray(a.probabilities)) return { status: "unavailable", reason: "invalid-response" };
        answers[name] = { type: "choice", choice: a.choice, probabilities: Object.fromEntries(a.probabilities.map((x) => [x.value, x.probability])) };
      }
      return { status: "ok", result: { model: body.model ?? model, answers } };
    },
  };
}

// The git identity the agent's own commits get, for a merge the plugin makes
// on its behalf: its managed GitHub identity's gitAuthor (an agent with its
// own tools.github uses that one's, even without a gitAuthor; others the
// system's), else its name as <slug>@localhost, else OpenClaw's.
export function agentGitIdentity(config, agentId) {
  const entry = config?.agents?.entries?.[agentId] ?? {};
  const author = entry.tools?.github ? entry.tools.github.gitAuthor : config?.tools?.github?.gitAuthor;
  const name = String(entry.identity?.name ?? entry.name ?? "").trim();
  const slug = name.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "");
  const fallback = slug ? { name, email: `${slug}@localhost` } : { name: "OpenClaw", email: "openclaw@localhost" };
  const id = { name: author?.name || fallback.name, email: author?.email || fallback.email };
  return { GIT_AUTHOR_NAME: id.name, GIT_AUTHOR_EMAIL: id.email, GIT_COMMITTER_NAME: id.name, GIT_COMMITTER_EMAIL: id.email };
}

// A child that owns a repository has a managed worktree to look at.
const worktreeOf = (entry) => (entry?.worktree ? entry.workspaceDir || entry.spawnedCwd || null : null);

export default {
  id: PLUGIN_ID,
  name: "taba",
  register(api) {
    // Without a runtime (the openclaw CLI's metadata capture, setup of a
    // disabled plugin) the session actions and services cannot work.
    if (api.registrationMode === "cli-metadata" || api.registrationMode === "setup-only") return;
    const run = async (argv, options = {}) => {
      const r = await api.runtime.system.runCommandWithTimeout(argv, { timeoutMs: 60000, killProcessTree: true, ...options });
      return { code: r.code ?? r.exitCode ?? null, stdout: String(r.stdout ?? ""), stderr: String(r.stderr ?? "") };
    };

    // Older supported hosts expose only the legacy readers. Select by
    // capability; a failed async read must never retry synchronously.
    const sessions = api.runtime.agent.session;
    const getSessionEntry = async (params) => typeof sessions.getSessionEntryAsync === "function"
      ? sessions.getSessionEntryAsync(params) : sessions.getSessionEntry(params);
    const listSessionEntries = async (params) => typeof sessions.listSessionEntriesAsync === "function"
      ? sessions.listSessionEntriesAsync(params) : sessions.listSessionEntries(params);

    // The collection around a session: itself when it has repository
    // children; otherwise its parent's when it is a repository's own session
    // (it has a worktree). A new conversation has a parent too (the
    // dashboard's, often the main session) and no repositories yet: it is a
    // collection of its own, still empty.
    const collectionOf = async (agentId, sessionKey) => {
      const all = await listSessionEntries({ agentId });
      const entryOf = new Map(all.map((s) => [s.sessionKey, s.entry]));
      const childrenOf = (key) => all.filter((s) => s.entry?.parentSessionKey === key && worktreeOf(s.entry));
      let parentKey = sessionKey;
      let children = childrenOf(parentKey);
      const parentOfSelf = entryOf.get(sessionKey)?.parentSessionKey;
      if (children.length === 0 && parentOfSelf && worktreeOf(entryOf.get(sessionKey))) {
        parentKey = parentOfSelf;
        children = childrenOf(parentKey);
      }
      return { parentKey, parent: entryOf.get(parentKey), children };
    };

    const collectionIdentity = (children) => {
      const collectionName = (entry) => String(entry.worktree?.branch ?? "").replace(/^openclaw\//, "");
      const names = [...new Set(children.map((c) => collectionName(c.entry)).filter(Boolean))];
      // The collection is the one wtc made for this conversation: wtc labels
      // the owners it spawns "<collection> <repo>". A child whose worktree is
      // another one's is a sub-session (a wtc worker, <collection>--<worker>,
      // or a session OpenClaw or the agent spawned with a worktree of its
      // own, named for its change). Its checkouts are shown under the
      // collection's, not as more of its repositories, and ✕ may delete it:
      // so a name another one extends with "--" is never taken for the
      // collection.
      const count = (n) => children.filter((c) => collectionName(c.entry) === n).length;
      const madeByWtc = (c, n) => c.entry.projectId ? c.entry.label === `${n} ${c.entry.projectId}` : String(c.entry.label ?? "").startsWith(n + " ");
      const made = names.filter((n) => children.some((c) => collectionName(c.entry) === n && madeByWtc(c, n)));
      const pool = made.length ? made : names;
      const extendsAnother = (n) => pool.some((m) => m !== n && n.startsWith(m + WORKER_SEPARATOR));
      const task = pool.filter((n) => !extendsAnother(n)).sort((a, b) => count(b) - count(a) || a.length - b.length)[0] ?? pool[0];
      return { collectionName, names, task };
    };

    // wtc --version, asked at most once a minute: requests that overlap
    // share the one that is running.
    let wtcVersion = { at: 0, output: null };
    const wtcVersionOutput = () => {
      if (Date.now() - wtcVersion.at > 60000) {
        wtcVersion = {
          at: Date.now(),
          output: run(["wtc", "--version"], { timeoutMs: 10000 }).then((r) => r.stdout, () => ""),
        };
      }
      return wtcVersion.output;
    };

    // Coalesce missing-checkout reads. Native listing may reconcile its own registry,
    // but this plugin never restores, clones or touches activity while reading status.
    let recoveryCache;
    const recoveryRecords = (fresh = false) => {
      if (fresh || !recoveryCache || Date.now() - recoveryCache.at > 60000) {
        recoveryCache = { at: Date.now(), value: run(["openclaw", "worktrees", "list", "--json"], { timeoutMs: 30000 }).then(r => {
          if (r.code !== 0) throw Error("Native worktree recovery status unavailable");
          const out = JSON.parse(r.stdout);
          if (!Array.isArray(out.worktrees)) throw Error("Native worktree recovery status unavailable");
          return out.worktrees;
        }) };
      }
      return recoveryCache.value;
    };
    const restoring = new Set();
    api.session.controls.registerSessionAction({
      id: "restore-checkout",
      description: "Restore one evicted collection checkout from its native snapshot, preserving its recorded owner and destination.",
      requiredScopes: ["operator.admin"],
      async handler(action) {
        const { ownerSessionKey, key } = action.payload ?? {};
        if (!action.agentId || !action.sessionKey || typeof ownerSessionKey !== "string" || typeof key !== "string") return { ok: false, error: "A collection checkout and recovery key are required" };
        const { children } = await collectionOf(action.agentId, action.sessionKey);
        const { collectionName, task } = collectionIdentity(children);
        const child = children.find(c => c.sessionKey === ownerSessionKey && collectionName(c.entry) === task);
        if (!child?.entry.worktree?.id) return { ok: false, error: "The checkout is no longer in this collection" };
        const entry = child.entry, id = entry.worktree.id;
        if (restoring.has(id)) return { ok: false, error: "This checkout is already being restored" };
        restoring.add(id);
        try {
          if (existsSync(path.join(worktreeOf(entry), ".git"))) return { ok: false, error: "The checkout is already present; refresh the collection" };
          const current = worktreeRecovery(entry, ownerSessionKey, await recoveryRecords(true));
          if (current.state !== "restorable" || current.key !== key) return { ok: false, error: "Recovery changed; refresh the collection before restoring" };
          const fresh = await getSessionEntry({ agentId: action.agentId, sessionKey: ownerSessionKey });
          if (!fresh || fresh.parentSessionKey !== entry.parentSessionKey || fresh.worktree?.id !== id || worktreeOf(fresh) !== worktreeOf(entry) || fresh.worktree?.repoRoot !== entry.worktree.repoRoot || fresh.worktree?.branch !== entry.worktree.branch) return { ok: false, error: "The checkout owner changed; refresh before restoring" };
          if (fresh.hasActiveRun || fresh.hasActiveSubagentRun || fresh.activeRunIds?.length) return { ok: false, error: "Wait for this checkout's active run to finish before restoring" };
          const result = await run(["openclaw", "worktrees", "restore", id, "--json"], { timeoutMs: 300000 });
          if (result.code !== 0) return { ok: false, error: (result.stderr || result.stdout).trim().slice(-600) || "Native restore failed; refresh and inspect the saved snapshot" };
          const restored = JSON.parse(result.stdout);
          if (restored.id !== id || restored.path !== worktreeOf(entry) || restored.ownerId !== ownerSessionKey || restored.branch !== entry.worktree.branch || restored.removedAt != null || !existsSync(path.join(worktreeOf(entry), ".git"))) return { ok: false, error: "Native restore returned an unexpected result; inspect the checkout before retrying" };
          return { ok: true, result: { message: "Checkout restored from its saved snapshot" } };
        } catch (err) { return { ok: false, error: String(err?.message ?? err) }; }
        finally { restoring.delete(id); recoveryCache = null; }
      },
    });

    const stateDir = api.runtime.state.resolveStateDir(process.env);

    // Repositories from the accounts the person connected that the gateway
    // has no search for itself. GitHub's is the gateway's projects.searchRemote.
    api.session.controls.registerSessionAction({
      id: "repo-search",
      description: "Search the Bitbucket repositories of the person this session belongs to, with their Bitbucket login.",
      requiredScopes: ["operator.read"],
      async handler(action) {
        const login = bitbucketLogin(stateDir, await sessionOwnerProfileId(getSessionEntry, action.sessionKey, action.agentId));
        if (!login) return { ok: true, result: { bitbucket: { connected: false }, repos: [] } };
        try {
          return { ok: true, result: { bitbucket: { connected: true, user: login.user }, repos: await bitbucketRepos(login, action.payload?.query) } };
        } catch (err) {
          return { ok: true, result: { bitbucket: { connected: true, user: login.user, error: String(err?.message ?? err) }, repos: [] } };
        }
      },
    });

    // The branches of a repository and its default, for choosing the branch a
    // collection works against before adding it. Read with git, so GitHub
    // and Bitbucket behave the same, with the gateway's GitHub login and the
    // person's Bitbucket login for private repositories.
    api.session.controls.registerSessionAction({
      id: "repo-branches",
      description: "List the branches of a GitHub or Bitbucket repository, or of a project on the gateway.",
      requiredScopes: ["operator.read"],
      async handler(action) {
        let url = String(action.payload?.url ?? "");
        let cwd;
        const project = String(action.payload?.project ?? "");
        if (project) {
          if (!/^[A-Za-z0-9._-]+$/.test(project) || project === "." || project === "..") return { ok: false, error: "not a project name" };
          const dirs = [path.join(stateDir, "repos", project)];
          try {
            for (const hash of readdirSync(path.join(stateDir, "projects"))) dirs.unshift(path.join(stateDir, "projects", hash, project));
          } catch { /* no clones yet */ }
          cwd = dirs.find((d) => existsSync(path.join(d, ".git")));
          if (!cwd) return { ok: false, error: `${project} is not on the gateway` };
          // Only the URL is read from the clone, whose config an agent can
          // write to; git then runs outside it, with the gateway's config.
          url = (await run(["git", "config", "--file", path.join(cwd, ".git", "config"), "--get", "remote.origin.url"])).stdout.trim();
        }
        if (!/^https:\/\/(github\.com|bitbucket\.org)\/[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+?(\.git)?$/.test(url)) {
          return { ok: false, error: cwd ? `${project}'s origin is not a GitHub or Bitbucket https URL` : "give a GitHub or Bitbucket https URL" };
        }
        const origin = url;
        const env = { GIT_TERMINAL_PROMPT: "0" };
        let auth = [];
        if (/github\.com/.test(origin)) {
          auth = ["-c", "credential.helper=", "-c", "credential.helper=!gh auth git-credential"];
        } else if (/bitbucket\.org/.test(origin)) {
          const login = bitbucketLogin(stateDir, await sessionOwnerProfileId(getSessionEntry, action.sessionKey, action.agentId));
          if (login) {
            auth = ["-c", "credential.helper=", "-c", "credential.helper=!f() { [ \"$1\" = get ] && printf 'username=x-bitbucket-api-token-auth\\npassword=%s\\n' \"$WTC_BITBUCKET_TOKEN\"; }; f"];
            env.WTC_BITBUCKET_TOKEN = login.token;
          }
        }
        const r = await run(["git", ...auth, "ls-remote", "--symref", url, "HEAD", "refs/heads/*"], { timeoutMs: 30000, env });
        if (r.code !== 0) return { ok: false, error: `cannot read the branches: ${r.stderr.trim().split("\n").pop()}` };
        const lines = r.stdout.split("\n");
        const head = lines.map((l) => l.match(/^ref: refs\/heads\/(\S+)\s+HEAD$/)).find(Boolean)?.[1] ?? null;
        const branches = lines.map((l) => l.match(/^[0-9a-f]{40}\s+refs\/heads\/(\S+)$/)?.[1]).filter(Boolean);
        return { ok: true, result: { defaultBranch: head, branches } };
      },
    });

    // Clones a Bitbucket repository onto the gateway, with the person's login
    // when the repository is private. The gateway's own projects.add takes
    // GitHub only; the Control UI registers the clone with projects.register.
    api.session.controls.registerSessionAction({
      id: "clone-repo",
      description: "Clone a Bitbucket repository onto the gateway for this session's collection.",
      requiredScopes: ["operator.write"],
      async handler(action) {
        const repo = parseBitbucketRepo(action.payload?.url);
        if (!repo) return { ok: false, error: "Give a Bitbucket repository as bitbucket.org/<workspace>/<repo> or its URL." };
        const dir = path.join(stateDir, "repos", repo.repo);
        if (existsSync(dir)) {
          const origin = await run(["git", "-C", dir, "remote", "get-url", "origin"]);
          const known = parseBitbucketRepo(origin.stdout.trim());
          if (origin.code !== 0 || known?.workspace.toLowerCase() !== repo.workspace.toLowerCase() || known?.repo.toLowerCase() !== repo.repo.toLowerCase()) {
            return { ok: false, error: `${dir} already holds another repository.` };
          }
          return { ok: true, result: { path: dir, name: repo.repo, cloned: false } };
        }
        const login = bitbucketLogin(stateDir, await sessionOwnerProfileId(getSessionEntry, action.sessionKey, action.agentId));
        // The token reaches git through its environment, never a URL or a config file.
        const auth = login ? ["-c", "credential.helper=", "-c", "credential.helper=!f() { [ \"$1\" = get ] && printf 'username=x-bitbucket-api-token-auth\\npassword=%s\\n' \"$WTC_BITBUCKET_TOKEN\"; }; f"] : [];
        const r = await run(["git", ...auth, "clone", "--quiet", repo.cloneUrl, dir], {
          timeoutMs: 300000,
          env: { GIT_TERMINAL_PROMPT: "0", ...(login ? { WTC_BITBUCKET_TOKEN: login.token } : {}) },
        });
        if (r.code !== 0) {
          return { ok: false, error: `git clone ${repo.cloneUrl} failed${login ? "" : " (no Bitbucket login: connect one on the Bitbucket tab for private repositories)"}: ${r.stderr.trim().split("\n").pop()}` };
        }
        return { ok: true, result: { path: dir, name: repo.repo, cloned: true } };
      },
    });

    // The pull requests of the collections, and how the conversation hears
    // that one changed, as pullRequestEvents says: queued for its next turn
    // and, with wake (the default), a turn now.
    const watch = pullRequestWatch(path.join(stateDir, "plugins", PLUGIN_ID, "pull-requests.json"));
    const tell = (agentId, sessionKey, lines) => {
      const mode = api.pluginConfig?.pullRequestEvents ?? "wake";
      if (lines.length === 0 || mode === "off") return;
      // A heartbeat turn tends to find nothing to do: say what is.
      const text = [
        "wtc: pull request news for this collection",
        ...lines.map((l) => `- ${l}`),
        // What to do about each is the wtc-follow skill's.
        "Act on it with the wtc-follow skill, and bring the task's progress up to date.",
        "Tell the person only what they need to act on.",
      ].join("\n");
      try {
        api.runtime.system.enqueueSystemEvent(text, { agentId, sessionKey });
        // Now, as the gateway's own system event with --mode now does: an
        // "event" wake waits for the agent's next heartbeat to be due. The
        // turn answers in the conversation, not in the operator's direct
        // messages.
        if (mode === "wake") api.runtime.system.requestHeartbeat?.({ source: "other", intent: "immediate", reason: "wtc-pull-request", agentId, sessionKey, heartbeat: { target: "last" } });
        api.logger?.info?.(`wtc: told ${sessionKey} about ${lines.length} pull request change${lines.length === 1 ? "" : "s"}`);
      } catch (err) {
        api.logger?.warn?.(`wtc: cannot tell ${sessionKey} about its pull requests: ${err?.message ?? err}`);
      }
    };

    // Identity belongs to the repository, even when its session checkout is gone.
    // Read only the canonical clone's local config; never fetch or inspect its branch.
    const identities = new Map();
    const repositoryIdentity = (entry) => {
      const root = entry.worktree?.repoRoot || entry.worktree?.canonicalWorkspaceDir;
      if (!root) return Promise.resolve({});
      const cached = identities.get(root);
      if (cached && Date.now() - cached.at < 60000) return cached.value;
      const value = run(["git", "config", "--no-includes", "--file", path.join(root, ".git", "config"), "--get", "remote.origin.url"], { timeoutMs: 5000 })
        .then(r => {
          if (r.code !== 0) return {};
          return repositoryOrigin(r.stdout);
        }, () => ({}));
      identities.set(root, { at: Date.now(), value });
      return value;
    };

    // The collection a session is in, with what wtc status says about it.
    const loadCollection = async (agentId, sessionKey) => {
      const { parentKey, parent, children } = await collectionOf(agentId, sessionKey);
      const result = {
        schema: "wtc.collection/v1",
        collection: { sessionKey: parentKey, label: parent?.label ?? parent?.displayName ?? null },
        requestedSessionKey: sessionKey,
        repos: [],
        warnings: [],
      };
      // The tunnel providers whose client the gateway has, and whether its
      // tailnet is the team's, so that a Tailscale tunnel is worth offering.
      result.tunnelProviders = Object.keys(TUNNEL_PROGRAMS).filter((p) => onPath(TUNNEL_PROGRAMS[p]));
      result.teamTailnet = api.pluginConfig?.teamTailnet === true;
      // Whether a portal gets an address beyond the gateway's own: a
      // wildcard domain or Tailscale run by the gateway.
      const gateway = (api.runtime?.config?.current?.() ?? api.config)?.gateway;
      result.portalIngress = !!gateway?.portals?.ingress?.domain || ["serve", "funnel"].includes(gateway?.tailscale?.mode);
      const versionLine = await wtcVersionOutput();
      result.version = { plugin: PLUGIN_VERSION, wtc: wtcVersionOf(versionLine) };
      const stale = versionWarning(PLUGIN_VERSION, versionLine);
      if (stale) result.warnings.push(stale);
      if (children.length === 0) return result;
      // The collection's name is its worktrees' name. wtc status reads
      // them, keeps the collection's folder and reports its services.
      const { collectionName, names, task } = collectionIdentity(children);
      const subName = (name) => (name.startsWith(task + WORKER_SEPARATOR) ? name.slice(task.length + WORKER_SEPARATOR.length) : name);
      const subDirs = children.filter((c) => collectionName(c.entry) && collectionName(c.entry) !== task).map((c) => worktreeOf(c.entry)).filter(Boolean);
      const compared = new Map();
      let checkouts = [];
      result.services = [];
      for (const name of names) {
        // The collection's status also compares the sub-sessions' worktrees
        // with its own checkouts.
        const argv = ["wtc", "status", name, "--json", ...(name === task ? subDirs.flatMap((d) => ["--sub", d]) : [])];
        let status;
        try {
          status = await run(argv, { env: { OPENCLAW_SHELL: "exec" } });
          const col = JSON.parse(status.stdout)[name] ?? {};
          checkouts.push(...(col.repos ?? []));
          if (name === task) for (const r of col.subSessions ?? []) compared.set(r.path, r);
          if (name !== task) continue;
          result.services.push(...(col.services ?? []));
          if (col.folder) result.collection.folder = col.folder;
          // The wtc project it works in: its harness, or ad hoc.
          if (col.harness?.name) result.collection.project = col.harness.name;
          else if (col.folder && path.basename(path.dirname(col.folder)) === "ad-hoc") result.collection.project = "ad-hoc";
          if (col.error) result.warnings.push(`collection folder: ${col.error}`);
        } catch (err) {
          result.warnings.push(`wtc status failed (exit ${status?.code ?? "unknown"}): ${(status?.stderr || status?.stdout || err?.message || "is wtc on the gateway's PATH?").trim().slice(0, 300)}`);
        }
      }
      const byPath = new Map(checkouts.map((c) => [c.path, c]));
      const rows = (await Promise.all(children
        .map(async ({ sessionKey, entry }) => ({
          sessionKey,
          label: entry.label ?? entry.displayName ?? null,
          project: entry.projectId ?? null,
          // A hidden owner has no conversation of its own.
          owner: sessionKey.includes(":subagent:") ? "hidden" : "conversation",
          sub: collectionName(entry) && collectionName(entry) !== task ? subName(collectionName(entry)) : null,
          ...(byPath.get(worktreeOf(entry)) ?? {
            ...await repositoryIdentity(entry),
            path: worktreeOf(entry), branch: entry.worktree?.branch ?? null,
            error: existsSync(path.join(worktreeOf(entry), ".git")) ? "status unavailable" : "checkout unavailable",
          }),
        }))))
        .sort((a, b) => String(a.project ?? a.repo).localeCompare(String(b.project ?? b.repo)));
      result.repos = rows.filter((r) => !r.sub);
      const missing = result.repos.filter(r => r.error === "checkout unavailable");
      if (missing.length) {
        try {
          const records = await recoveryRecords();
          for (const row of missing) row.recovery = worktreeRecovery(children.find(c => c.sessionKey === row.sessionKey).entry, row.sessionKey, records);
        } catch { for (const row of missing) row.recovery = { state: "unknown", reason: "Native recovery status unavailable" }; }
      }
      // Each sub-session with its checkouts, and per checkout how it stands
      // against the collection's: ↑ what the collection lacks, ↓ what it
      // lacks, its uncommitted files. Not against origin: a sub-session's
      // work reaches the forge only through the collection.
      const subs = new Map();
      for (const r of rows.filter((r) => r.sub)) {
        if (!subs.has(r.sub)) subs.set(r.sub, { name: r.sub, collection: collectionName({ worktree: { branch: r.branch } }) || r.sub, repos: [] });
        const c = r.error ? null : compared.get(r.path);
        subs.get(r.sub).repos.push({ ...r, toMerge: c?.toMerge ?? null, mergeError: c?.error ?? null,
          behind: c?.behind ?? null, uncommitted: c?.uncommitted ?? [],
          // The collection's own checkout of the repository, which a merge
          // goes into, and its branch, which ↓ merges into the sub-session.
          taskSessionKey: r.error ? null : result.repos.find((t) => !t.error && t.repo && t.repo === r.repo)?.sessionKey ?? null,
          taskBranch: r.error ? null : result.repos.find((t) => !t.error && t.repo && t.repo === r.repo)?.branch ?? null });
      }
      result.subSessions = [...subs.values()].sort((a, b) => a.name.localeCompare(b.name));
      tell(agentId, parentKey, watch.observe(agentId, parentKey, result.repos));
      return result;
    };

    // The gateway's wtc projects, asked at most once a minute.
    let projectsCache = { at: 0, projects: null };
    const gatewayProjects = async () => {
      if (Date.now() - projectsCache.at > 60000 || !projectsCache.projects) {
        const r = await run(["wtc", "projects", "--json"], { timeoutMs: 30000, env: { OPENCLAW_SHELL: "exec" } });
        let projects = [];
        try {
          projects = JSON.parse(r.stdout)?.data?.projects ?? [];
        } catch {}
        projectsCache = { at: Date.now(), projects };
      }
      return projectsCache.projects;
    };

    // Where the person's latest message asks to work: the gateway's decision
    // model, else OpenAI's asked directly (WTC_DECISIONS_OPENAI_API_KEY),
    // else the words' match.
    api.session.controls.registerSessionAction({
      id: "suggest",
      description: "Suggest the wtc project and repositories a conversation's latest message asks to work on.",
      requiredScopes: ["operator.read"],
      async handler(action) {
        const text = String(action.payload?.text ?? "").trim();
        if (!text) return { ok: true, result: null };
        const projects = await gatewayProjects();
        // The gateway's decision model, else OpenAI's called directly with a
        // key meant for nothing else; its "none" stands, and the words
        // decide only without either.
        // A draft not sent yet stays on the gateway: the words only.
        const draft = action.payload?.draft === true;
        let decided = draft ? null : await decideProjects(api.runtime.decisions, action.agentId, text, projects);
        const key = productEnv("WTC_DECISIONS_OPENAI_API_KEY");
        if (!decided && key && !draft) decided = await decideProjects(openaiDecisions({ apiKey: key, model: api.pluginConfig?.decisionModel || undefined }), action.agentId, text, projects);
        return { ok: true, result: decided?.none ? null : decided ?? matchProjects(text, projects) };
      },
    });

    api.session.controls.registerSessionAction({
      id: "collection",
      description: "The repositories of this session's worktree collection: branch, pending changes and pull request of each.",
      requiredScopes: ["operator.read"],
      async handler(action) {
        if (!action.sessionKey || !action.agentId) return { ok: false, error: "sessionKey and agentId are required" };
        return { ok: true, result: await loadCollection(action.agentId, action.sessionKey) };
      },
    });

    // Collections with an open pull request are looked at every few minutes
    // also while nobody has their card open, so that a merge, a failed check
    // or a review reaches the conversation.
    let timer;
    let polling = false;
    const poll = async () => {
      if (polling) return;
      polling = true;
      try {
        for (const c of watch.open()) {
          const known = (await listSessionEntries({ agentId: c.agentId })).some((s) => s.sessionKey === c.sessionKey);
          if (!known) watch.forget(c.agentId, c.sessionKey);
          else await loadCollection(c.agentId, c.sessionKey).catch(() => {});
        }
      } catch {
        // Preserve watch membership when the session store is unavailable.
        api.logger?.warn?.("wtc: pull-request watch could not read sessions; will retry next interval");
      } finally {
        polling = false;
      }
    };
    api.registerService?.({
      id: "pull-request-watch",
      start() {
        const minutes = Number(api.pluginConfig?.pullRequestPollMinutes ?? 5);
        if (!(minutes > 0) || (api.pluginConfig?.pullRequestEvents ?? "wake") === "off") return;
        timer = setInterval(() => void poll(), minutes * 60000);
        timer.unref?.();
      },
      stop() {
        clearInterval(timer);
        timer = undefined;
      },
    });

    // Log reads resolve the collection shown in Services and have a separate
    // read-scoped action that cannot select a write op.
    const svcAction = async (action, op) => {
      const payload = action.payload ?? {};
      const group = String(payload.group ?? "");
      const provider = String(payload.provider ?? "");
      if (!["up", "down", "restart", "tunnel", "logs"].includes(op)) return { ok: false, error: "op is up, down, restart, tunnel or logs" };
      if (group && (!(op === "logs" ? /^[A-Za-z0-9._-]+(?:\/[A-Za-z0-9._-]+)+$/ : /^[A-Za-z0-9._-]+\/[A-Za-z0-9._-]+$/).test(group) || group.length > 512 || group.split("/").some(part => part === "." || part === ".."))) return { ok: false, error: "name a service as <repo>/<service>" };
      if (op === "logs" && !group) return { ok: false, error: "name the service whose logs to read" };
      if (op === "tunnel" && !group) return { ok: false, error: "name the service to tunnel to" };
      if (op === "tunnel" && !TUNNEL_PROGRAMS[provider]) return { ok: false, error: `the provider is one of ${Object.keys(TUNNEL_PROGRAMS).join(", ")}` };
      if (!action.sessionKey || !action.agentId) return { ok: false, error: "sessionKey and agentId are required" };
      const { children } = await collectionOf(action.agentId, action.sessionKey);
      const name = op === "logs" ? collectionIdentity(children).task : children.map((c) => String(c.entry.worktree?.branch ?? "").replace(/^openclaw\//, "")).find(Boolean);
      if (!name) return { ok: false, error: "this session has no collection" };
      const argv = ["wtc", "svc", op, ...(group ? [group] : [])];
      if (op === "tunnel") {
        argv.push(provider);
        if (payload.stop) argv.push("--stop");
        // Only a person's click opens a tunnel anyone with the link can use.
        else if (payload.public && PUBLIC_TUNNELS.has(provider)) argv.push("--public");
        // A new tunnel runs the provider's client from the PATH; without it
        // the conversation can install one first.
        if (payload.add && !payload.stop && !onPath(TUNNEL_PROGRAMS[provider])) return { ok: true, result: { missing: TUNNEL_PROGRAMS[provider] } };
      }
      const cwd = collectionFolder(stateDir, name);
      if (!cwd) return { ok: false, error: `collection ${name} has no folder yet: wtc status ${name} makes it` };
      const r = await run(argv, { cwd, timeoutMs: op === "logs" ? 30000 : 180000, env: { OPENCLAW_SHELL: "exec" } });
      if (op === "logs" && r.code !== 0 && /(?:^|\n)(?:wtc: )?(?:runtime is not configured|(?:lstat|stat) [^\n]+: no such file or directory)\s*$/.test((r.stderr || r.stdout).trim())) return { ok: true, result: { logs: "", truncated: false } };
      if (op === "logs" && r.code === 0) {
        const text = String(r.stdout ?? "");
        return { ok: true, result: { logs: text.slice(-65536), truncated: text.length > 65536 } };
      }
      return r.code === 0 ? { ok: true, result: { message: `${argv.slice(2).join(" ")}: done` } } : { ok: false, error: (r.stderr || r.stdout).trim().slice(-400) || `exit ${r.code}` };
    };
    api.session.controls.registerSessionAction({
      id: "svc-logs",
      description: "Read recent output from one service in this session's collection (wtc svc logs).",
      requiredScopes: ["operator.read"],
      handler: (action) => svcAction(action, "logs"),
    });
    api.session.controls.registerSessionAction({
      id: "svc",
      description: "Start, stop or restart this session's collection's services, all or one, or open or close a service's tunnel (wtc svc up|down|restart|tunnel).",
      requiredScopes: ["operator.write"],
      handler: (action) => svcAction(action, String(action.payload?.op ?? "")),
    });
    // The earlier name, for a Control UI still loaded from before.
    api.session.controls.registerSessionAction({
      id: "svc-restart",
      description: "Restart one service of this session's collection (wtc svc restart).",
      requiredScopes: ["operator.write"],
      handler: (action) => svcAction(action, "restart"),
    });

    // The gateway's git service for sandboxed sessions, when configured.
    const sandboxGit = api.pluginConfig?.sandboxGit;
    if (sandboxGit?.port && api.registerService) {
      let server = null;
      api.registerService({
        id: "sandbox-git",
        start() {
          server = startSandboxGit({
            host: sandboxGit.host ?? "0.0.0.0",
            port: sandboxGit.port,
            projectsByAgent: sandboxGit.projectsByAgent,
            run,
            openclaw: onPath("openclaw") ? ["openclaw"] : [process.execPath, path.resolve(process.argv[1] ?? "")],
            listSessionEntries,
            collectionOf,
            worktreeOf,
            logger: api.logger,
          });
        },
        stop() {
          server?.close();
          server = null;
        },
      });
    }

    const catchingUp = new Set();
    const catchUp = async (action) => {
      if (catchingUp.has(action.sessionKey)) return { ok: false, error: "this repository is already catching up" };
      catchingUp.add(action.sessionKey);
      try {
        const entry = action.sessionKey && await getSessionEntry({ agentId: action.agentId, sessionKey: action.sessionKey });
        const cwd = worktreeOf(entry);
        if (!cwd) return { ok: false, error: "this session owns no managed worktree" };
        const from = action.payload?.from;
        if (from !== undefined && (!/^[A-Za-z0-9][A-Za-z0-9._\/-]*$/.test(String(from)) || /\.\.|\/\/|\/$|\.lock$/.test(String(from)))) return { ok: false, error: "from names a branch" };
        const config = api.runtime?.config?.current?.() ?? api.config;
        const r = await run(["wtc", "catch-up", ...(from ? ["--from", String(from)] : []), "--json"], { cwd, timeoutMs: 120000, env: { OPENCLAW_SHELL: "exec", ...agentGitIdentity(config, action.agentId) } });
        let json = null;
        try { json = JSON.parse(r.stdout); } catch { /* reported as text below */ }
        return { ok: true, result: { code: r.code, json, message: (json ? "" : r.stdout) + r.stderr } };
      } finally { catchingUp.delete(action.sessionKey); }
    };

    // The browser supplies an identity, never an executable command. Read the
    // collection again before acting: a repository may have become dirty or
    // moved to a step that needs the agent or person's agreement.
    const acting = new Set();
    api.session.controls.registerSessionAction({
      id: "next",
      description: "Recheck a repository's next step; run a catch-up needing no approval or offer its current handoff.",
      requiredScopes: ["operator.write"],
      async handler(action) {
        const { ownerSessionKey, key } = action.payload ?? {};
        if (!action.agentId || !action.sessionKey || !ownerSessionKey || typeof key !== "string") return { ok: false, error: "session and next step are required" };
        const lock = `${action.agentId}:${ownerSessionKey}`;
        if (acting.has(lock)) return { ok: false, error: "this repository's next step is already running" };
        acting.add(lock);
        try {
          const collection = await loadCollection(action.agentId, action.sessionKey);
          const repo = collection.repos.find((r) => r.sessionKey === ownerSessionKey);
          const choice = repo?.choices?.actions?.find((a) => a.available && a.key === key);
          const step = choice ?? (!repo?.choices && repo?.next?.key === key ? repo.next : null);
          if (!repo || repo.error || !step) return { ok: false, error: "the next step changed; refresh the collection" };
          const selectedRepo = { ...repo, next: step };
          const approvedCatchUp = action.payload?.approveCatchUp === true && step.step === "catch-up" && step.approve === "agent";
          if (step.by === "wtc" && (!step.approve || approvedCatchUp) && JSON.stringify(step.command) === JSON.stringify(["wtc", "catch-up"])) {
            const out = await catchUp({ ...action, sessionKey: ownerSessionKey, payload: {} });
            return out.ok ? { ok: true, result: { ...out.result, performed: true, repo: selectedRepo } } : out;
          }
          return { ok: true, result: { performed: false, repo: selectedRepo } };
        } finally { acting.delete(lock); }
      },
    });

    api.session.controls.registerSessionAction({
      id: "catch-up",
      description: "Merge the destination branch into this session's worktree (wtc catch-up; in a worker's worktree, the task's branch), or with payload.from another branch of the repository, a sub-session's (wtc catch-up --from). Never rebases, never pushes.",
      requiredScopes: ["operator.write"],
      handler: catchUp,
    });
  },
};
