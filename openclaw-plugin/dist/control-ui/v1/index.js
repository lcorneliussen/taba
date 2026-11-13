import { renderPRPreview } from "./pr-preview.js";
// The Collection panel: everything a collection's session works with — its
// repositories, their pull requests and the services running for it — in one
// place. Plain ESM, no build step and no SDK import.
//
// A collection is driven from one conversation. Each repository's worktree is
// held by an owner session the gateway publishes for, usually a hidden one;
// this panel publishes through that owner, the way the chat's own Publish PR
// button does for a session's single checkout.
const PLUGIN_ID = "wtc";
const HOME = "https://taba.sh/";
const TITLE = "taba Collection";

const el = (tag, props = {}, ...kids) => {
  const node = Object.assign(document.createElement(tag), props);
  node.append(...kids.filter((k) => k != null && k !== false));
  return node;
};
const css = (node, text) => ((node.style.cssText = text), node);

async function call(host, actionId, sessionKey, agentId, payload) {
  const res = await host.request("plugins.sessionAction", { pluginId: PLUGIN_ID, actionId, sessionKey, ...(agentId ? { agentId } : {}), ...(payload ? { payload } : {}) });
  if (res && res.ok === false) throw new Error(res.error ?? "action failed");
  return res?.result ?? res;
}

const plural = (n, word) => `${n} ${word}${n === 1 ? "" : "s"}`;
// http too: a gateway's direct portal is plain HTTP.
const link = (text, href) => (/^https?:\/\//.test(href ?? "") ? el("a", { textContent: text, href, target: "_blank", rel: "noopener noreferrer" }) : text);
const uuid = () => (crypto.randomUUID ? crypto.randomUUID() : `${Date.now()}-${Math.random().toString(16).slice(2)}`);

const isRunning = (s) => s.state === "ready" || s.state === "running";
// Ready is only a startup probe: wtc also looks at the address now, and at
// whether the tunnel provider's client is signed in.
// A closed tunnel's provider login matters only once it is opened again.
const isFailed = (s) => s.state === "failed" || (s.state === "exited" && s.exit_code > 0) || s.answering === "no" || (!!s.login && isRunning(s));
const serviceState = (s) => `${s.state ?? "?"}${s.exit_code > 0 ? ` (exit ${s.exit_code})` : ""}${s.answering === "no" ? `, not answering now (${s.answer_detail ?? "no answer"})` : ""}${s.login && isRunning(s) ? `: ${s.login}` : ""}`;

// Pushing is the person's, through the gateway, never the agent's: the
// gateway publishes an owner's worktree (pushes the branch, opens or updates
// a draft pull request) with the person's own account, which only the
// gateway holds, and only after they confirm it. GitHub's is OpenClaw's
// personal connection (and OpenClaw commits what is pending first); the
// shared account the gateway may also have (often the gh login its agents
// use too) is never used for it. Bitbucket's is the person's login in the
// Bitbucket plugin, which publishes what is committed.
const CONNECT = { github: "connect your own GitHub account first", bitbucket: "connect your own Bitbucket login first" };
const BITBUCKET = "bitbucket";
const BITBUCKET_CONNECT = "/plugins/bitbucket/connect";
const PUSHES = new Set(["github", "bitbucket"]);
// The Control UI's own pages sit under its base path, before /chat and the like.
const appHref = (path) => {
  const here = typeof location === "undefined" ? "" : location.pathname.replace(/\/(chat|dashboard|settings|sessions|share|skills|worktrees|agents)(\/.*)?$/, "");
  return `${here.replace(/\/$/, "")}${path}`;
};
const personalAccount = (options) => {
  const mine = options?.personal;
  return mine?.state === "connected" && mine.account && mine.generation ? mine : null;
};
// Asks the gateway to publish; returns the confirmation it waits for, if any.
async function publishRepo(host, agentId, r, say, review, mayDispatch = () => true) {
  if (!mayDispatch()) return { cancelled: true };
  const name = r.repo ?? r.project;
  const target = { sessionKey: r.sessionKey, agentId };
  const mine = personalAccount(await host.request("sessions.github.options", target));
  if (!mayDispatch()) return { cancelled: true };
  if (!mine) {
    say(`${name}: ${CONNECT.github}.`);
    return { connect: true };
  }
  say(`Pushing ${name}…`);
  const p = proposalOf(r);
  const out = await host.request("wtc.github.publish", {
    ...target, idempotencyKey: uuid(), selection: { source: "personal", account: mine.account, generation: mine.generation },
    ...(p?.title ? { title: p.title } : {}), ...(p?.body ? { body: p.body } : {}),
  });
  return settle(host, target, r, out, say, review, mayDispatch);
}
async function confirmRepo(host, agentId, r, pending, say, review, mayDispatch = () => true) {
  if (!mayDispatch()) return { cancelled: true };
  const name = r.repo ?? r.project;
  const target = { sessionKey: r.sessionKey, agentId };
  say(`Pushing ${name}…`);
  const out = await host.request("wtc.github.confirm", { ...target, requestId: pending.requestId, ...pending.confirmation });
  return settle(host, target, r, out, say, review, mayDispatch);
}
// What a publish or confirm came back with: done, failed, or a confirmation
// to ask the person for (its account, generation and digest, from status).
async function settle(host, target, r, out, say, review, mayDispatch = () => true) {
  const name = r.repo ?? r.project;
  if (out?.status === "needs_confirmation") {
    if (!mayDispatch()) return { cancelled: true };
    const status = await host.request("sessions.github.status", { ...target, requestId: out.requestId }).catch(() => null);
    if (!mayDispatch()) return { cancelled: true };
    const c = status?.confirmation ?? out.confirmation ?? null;
    if (!c) { say(`${name}: the gateway waits for a confirmation; confirm it under Publish in the owner session.`); return { waiting: true }; }
    say(`${name}: confirm to push as @${c.account?.login ?? "you"}.`);
    return { requestId: out.requestId, preview: c, confirmation: { generation: c.generation, account: c.account, requestDigest: c.requestDigest } };
  }
  if (out?.status === "published") { publicationResult(r, out, say, review); return { published: true }; }
  else say(`${name}: ${out?.status ?? "no answer"}${out?.error ? ` — ${out.error.message ?? out.error}` : out?.message ? ` — ${wtcMessage(out.message)}` : ""}`, true);
  return null;
}

// Report only what the publisher established. Some hosts return a PR URL
// without saying whether this operation created it.
function publicationResult(r, out, say, review) {
  const name = r.repo ?? r.project;
  const url = out.url ?? out.pullRequestUrl ?? "";
  const number = String(url).match(/\/(?:pull|pull-requests)\/(\d+)(?:[/?#]|$)/)?.[1];
  const pr = number ? `PR #${number}` : "PR";
  const existing = out.created === false || (out.created == null && url && r.pullRequest?.state === "open" && r.pullRequest.url === url);
  const message = out.created === true
    ? `pushed commits and created ${r.forge === BITBUCKET ? "draft " : ""}${pr}.`
    : existing
      ? `pushed commits to existing ${pr}. Its title and description are unchanged.`
      : `pushed branch commits.${number ? ` Pull request: ${pr}.` : ""}`;
  say(`${name}: ${message}${url ? ` ${url}` : ""}${out.warning ? ` Warning: ${out.warning}` : ""}${out.tracking?.state === "pending" ? ` Tracking pending: ${out.tracking.message}` : ""}`, false, number && review ? {
    label: "Review PR text",
    title: "Ask the conversation to review the PR title and description against its full current diff",
    run: (action) => review(r, url, action),
  } : null);
}

// The Bitbucket plugin's own publish: ask, then confirm what it names.
async function bitbucketAction(host, agentId, r, actionId, payload) {
  let res;
  try {
    res = await host.request("plugins.sessionAction", { pluginId: BITBUCKET, actionId, sessionKey: r.sessionKey, ...(agentId ? { agentId } : {}), payload });
  } catch (err) {
    if (/unknown plugin session action/.test(String(err?.message ?? err))) throw new Error("the gateway has no Bitbucket publish: install openclaw-bitbucket 0.3.0 or later");
    throw err;
  }
  if (res?.ok === false) throw new Error(res.error ?? "refused");
  return res?.result ?? res;
}
const bitbucketAccount = (c) => c?.name ? `${c.name} (${c.account})` : c?.account ?? "you";
async function publishBitbucket(host, agentId, r, say, dest, mayDispatch = () => true) {
  if (!mayDispatch()) return { cancelled: true };
  const name = r.repo ?? r.project;
  say(`Asking to push ${name}…`);
  // Its rows carry no pull request, so proposed text accompanies every push;
  // the publisher uses it only when creating a pull request.
  const proposal = proposalOf(r);
  let out;
  try {
    out = await bitbucketAction(host, agentId, r, "publish", { draft: true, ...(dest ? { dest } : {}), ...(proposal?.title ? { title: proposal.title } : {}), ...(proposal?.body ? { description: proposal.body } : {}) });
  } catch (err) {
    if (proposal?.body && /payload.*schema|additional propert/i.test(String(err?.message ?? err))) {
      throw new Error("Publishing the proposed description requires Bitbucket plugin 0.3.4 or later. Upgrade it and try again; nothing was published.");
    }
    throw err;
  }
  if (out?.status === "not_connected") {
    say(`${name}: ${CONNECT.bitbucket}.`);
    return { connect: true };
  }
  const c = out?.confirmation;
  if (out?.status !== "needs_confirmation" || !c) { say(`${name}: ${out?.status ?? "no answer"}${out?.error ? ` — ${out.error.message ?? out.error}` : out?.message ? ` — ${out.message}` : ""}`, true); return null; }
  if (proposal?.body && c.description !== proposal.body) {
    throw new Error("The publisher did not confirm the proposed description. Upgrade to Bitbucket plugin 0.3.4 or later and try again; nothing was published.");
  }
  const left = c.uncommitted ? `; ${plural(c.uncommitted, "uncommitted file")} stay behind` : "";
  say(`${name}: confirm to push ${c.branch} (${String(c.head).slice(0, 7)}) to ${c.dest} as ${bitbucketAccount(c)}${left}.`);
  return { forge: BITBUCKET, requestId: out.requestId, confirmation: c };
}
async function confirmBitbucket(host, agentId, r, pending, say, review, mayDispatch = () => true) {
  if (!mayDispatch()) return { cancelled: true };
  const name = r.repo ?? r.project;
  say(`Pushing ${name}…`);
  const out = await host.request("wtc.bitbucket.confirm", {sessionKey:r.sessionKey,agentId,requestId:pending.requestId,requestDigest:pending.confirmation.requestDigest});
  if (out?.status !== "published") {
    say(`${name}: ${out?.status ?? "no answer"}${out?.error ? ` — ${out.error.message ?? out.error}` : out?.message ? ` — ${out.message}` : ""}`, true);
    return null;
  }
  publicationResult(r, out, say, review);
  return { published: true };
}

// The files a catch-up's merge stopped on, from wtc's message (Checkout.merge
// in internal/openclaw/checkout.go, whose test pins the sentence); none when
// it did not stop on a conflict.
export const conflictsOf = (message) => (/conflicts in: (.+?)\. The merge was aborted/.exec(message ?? "")?.[1] ?? "")
  .split(/,\s*/).filter(Boolean);

// What wtc printed, without its "wtc: " prefix: the card names the repository.
const wtcMessage = (message) => String(message ?? "").trim().replace(/^wtc: /, "");
// A note that reports a failure reads as one.
const noteNode = (text, failed, style) => css(el("span", { textContent: failed ? `⚠ ${text}` : text, title: text }),
  `color:${failed ? TONE.failure : "var(--muted, #888)"};font-size:12px;${style ?? ""}`);

// The plugin installs its skills as taba-<name>. A collection pinned before
// the rename still names them wtc-<name> in its next steps.
const CATCH_UP = "taba-catch-up";
const currentSkill = (skill) => typeof skill === "string" && skill.startsWith("wtc-") ? "taba-" + skill.slice(4) : skill;

// Asks the conversation to run one of the agent's skills on what the card saw.
const askSkill = (host, sessionKey, skill, input) => askConversation(host, sessionKey, `/skill ${currentSkill(skill)} ${input}`);

// These phrases are emitted by internal/openclaw/catch_up_changes.go and
// catch_up_recovery.go (requireRecoveredChanges). Keep both emitters in sync.
const needsLocalRecovery = message => /local changes (?:need recovery|still need recovery)|local changes are safe in/.test(message ?? "");
const recoveryResult = out => out?.json?.data?.recoveryRequired === true;
const recoveryHandoff = r => r.catchUpRecovery
  ? ` Recovery operation: ${r.catchUpRecovery}. Follow the bundled recovery procedure (wtc docs instructions/openclaw-recovery.md), verify the intended edits, then finish that exact recovery.` : "";
async function catchUpRepo(host, agentId, r, say, ask, request) {
  const name = r.repo ?? r.project;
  say(`Catching up ${name}…`);
  const out = await request();
  if (out?.cancelled) return out;
  // HEAD may have moved; the local edits remain in the pinned recovery copy.
  if (out.code && (recoveryResult(out) || needsLocalRecovery(out.message))) {
    const recoveryRef = out.json?.data?.recoveryRef || r.catchUpRecovery;
    const handoff = await ask(`${name}: catch-up recovery needs inspection.${recoveryHandoff({ catchUpRecovery: recoveryRef })} Do not reapply a recovery copy blindly or publish.`, recoveryRef);
    if (handoff?.cancelled || handoff?.unconfirmed) return {...handoff,recoveryRef};
    say(`${name}: the conversation was asked to verify or recover local edits.`);
    return { handedOff: true, recoveryRef };
  }
  // A conflicting base merge was aborted; resolve it in the owning conversation.
  const conflicts = conflictsOf(out.message);
  if (conflicts.length) {
    const base = r.base ?? "its base";
    const handoff = await ask(`${name}: catching up with ${base} conflicts in ${conflicts.join(", ")}. The merge was aborted; any saved local edits were restored. Before git merge, preserve dirty edits and their staging in a separately pinned Git stash and verify a clean checkout. Merge and resolve the base, then reconcile the saved staged, unstaged and untracked edits. Follow the bundled recovery procedure; do not publish or discard the backup.`);
    if (handoff?.cancelled || handoff?.unconfirmed) return handoff;
    say(`${name}: ${conflicts.join(", ")} changed on both sides; the conversation was asked to merge and resolve them.`);
    return { handedOff: true };
  }
  say(`${name}: ${out.json?.summary ?? (wtcMessage(out.message) || "exit " + out.code)}${out.code === 0 && r.pullRequest?.state === "open" ? ". Local catch-up complete; use Update PR / Push to send the updated branch to its PR." : ""}`, out.code !== 0);
}

// One load of a session's collection, shared by the panel and the card above
// the message box, so that both show the same thing and the gateway runs wtc status
// once per refresh rather than once per view.
const REFRESH_MS = 30000;
const store = new Map();
function watchCollection(host, sessionKey, agentId, onChange) {
  let entry = store.get(sessionKey);
  if (!entry) {
    entry = { data: null, error: null, at: 0, inflight: null, listeners: new Set(), timer: null };
    store.set(sessionKey, entry);
  }
  const notify = () => entry.listeners.forEach((fn) => fn(entry));
  const load = (force) => {
    if (entry.inflight) return entry.inflight;
    if (!force && entry.data && Date.now() - entry.at < REFRESH_MS / 2) return Promise.resolve(entry);
    // The gateway's portals, to show beside the services they expose.
    const generation = entry.generation ?? 0;
    entry.inflight = Promise.all([call(host, "collection", sessionKey, agentId), host.request("portal.list", {}).catch(() => null)])
      .then(([data, portals]) => {
        if (generation !== (entry.generation ?? 0)) return;
        entry.data = { ...data, portals: portals?.portals ?? [] }; entry.error = null; entry.at = Date.now();
        reconcileRecoveryActivity(data.collection?.sessionKey ?? sessionKey, data.repos ?? []);
      }, (err) => { if (generation === (entry.generation ?? 0)) entry.error = String(err?.message ?? err); })
      .then(() => { entry.inflight = null; notify(); return entry; });
    notify();
    return entry.inflight;
  };
  entry.listeners.add(onChange);
  // Pull requests and CI move on their own; look again while someone watches.
  entry.timer ??= setInterval(() => { if (!document.hidden && !entry.paused) void load(true); }, REFRESH_MS);
  if (entry.data || entry.error) onChange(entry);
  void load(false);
  return {
    refresh: () => {
      if (!entry.inflight) return load(true);
      return entry.inflight.then(() => load(true));
    },
    pause: (on) => {
      entry.paused = Math.max(0, (entry.paused ?? 0) + (on ? 1 : -1));
      entry.generation = (entry.generation ?? 0) + 1;
    },
    dispose() {
      entry.listeners.delete(onChange);
      if (entry.listeners.size === 0) {
        clearInterval(entry.timer);
        store.delete(sessionKey);
      }
    },
  };
}
const pauseCollection = (watch) => {
  watch?.pause(true);
  return () => watch?.pause(false);
};

// The collection card above the message box, where OpenClaw shows a task's
// progress: collapsed, what is not pushed yet, the open pull requests with
// their CI and the services that run; expanded, each repository on its branch
// with its Push and Catch up buttons, and each service with its local address
// and tunnels. It wraps the built-in composer. In a session that is not a
// collection yet it shows only a suggestion of where to start one, if any.
const OPEN_KEY = "wtc.collectionCard.open";
const remembered = () => { try { return localStorage.getItem(OPEN_KEY) === "1"; } catch { return false; } };
const remember = (open) => { try { localStorage.setItem(OPEN_KEY, open ? "1" : "0"); } catch {} };
const TONE = { success: "var(--ok, #2da44e)", failure: "var(--danger, #cf222e)", pending: "var(--warn, #bf8700)", none: "var(--muted, #888)" };
const ago = (at) => {
  const s = Math.round((Date.now() - at) / 1000);
  return s < 60 ? "just now" : s < 3600 ? `${Math.round(s / 60)}m ago` : `${Math.round(s / 3600)}h ago`;
};

// Clock ticks change labels, not controls: replacing a hovered or focused node
// resets its native tooltip and can dismiss an open menu.
const timeLabel = (at, { prefix = "", updated = false } = {}) => Object.assign(el("span", { className:"wtc-time-label", textContent:prefix + ago(at) }), { wtcTimeAt:at, wtcTimePrefix:prefix, wtcUpdated:updated });
const refreshTitle = (entry) => `${entry.inflight ? "Refreshing… " : ""}Updated ${ago(entry.at)}. Refresh`;
function syncTimeLabels(root, entry, { composer = false } = {}) {
  for (const n of root.querySelectorAll?.(".wtc-time-label") ?? []) {
    if (n.wtcUpdated) n.wtcTimeAt = entry.at;
    const text = n.wtcTimePrefix + ago(n.wtcTimeAt);
    if (n.textContent !== text) n.textContent = text;
  }
  for (const b of root.querySelectorAll?.(".wtc-refresh-control") ?? []) {
    const busy = String(!!entry.inflight);
    if (b.getAttribute("aria-busy") !== busy) b.setAttribute("aria-busy", busy);
    // Changing a hovered title resets its tooltip; catch its age up when idle.
    if (composer && !b.matches(":hover,:focus-visible")) {
      const title = refreshTitle(entry);
      if (b.title !== title) b.title = b.ariaLabel = title;
    }
  }
}
function collectionStamp(entry, sessionKey) {
  const key = entry?.data?.collection?.sessionKey ?? sessionKey;
  return JSON.stringify([entry?.data, entry?.error, feedbackStore.get(key), [...(activityStore.get(key)?.values() ?? [])], [...requestFeedback].filter(([id]) => id.startsWith(`${key}|`))]);
}

const ciText = (pr) => ({ success: "CI passed", failure: `CI ${pr.failed} failed`, pending: `CI ${pr.running} running` })[pr.checks] ?? "no checks";
// The state of an open pull request at a glance: CI, whether it merges, review.
const prTone = (pr) => pr.mergeable === "conflicting" || pr.checks === "failure" || pr.review === "changes_requested" ? TONE.failure
  : pr.checks === "pending" ? TONE.pending : pr.checks === "success" ? TONE.success : TONE.none;
const prState = (pr) => [
  pr.checks && pr.checks !== "none" ? ciText(pr) : "",
  pr.mergeable === "conflicting" ? "conflicts with its base" : "",
  { approved: "approved", changes_requested: "changes requested", review_required: "review required" }[pr.review] ?? "",
].filter(Boolean);
// Forge marks before repository names, the way people know their repositories.
const FORGE_PATHS = {
  github: "M12 .297c-6.63 0-12 5.373-12 12 0 5.303 3.438 9.8 8.205 11.385.6.113.82-.258.82-.577 0-.285-.01-1.04-.015-2.04-3.338.724-4.042-1.61-4.042-1.61C4.422 18.07 3.633 17.7 3.633 17.7c-1.087-.744.084-.729.084-.729 1.205.084 1.838 1.236 1.838 1.236 1.07 1.835 2.809 1.305 3.495.998.108-.776.417-1.305.76-1.605-2.665-.3-5.466-1.332-5.466-5.93 0-1.31.465-2.38 1.235-3.22-.135-.303-.54-1.523.105-3.176 0 0 1.005-.322 3.3 1.23.96-.267 1.98-.399 3-.405 1.02.006 2.04.138 3 .405 2.28-1.552 3.285-1.23 3.285-1.23.645 1.653.24 2.873.12 3.176.765.84 1.23 1.91 1.23 3.22 0 4.61-2.805 5.625-5.475 5.92.42.36.81 1.096.81 2.22 0 1.606-.015 2.896-.015 3.286 0 .315.21.69.825.57C20.565 22.092 24 17.592 24 12.297c0-6.627-5.373-12-12-12",
  bitbucket: "M.778 1.213a.768.768 0 00-.768.892l3.263 19.81c.084.5.515.868 1.022.873H19.95a.772.772 0 00.77-.646l3.27-20.03a.768.768 0 00-.768-.891zM14.52 15.53H9.522L8.17 8.466h7.561z",
};
const FORGE_COLORS = { github: "currentColor", bitbucket: "#2684FF" };
function forgeIcon(forge, size = 14) {
  const d = FORGE_PATHS[forge];
  const NS = "http://www.w3.org/2000/svg";
  const svg = document.createElementNS(NS, "svg");
  svg.setAttribute("viewBox", "0 0 24 24");
  svg.setAttribute("width", String(size));
  svg.setAttribute("height", String(size));
  svg.setAttribute("aria-label", forge === "github" ? "GitHub" : forge === "bitbucket" ? "Bitbucket" : "Repository");
  svg.style.cssText = "flex:none;align-self:center";
  const pathEl = document.createElementNS(NS, "path");
  pathEl.setAttribute("d", d ?? "M4 3h16v18H4zM8 3v18M11 7h6M11 11h6");
  pathEl.setAttribute("fill", d ? FORGE_COLORS[forge] : "none");
  if (!d) {
    pathEl.setAttribute("stroke", "currentColor");
    pathEl.setAttribute("stroke-width", "1.6");
    pathEl.setAttribute("stroke-linejoin", "round");
  }
  svg.append(pathEl);
  return svg;
}
const webUrlOf = (r) => r.owner && r.repo
  ? (r.forge === "github" ? `https://github.com/${r.owner}/${r.repo}` : r.forge === "bitbucket" ? `https://bitbucket.org/${r.owner}/${r.repo}` : null)
  : null;
// icon, owner/ (muted), name; the name links to the repository.
function repoTitle(r, size = "14px") {
  const name = r.repo ?? r.project ?? r.label ?? "?";
  const href = webUrlOf(r);
  const label = el(href ? "a" : "span", { title: r.path ?? "" },
    r.owner ? css(el("span", { textContent: `${r.owner}/` }), "font-weight:400;color:var(--muted, #888)") : null,
    css(el("span", { textContent: name }), "font-weight:600;color:var(--text-strong, inherit)"));
  if (href) Object.assign(label, { href, target: "_blank", rel: "noopener noreferrer" });
  label.style.cssText = `text-decoration:none;color:inherit;font-size:${size};overflow:hidden;text-overflow:ellipsis;white-space:nowrap;min-width:0`;
  return css(el("span", {}, forgeIcon(r.forge), label), "display:inline-flex;align-items:baseline;gap:6px;min-width:0");
}
// A gateway worktree is on its own branch from the start; until something is
// on it, it is its base, as a detached worktree is in a local collection.
const untouched = (r) => !!r.branch && !r.ahead && !r.dirty && !r.published && !r.pullRequest;
// The branch most of a collection's repositories with work on them are on:
// shown once, and per repository only where it differs.
const collectionBranch = (repos) => {
  const counts = new Map();
  for (const r of repos) if (r.branch && !untouched(r)) counts.set(r.branch, (counts.get(r.branch) ?? 0) + 1);
  return [...counts].sort((a, b) => b[1] - a[1])[0]?.[0] ?? null;
};
const serviceName = (s) => s.label || String(s.group ?? s.path ?? "?").split("/").pop();
// A service with its tunnels is one line, as in wtc status: wtc svc puts a
// tunnel in the group of the service it exposes and marks it an endpoint.
function serviceGroups(services) {
  const groups = new Map();
  for (const s of services) {
    const key = s.group ?? s.path;
    if (!groups.has(key)) groups.set(key, { group: s.group, repo: s.repo, members: [], tunnels: [], main: null });
    const g = groups.get(key);
    g.members.push(s);
    if (s.kind === "endpoint") g.tunnels.push(s);
    else g.main ??= s;
  }
  return [...groups.values()].map((g) => {
    const main = g.main ?? g.members[0];
    const failed = g.members.some(isFailed);
    return {
      ...g, label: serviceName(main), url: g.main?.url ?? null,
      // A tunnel started as a service of its own rather than with wtc svc
      // tunnel: it shows apart from the service it exposes.
      loose: !g.main || TUNNEL_URL.test(g.main.url ?? ""),
      failed, running: !failed && isRunning(main), exited: g.members.some((s) => s.state === "exited"),
      // The state that most needs attention.
      state: serviceState(g.members.find(isFailed) ?? g.members.find((s) => !isRunning(s)) ?? main),
    };
  });
}
// A service listens on the gateway, often in a container: its loopback address
// opens nowhere else, so it shows as its port, and a tunnel makes it reachable.
const LOOPBACK = /^https?:\/\/(127\.0\.0\.1|localhost|\[::1\])(:\d+)?(\/|$)/;
const address = (url) => LOOPBACK.test(url)
  ? css(el("span", { textContent: `:${new URL(url).port || "80"}`, title: `${url} on the gateway: open a tunnel to reach it from here` }), "font-family:var(--mono, ui-monospace, monospace);color:var(--muted, #888)")
  : link(url.replace(/^https?:\/\//, ""), url);
const TUNNEL_URL = /^https:\/\/[^/]*(trycloudflare\.com|\.ts\.net|ngrok[-a-z]*\.(app|io|dev)|devtunnels\.ms)(:\d+)?(\/|$)/;
const consolidate = (g) => `The tunnel ${g.group} runs as a service of its own. Consolidate it as the taba-svc skill says: remove it and open it again with wtc svc tunnel on the service it exposes.`;
const tunnelName = (t) => t.provider || (/trycloudflare\.com/.test(t.url ?? "") ? "cloudflare" : /\.ts\.net/.test(t.url ?? "") ? "tailscale" : "tunnel");
// Things only the conversation can do (resolve a conflict, publish where the
// gateway cannot) are asked of it, in the conversation, where it shows.
const askConversation = (host, sessionKey, message) => host.request("sessions.send", { key: sessionKey, message });
// Pushing goes to the repository's own branch and, without one yet, opens a
// draft pull request for it.
const pushTitle = (pr, forge) => {
  const operation = forge === BITBUCKET ? "Push committed changes" : "Commit pending changes and push the branch";
  return pr ? `${operation} to PR #${pr.number}; its title and description are kept`
    : `${operation}; create a draft pull request if none exists`;
};
// The title and description the agent proposed with wtc publish, which a new
// pull request opens with; the gateway takes them only when it opens one.
const proposalOf = (r) => (r.pullRequest ? null : r.proposal?.title || r.proposal?.body ? r.proposal : null);
const proposalTitle = (r) => {
  if (r.pullRequest) return "";
  const p = proposalOf(r);
  const body = p?.body;
  if (!p?.title && !body) return " The agent proposed no title or description: ask it to run wtc publish with them, or the pull request opens as \"Publish <branch>\".";
  return p.title ? ` Title: "${p.title}"${body ? ", with the agent's description" : r.forge === BITBUCKET ? ", without a proposed description" : ""}.` : " With the agent's description.";
};

// Services. wtc svc starts, stops and restarts them (one, or all without a
// group) and opens or closes their tunnels; the gateway runs it in the
// collection's folder.
async function svcRequest(host, sessionKey, agentId, payload) {
  // A gateway portal is OpenClaw's own: the Control UI opens and closes it.
  if (payload.op === "portal") {
    if (payload.stop) return host.request("portal.close", { id: payload.id });
    const opened = await host.request("portal.open", { port: payload.port, title: payload.title });
    if (opened?.id && opened.url) portalUrls.set(opened.id, opened.url);
    return opened;
  }
  const res = await host.request("plugins.sessionAction", { pluginId: PLUGIN_ID, actionId: "svc", sessionKey, agentId, payload });
  if (res?.ok === false) throw new Error(res.error ?? "wtc svc failed");
  return res?.result ?? res;
}
// What a new tunnel can go through, first what to try first: the gateway's
// own portal, then a public link for when a portal does not work, a dev
// tunnel for the person's account, and the tailnet when it is the team's.
const TUNNEL_PROVIDERS = [
  { name: "gateway", label: "Gateway", reach: "OpenClaw portal", portal: true },
  { name: "cloudflare", label: "Cloudflare", reach: "public link", public: true },
  { name: "ngrok", label: "ngrok", reach: "public link", public: true },
  { name: "devtunnel", label: "Dev Tunnel", reach: "your account" },
  { name: "tailscale", label: "Tailscale", reach: "team tailnet", team: true },
];
// portal.list leaves out a portal's URL with its access token; opening the
// same port again returns it. Kept for the portals this page opened.
const portalUrls = new Map();
const portOf = (url) => { try { return Number(new URL(url).port) || null; } catch { return null; } };
// A portal at a loopback or private address the browser is not on (a
// container's, say) needs portal ingress on the gateway to be reached.
const PRIVATE_HOST = /^(127\.|10\.|192\.168\.|172\.(1[6-9]|2\d|3[01])\.|localhost$|\[::1\]$)/;
const unreachable = (url) => {
  try {
    const host = new URL(url).hostname;
    const here = typeof location === "undefined" ? "" : location.hostname;
    return host !== here && PRIVATE_HOST.test(host) && !PRIVATE_HOST.test(here);
  } catch { return false; }
};
const installTunnel = (g, provider, program) => `Open a ${provider} tunnel to ${g.group}. ${program} is not on the gateway's PATH: install it for this collection, then open the tunnel as the taba-svc skill says.`;
const serviceStateIcon = glyph => {
  // Use a supported shell for the filled square, avoiding the unknown-glyph fallback.
  const icon = controlIcon(glyph === "■" ? "▶" : glyph, 12);
  const svg = icon.children[0], path = svg.children[0];
  if (glyph === "▶" || glyph === "■") {
    path.setAttribute("d", glyph === "▶" ? "M8 4l13 8-13 8V4z" : "M5 5h14v14H5z");
    svg.setAttribute("fill", "currentColor"); svg.setAttribute("stroke", "none");
  }
  return icon;
};
// A button in front of a service, where a repository has its forge icon and
// as small, that is also its state: ■ stops a running one, ▶ starts a
// stopped one, ↻ restarts a failed one.
function stateGlyph(glyph, title, tone, run) {
  const b = css(el("button", { type: "button", className: "wtc-service-play", title, ariaLabel: title }, serviceStateIcon(glyph)),
    `flex:none;box-sizing:border-box;width:var(--wtc-state-size,20px);height:var(--wtc-state-size,20px);display:inline-flex;align-items:center;justify-content:center;border:0;border-radius:4px;background:var(--wtc-service-play-bg,transparent);color:var(--wtc-service-play-color,${tone});cursor:pointer;font-size:${glyph === "↻" ? 13 : 10}px;line-height:1;padding:0`);
  b.onclick = (e) => { e?.preventDefault(); e?.stopPropagation(); return run(); };
  return b;
}
// A pill like Push + PR, for the actions on the right.
function pill(text, title, run) {
  const b = css(el("button", { type: "button", textContent: text, title, ariaLabel: title }),
    "border:1px solid var(--border-strong, rgba(128,128,128,.4));background:transparent;color:inherit;cursor:pointer;font:inherit;font-size:12px;line-height:18px;padding:0 9px;border-radius:999px;white-space:nowrap");
  b.onclick = (e) => { e?.preventDefault(); e?.stopPropagation(); return run(); };
  return b;
}
const busyMark = () => css(el("span", { textContent: "…", title: "Working" }),
  "flex:none;width:14px;text-align:center;color:var(--muted, #888)");
// svc: { run(payload, doing), busy(key), ask(text) } from the view.
function stateButton(g, svc) {
  if (svc.busy(g.group) || svc.busy("*")) return busyMark();
  if (g.failed) return stateGlyph("↻", `${g.label}: ${g.state}. Restart it`, TONE.failure, () => svc.run({ op: "restart", group: g.group }, `Restarting ${g.label}…`));
  if (g.running) return stateGlyph("■", `${g.label} is running. Stop it`, TONE.success, () => svc.run({ op: "down", group: g.group }, `Stopping ${g.label}…`));
  return stateGlyph("▶", `${g.label}: ${g.state}. Start it`, TONE.none, () => svc.run({ op: "up", group: g.group }, `Starting ${g.label}…`));
}
// A service's tunnels: a running one links to its address and closes with ✕,
// a closed one is a ▶ button to open it again. A new one opens through the
// one provider the gateway has ("+ cloudflare"), or a choice of them
// ("+ Tunnel…"); with none, any of them, installed by the conversation first.
const smallButton = (text, title, run) => {
  const b = css(el("button", { type: "button", textContent: text, title, ariaLabel: title }),
    "border:0;background:transparent;color:var(--muted, #888);cursor:pointer;font:inherit;font-size:11px;padding:0 2px");
  b.onclick = (e) => { e?.preventDefault(); e?.stopPropagation(); return run(); };
  return b;
};
const resultAction = (action, say) => {
  if (!action) return null;
  const button = smallButton(action.done ? "Sent to agent" : action.label, action.done ? "Handoff accepted; completion is pending in the conversation" : action.title, async () => {
    if (action.done) return;
    try { await action.run(action); } catch (err) { say(String(err?.message ?? err), true); }
  });
  button.disabled = !!action.done;
  return button;
};
// The gateway's portals to a service, by the port it listens on.
const portalsOf = (g, data = {}) => {
  const port = portOf(g.url);
  return (data.portals ?? []).filter((p) => port && p.port === port);
};
// An open portal: a link (or, listed without its URL, a button that opens it
// again) and ✕. label is what the link says, "gateway" by default.
function portalNode(g, p, svc, label = "gateway") {
  const far = unreachable(p.publicUrl);
  const name = far ? `${label} ⚠` : label;
  const url = p.url ?? portalUrls.get(p.id);
  // Without its URL, the portal is opened again for it, in a tab opened
  // on the click so that no popup blocker stops it.
  const go = url ? link(name, url) : smallButton(name, "Open the portal", async () => {
    const tab = typeof window === "undefined" ? null : window.open("about:blank", "_blank");
    const r = await svc.run({ op: "portal", port: p.port, title: p.title }, "Opening the portal…", `${g.group}:gateway`);
    if (tab && r?.url) { tab.opener = null; tab.location.href = r.url; } else tab?.close();
  });
  if (!url) go.style.color = "inherit";
  return css(el("span", { title: far ? `The portal listens on ${new URL(p.publicUrl).host}, which this browser cannot reach: the gateway needs portal ingress (managed Tailscale or a wildcard domain). Use a tunnel instead.` : `OpenClaw portal ${p.publicUrl}` },
    go,
    smallButton("✕", "Close the portal", () => svc.run({ op: "portal", stop: true, id: p.id }, "Closing the portal…", `${g.group}:gateway`))),
  `display:inline-flex;align-items:center;gap:2px;white-space:nowrap;${far ? `color:${TONE.pending}` : ""}`);
}
// A wtc tunnel: running, a link and ✕; closed, a button that opens it again.
// label is what the link says, the provider by default; closed, opener is
// what the button says.
function tunnelNode(g, t, svc, { label, opener } = {}) {
  const name = tunnelName(t);
  const pub = TUNNEL_PROVIDERS.some((p) => p.name === name && p.public);
  if (svc.busy(`${g.group}:${name}`)) return css(el("span", { textContent: label ? "…" : `${name} …` }), "color:var(--muted, #888)");
  // A closed tunnel is an action: open it again.
  if (!isRunning(t)) {
    const title = `Open the ${name} tunnel again${pub ? ": anyone with the link can use it" : ""} (${serviceState(t)})${t.login ? `. First: ${t.login}` : ""}`;
    const again = () => svc.run({ op: "tunnel", group: g.group, provider: name, public: pub }, `Opening the ${name} tunnel…`, `${g.group}:${name}`);
    return opener ? opener(title, again) : pill(`▶ ${name}`, title, again);
  }
  // Running, but not answering or not signed in: say so on it.
  const trouble = isFailed(t) ? css(el("span", { textContent: "⚠", title: serviceState(t) }), "color:var(--danger, #cf222e)") : null;
  const route = t.url ? link(label ?? name, t.url) : label ?? name;
  if (typeof route === "object") {
    css(route, "min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap");
    route.title = t.url;
  }
  const close = smallButton("✕", `Close the ${name} tunnel`, () => svc.run({ op: "tunnel", group: g.group, provider: name, stop: true }, `Closing the ${name} tunnel…`, `${g.group}:${name}`));
  close.style.flex = "none";
  return css(el("span", {}, trouble, route, close),
    "display:inline-flex;align-items:center;gap:2px;min-width:0;max-width:100%;white-space:nowrap");
}
// A new way to a service, or null when none is left. compact: a "+" only,
// whose title and options name the ways.
function newTunnel(g, svc, data = {}, { compact = false } = {}) {
  const port = portOf(g.url);
  const portals = portalsOf(g, data);
  const taken = (p) => p.portal ? portals.length > 0 || !port : g.tunnels.some((t) => tunnelName(t) === p.name);
  // Without portal ingress a portal comes back at the gateway's own address,
  // which a remote browser rarely reaches: it is offered last, as such.
  const ingress = data.portalIngress !== false;
  const free = TUNNEL_PROVIDERS.filter((p) => !taken(p) && (!p.team || data.teamTailnet))
    .sort((a, b) => Number(!ingress && !!a.portal) - Number(!ingress && !!b.portal));
  const ready = (p) => p.portal || data.tunnelProviders?.includes(p.name);
  const note = (p) => p.portal ? (ingress ? "" : " · needs ingress") : ready(p) ? "" : " · install first";
  const open = async (p) => {
    if (p.portal) return svc.run({ op: "portal", port, title: g.label }, `Opening a portal to ${g.label}…`, `${g.group}:gateway`);
    const r = await svc.run({ op: "tunnel", group: g.group, provider: p.name, public: !!p.public, add: true }, `Opening a ${p.name} tunnel to ${g.label}…`, `${g.group}:${p.name}`);
    if (r?.missing) await svc.ask(installTunnel(g, p.name, r.missing));
  };
  if (!g.main || g.loose || !free.length) return null;
  const trigger = css(el("button", { type: "button", className: "wtc-tunnel-trigger", title: `Add a tunnel to ${g.label}`, ariaLabel: `Add a tunnel to ${g.label}` }, controlIcon("+"), compact ? null : " Tunnel…"),
    `display:inline-flex;align-items:center;justify-content:center;gap:4px;box-sizing:border-box;${compact ? "width:24px;" : "padding:0 6px;"}height:24px;min-width:24px;border:1px solid transparent;border-radius:var(--radius-sm,6px);background:transparent;color:var(--muted,#888);font:inherit;font-size:12px;line-height:16px;cursor:pointer;${compact ? "padding:0;" : ""}`);
  const wrap = (...nodes) => css(el("span", { className: "wtc-tunnel-control" }, ...nodes), "display:inline-flex;align-items:center;vertical-align:middle");
  if (free.length === 1) {
    const p = free[0];
    if (!compact) trigger.replaceChildren(controlIcon("+"), ` ${p.label ?? p.name}`);
    trigger.title = trigger.ariaLabel = `Open ${p.label ?? p.name} (${p.reach}) to this service${p.public ? ": anyone with the link can use it" : ""}${!ready(p) ? "; the conversation installs it first" : p.portal && !ingress ? "; the gateway has no portal ingress, so it may not be reachable from here: a tunnel is" : ""}`;
    trigger.onclick = (e) => { e.preventDefault(); e.stopPropagation(); return open(p); };
    return wrap(trigger);
  }
  const menu = css(el("div", { id: `wtc-tunnels-${uuid()}`, className: "wtc-tunnel-menu", popover: "auto", role: "menu", ariaLabel: `Tunnel for ${g.label}` }),
    "position:fixed;inset:auto;margin:0;padding:4px;box-sizing:border-box;width:252px;max-width:calc(100vw - 24px);max-height:calc(100dvh - 24px);overflow:auto;border:1px solid var(--border-strong,var(--border,rgba(128,128,128,.3)));border-radius:var(--radius-md,8px);background:var(--card,var(--bg,#161920));color:var(--text,inherit);box-shadow:0 6px 20px rgba(0,0,0,.2);font:inherit;font-size:12px;line-height:16px;visibility:hidden");
  trigger.popoverTargetElement = menu;
  trigger.ariaHasPopup = "menu";
  trigger.ariaControls = menu.id;
  trigger.ariaExpanded = "false";
  let shown = false, nextFocus = 0, cleanup = () => {};
  const close = (focus = false) => {
    if (shown) menu.hidePopover();
    if (focus && trigger.isConnected) trigger.focus();
  };
  const items = free.map((p) => {
    const item = css(el("button", { type: "button", role: "menuitem", value: p.name, tabIndex: -1, className: "wtc-tunnel-option", ariaLabel: `${p.label ?? p.name} · ${p.reach}${note(p)}` },
      el("span", { textContent: p.label ?? p.name }),
      css(el("span", { textContent: `${p.reach}${note(p)}` }), "display:block;font-size:11px;line-height:15px;color:var(--muted,#888)")),
      "display:block;width:100%;box-sizing:border-box;margin:0;padding:6px 8px;border:0;border-radius:4px;background:transparent;color:inherit;text-align:left;white-space:normal;font:inherit;line-height:16px;cursor:pointer");
    item.onclick = (e) => { e.preventDefault(); e.stopPropagation(); close(true); return open(p); };
    return item;
  });
  menu.append(...items);
  // Native popovers handle light dismissal. Open menus own temporary viewport
  // listeners; closing the menu or removing its trigger releases them.
  menu.onbeforetoggle = (e) => {
    shown = e.newState === "open";
    trigger.ariaExpanded = String(shown);
    cleanup();
    if (shown) menu.style.visibility = "hidden";
  };
  // A closed popover has no box: measure after it enters the top layer.
  menu.ontoggle = (e) => {
    if (e.newState !== "open" || !shown || !trigger.isConnected) return;
    const viewport = globalThis.visualViewport;
    const edge = 12, gap = 6;
    const left = (viewport?.offsetLeft ?? 0) + edge, top = (viewport?.offsetTop ?? 0) + edge;
    const right = left + (viewport?.width ?? globalThis.innerWidth ?? 1024) - 2 * edge;
    const bottom = top + (viewport?.height ?? globalThis.innerHeight ?? 768) - 2 * edge;
    menu.style.maxWidth = `${Math.max(0, right - left)}px`;
    menu.style.maxHeight = `${Math.max(0, bottom - top)}px`;
    const anchor = trigger.getBoundingClientRect(), box = menu.getBoundingClientRect();
    menu.style.left = `${Math.max(left, Math.min(anchor.right - box.width, right - box.width))}px`;
    const above = anchor.top - gap - box.height;
    const y = above >= top ? above : anchor.bottom + gap;
    menu.style.top = `${Math.max(top, Math.min(y, bottom - box.height))}px`;
    menu.style.visibility = "visible";
    items[nextFocus].focus(); nextFocus = 0;
    if (!shown || !trigger.isConnected) return;
    const events = new AbortController();
    globalThis.addEventListener?.("resize", () => close(), { signal: events.signal });
    viewport?.addEventListener("resize", () => close(), { signal: events.signal });
    viewport?.addEventListener("scroll", () => close(), { signal: events.signal });
    document.addEventListener("scroll", (event) => { if (!menu.contains(event.target)) close(); }, { capture: true, signal: events.signal });
    const observer = typeof MutationObserver === "undefined" ? null : new MutationObserver(() => { if (!trigger.isConnected) { shown = false; cleanup(); } });
    observer?.observe(document.documentElement, { childList: true, subtree: true });
    cleanup = () => { events.abort(); observer?.disconnect(); };
  };
  trigger.onclick = (e) => e.stopPropagation();
  trigger.onkeydown = (e) => {
    if (e.key !== "ArrowDown" && e.key !== "ArrowUp") return;
    e.preventDefault(); e.stopPropagation();
    nextFocus = e.key === "ArrowUp" ? items.length - 1 : 0;
    if (shown) { items[nextFocus].focus(); nextFocus = 0; }
    else trigger.click();
  };
  menu.onkeydown = (e) => {
    if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); close(true); return; }
    if (e.key === "Tab") { close(); return; }
    const index = items.indexOf(document.activeElement);
    const next = e.key === "Home" ? 0 : e.key === "End" ? items.length - 1
      : e.key === "ArrowDown" ? (index + 1) % items.length
      : e.key === "ArrowUp" ? (index < 0 ? items.length - 1 : index + items.length - 1) % items.length : -1;
    if (next >= 0) { e.preventDefault(); e.stopPropagation(); items[next].focus(); }
  };
  return wrap(trigger, menu);
}

// Native popovers escape table clipping; disabled alternatives keep their reason visible.
function actionMenu(trigger, options, label, fail) {
  const menu = css(el("div", { id: `wtc-actions-${uuid()}`, className: "wtc-context-menu", popover: "auto", role: "menu", ariaLabel: label }),
    "position:fixed;inset:auto;margin:0;padding:4px;box-sizing:border-box;width:264px;max-width:calc(100vw - 24px);max-height:calc(100dvh - 24px);overflow:auto;border:1px solid var(--border-strong,var(--border,#444));border-radius:8px;background:var(--card,var(--bg,#161920));color:var(--text,inherit);box-shadow:0 6px 20px rgba(0,0,0,.2);font:inherit;font-size:12px;line-height:16px;visibility:hidden");
  trigger.popoverTargetElement = menu; trigger.ariaHasPopup = "menu"; trigger.ariaControls = menu.id; trigger.ariaExpanded = "false";
  let shown = false, focusIndex = 0, cleanup = () => {};
  const close = (focus = false) => { options.forEach(o => o.preview?.(false)); if (shown) menu.hidePopover(); if (focus && trigger.isConnected) trigger.focus(); };
  const items = options.map(option => {
    const navigate = !option.disabled && /^https?:\/\//.test(option.href ?? "");
    const item = css(el(navigate ? "a" : "button", { ...(navigate ? {href:option.href,target:"_blank",rel:"noopener noreferrer"} : {type:"button"}), role: "menuitem", tabIndex: -1, disabled: !!option.disabled, ariaLabel: option.label, title: option.detail ?? option.label, className: "wtc-context-option" },
      el("span", { textContent: option.label }), option.detail ? css(el("span", { textContent: option.detail }), "display:block;font-size:11px;color:var(--muted,#888);line-height:15px") : null),
      `display:block;width:100%;margin:0;padding:6px 8px;box-sizing:border-box;border:0;border-radius:4px;background:transparent;color:inherit;text-decoration:none;text-align:left;white-space:normal;font:inherit;line-height:16px;cursor:${option.disabled ? "default" : "pointer"};opacity:${option.disabled ? ".65" : "1"}`);
    if (option.preview) {
      for (const event of ["pointerenter", "focus"]) item.addEventListener(event, () => { if (!item.disabled) option.preview(true); });
      for (const event of ["pointerleave", "blur"]) item.addEventListener(event, () => option.preview(false));
    }
    item.onclick = async e => { if (!navigate) e?.preventDefault(); e?.stopPropagation(); if (item.disabled) return; close(true); if (navigate) return; try { await option.run?.(); } catch (err) { fail(String(err?.message ?? err)); } };
    return item;
  });
  menu.append(...items);
  const available = items.filter(item => !item.disabled);
  menu.onbeforetoggle = e => { shown = e.newState === "open"; trigger.ariaExpanded = String(shown); cleanup(); if (shown) menu.style.visibility = "hidden"; };
  menu.ontoggle = e => {
    if (e.newState !== "open" || !shown || !trigger.isConnected) return;
    const viewport = globalThis.visualViewport, edge = 12;
    const left = (viewport?.offsetLeft ?? 0) + edge, top = (viewport?.offsetTop ?? 0) + edge;
    const right = left + (viewport?.width ?? globalThis.innerWidth ?? 1024) - 2 * edge, bottom = top + (viewport?.height ?? globalThis.innerHeight ?? 768) - 2 * edge;
    menu.style.maxWidth = `${right-left}px`; menu.style.maxHeight = `${bottom-top}px`;
    const small = right - left <= 496; menu.style.width = small ? `${right-left}px` : "264px";
    const a = trigger.getBoundingClientRect(), box = menu.getBoundingClientRect();
    menu.style.left = `${small ? left : Math.max(left,Math.min(a.right-box.width,right-box.width))}px`;
    menu.style.top = `${Math.max(top,Math.min(a.top-6-box.height >= top ? a.top-6-box.height : a.bottom+6,bottom-box.height))}px`;
    menu.style.visibility = "visible"; available[focusIndex]?.focus(); focusIndex = 0;
    if (!shown || !trigger.isConnected) return;
    const events = new AbortController();
    globalThis.addEventListener?.("resize", () => close(), {signal:events.signal}); viewport?.addEventListener("resize", () => close(), {signal:events.signal});
    viewport?.addEventListener("scroll", () => close(), {signal:events.signal});
    document.addEventListener("scroll", e => { if (!menu.contains(e.target)) close(); }, {capture:true,signal:events.signal});
    const observer = typeof MutationObserver === "undefined" ? null : new MutationObserver(() => { if (!trigger.isConnected) { shown=false; cleanup(); } });
    observer?.observe(document.documentElement,{childList:true,subtree:true}); cleanup=()=>{events.abort();observer?.disconnect();};
  };
  trigger.onclick = e => e?.stopPropagation();
  trigger.onkeydown = e => { if (!["ArrowDown","ArrowUp"].includes(e.key)) return; e.preventDefault();e.stopPropagation();focusIndex=e.key==="ArrowUp"?Math.max(0,available.length-1):0;if(shown)available[focusIndex]?.focus();else trigger.click(); };
  menu.onkeydown = e => {
    if(e.key==="Escape"){e.preventDefault();e.stopPropagation();close(true);return;}if(e.key==="Tab"){close();return;}
    const i=available.indexOf(document.activeElement), n=available.length;
    const next=e.key==="Home"?0:e.key==="End"?n-1:e.key==="ArrowDown"?(i+1)%n:e.key==="ArrowUp"?(i<0?n-1:(i+n-1)%n):-1;
    if(next>=0&&n){e.preventDefault();e.stopPropagation();available[next].focus();}
  };
  return css(el("span",{className:"wtc-action-menu"},trigger,menu),"display:inline-flex;width:24px;flex:0 0 24px;align-items:baseline;justify-content:center");
}

// Geometric control glyphs avoid font-dependent centres and spinning hit targets.
const controlIcon = (glyph, size = 14) => {
  const paths = { "+": "M12 5v14M5 12h14", "▶": "M7 4l13 8-13 8V4z", "↻": "M20 11a8 8 0 1 0-2.3 6M20 4v7h-7", "spinner": "M21 12a9 9 0 1 1-9-9", "⤢": "M14 4h6v6M20 4l-7 7M4 20l7-7M4 14v6h6", "⌃": "M6 15l6-6 6 6", "⌄": "M6 9l6 6 6-6", "✕": "M6 6l12 12M18 6L6 18" };
  const ns = "http://www.w3.org/2000/svg", svg = document.createElementNS(ns, "svg"), path = document.createElementNS(ns, "path");
  for (const [key, value] of Object.entries({ viewBox: "0 0 24 24", width: String(size), height: String(size), fill: "none", stroke: "currentColor", "stroke-width": "1.8", "stroke-linecap": "round", "stroke-linejoin": "round", "aria-hidden": "true" })) svg.setAttribute(key, value);
  svg.setAttribute("class", "wtc-header-glyph"); svg.style.cssText = "display:block;transform-box:fill-box;transform-origin:center";
  path.setAttribute("d", paths[glyph] ?? paths["✕"]); svg.append(path);
  return css(el("span", { className: "wtc-header-icon" }, svg), `display:inline-flex;width:${size}px;height:${size}px`);
};

// A shared icon rail and text rail for the brand, repositories and services.
const leadSlot = (icon, width = 20) => css(el("span", { className: "wtc-lead-slot" },
  css(el("span", { className: "wtc-lead-icon" }, icon), "position:absolute;left:50%;top:-.35em;transform:translate(-50%,-50%);display:grid;place-items:center")), `display:inline-block;position:relative;flex:0 0 ${width}px;width:${width}px;height:0;align-self:baseline`);

// Visible branding can change while the plugin and CLI keep their legacy IDs.
// version is what the collection reports: the plugin's and the gateway's wtc.
const versionText = (v) => (v?.plugin || v?.wtc ? `plugin ${v.plugin ?? "?"}, wtc ${v.wtc ?? "?"}` : "");
// The published Taba mark, bundled as vectors so host fonts cannot change it.
const brandMark = () => {
  const ns = "http://www.w3.org/2000/svg", svg = document.createElementNS(ns, "svg");
  for (const [key, value] of Object.entries({ viewBox: "0 0 64 64", width: "18", height: "18", "aria-hidden": "true", focusable: "false", class: "wtc-brand-icon" })) svg.setAttribute(key, value);
  const rect = (attrs) => { const node = document.createElementNS(ns, "rect"); for (const [key, value] of Object.entries(attrs)) node.setAttribute(key, value); svg.append(node); };
  rect({ width: "64", height: "64", rx: "16", fill: "#234e42" });
  for (const [x, y, width] of [[19, 17, 32], [11, 28.5, 36], [15, 40, 38]]) rect({ x, y, width, height: "7", rx: "3.5", fill: "#f7f7ed" });
  rect({ x: "30", y: "12", width: "7", height: "40", rx: "3.5", fill: "#c9d98a", stroke: "#234e42", "stroke-width": "2.5" });
  return css(svg, "display:block;flex:none");
};
// Keep the centered logo out of text flow so it cannot supply the link baseline.
const brand = (size, version, compact = false) => css(el("span", {},
  css(el("a", { href: HOME, target: "_blank", rel: "noopener noreferrer", ariaLabel: "taba home", title: `taba — Make room for the whole task. (opens taba.sh)${version ? `\n${versionText(version)}` : ""}` },
    leadSlot(brandMark()),
    " ", el("span", { className: "wtc-brand-word", textContent: "taba" })),
    "display:inline-flex;align-items:baseline;gap:6px;min-height:1.5em;color:var(--text-strong, inherit);text-decoration:none;font-weight:700"),
  compact ? null : el("span", { className: "wtc-brand-suffix", textContent: "Collection" })), `display:inline-flex;align-items:baseline;gap:.3em;flex:none;white-space:nowrap;font-weight:600;font-size:${size}`);

const sectionHeading = (text, ...extra) => css(el("div", {},
  css(el("h3", { textContent: text }), "margin:0;font-size:14px;font-weight:600;color:var(--text-strong,inherit)"), ...extra),
  "display:flex;align-items:baseline;gap:8px;margin-bottom:6px");

// Browser surfaces share the CLI's facts and actions. Rows retain fact tracks
// observed until the conversation changes; narrow views fold beneath repo text.
// Actions stay on the relevant fact, in the row's next-step slot, or in the
// header toolbar, which remains available when the card is collapsed.
// v supplies the view lifecycle, conversation and gateway operations.
const NARROW_PX = 520;
const REPO_IDENTITY_INSET = 26;
const pullRequestRows = (repos) => {
  const rows = new Map();
  for (const r of repos) for (const pr of [...(r.pullRequests ?? []), ...(r.pullRequest ? [r.pullRequest] : [])]) {
    rows.set(`${r.forge}/${r.owner}/${r.repo ?? r.project}#${pr.number}`, { ...r, pullRequest: pr });
  }
  return [...rows.values()];
};
// Activity is shared by views of one collection for this browser session.
// It is UI feedback, not an executable plan or a durable completion receipt.
const activityStore = new Map();
const recoveryQueues = new Map();
const requestFeedback = new Map();
function notifyFactFeedback(collection) {
  for (const [key, entry] of store) if (key === collection || entry.data?.collection?.sessionKey === collection) for (const notify of entry.listeners) notify(entry);
}
const feedbackStore = new Map();
// Hand off the requested service locally, independently of optional host panel
// props; log output stays in the panel that fetched it.
const serviceLogIntents = new Map(), serviceLogListeners = new Set();
function recordActivity(collection, id, item) {
  const records = activityStore.get(collection) ?? new Map();
  const previous = records.get(id);
  // A late handoff response cannot undo a verified native recovery receipt.
  const updated = previous?.completedRecovery ? { ...previous, ...item, state:previous.state, status:previous.status, detail:previous.detail, followUp:undefined, at:previous.at } : { ...previous, ...item, at: Date.now() };
  records.set(id, updated);
  while (records.size > 100) {
    const removable = [...records.keys()].find(id => ![...requestFeedback.values()].some(state => state.id === id && ["running","pending","unconfirmed"].includes(state.phase)));
    if (!removable) break;
    records.delete(removable);
  }
  activityStore.set(collection, records);
  for (const [key, entry] of store) if (key === collection || entry.data?.collection?.sessionKey === collection) for (const notify of entry.listeners) notify(entry);
  return updated;
}
// Native completion is durable evidence; the surrounding activity remains
// browser-session feedback. Never infer success merely from a missing marker.
function reconcileRecoveryActivity(collection, repos) {
  const records = activityStore.get(collection);
  if (!records) return;
  let changed = false;
  for (const r of repos) {
    const receipt = r.catchUpRecoveryCompleted;
    if (r.error || r.catchUpRecovery || r.catchUpRecoveryError || receipt?.kind !== "taba.catch-up-recovery-completed/v1" || !receipt.recoveryRef || !/^(?:[a-f0-9]{40}|[a-f0-9]{64})$/.test(receipt.head ?? "")) continue;
    for (const [id, item] of records) {
      if (!item.recoveryRef || item.recoveryRef !== receipt.recoveryRef || item.repoSessionKey !== r.sessionKey || !item.repoPath || item.repoPath !== r.path) continue;
      const request = requestFeedback.get(`${collection}|${r.sessionKey}`);
      if (request?.id === id && request.path === r.path && ["catch-up", "resolve", "merge"].includes(request.kind) && ["running", "pending", "unconfirmed"].includes(request.phase)) { request.phase = "completed"; changed = true; }
      if (item.completedRecovery) continue;
      changed = true;
      const text = `${item.repo}: local edits recovered; backup retained.${r.dirty ? " Changes remain uncommitted." : ""}`;
      records.set(id, { ...item, state: "completed", status: "Completed", detail: text, completedRecovery: receipt, at: Date.now() });
      if (feedbackStore.get(collection)?.activityId === id) feedbackStore.set(collection, { text, failed: false, action: null, activityId: id, at: Date.now() });
    }
  }
  if (changed) notifyFactFeedback(collection);
}
async function trackedServiceRequest(host, sessionKey, agentId, payload, doing, say, logKey = sessionKey) {
  const id = uuid(), repo = payload.group ?? "Services";
  feedbackStore.set(logKey, { text: doing, failed: false, action: null, activityId: id, at: Date.now() });
  recordActivity(logKey, id, { repo, action: `svc-${payload.op}`, state: "running", status: doing });
  try {
    const result = await svcRequest(host, sessionKey, agentId, payload);
    const message = result?.missing ? `Install ${result.missing} before retrying this service action.` : result?.message ?? `${payload.op} request completed; check the service state.`;
    recordActivity(logKey, id, { state: result?.missing ? "blocked" : "completed", status: message });
    say(`${repo}: ${message}`, false, null, id);
    return result;
  } catch (err) {
    recordActivity(logKey, id, { state: "failed", status: String(err?.message ?? err) });
    throw err;
  }
}

async function trackedRefresh(watch, sessionKey, say, currentSession = () => sessionKey, alive = () => true) {
  const id = uuid();
  recordActivity(sessionKey, id, { repo: "Collection", action: "refresh", state: "running", status: "Refreshing collection status…" });
  const entry = await watch?.refresh();
  if (entry?.error) {
    recordActivity(sessionKey, id, { state: "failed", status: entry.error });
    if (alive() && currentSession() === sessionKey) say(`Refresh failed: ${entry.error}`, true, null, id);
  } else {
    recordActivity(sessionKey, id, { state: "completed", status: "Collection status refreshed." });
    if (alive() && currentSession() === sessionKey) say("Collection status refreshed.", false, null, id);
  }
}

const REPO_NEXT_WIDTH = 132, REPO_COLUMN_GAP = 10;
const headerActionCapacity = width => Math.max(1, Math.min(4, Math.floor(((width || 0) - 300) / 108)));
const repositoryColumns = (narrow, actionWidth = REPO_NEXT_WIDTH, facts = { width: 104, pr: true }) => narrow
  ? [{ head: "Repository", width: "minmax(0,1fr)", overflow: true }, { head: "Next", width: `${actionWidth}px`, align: "end" }]
  : [{ head: "Repository", width: "fit-content(40%)" }, { head: "Branch", width: "minmax(64px,200px)" }, { head: "Changes", width: `${facts.width}px` }, ...(facts.pr ? [{ head: "Pull request", width: "164px" }] : []), { head: "Next", width: `minmax(${actionWidth}px,1fr)`, align: "end" }];
const actionFeedback = () => el("style", { textContent: `
.wtc-tunnel-trigger:hover,.wtc-tunnel-existing:hover,.wtc-tunnel-option:is(:hover,:focus-visible) { background:var(--bg-hover,rgba(128,128,128,.12)) !important;color:var(--text-strong,var(--text,inherit)) !important; }
.wtc-tunnel-trigger:focus-visible,.wtc-tunnel-option:focus-visible { outline:2px solid var(--focus,var(--accent,#888));outline-offset:2px; }
.wtc-tunnel-option:focus-visible { outline-offset:-2px; }

.wtc-service-bulk:is(:hover,:focus-visible) { background:var(--bg-hover,rgba(128,128,128,.12)) !important;color:var(--text-strong,var(--text,inherit)) !important; }
.wtc-service-play:is(:hover,:focus-visible) { --wtc-service-play-bg:var(--bg-hover,rgba(128,128,128,.12));--wtc-service-play-color:var(--text-strong,var(--text,inherit)); }

.wtc-service-label:disabled { cursor:default !important;text-decoration:none !important; }
.wtc-repo-identity .wtc-repo-warning:empty { display:none !important; }
.wtc-next-step { width:var(--wtc-action-width,${REPO_NEXT_WIDTH}px);justify-content:flex-end !important; }
 .wtc-next-step>.wtc-action-control,.wtc-next-step>select,.wtc-next-step>.wtc-step-status,.wtc-next-step>.wtc-primary-spacer { width:104px;flex:0 0 104px; }
.wtc-next-step>.wtc-action-menu { width:24px;flex:0 0 24px; }
.wtc-toolbar-inline { width:104px;flex:0 0 104px; }
.wtc-action-toolbar>.wtc-action-control,.wtc-toolbar-primary>.wtc-action-control { min-height:24px !important;height:24px; }
.wtc-action-menu>button { width:24px !important;min-width:24px !important;padding:0 !important; }
.wtc-step-status { width:104px;text-align:center; }
/* Primary actions and their menu rail share one fixed layout in every row. */
.wtc-next-step { display:flex;align-items:baseline;gap:4px; }
.wtc-table .wtc-next-step>.wtc-action-control,.wtc-next-step .wtc-action-menu>.wtc-action-control { min-height:24px !important;height:24px; }
.wtc-context-option:not(:disabled):is(:hover,:focus-visible) { background:var(--bg-hover,rgba(128,128,128,.12)) !important;outline:2px solid var(--accent,#75c8c1);outline-offset:-2px; }
.wtc-restore-control { min-height:24px !important;height:24px;--wtc-control-border:var(--border-strong,rgba(128,128,128,.35)); }
.wtc-toolbar-primary>.wtc-restore-control:disabled { --wtc-control-border:var(--border-strong,rgba(128,128,128,.35)); }
.wtc-toolbar-primary:empty,.wtc-summary-facts:empty,.wtc-header-controls>button,.wtc-header-controls>span:last-child { align-self:center; }
.wtc-action-control:not(:disabled):hover { --wtc-control-bg:var(--bg-hover,rgba(128,128,128,.16)); --wtc-control-border:var(--border-strong,rgba(128,128,128,.5)); --wtc-control-text:var(--text-strong,var(--text,inherit)); box-shadow:none; }
.wtc-next-actions button:disabled { background:transparent !important;border-color:var(--border-strong,rgba(128,128,128,.35)) !important;color:var(--muted,#888) !important; }
.wtc-confirm-publication:not(:disabled) { background:var(--primary,var(--accent,#75c8c1)) !important;color:var(--primary-foreground,var(--accent-foreground,#151a22)) !important;border-color:var(--primary,var(--accent,#75c8c1)) !important; }
:is(.wtc-action-toolbar,.wtc-bulk-actions) .wtc-action-control,button.wtc-pr-history { --wtc-control-bg:transparent;--wtc-control-border:transparent;--wtc-control-text:var(--muted,#888); }
:is(.wtc-action-toolbar,.wtc-bulk-actions) .wtc-action-control:not(:disabled):hover,button.wtc-pr-history:not(:disabled):hover { --wtc-control-bg:var(--bg-hover,rgba(128,128,128,.12));--wtc-control-border:transparent;--wtc-control-text:var(--text-strong,var(--text,inherit));box-shadow:none; }
.wtc-action-toolbar .wtc-toolbar-primary>.wtc-action-control:not(:disabled) { --wtc-control-border:var(--border-strong,rgba(128,128,128,.35));--wtc-control-text:var(--text,inherit); }


.wtc-count-control,.wtc-repositories .wtc-count-control.wtc-action-control { box-sizing:border-box;width:36px;min-width:0 !important;min-height:24px !important;height:24px;align-self:baseline;padding:0 1px !important;display:inline-grid !important;grid-template-columns:8px minmax(0,1fr);column-gap:1px;align-content:center;align-items:baseline !important; }
.wtc-count-icon { text-align:center;font-size:12px; }
.wtc-repo-facts>span { min-height:24px !important; }
@keyframes wtc-cell-spin { to { transform:rotate(360deg); } }
.wtc-cell-spinner { display:inline-block;animation:wtc-cell-spin 1s linear infinite; }
@media (prefers-reduced-motion:reduce) { .wtc-cell-spinner { animation:none; } }
.wtc-repositories .wtc-count-control:disabled { opacity:.5;cursor:default !important; }
.wtc-repositories .wtc-count-control[data-feedback="completed"] { opacity:1; }
.wtc-repositories .wtc-count-control[aria-busy="true"] { opacity:.85; }
.wtc-repo-facts .wtc-repo-pr-slots { grid-template-columns:minmax(0,1fr) !important; }
.wtc-repositories .wtc-count-control { border-color:transparent !important;background:transparent !important; }
.wtc-repositories { --wtc-control-height:24px; }
.wtc-bulk-actions .wtc-count-control:not(.wtc-local-change) { width:24px !important; }
.wtc-repositories .wtc-action-preview-wash[data-action-preview="true"] { background:color-mix(in srgb,var(--accent,#75c8c1) 8%,transparent); }
.wtc-repositories .wtc-count-control:is(:hover,:focus-visible):not(:disabled) { border-color:var(--border-strong,rgba(128,128,128,.4)) !important;background:var(--bg-hover,rgba(128,128,128,.12)) !important; }
.wtc-repositories .wtc-local-change { box-sizing:border-box;width:44px !important;min-width:44px !important;max-width:44px;height:24px !important;min-height:24px !important;padding:0 1px !important;grid-template-columns:12px minmax(0,1fr); }
.wtc-local-change .wtc-count-value { width:24px;justify-self:start; }
.wtc-column-commit { justify-self:start;text-align:left; }

.wtc-count-value { min-width:0;text-align:right;font-size:11px;overflow:hidden;text-overflow:ellipsis; }
@container wtc-panel (max-width:326px) { .wtc-brand-suffix { display:none; } }
.wtc-collection>header>div:first-child { flex-wrap:nowrap !important; }
.wtc-action-control:not(:disabled):active { --wtc-control-bg:var(--secondary,rgba(128,128,128,.22)); }
@media(min-width:521px) { .wtc-header-controls .wtc-header-icon { transform:translateY(-.16em); } }
@keyframes wtc-refresh { to { transform:rotate(360deg); } }
.wtc-refresh-control[aria-busy="true"] .wtc-header-glyph { animation:wtc-refresh 1s linear infinite; }
@media(prefers-reduced-motion:reduce) { .wtc-refresh-control[aria-busy="true"] .wtc-header-glyph { animation:none; opacity:.6; } }
.wtc-action-control:focus-visible { outline:2px solid var(--accent,#75c8c1); outline-offset:-2px; }
.wtc-collection-card summary:focus-visible { outline:2px solid var(--accent,#75c8c1); outline-offset:2px; border-radius:10px; }
@media(pointer:coarse) {
  .wtc-base-services { --wtc-state-size:24px; }
  .wtc-base-services .wtc-table { row-gap:6px !important; }
}
@media(max-width:520px) {
  .wtc-collection-card { --wtc-header-top-extra:0px; }
  .wtc-collection-card>summary { column-gap:4px !important; }
  .wtc-collection-card, .wtc-collection, .wtc-transient-actions { --wtc-control-height:32px; --wtc-control-width:24px; --wtc-icon-size:36px; --wtc-state-size:24px; }
  .wtc-header-controls { --wtc-control-height:36px; --wtc-control-width:36px; }
  .wtc-header-feedback { display:none; }
  .wtc-header-controls { gap:0 !important; }
  .wtc-next-step .wtc-action-control, .wtc-bulk-actions .wtc-action-control { min-height:32px !important; }
  .wtc-repo-identity { min-height:20px !important; }
  .wtc-header-controls .wtc-toolbar-primary-required { max-width:104px; }
}
@container wtc-card (max-width:520px) { .wtc-summary-facts { display:none !important; } }
@container wtc-card (max-width:340px) {
  .wtc-brand-word { font-size:11px; }
  .wtc-collection-card>summary { column-gap:4px !important; }
  .wtc-header-controls { gap:0 !important; }
  .wtc-header-controls .wtc-toolbar-primary { width:88px !important;min-width:88px !important;flex-basis:88px !important; }
  .wtc-header-controls>button { min-width:min(32px,var(--wtc-icon-size,24px)) !important; }
  .wtc-header-controls .wtc-action-toolbar>button { width:28px !important;min-width:28px !important; }
}
@container wtc-card (max-width:260px) { .wtc-brand-word { display:none; } .wtc-collection-card>summary>span:first-child { align-self:center; } }

.wtc-pr-preview { overflow-wrap:anywhere;border-left:2px solid var(--border,rgba(128,128,128,.3));padding-left:10px;margin-top:6px;padding-bottom:8px; }
.wtc-publication-summary>strong { font-size:14px !important; }
.wtc-publication-controls>.wtc-action-control { height:32px !important;min-height:32px !important; }
.wtc-pr-preview-cue { font-size:11px;color:var(--muted,#888);padding:0 0 6px;border-bottom:1px solid var(--border,rgba(128,128,128,.3)); }
.wtc-pr-preview>summary { cursor:pointer;color:var(--muted,#888);font-size:11px;margin-bottom:6px; }
.wtc-pr-preview>strong { display:block;font-size:12px;line-height:1.5; }
.wtc-pr-preview-markdown { font-size:12px;line-height:1.55;overflow-wrap:anywhere; }
.wtc-pr-preview-markdown :is(p,ul,ol,blockquote,pre,table) { margin:8px 0; }
.wtc-pr-preview-markdown :is(h2,h3,h4,h5,h6) { font-size:12px;line-height:1.5;margin:12px 0 4px;font-weight:600; }
.wtc-pr-preview-markdown :is(ul,ol) { padding-left:20px; }
.wtc-pr-preview-markdown li>p { margin:3px 0; }
.wtc-pr-preview-markdown p { color:inherit; }
.wtc-preview-task-item { list-style:none; }
.wtc-preview-task { display:inline-block;width:14px;margin-left:-18px;margin-right:4px; }
.wtc-pr-preview-markdown blockquote { border-left:2px solid var(--border);padding-left:10px;color:var(--muted); }
.wtc-pr-preview-markdown code { font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:11px;white-space:pre-wrap; }
.wtc-pr-preview-markdown pre { padding:8px;background:var(--bg-elevated,rgba(128,128,128,.08));border-radius:6px;overflow:auto; }
.wtc-pr-preview-markdown a { color:var(--text,inherit);text-decoration:underline; }
.wtc-pr-preview-markdown table { border-collapse:collapse;width:100%; }
.wtc-pr-preview-table { overflow:auto; }
.wtc-pr-preview-markdown :is(th,td) { text-align:left;padding:3px 8px;border-bottom:1px solid var(--border); }
.wtc-publication-confirmation>.wtc-publication-controls { flex:none; }

` });
function collectionTables(v) {
  let inlineCapacity = 1, actionColumns = 0;
  let previewRoot = null;
  const preview = (targets, on) => {
    const keys = new Set(on ? targets.map(r => r.sessionKey) : []);
    for (const cell of previewRoot?.querySelectorAll?.("[data-repo-session]") ?? []) cell.setAttribute("data-action-preview", String(keys.has(cell.getAttribute("data-repo-session"))));
  };
  const previewButton = (button, targets) => {
    for (const event of ["pointerenter", "focus"]) button.addEventListener(event, () => { if (!button.disabled) preview(targets, true); });
    for (const event of ["pointerleave", "blur"]) button.addEventListener(event, () => preview(targets, false));
    return button;
  };
  let repoFacts = { ahead: false, behind: false, pr: false };
  const FACT_GAP = 10;
  const factTracks = () => [44, ...(repoFacts.ahead ? [36] : []), ...(repoFacts.behind ? [36] : [])];
  const factWidth = () => factTracks().reduce((n, x) => n + x, 0) + (factTracks().length - 1) * FACT_GAP;
  const repoBaseWidth = () => 128 + factWidth() + (repoFacts.pr ? 164 : 0) + (repoFacts.pr ? 4 : 3) * REPO_COLUMN_GAP;
  const repositoryCompact = width => width > 0 && width < repoBaseWidth() + (actionColumns ? REPO_NEXT_WIDTH : 24);
  const repoCapacity = width => Math.max(1, Math.floor(((width || 0) - repoBaseWidth() - 24) / 108));
  const repositoryLayoutKey = width => `${repositoryCompact(width)}:${repositoryCompact(width) ? 1 : repoCapacity(width)}`;
  // Repositories being pushed, and being caught up.
  const busy = new Set();
  const acting = new Set();
  const openDisclosures = new Map();
  const keepDisclosure = (node, key) => {
    key = JSON.stringify([v.collectionKey?.() ?? v.key(), key]);
    node.open = openDisclosures.has(key) ? openDisclosures.get(key) : !!node.open;
    node.ontoggle = () => {
      openDisclosures.set(key, !!node.open);
      if (openDisclosures.size > 200) openDisclosures.delete(openDisclosures.keys().next().value);
    };
    return node;
  };
  const active = (conversation) => v.alive?.() !== false && v.key() === conversation;
  let serviceLog = null, serviceLogRequest = 0;
  const clearServiceLogs = () => { serviceLog = null; serviceLogRequest++; };
  async function loadServiceLogs(group, label, targets = []) {
    if (v.alive?.() === false) return;
    if (v.openServiceLogs) return v.openServiceLogs(group, label, targets);
    const key = v.key(), request = ++serviceLogRequest;
    serviceLog = { key, group, label, targets, loading: true };
    v.showServiceLogs?.();
    v.rerender();
    try {
      const result = await call(v.host, "svc-logs", key, v.agent(), { group });
      if (active(key) && request === serviceLogRequest) serviceLog = { key, group, label, targets, text: String(result?.logs ?? ""), truncated: !!result?.truncated };
    } catch (error) {
      if (active(key) && request === serviceLogRequest) serviceLog = { key, group, label, targets, error: String(error?.message ?? error) };
    }
    if (active(key) && request === serviceLogRequest) v.rerender();
  }
  function serviceLogs() {
    const current = serviceLog;
    if (!current || current.key !== v.key()) return null;
    if (current.node) return current.node;
    const refresh = nextButton("Refresh", "Refresh service logs", () => loadServiceLogs(current.group, current.label, current.targets), current.loading);
    const close = nextButton("Close", "Close service logs", () => { clearServiceLogs(); v.rerender(); });
    const text = current.loading ? "Loading logs…" : current.error ? `Could not read logs: ${current.error}` : current.text || "No logs recorded yet.";
    const pre = css(el("pre", { textContent: text, tabIndex: 0, ...(current.error ? { role: "alert" } : current.loading ? { role: "status" } : {}) }),
      "margin:0;min-width:0;max-height:240px;overflow:auto;white-space:pre-wrap;overflow-wrap:anywhere;font:12px/1.5 var(--mono,ui-monospace,monospace)");
    if (!current.loading && !current.error) queueMicrotask(() => { if (pre.isConnected && serviceLog === current) pre.scrollTop = pre.scrollHeight; });
    current.node = css(el("section", { className: "wtc-service-log", ariaLabel: `${current.label} logs` },
      css(el("div", {}, css(el("strong", { textContent: `Logs · ${current.label}` }), "min-width:0;overflow-wrap:anywhere"),
        css(el("span", {}, refresh, close), "margin-left:auto;display:flex;gap:4px;flex:none")), "display:flex;align-items:baseline;gap:8px"),
      current.targets.length > 1 ? css(el("div", { ariaLabel: "Service log tasks" }, ...current.targets.map(target =>
        nextButton(target.split("/").slice(2).join("/") || "main", `Read logs for ${target}`, () => loadServiceLogs(target, current.label, current.targets), current.loading || target === current.group))), "display:flex;flex-wrap:wrap;gap:4px") : null,
      el("span", { textContent: `Recent output · last 100 lines${current.truncated ? " · showing recent output only (text limit)" : ""}` }), pre),
      "min-width:0;display:grid;gap:8px;padding:10px 12px;border:1px solid var(--border,rgba(128,128,128,.3));border-radius:8px;font-size:12px;color:var(--text,inherit)");
    return current.node;
  }
  // Mirror browser-local publication progress; this is not a gateway lock.
  const sharedPushBusy = (r, collection = v.collectionKey?.() ?? v.key()) => [...(activityStore.get(collection)?.values() ?? [])].some(item => item.action === "push" && item.repoSessionKey === r.sessionKey && item.inFlight);
  const requestUnresolved = (r, collection = v.collectionKey?.() ?? v.key()) => ["running", "pending", "unconfirmed"].includes(requestFeedback.get(`${collection}|${r.sessionKey}`)?.phase);
  const reservedInCollection = (r, collection) => requestUnresolved(r, collection) || acting.has(r.sessionKey) || busy.has(r.sessionKey) || sharedPushBusy(r, collection);
  const reserved = r => reservedInCollection(r, v.collectionKey?.() ?? v.key());
  const catchUpRunning = r => {
    const state = requestFeedback.get(`${v.collectionKey?.() ?? v.key()}|${r.sessionKey}`);
    return state?.phase === "running" && ["catch-up","return-to-tip"].includes(state.kind) && !state.handoffAt;
  };
  const recoveryQueueBusy = () => recoveryQueues.has(v.collectionKey?.() ?? v.key());
  const recoveryBusy = r => [...(activityStore.get(v.collectionKey?.() ?? v.key())?.values() ?? [])].some(item => item.action === "restore" && item.repoSessionKey === r.sessionKey && item.inFlight);
  async function restoreCheckouts(repos) {
    const conversation = v.key(), logKey = v.collectionKey?.() ?? conversation;
    if (repos.some(r => requestUnresolved(r))) { v.say("A repository action is unresolved; inspect its result before restoring the checkout."); return; }
    if (recoveryQueues.has(logKey)) { v.say("A checkout restore is already running; wait for its results."); return; }
    const captured = repos.map(r => structuredClone(r));
    const queue = { targets: new Set(captured.map(r => r.sessionKey)), index: 0, total: captured.length };
    recoveryQueues.set(logKey, queue);
    try { for (const r of captured) {
      if (!active(conversation)) {
        recordActivity(logKey, uuid(), { repo: nameOf(r), repoSessionKey: r.sessionKey, action: "restore", state: "cancelled", status: "Restore cancelled before dispatch: the view closed or changed" });
        continue;
      }
      if (recoveryBusy(r) || r.recovery?.state !== "restorable") continue;
      queue.index += 1;
      const id = uuid(), name = nameOf(r);
      const say = (text, failed = false) => { if (active(conversation)) v.say(text, failed, null, id); };
      recordActivity(logKey, id, { repo: name, repoSessionKey: r.sessionKey, action: "restore", inFlight: true, state: "running", status: `Restoring ${name}…` });
      say(`Restoring ${name}…`); v.rerender();
      try {
        const out = await call(v.host, "restore-checkout", conversation, v.agent(), { ownerSessionKey: r.sessionKey, key: r.recovery.key });
        const status = `${name}: ${out.message ?? "checkout restored"}`;
        recordActivity(logKey, id, { state: "completed", status }); say(status);
      } catch (err) {
        const status = `${name}: restore failed — ${String(err?.message ?? err)}`;
        recordActivity(logKey, id, { state: "failed", status }); say(status, true);
      } finally {
        recordActivity(logKey, id, { inFlight: false });
        queue.targets.delete(r.sessionKey);
        if (active(conversation)) { try { await v.refresh(); } catch {} v.rerender(); }
      }
    } } finally { recoveryQueues.delete(logKey); if (active(conversation)) v.rerender(); }
  }
  async function sendToAuthor(conversation, skill, input) {
    if (!active(conversation)) throw Error("The view closed or changed before handoff");
    return skill ? askSkill(v.host, conversation, skill, input) : askConversation(v.host, conversation, input);
  }

  const revealPublication = () => { if (!v.publicationBesideCollapsedBar) v.reveal?.(); };
  const actionable = (r) => !r.error && (r.next || r.choices?.actions?.length || r.dirty > 0);
  async function askCommit(r, request) {
    if (batchIsRunning() || v.alive?.() === false) return;
    const conversationKey=v.key();
    const say=(...args)=>{if(active(conversationKey))v.say(...args)};
    say(`Asking the conversation to create local commits for ${nameOf(r)}…`);
    v.rerender();
    try {
      const result = await dispatchRepoRequests([{repo:r, state:request}], conversationKey, v.collectionKey?.() ?? conversationKey, "wtc", `${nameOf(r)}: review and commit the intended uncommitted changes. Recheck its current state and branch policy first, and report the resulting commit. Leave publication for the person's push action.`);
      if (!result.handedOff) return result;
      say(`${nameOf(r)}: asked the conversation to save intended changes as local commits. Pushing and opening a PR are separate.`);
      return { handedOff: true };
    } catch (err) { say(`${nameOf(r)}: ${String(err?.message ?? err)}`, true); return { error: String(err?.message ?? err) }; }
    finally { if(active(conversationKey))await v.refresh(); }
  }
  async function reviewPRText(r, url, conversationKey, action) {
    if (!active(conversationKey)) return;
    if (acting.has(r.sessionKey)) return;
    acting.add(r.sessionKey);
    try {
      await sendToAuthor(conversationKey, "wtc", `${nameOf(r)}: review the title and description of ${url} against the full current PR diff, including the latest pushed commits. Recheck the PR's current state and head first. Preserve useful human edits. If the text no longer describes the complete change, propose a revised title and description for the person to review; otherwise explain that it still fits. This is a review of the PR text, not a request for a code review. Do not change commits, push, merge, or edit the remote PR text in this step.`);
      if (action) action.done = true;
      if (active(conversationKey)) v.say(`${nameOf(r)}: asked the conversation to review the PR title and description and propose any needed revisions.`);
    } catch (err) {
      if (active(conversationKey)) v.say(String(err?.message ?? err), true);
    } finally { acting.delete(r.sessionKey); }
  }
  const nextLabel = (s) => ({ "refresh": "Refresh", "svc-up": "Start service", "svc-down": "Stop service", "svc-restart": "Restart service", "svc-tunnel": "Tunnel", "svc-portal": "Portal", "return-to-tip": "Return to tip", "catch-up": "Catch up", "new-branch": "Create a branch", resolve: "Resolve conflicts", merge: "Merge the base", "fix-checks": "Fix checks", "address-review": "Address review", push: "Push", "merge-pr": "Open pull request", commit: "Commit changes", "follow-pr": "Check PR progress", review: "Run independent review" })[s.step] ?? s.step;
  const nextRoute = (s) => s.by === "person" && s.step === "merge-pr" ? "pr"
    : s.by === "wtc" && s.approve === "person" && s.gateway === "github_publish" ? "publish"
    : s.by === "agent" && (!s.approve || s.approve === "agent") && s.skill ? "agent"
    : s.by === "wtc" && s.approve === "agent" && s.command?.[0] === "wtc" && s.command?.[1] === "catch-up" ? "run"
    : s.by === "wtc" && !s.approve && JSON.stringify(s.command) === JSON.stringify(["wtc", "catch-up"]) ? "run" : null;
  const nextPayload = (r, step) => ({ ownerSessionKey: r.sessionKey, key: step.key,
    ...(step.step === "catch-up" && step.by === "wtc" && step.approve === "agent" ? { approveCatchUp: true } : {}) });
  async function actNext(r, logId = uuid()) {
    if (r.next?.step === "commit" && commitBlocked(r)) return;
    if (batchIsRunning() || v.alive?.() === false) return;
    const conversationKey = v.key();
    const logKey = v.collectionKey?.() ?? conversationKey;
    const tracked = ["agent", "run"].includes(nextRoute(r.next));
    const request = r.next.step === "commit" ? requestFeedback.get(requestKey(r, logKey)) : tracked ? beginRepoRequest(r, logKey, r.next.step, logId) : null;
    if (tracked && (!ownsRepoRequest(r, request, logKey) || request.id !== logId)) return;
    const say = (text, failed = false, action = null) => {
      if (request?.phase === "completed") return;
      const previous = activityStore.get(logKey)?.get(logId);
      const item = recordActivity(logKey, logId, { repo: nameOf(r), action: r.next.step, status: previous?.status ?? text, ...(failed ? { state: "failed" } : {}), detail: text, followUp: action });
      if (active(conversationKey) && !item.completedRecovery) v.say(text, failed, action, logId);
    };
    const log = (status) => request?.phase !== "completed" && recordActivity(logKey, logId, { repo: nameOf(r), repoSessionKey:r.sessionKey, repoPath:r.path, recoveryRef:r.catchUpRecovery, action: r.next.step, status });
    say(`${nameOf(r)}: ${r.next.step === "catch-up" ? "Catching up" : nextLabel(r.next)}…`);
    log("Rechecking…");
    v.rerender();
    try {
      if (tracked && nextRoute(r.next) === "run") request.dispatchedAt = Date.now();
      const out = await call(v.host, "next", conversationKey, v.agent(), nextPayload(r, r.next));
      if (!active(conversationKey)) return;
      if (tracked && !ownsRepoRequest(r, request, logKey)) return;
      const fresh = out.repo;
      if (tracked && nextRoute(r.next) === "run" && !out.performed) delete request.dispatchedAt;
      if (out.performed) {
        const outcome = await catchUpRepo(v.host, v.agent(), fresh, say, (input, recoveryRef) => dispatchRepoRequests([{repo:r,state:request}], conversationKey, logKey, CATCH_UP, input, recoveryRef), async () => out);
        log(outcome?.unconfirmed ? "Action handoff outcome unconfirmed; inspect the conversation" : outcome?.handedOff ? "Sent to conversation; completion pending" : out.code ? "Failed: " + (out.message ?? out.code) : "Completed");
        if (outcome?.recoveryRef && ownsRepoRequest(r, request, logKey)) recordActivity(logKey, logId, { recoveryRef:outcome.recoveryRef });
        if (!outcome?.handedOff && !outcome?.unconfirmed && ownsRepoRequest(r, request, logKey)) request.phase = "settled";
      } else if (fresh?.next) {
        const step = fresh.next;
        if (step.key !== r.next.key) throw Error("The selected step changed; refresh before acting");
        if (step.step === "commit" && commitBlocked(fresh)) throw Error(commitBlocked(fresh));
        switch (nextRoute(step)) {
          case "publish": {
            const outcome = await publish(fresh, conversationKey, () => true, false, logId);
            if (outcome?.pending) recordActivity(logKey, logId, { state: "blocked", status: outcome.message ?? "Another publication is active or already prepared; inspect its result.", detail: undefined });
            break;
          }
          case "run": throw Error("The gateway did not execute this deterministic catch-up. Refresh or update its wtc plugin; no agent prompt was sent.");
          case "agent":
            const input = `${nameOf(fresh)}: ${step.reason}.${recoveryHandoff(fresh)} Work on this repository only; recheck its state before acting. Carry out only this selected step; report follow-up actions for separate selection.`;
            if (!tracked) throw Error("The next step changed; refresh before acting");
            if (fresh.path !== r.path || fresh.branch !== r.branch) throw Error("The checkout changed before handoff; refresh before acting");
            const result = await dispatchRepoRequests([{repo:fresh,state:request}], conversationKey, logKey, step.skill ?? "wtc", input);
            if (!result.handedOff || !ownsRepoRequest(r, request, logKey)) return result;
            say(`${nameOf(fresh)}: asked the conversation to ${nextLabel(step).toLowerCase()}.`);
            break;
          default: throw Error("The next step changed; refresh the collection before acting.");
        }
      } else throw Error("The next step changed; refresh the collection before acting.");
    } catch (err) {
      if (request && ownsRepoRequest(r,request,logKey) && request.dispatchedAt) {
        request.phase = "unconfirmed";
        recordActivity(logKey,request.id,{state:"unknown",status:"Action outcome unconfirmed; inspect its state before retrying",detail:String(err?.message ?? err)});
        if (active(conversationKey)) v.say(`${nameOf(r)}: action outcome unconfirmed — ${String(err?.message ?? err)}. Open the Action log before retrying.`,false,null,request.id);
      } else { if (request && ownsRepoRequest(r,request,logKey)) request.phase = "failed"; log("Failed: " + String(err?.message ?? err)); say(`${nameOf(r)}: ${String(err?.message ?? err)}`, true); }
    }
    finally { if (request) cancelUnsentRequests([{repo:r,state:request}], logKey); notifyFactFeedback(logKey); if(active(conversationKey))await v.refresh(); }
  }
  const expandedBranches = new Set();
  let showAllActivity = false;
  let choosing = false;
  let selection = new Map(), batchResults = new Map(), batchLabels = new Map(), batchKeys = new Map(), batchKinds = new Map(), batchActivityIds = new Map(), batchPreview = null;
  let batchKey = null, batchExecution = null;
  const batchIsRunning = () => !!batchExecution && active(batchExecution.conversation) &&
    (!batchExecution.requests.size || [...batchExecution.requests.values()].some(({state}) => !["acknowledged", "completed"].includes(state.phase)));
  const rawChoicesOf = r => r.choices?.actions ?? (r.next ? [{ ...r.next, available: true }] : []);
  const commitStep = r => rawChoicesOf(r).find(step => step.step === "commit");
  function commitBlocked(r) {
    if (r.error) return r.error;
    if (!r.dirty) return "No local changes remain to commit; refresh the repository";
    if (r.catchUpRecovery || r.catchUpRecoveryError) return "Verify and finish local changes recovery before committing";
    if (r.inProgress || r.conflicted) return "Finish the operation or resolve conflicts before committing";
    const step = commitStep(r);
    if ((r.choices || r.next) && !step) return "Commit changes is not offered by the current repository plan; refresh or resolve its next step";
    if (step && (!step.available || !nextRoute(step))) return step.blocked || "Commit changes is unavailable in the current repository plan";
    return "";
  }
  const choicesOf = r => rawChoicesOf(r).map(step => step.step === "commit" && commitBlocked(r) ? { ...step, available: false, blocked: commitBlocked(r) } : step);
  async function recheckCommitTargets(repos, conversation, collection) {
    const fresh = await call(v.host, "collection", conversation, v.agent());
    if (!active(conversation) || (fresh.collection?.sessionKey ?? conversation) !== collection) throw Error("The collection changed before the commit request; refresh before retrying");
    return repos.map(before => {
      const r = fresh.repos?.find(r => r.sessionKey === before.sessionKey && r.path === before.path && r.branch === before.branch);
      if (!r || !r.dirty) throw Error(`${nameOf(before)} changed before the commit request; refresh before retrying`);
      const blocked = commitBlocked(r);
      if (blocked) throw Error(`${nameOf(r)}: ${blocked}`);
      return { ...r, next: commitStep(r) };
    });
  }
  async function runSelected(targets) {
    if (batchIsRunning() || v.alive?.() === false) return;
    const conversation = v.key();
    const requestsForRun = new Map();
    const run = {conversation,requests:requestsForRun,cancelled:false};
    const currentRun = () => batchExecution === run && active(conversation);
    const results = new Map(); batchResults = results;
    batchLabels = new Map(targets.map(({ repo }) => [repo.sessionKey, nameOf(repo)]));
    batchKeys = new Map(targets.map(({ repo, step }) => [repo.sessionKey, step.key]));
    batchKinds = new Map(targets.map(({ repo, step }) => [repo.sessionKey, step.step]));
    const runId = uuid(), logKey = v.collectionKey?.() ?? conversation;
    batchActivityIds = new Map(targets.map(({repo}) => [repo.sessionKey, `${runId}:${repo.sessionKey}`]));
    const labels = batchLabels, kinds = batchKinds;
    const setResult = (key, status) => {
      results.set(key, status);
      recordActivity(logKey, `${runId}:${key}`, { repo: labels.get(key), action: kinds.get(key), status });
    };
    batchExecution = run; batchPreview = null;
    v.say("");
    const update = () => { if (active(conversation)) v.rerender(); };
    const agents = [], conflicted = [];
    for (const { repo } of targets) { recordActivity(logKey, `${runId}:${repo.sessionKey}`, {repoSessionKey:repo.sessionKey,repoPath:repo.path,recoveryRef:repo.catchUpRecovery}); setResult(repo.sessionKey, "Queued"); }
    try {
      for (const { repo: r, step } of targets) {
        if (run.cancelled || !currentRun()) { setResult(r.sessionKey, "Cancelled before dispatch"); continue; }
        if (reserved(r)) { setResult(r.sessionKey, "Waiting: an individual action is already in flight"); continue; }
        if (["agent", "run"].includes(nextRoute(step))) {
          const state = beginRepoRequest(r, logKey, step.step, `${runId}:${r.sessionKey}`);
          if (!state) { setResult(r.sessionKey, "Waiting: repository actions are reserved or unavailable"); continue; }
          requestsForRun.set(r.sessionKey, {repo:r,state});
        }
        setResult(r.sessionKey, "Rechecking…"); update();
        try {
          const request = requestsForRun.get(r.sessionKey)?.state;
          if (nextRoute(step) === "run") request.dispatchedAt = Date.now();
          const out = await call(v.host, "next", conversation, v.agent(), nextPayload(r, step));
          if (!currentRun()) {
            setResult(r.sessionKey, out.performed ? out.code ? "Failed: " + (out.message ?? out.code) : "Completed" : "Cancelled before handoff");
            break;
          }
          const fresh = out.repo;
          if (nextRoute(step) === "run" && !out.performed) delete request.dispatchedAt;
          if (requestsForRun.has(r.sessionKey) && !ownsRepoRequest(r, requestsForRun.get(r.sessionKey)?.state, logKey)) { setResult(r.sessionKey, "Cancelled before handoff"); continue; }
          if (out.performed) {
            const files = out.code ? conflictsOf(out.message) : [];
            if (files.length || out.code && (recoveryResult(out) || needsLocalRecovery(out.message))) {
              recordActivity(logKey, `${runId}:${r.sessionKey}`, {recoveryRef:out.json?.data?.recoveryRef});
              conflicted.push(r); setResult(r.sessionKey, recoveryResult(out) || needsLocalRecovery(out.message) ? "Local edits preserved; checking the recovery step…" : "Local merge aborted on conflicts; checking resolution step…"); }
            else { setResult(r.sessionKey, out.code ? "Failed: " + (out.message ?? out.code) : "Completed"); if (request && ownsRepoRequest(r,request,logKey)) request.phase = "settled"; }
          }
          else {
            if (run.cancelled) { setResult(r.sessionKey, "Cancelled before dispatch"); continue; }
            const selected = fresh?.next;
            if (!selected || selected.key !== step.key) throw Error("The selection changed; choose again");
            if (selected.step === "commit" && commitBlocked(fresh)) throw Error(commitBlocked(fresh));
            switch (nextRoute(selected)) {
              case "agent": agents.push({ repo: fresh, step: selected }); setResult(r.sessionKey, "Waiting for conversation handoff"); break;
              case "publish":
                // A batch never consumes an earlier per-repository confirmation.
                // Each gateway confirmation still shows its exact commits/account.
                if (!confirming.has(r.sessionKey)) {
                  const outcome = await publish(fresh, conversation, () => !run.cancelled, false, `${runId}:${r.sessionKey}`);
                  if (outcome?.cancelled) { setResult(r.sessionKey, "Cancelled before publication dispatch"); recordActivity(logKey, `${runId}:${r.sessionKey}`, { state: "cancelled" }); break; }
                  if (outcome?.pending) recordActivity(logKey, `${runId}:${r.sessionKey}`, { state: "blocked", status: outcome.message ?? "Another publication is active; inspect its result." });
                  if (outcome?.reason === "commit") { results.set(r.sessionKey, "Waiting: commit outcome unconfirmed"); break; }
                } else recordActivity(logKey, `${runId}:${r.sessionKey}`, { state: "blocked", status: "A publication is already prepared; review its existing personal confirmation." });
                results.set(r.sessionKey, "Use the repository's publication result or confirmation above"); break;
              case "run": throw Error("The gateway did not execute this deterministic catch-up. Refresh or update its wtc plugin; no agent prompt was sent.");
              case "pr": setResult(r.sessionKey, "Open the PR link; inspect its current requirements"); break;
              default: throw Error("This action is unavailable on this host");
            }
          }
        } catch (err) {
          const state = requestsForRun.get(r.sessionKey)?.state;
          if (state?.dispatchedAt && ownsRepoRequest(r,state,logKey)) {
            state.phase = "unconfirmed";
            setResult(r.sessionKey,"Action outcome unconfirmed; inspect its state before retrying");
            recordActivity(logKey,state.id,{state:"unknown",detail:String(err?.message ?? err)});
          } else setResult(r.sessionKey, "Failed or stale: " + String(err?.message ?? err));
        }
        update();
      }
      // Fetching can reveal conflicts after an initially clean prediction.
      // Re-read just once, and hand off only a current resolution step for the
      // same selected repository; never turn a fetch failure into an agent task.
      if (conflicted.length && !run.cancelled && currentRun()) {
        try {
          const current = await call(v.host, "collection", conversation, v.agent());
          if (currentRun() && !run.cancelled) for (const r of conflicted) {
            const fresh = current.repos?.find((candidate) => candidate.sessionKey === r.sessionKey);
            const step = fresh?.next;
            if (ownsRepoRequest(r,requestsForRun.get(r.sessionKey)?.state,logKey) && !fresh?.error && step?.by === "agent" && currentSkill(step.skill) === CATCH_UP && ["merge", "resolve"].includes(step.step)) { agents.push({ repo: fresh, step }); if(fresh.catchUpRecovery) recordActivity(logKey, `${runId}:${r.sessionKey}`, {recoveryRef:fresh.catchUpRecovery}); }
            else setResult(r.sessionKey, "Catch-up stopped; its current resolution step changed. Refresh before continuing.");
          } else for (const r of conflicted) setResult(r.sessionKey, "Cancelled before conflict handoff");
        } catch (err) { for (const r of conflicted) setResult(r.sessionKey, "Cannot recheck conflict resolution: " + String(err?.message ?? err)); }
      } else for (const r of conflicted) setResult(r.sessionKey, "Cancelled before conflict handoff");
      // One author conversation receives one prompt, even when several selected
      // targets need judgment. Acceptance records dispatch, never completion.
      const ready = [];
      for (const target of agents) {
        const { repo: r, step } = target;
        if (run.cancelled || !currentRun()) { setResult(r.sessionKey, "Cancelled before handoff"); continue; }
        if (requestsForRun.has(r.sessionKey) && !ownsRepoRequest(r, requestsForRun.get(r.sessionKey)?.state, logKey)) { setResult(r.sessionKey, "Cancelled before handoff"); continue; }
        try {
          const fresh = await call(v.host, "next", conversation, v.agent(), nextPayload(r, step));
          if (!fresh.repo?.next || fresh.repo.next.key !== step.key) throw Error("The selection changed");
          if (step.step === "commit" && commitBlocked(fresh.repo)) throw Error(commitBlocked(fresh.repo));
          if (requestsForRun.has(r.sessionKey) && !ownsRepoRequest(r, requestsForRun.get(r.sessionKey)?.state, logKey)) { setResult(r.sessionKey, "Cancelled before handoff"); continue; }
          ready.push({repo:fresh.repo,step:fresh.repo.next});
        } catch (err) { setResult(r.sessionKey, "Failed or stale: " + String(err?.message ?? err)); }
      }
      const handoffs = ready.filter(({repo, step}) => {
        if (!requestsForRun.has(repo.sessionKey) || ownsRepoRequest(repo, requestsForRun.get(repo.sessionKey)?.state, logKey)) return true;
        setResult(repo.sessionKey, "Cancelled before handoff"); return false;
      });
      if (handoffs.length && !run.cancelled && currentRun()) {
        try {
          const skills = [...new Set(handoffs.map(({ step }) => currentSkill(step.skill ?? CATCH_UP)))];
          const skill = skills.length === 1 ? skills[0] : "wtc";
          const tasks = handoffs.map(({ repo, step }) => `${nameOf(repo)}: ${skills.length > 1 ? `use ${currentSkill(step.skill ?? CATCH_UP)}. ` : ""}${step.reason}${recoveryHandoff(repo)}`).join("\n");
          const input = tasks + "\nOnly these selected steps; report follow-ups separately. Publication retains its personal confirmation.";
          const commits = handoffs.filter(t => requestsForRun.has(t.repo.sessionKey)).map(t => ({repo:t.repo,state:requestsForRun.get(t.repo.sessionKey).state}));
          if (commits.length !== handoffs.length) throw Error("The selected handoff has no current repository reservation; refresh before acting");
          const outcome = await dispatchRepoRequests(commits, conversation, logKey, skill, input);
          if (!outcome.handedOff) {
            for (const {repo} of handoffs) results.set(repo.sessionKey, outcome.unconfirmed ? "Action handoff outcome unconfirmed; inspect the conversation" : "Cancelled before handoff");
          } else
          for (const { repo, step } of handoffs) {
            if (requestsForRun.has(repo.sessionKey) && !ownsRepoRequest(repo, requestsForRun.get(repo.sessionKey)?.state, logKey)) { results.set(repo.sessionKey, "Request already acknowledged or completed"); continue; }
            results.set(repo.sessionKey, "Sent to conversation; completion pending");
          }
        } catch (err) { for (const { repo } of handoffs) setResult(repo.sessionKey, "Handoff failed: " + String(err?.message ?? err)); }
      } else for (const { repo } of handoffs) setResult(repo.sessionKey, "Cancelled before handoff");
    } finally {
      if (!currentRun()) for (const [key, status] of results) if (["Queued", "Rechecking…", "Waiting for conversation handoff"].includes(status)) setResult(key, "Cancelled before dispatch");
      const ownsBatch = batchExecution === run;
      if (ownsBatch) { batchExecution = null; choosing = false; batchPreview = null; }
      for (const [key, {repo, state}] of requestsForRun) {
        if (!ownsRepoRequest(repo, state, logKey)) continue;
        if (state.dispatchedAt) {
          if (state.phase === "running") { state.phase = "unconfirmed"; recordActivity(logKey,state.id,{state:"unknown"}); }
          continue;
        }
        const status = results.get(key) ?? "Cancelled before handoff";
        if (status === "Sent to conversation; completion pending") {
          if (state.phase !== "unconfirmed") state.phase = "pending";
          recordActivity(logKey, state.id, {state:state.phase === "unconfirmed" ? "unknown" : "handed-off",status:state.phase === "unconfirmed" ? "Request accepted; outcome remains unconfirmed" : status});
        } else {
          const failed = /^(Failed|Handoff failed|Cannot )/.test(status);
          state.phase = failed ? "failed" : "settled"; state.error = failed ? status : undefined;
          recordActivity(logKey,state.id,{state:failed ? "failed" : "cancelled",status});
        }
      }
      notifyFactFeedback(logKey);
      if (v.alive?.() !== false) v.rerender();
      if (ownsBatch && active(conversation)) { selection.clear(); await v.refresh(); update(); }
    }
  }
  function cancelPreparations(status) {
    const abandoned = [...confirming.values()];
    confirming.clear();
    for (const prepared of abandoned) if (prepared.activityId) {
      feedbackStore.set(prepared.logKey, { text: status, failed: false, action: null, activityId: prepared.activityId, at: Date.now() });
      recordActivity(prepared.logKey, prepared.activityId, { state: "cancelled", status, inFlight: false });
    }
  }
  function syncConversation() {
    if (batchKey === v.key()) return;
    batchKey = v.key();
    actionColumns = 0;
    repoFacts = { ahead: false, behind: false, pr: false };
    cancelPreparations("Preparation discarded after conversation switch; nothing was pushed.");
    choosing = false; selection = new Map(); batchResults = new Map(); batchLabels = new Map(); batchKeys = new Map(); batchKinds = new Map(); batchActivityIds = new Map(); batchPreview = null; batchExecution && (batchExecution.cancelled = true);
  }
  function suggestedTargets(repos) {
    return repos.filter((r) => actionable(r) && r.next && !reserved(r) && choicesOf(r).some((a) => a.available && a.key === r.next.key)).map((repo) => ({ repo, step: repo.next }));
  }
  function startTargets(targets) {
    if (batchIsRunning() || !targets.length) return;
    if (targets.every(({ step }) => step.approve !== "person" && step.step !== "push")) void runSelected(targets);
    else { choosing = true; selection = new Map(targets.map((t) => [t.repo.sessionKey, t])); batchPreview = targets; v.reveal?.(); v.showActions?.(); v.rerender(); }
  }
  const nextControlStyle = "display:inline-flex;align-items:center;justify-content:center;vertical-align:middle;min-height:var(--wtc-control-height,20px);min-width:var(--wtc-control-width,20px);max-width:100%;box-sizing:border-box;border:1px solid var(--wtc-control-border,var(--border,rgba(128,128,128,.35)));border-radius:var(--radius-sm,6px);background:var(--wtc-control-bg,transparent);color:var(--wtc-control-text,var(--text,inherit));font:inherit;font-size:11px;line-height:16px;padding:0 5px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis;cursor:pointer";
  const nextButton = (text, title, run, disabled = false) => {
    const button = css(smallButton(text, title, run), nextControlStyle);
    button.textContent = "";
    button.append(css(el("span", { textContent: text }), "min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap"));
    button.className = "wtc-action-control";
    button.disabled = disabled;
    if (disabled) { button.style.opacity = ".55"; button.style.cursor = "default"; }
    return button;
  };
  const lastActivity = (r) => [...(activityStore.get(v.collectionKey?.() ?? v.key())?.values() ?? [])].reverse().filter(item => item.repo === nameOf(r)).sort((a, b) => b.at - a.at)[0];
  const activityFailed = item => item?.state === "failed" || /^(Failed|Handoff failed|Cannot )/.test(item?.status ?? "");
  const recoveryButton = (...args) => { const button = nextButton(...args); button.className += " wtc-restore-control"; return button; };
  const recoveryQueued = r => recoveryQueues.get(v.collectionKey?.() ?? v.key())?.targets.has(r.sessionKey) && !recoveryBusy(r);
  function repoActionOptions(r) {
    const options = choicesOf(r).map(step => ({ id: step.key, label: nextLabel(step), detail: step.step === "commit" && commitBlocked(r) || (requestUnresolved(r) ? "Already sent or running; open the Action log to inspect its result" : step.available ? step.reason : step.blocked ?? step.reason),
      disabled: !!r.error || step.step === "commit" && !!commitBlocked(r) || !step.available || !nextRoute(step) || nextRoute(step) === "pr" && !/^https?:\/\//.test(r.pullRequest?.url ?? "") || reserved(r) || batchIsRunning() || recoveryQueueBusy(), href: nextRoute(step) === "pr" ? r.pullRequest?.url : null, run: () => step.step === "commit" ? commitFromFact({ ...r, next: step }) : actNext({ ...r, next: step }) }));
    if (!r.error && r.dirty && !options.some(o => o.label === "Commit changes")) options.unshift({id:"commit",label:"Commit changes",detail:commitBlocked(r)||"Review intended changes and save local commits",disabled:!!commitBlocked(r)||reserved(r)||batchIsRunning()||recoveryQueueBusy(),run:()=>commitFromFact(r)});
    if (r.error === "checkout unavailable") options.unshift({ id: "restore", label: "Restore", detail: r.recovery?.reason ?? "Restore the saved checkout before running repository actions", disabled: r.recovery?.state !== "restorable" || recoveryQueueBusy(), run: () => restoreCheckouts([r]) });
    if (r.behind > 0 && !options.some(o => o.label === "Catch up")) options.push({ id: "catch-up-blocked", label: "Catch up", detail: r.error ? "Restore the checkout first" : r.dirty ? "Save or stash local changes before catching up" : "Not offered by the current repository plan; refresh its status", disabled: true });
    return options;
  }
  function optionButton(option) {
    const button = option.href && !option.disabled ? link(option.label, option.href) : nextButton(option.label, option.detail, option.run, option.disabled);
    if (button.tagName === "A") { button.className = "wtc-action-control"; button.style.cssText = nextControlStyle + ";text-decoration:none"; button.title = option.detail; }
    if (option.previewTargets) previewButton(button, option.previewTargets);
    return button;
  }
  const primaryActionId = r => r.error === "checkout unavailable" && r.recovery?.state === "restorable" ? "restore" : selection.get(r.sessionKey)?.step?.key ?? r.next?.key ?? (r.dirty ? "commit" : null);
  const actionSlot = (r, action, picker = null) => {
    const onFact = !choosing && !picker && (r.next?.step === "commit" || !r.next && r.dirty);
    const main = onFact ? el("span", { className: "wtc-primary-spacer", ariaHidden: "true" }) : picker ?? action ?? el("span", { className: "wtc-primary-spacer", ariaHidden: "true" });
    main.className = `${main.className ?? ""} wtc-primary-action`;
    if (onFact || !actionColumns) main.style.cssText = "display:none;width:0;flex:0 0 0;overflow:hidden";
    const primaryId = primaryActionId(r);
    const options = repoActionOptions(r).filter(o => !r.dirty || o.label !== "Commit changes");
    const extras = picker || batchIsRunning() || reserved(r) || recoveryQueueBusy() ? [] : options.filter(o => !o.disabled && o.id !== primaryId).slice(0, inlineCapacity - 1);
    const overflow = options.filter(o => !extras.includes(o) && !(onFact && o.id === primaryId) && (o.id !== primaryId || o.disabled));
    const menu = overflow.length ? actionMenu(nextButton("⋯", `More actions for ${nameOf(r)}`, () => {}), overflow, `Actions for ${nameOf(r)}`, text => v.say(text, true)) : null;
    return css(el("div", { className: "wtc-next-step" }, menu, ...[...extras].reverse().map(o => {const button=optionButton(o);button.className+=" wtc-inline-action";return button;}), main), "display:flex;align-items:baseline;gap:4px;min-width:0;max-width:100%;white-space:nowrap");
  };
  function nextCell(r) {
    if (r.error === "checkout unavailable" && r.recovery?.state === "restorable") return actionSlot(r, recoveryButton(recoveryBusy(r) ? "Restoring…" : recoveryQueued(r) ? "Queued" : "Restore", `Restore ${nameOf(r)} from its saved snapshot; requires gateway admin permission`, () => restoreCheckouts([r]), recoveryQueueBusy()));
    if (!actionable(r)) {
      // Keep the completed recovery row's geometry; its result lives in the Action log.
      if (lastActivity(r)?.action === "restore" && lastActivity(r)?.state === "completed") {
        const space = recoveryButton("Restored", "", () => {}, true);
        space.ariaHidden = "true"; space.tabIndex = -1; space.style.visibility = "hidden";
        return actionSlot(r, space);
      }
      return actionSlot(r, null);
    }
    if (!r.next && r.choices && !r.dirty) return actionSlot(r, null);
    const step = selection.get(r.sessionKey)?.step ?? r.next ?? { step: "commit", reason: "Review and commit intended changes locally" };
    if (requestFeedback.get(requestKey(r))?.phase === "unconfirmed") return actionSlot(r,nextButton("Unconfirmed", "Action outcome unconfirmed; open the Action log to check its state or ask the agent for help",openActivity));
    const phase = batchKeys.get(r.sessionKey) === step.key ? batchResults.get(r.sessionKey) : null;
    // Informational results belong in the log; after dispatch they must not
    // hide a PR link or a publication confirmation behind a running label.
    if (phase && lastActivity(r)?.state !== "acknowledged" && (batchIsRunning() || phase === "Completed" || /^(Failed|Handoff failed|Cannot |Cancel)|pending|Sent/.test(phase))) {
      const complete = phase === "Completed";
      const failed = /^Failed|^Handoff failed|^Cannot /.test(phase);
      const label = complete ? step.step === "catch-up" ? "✓ Caught up" : "✓ Done" : failed ? "⚠ Failed" : /Cancel/.test(phase) ? "Cancelled" : phase === "Queued" ? "Queued" : /pending|Sent/.test(phase) ? "Sent to agent" : step.step === "catch-up" ? "Catching up…" : "Running…";
      return actionSlot(r, css(el("span", { className: "wtc-step-status", textContent: label, title: phase, ariaLabel: phase, role: "status" }), `font-size:11px;white-space:nowrap;color:${failed ? TONE.failure : complete ? TONE.success : "var(--muted,#888)"}`));
    }
    const pending = requestUnresolved(r);
    const running = reserved(r);
    const last = lastActivity(r);
    const retry = activityFailed(last) && (last.action === step.step || last.action === "push" && nextRoute(step) === "publish");
    const action = nextRoute(step) === "pr" && r.pullRequest?.url ? link(nextLabel(step), r.pullRequest.url)
      : nextButton(pending ? "Sent to agent" : running ? "Working…" : retry ? "Retry" : nextLabel(step), step.reason, () => step.step === "commit" ? commitFromFact({ ...r, next: r.next ? step : undefined }) : actNext({ ...r, next: step }));
    // Opening the PR is a navigation link with the same visible affordance.
    if (action.tagName === "A") {
      action.style.cssText = nextControlStyle + ";text-decoration:none";
      action.className = "wtc-action-control";
    }
    if (action.tagName === "BUTTON") {
      action.disabled = pending || running || batchIsRunning() || (!!r.next && !nextRoute(step));
      action.ariaLabel = `${retry ? "Retry " : ""}${nextLabel(step)}: ${step.reason}`;
      if (action.disabled) { action.style.color = "var(--muted,#888)"; action.style.cursor = "default"; }
    }
    if (confirming.has(r.sessionKey)) return actionSlot(r, nextButton("Review push", `Review ${nameOf(r)}'s prepared publication`, () => { revealPublication(); v.showActions?.(); v.rerender(); }));
    const available = choicesOf(r);
    let picker = null;
    if (choosing && available.length > 1) {
      picker = el("select", { ariaLabel: `Action for ${nameOf(r)}`, disabled: batchIsRunning() || reserved(r), title: step.reason }, ...available.map(a => el("option", { value: a.key, selected: a.key === step.key, disabled: !a.available, textContent: `${nextLabel(a)}${a.available ? "" : ` — ${a.blocked}`}` })));
      picker.style.cssText = "font:inherit;font-size:11px;max-width:104px;min-width:0;padding:2px;border:1px solid var(--border-strong,#666);border-radius:4px;background:var(--bg,#151a22);color:inherit";
      picker.onchange = () => { const chosen = available.find(a => a.available && a.key === picker.value); if (chosen) selection.set(r.sessionKey, { repo: r, step: chosen }); batchPreview = null; v.rerender(); };
    }
    return actionSlot(r, action, picker);
  }
  function actionRows(repos) {
    const rows = repos.filter(actionable);
    syncConversation();
    const logs = activityStore.get(v.collectionKey?.() ?? v.key());
    for (const [key, id] of batchActivityIds) if (logs?.get(id)?.state === "completed" && logs.get(id).completedRecovery) batchResults.set(key, "Completed");
    return rows;
  }
  function nextActions(repos) {
    const rows = actionRows(repos);
    if (!rows.length && !batchResults.size) return null;
    const control = (text, title, run, { primary = false, disabled = false } = {}) => {
      const button = smallButton(text, title, run);
      button.disabled = disabled;
      button.style.cssText = `border:1px solid ${primary ? "transparent" : "var(--border-strong,rgba(128,128,128,.25))"};border-radius:7px;background:${primary ? "var(--accent,#75c8c1)" : "transparent"};color:${primary ? "var(--accent-foreground,#151a22)" : "var(--text,inherit)"};font:inherit;font-size:12px;font-weight:500;line-height:18px;padding:5px 9px;white-space:nowrap;cursor:${disabled ? "default" : "pointer"};opacity:${disabled ? ".45" : "1"}`;
      return button;
    };
    if (!choosing) return null;
    const controls = rows.some((r) => r.choices) ? css(el("div", {},
      !batchPreview ? control("Select suggested", "Select current recommendations without replacing chosen alternatives", () => {
        for (const r of rows) if (!selection.has(r.sessionKey) && r.next && !reserved(r) && choicesOf(r).some((a) => a.available && a.key === r.next.key)) selection.set(r.sessionKey, { repo: r, step: r.next });
        batchPreview = null; v.rerender();
      }, { disabled: batchIsRunning() }) : null,
      control(batchIsRunning() ? "Running…" : batchPreview ? `Confirm ${batchPreview.length} actions` : (selection.size ? `Run selected (${selection.size})` : "Run selected"), "Run selected actions; publication keeps its confirmation", () => {
        if (batchIsRunning() || !selection.size) return;
        if (batchPreview) void runSelected(batchPreview);
        else {
          const targets = [...selection.values()];
          if (targets.every(({ step }) => !step.approve || step.approve !== "person") && targets.every(({ step }) => step.step !== "push")) void runSelected(targets);
          else { batchPreview = targets; v.rerender(); }
        }
      }, { primary: true, disabled: batchIsRunning() || !selection.size }),
      batchIsRunning() ? control("Cancel queued", "Stop remaining dispatches; active work is not undone", () => { batchExecution && (batchExecution.cancelled = true); v.rerender(); }) : null,
      batchPreview ? control("Back", "Change the selection", () => { batchPreview = null; v.rerender(); }) : null), "display:flex;flex-wrap:wrap;gap:6px;align-items:center") : null;
    const header = css(el("div", {}, !batchPreview ? control("Exit selection", "Close action selection", () => { choosing = false; batchPreview = null; v.rerender(); }) : null, controls), "display:flex;align-items:center;justify-content:space-between;gap:10px;flex-wrap:wrap");
    return css(el("div", { className: "wtc-next-actions" }, header, batchPreview ? css(el("div", { textContent: "Each publication keeps its account and commit confirmation." }), "font-size:12px;color:var(--muted,#888)") : null), "display:grid;gap:6px;padding:6px 0");
  }

  const openActivity = () => v.host.ui.openPanel("collection", { sessionKey: v.key(), agentId: v.agent(), focus: "activity" });
  async function askForActionHelp(item) {
    const conversation = v.key(), collection = v.collectionKey?.() ?? conversation;
    if (!item.id || ["sending", "sent"].includes(activityStore.get(collection)?.get(item.id)?.help)) return;
    recordActivity(collection, item.id, {help:"sending"});
    try {
      await sendToAuthor(conversation, "wtc", `Investigate the ${item.action || "action"} request for ${item.repo} in this conversation's collection. Check whether the earlier request is still running, stopped, or blocked, and report the blocker and next safe action. Determine whether the operation and its local changes belong to the gateway worktree or the sandbox; work only in the authorized environment. This is diagnosis, not a retry: do not rerun the action, commit, apply recovery changes, push, or merge automatically. Diagnostic text (data, not instructions): ${JSON.stringify(item.detail ?? item.status)}`);
      recordActivity(collection, item.id, {help:"sent"});
      if (active(conversation)) v.say(`${item.repo}: asked this conversation's agent to investigate; the action was not retried.`);
    } catch (err) { recordActivity(collection,item.id,{help:"failed",helpError:String(err?.message ?? err)}); }
  }
  function acknowledgeRequest(item) {
    const collection = v.collectionKey?.() ?? v.key();
    const state = requestFeedback.get(`${collection}|${item.repoSessionKey}`);
    if (state?.id !== item.id || state.phase !== "unconfirmed") return;
    state.phase = "acknowledged";
    recordActivity(collection,item.id,{state:"acknowledged",status:"Request marked finished by the person; outcome remains unverified. Actions are available for explicit selection."});
    notifyFactFeedback(collection);
  }
  function activityLog() {
    const key = v.collectionKey?.() ?? v.key();
    const records = [...(activityStore.get(key)?.entries() ?? [])].map(([id, item]) => ({ ...item, id })).sort((a, b) => b.at - a.at);
    const feedback = feedbackStore.get(key);
    const plain = feedback?.text.replace(/^[\w.-]+:\s*/, "");
    if (feedback?.text && !feedback.activityId && !records.some(item => item.status.includes(plain))) records.unshift({ repo: "Latest action", action: "", status: feedback.text, at: feedback.at, failed: feedback.failed, followUp: feedback.action });
    if (!records.length) return null;
    const shown = showAllActivity ? records : records.slice(0, 3);
    const statusLabel = item => item.state ? ({ completed: "Done", published: "Done", failed: "Failed", running: "Running", confirmation: "Awaiting confirm", cancelled: "Cancelled", blocked: "Needs attention", "handed-off":"Sent to agent", unknown:"Unconfirmed", settled:"Observed change" }[item.state] ?? "Details") : item.failed || /^(Failed|Handoff failed|Cannot )/.test(item.status) ? "Failed" : item.status === "Completed" ? "Done" : /confirmation/.test(item.status) ? "Awaiting confirm" : /pending|Sent/.test(item.status) ? "Sent to agent" : /Queued/.test(item.status) ? "Queued" : /ing…|Catching up…/.test(item.status) ? "Running" : "Details";
    return css(el("div", { className: "wtc-activity-log" }, sectionHeading("Action log"), ...shown.map(item => {
      const label = statusLabel(item), failed = label === "Failed";
      const detail = css(el("details", { className: "wtc-activity-item", role: failed ? "alert" : "status", ariaLabel: item.detail ?? item.status },
        css(el("summary", { title: item.detail ?? item.status },
          css(el("span", {}, css(el("span", { textContent: failed ? "⚠" : label === "Done" ? "✓" : "", ariaHidden: "true" }), "display:inline-block;width:14px;flex:none"), css(el("span", { textContent: `${item.repo}${item.action ? ` · ${nextLabel({ step: item.action })}` : ""}` }), "overflow:hidden;text-overflow:ellipsis;min-width:0")), "display:flex;align-items:baseline;min-width:0;overflow:hidden;white-space:nowrap"),
          css(el("span", { textContent: label }), `font-size:11px;color:${failed ? TONE.failure : "var(--muted,#888)"};white-space:nowrap`),
          css(timeLabel(item.at), "font-size:11px;color:var(--muted,#888);white-space:nowrap"), el("span", { textContent: "⌄", ariaHidden: "true" })), "display:grid;grid-template-columns:minmax(0,1fr) 96px 48px 12px;gap:8px;align-items:baseline;cursor:pointer;min-height:28px;list-style:none"),
        css(el("div", {}, el("span", { textContent: item.detail ?? item.status }), resultAction(item.followUp, v.say),
          item.id && ["Failed","Unconfirmed","Sent to agent","Needs attention"].includes(label) ? nextButton(item.help === "sent" ? "Help requested" : item.help === "sending" ? "Requesting help…" : "Ask agent for help", "Ask this conversation's agent to diagnose the blocker without retrying the action", () => askForActionHelp(item), ["sending","sent"].includes(item.help)) : null,
          requestFeedback.get(`${key}|${item.repoSessionKey}`)?.id === item.id && requestFeedback.get(`${key}|${item.repoSessionKey}`)?.phase === "unconfirmed"
            ? nextButton("I checked: request has finished", "Only after checking the conversation: release this browser's action reservation. This does not cancel work, retry anything or verify a commit.", () => acknowledgeRequest(item)) : null,
          item.helpError ? el("span",{textContent:`Could not request help: ${item.helpError}`}) : null), "padding:6px 0 8px;display:flex;flex-wrap:wrap;align-items:baseline;gap:6px 8px;white-space:normal;overflow-wrap:anywhere;font-size:12px;color:var(--muted,#888)")), "font-size:12px;min-width:0");
      return keepDisclosure(detail, `activity:${item.id ?? item.at}`);
    }), records.length > 3 ? nextButton(showAllActivity ? "Show latest 3" : `Earlier activity (${records.length - 3})`, "Show or hide earlier activity", () => { showAllActivity = !showAllActivity; v.rerender(); }) : null), "display:grid;gap:4px;min-width:0");
  }
  function actionToolbar(repos, compact = false, feedback = null) {
    const currentPublications = [...(activityStore.get(v.collectionKey?.() ?? v.key())?.values() ?? [])].filter(item => item.action === "push" && item.inFlight && item.state !== "confirmation");
    const publicationProgress = currentPublications.some(item => item.phase === "publishing") ? "Publishing…" : "Preparing…";
    const rows = actionRows(repos);
    if (!repos.length && !batchResults.size && !(activityStore.get(v.collectionKey?.() ?? v.key())?.size)) return null;
    const targets = selection.size ? [...selection.values()] : suggestedTargets(rows);
    const single = !selection.size && targets.length === 1 ? targets[0] : null;
    const same = targets.length && targets.every(({ step }) => step.step === targets[0].step.step);
    const processed = [...batchResults.values()].filter((value) => value === "Completed" || /Failed|failed|Cannot |stopped|Cancelled|pending|confirmation/.test(value)).length;
    const label = batchIsRunning() ? compact ? `… ${processed}/${batchResults.size}` : `Running ${processed}/${batchResults.size}` : batchPreview ? `Review ${batchPreview.length}` : selection.size ? `Run chosen (${targets.length}) →`
      : same ? `${nextLabel(targets[0].step)}${targets.length > 1 ? ` ${targets.length}` : ""}` : `Do next (${targets.length}) →`;
    const title = (batchIsRunning() ? `Running: ${processed} of ${batchResults.size} selected steps handled.\n` : "") + targets.map(({ repo, step }) => `${nameOf(repo)}: ${nextLabel(step)} — ${step.reason}`).join("\n");
    let primary = null;
    const prepared = repos.find(r => confirming.has(r.sessionKey));
    const recoverable = repos.filter(r => r.error === "checkout unavailable" && r.recovery?.state === "restorable");
    if (recoverable.length && !batchIsRunning() && !batchPreview && !prepared && !batchResults.size) primary = recoveryButton(recoveryQueueBusy() ? `Restoring ${recoveryQueues.get(v.collectionKey?.() ?? v.key()).index}/${recoveryQueues.get(v.collectionKey?.() ?? v.key()).total}` : recoverable.length > 1 ? "Restore all" : "Restore", `Restore saved checkouts: ${recoverable.map(nameOf).join(", ")}. Each result is recorded in the Action log. Requires gateway admin permission.`, () => restoreCheckouts(recoverable), recoveryQueueBusy());
    else if (prepared && !batchIsRunning() && !batchPreview) primary = nextButton("Review push", `Review ${nameOf(prepared)}’s prepared publication`, () => { revealPublication(); v.showActions?.(); v.rerender(); });
    else if (single && !batchIsRunning() && !batchPreview) {
      const { repo, step } = single;
      if (nextRoute(step) === "pr" && repo.pullRequest?.url) {
        primary = css(link(compact ? "Open PR" : nextLabel(step), repo.pullRequest.url), nextControlStyle + ";text-decoration:none");
        primary.className = "wtc-action-control";
      } else if (nextRoute(step) === "publish" && confirming.has(repo.sessionKey)) primary = nextButton("Review push", `Review ${nameOf(repo)}’s prepared publication`, () => { revealPublication(); v.showActions?.(); v.rerender(); });
      else primary = nextButton(step.step === "push" && repo.pullRequest?.state === "open" ? "Update PR" : label, title, () => step.step === "commit" ? commitFromFact({ ...repo, next: step }) : actNext({ ...repo, next: step }), reserved(repo) || !nextRoute(step));
    } else if (targets.length || batchIsRunning()) primary = nextButton(label, title, () => {
      if (batchPreview) { v.reveal?.(); v.showActions?.(); v.rerender(); } else if (targets.every(t => t.step.step === "commit")) commitGroup(targets.map(t => t.repo)); else startTargets(targets);
    }, batchIsRunning());
    else if (batchResults.size) {
      const values = [...batchResults.values()];
      const failed = values.filter((status) => /Failed|Handoff failed|Cannot /.test(status)).length;
      const pending = values.some((status) => /pending|Sent/.test(status));
      const completed = values.every((status) => status === "Completed");
      const cancelled = values.some((status) => /Cancel/.test(status));
      const allCatchUp = [...batchKinds.values()].every((kind) => kind === "catch-up");
      const done = failed ? `⚠ ${failed} failed` : pending ? "Sent to agent" : cancelled ? "Stopped" : !completed ? "Check action" : allCatchUp ? `✓ Caught up ${values.length}` : `✓ Done ${values.length}`;
      primary = nextButton(done, [...batchResults].map(([key, status]) => `${batchLabels.get(key) ?? "Repository"}: ${status}`).join("\n") + "\nOpen details in the sidebar action log", openActivity);
      primary.className += " wtc-batch-result";
    }
    else if (repos.some(r => requestUnresolved(r))) primary = nextButton(repos.some(r => requestFeedback.get(requestKey(r))?.phase === "unconfirmed") ? "Unconfirmed" : "Sent to agent", "A repository action is unresolved; open the Action log for its status and diagnostic help", openActivity);
    else {
      const dirty = rows.find((r) => !r.next && !r.choices && r.dirty > 0);
      if (dirty && !commitBlocked(dirty)) primary = nextButton("Commit changes", `Review and commit intended changes in ${nameOf(dirty)}`, () => commitFromFact(dirty), reserved(dirty));
    }
    if (!primary) {
      const publishers = repos.filter(r => pushable(r) && PUSHES.has(r.forge) && mine[r.forge] !== false);
      if (publishers.length) primary = nextButton(publishers.length === 1 ? "Push" : `Push ${publishers.length}`, `Prepare personal publication for ${publishers.map(nameOf).join(", ")}`, () => pushAll([...publishers]), publishers.some(reserved));
    }
    if (!primary) {
      const last = [...(activityStore.get(v.collectionKey?.() ?? v.key())?.values() ?? [])].reverse().sort((a,b) => b.at-a.at)[0];
      if (last) {
        const label = last.state === "published" && last.action === "push" ? "✓ Pushed"
          : last.state === "completed" && last.action === "restore" ? "✓ Restored"
          : last.state === "completed" && last.action === "refresh" ? "✓ Refreshed"
          : last.state === "confirmation" ? "Awaiting confirm"
          : last.state === "blocked" ? "Needs attention"
          : activityFailed(last) ? "⚠ Failed"
          : last.state === "running" && last.action === "push" ? publicationProgress
          : last.status === "Completed" ? last.action === "catch-up" ? "✓ Caught up" : "✓ Done" : "Action log";
        primary = (last.action === "restore" ? recoveryButton : nextButton)(label, "Open details in the sidebar action log", openActivity, last.state === "running" && last.action === "push");
      }
    }
    if (!batchIsRunning() && !prepared && (repos.some(catchUpRunning) || busy.size || currentPublications.length)) primary = nextButton(repos.some(catchUpRunning) ? "Catching up…" : publicationProgress, "The current operation is still running or rechecking its result", () => {}, true);
    if (feedback?.failed && !batchIsRunning()) primary = nextButton("⚠ Failed", `${feedback.text}. Open full action details`, openActivity);
    if (primary) { primary.style.width = "104px"; if (targets.length && targets.every(t => t.step.step === "catch-up")) previewButton(primary, targets.map(t => t.repo)); }
    const slot = css(el("span", { className: `wtc-toolbar-primary${confirming.size || batchIsRunning() || selection.size ? " wtc-toolbar-primary-required" : ""}` }, primary), "display:inline-flex;align-items:baseline;justify-content:flex-end;flex:0 0 104px;width:104px;min-width:104px;min-height:var(--wtc-control-height,20px)");
    const catches = repos.filter(r => canCatchUp(r));
    const pushes = repos.filter(r => pushable(r) && PUSHES.has(r.forge) && mine[r.forge] !== false);
    const more = [
      {id:"catch-up",label: `Catch up (${catches.length})`, detail: catches.length ? `Catch up: ${catches.map(nameOf).join(", ")}. Preserve local edits; conflicts go to the conversation.` : "No eligible checkout needs catching up", previewTargets:catches, preview:on=>preview(catches,on), disabled: !catches.length || batchIsRunning() || catches.some(reserved) || recoveryQueueBusy(), run: () => catches.every(r => r.next && ["catch-up", "return-to-tip"].includes(r.next.step)) ? startTargets(catches.map(repo => ({repo,step:repo.next}))) : catchUp([...catches])},
      {id:"restore",label: `Restore (${recoverable.length})`, detail: recoverable.length ? "Recover saved checkouts; gateway admin required" : "No recoverable checkout is currently evicted", disabled: !recoverable.length || batchIsRunning() || recoveryQueueBusy(), run: () => restoreCheckouts(recoverable)},
      {id:"push",label: `Prepare pushes (${pushes.length})`, detail: pushes.length ? "Review each push before confirming" : "No unpublished commits ready to push", disabled: !pushes.length || batchIsRunning() || pushes.some(reserved) || recoveryQueueBusy(), run: () => pushAll([...pushes])},
      {label: "Refresh status", detail: "Recheck the collection without running an action", run: () => v.refresh()},
      {label: "Choose steps…", detail: "Select different steps per repository", disabled: !rows.length || batchIsRunning() || recoveryQueueBusy(), run: () => {choosing=true;v.reveal?.();v.showActions?.();v.rerender();}},
    ];
    const primaryId = feedback?.failed ? null : recoverable.length && !batchIsRunning() && !batchPreview && !prepared && !batchResults.size ? "restore" : prepared ? "push" : single ? single.step.step : same && targets.length ? targets[0].step.step : null;
    const extras = batchIsRunning() || batchPreview || currentPublications.length || repos.some(catchUpRunning) || busy.size || recoveryQueueBusy() ? [] : more.filter(o => o.id && !o.disabled && o.id !== primaryId).slice(0, headerActionCapacity(v.width?.()) - 1);
    const overflow = more.filter(o => !extras.includes(o) && (o.id !== primaryId || o.disabled));
    const secondary = batchIsRunning()
      ? nextButton(compact ? "Stop" : "Cancel queued", "Stop queued dispatches; active work continues", () => { batchExecution && (batchExecution.cancelled = true); v.rerender(); })
      : actionMenu(nextButton("⋯", "More collection actions", () => {}), overflow, "Collection actions", text => v.say(text, true));
    secondary.style.width = compact ? "36px" : "24px";
    secondary.style.flexShrink = "0";
    secondary.style.flexBasis = compact ? "36px" : "24px";
    return css(el("span", { className: "wtc-action-toolbar" }, secondary, ...[...extras].reverse().map(o => { const button = optionButton(o); button.className += " wtc-toolbar-inline"; return button; }), slot), `display:inline-flex;align-items:baseline;gap:${compact ? 4 : 8}px;min-width:0;flex-wrap:nowrap;white-space:nowrap`);
  }
  function publicationConfirmations(repos, above = false) {
    const pending = repos.filter(r => confirming.has(r.sessionKey));
    if (!pending.length) return null;
    const region = css(el("div", { className: "wtc-publication-confirmations", ...(above ? {tabIndex:0,role:"region",ariaLabel:`${pending.length} prepared publications`} : {}) }, ...pending.map(r => {
      const prepared = confirming.get(r.sessionKey), snapshot = prepared.repository ?? r;
      const c = prepared.preview ?? prepared.confirmation;
      const accountKnown = prepared.forge === BITBUCKET ? !!c.account : !!c.account?.login;
      const account = prepared.forge === BITBUCKET ? bitbucketAccount(c) : `@${c.account?.login ?? "unknown"}`;
      const commits = Array.isArray(c.commits) ? c.commits : [], count = snapshot.unpublished ?? snapshot.ahead ?? 0;
      const branch = c.branch ?? snapshot.branch ?? "current branch", dest = c.dest ?? c.baseBranch ?? snapshot.base?.replace(/^origin\//, "") ?? "base";
      const proposal = proposalOf(snapshot);
      const confirm = nextButton("Confirm push", `Confirm the captured push of ${nameOf(r)} as ${account}`, () => { if (confirming.get(r.sessionKey) === prepared) return publish(r, prepared.conversationKey, () => true, true); }, reserved(r) || !accountKnown);
      confirm.className += " wtc-confirm-publication";
      const preview = proposal?.title || proposal?.body ? keepDisclosure(css(el("details", {className:"wtc-pr-preview",open:true},
        el("summary", {textContent:"Proposed PR text"}), el("strong", {textContent:proposal.title ?? ""}),
        css(el("div", {textContent:"For new PRs; existing PR text is kept."}),"color:var(--muted,#888);font-size:11px"),
        renderPRPreview(proposal.body ?? "",el),
        keepDisclosure(css(el("details",{className:"wtc-pr-preview-source"},el("summary",{textContent:"Source"}),css(el("pre",{textContent:proposal.body??""}),"white-space:pre-wrap;overflow-wrap:anywhere;margin:6px 0;font-size:11px")),"font-size:11px;color:var(--muted,#888)"),`publication:${prepared.activityId}:source`)),`font-size:12px;min-height:0;overflow:auto;${above ? "max-height:240px;flex:1 1 auto" : "flex:none"}`),`publication:${prepared.activityId}:proposal`) : null;
      const scrollCue = above && preview ? el("div",{className:"wtc-pr-preview-cue",textContent:"↓ Scroll to read the full proposal",hidden:true}) : null;
      if (scrollCue) {
        const measure = () => { scrollCue.hidden = !preview.open || preview.scrollHeight <= preview.clientHeight + 1; };
        const toggled = preview.ontoggle;
        preview.ontoggle = () => { toggled?.(); measure(); };
        queueMicrotask(measure);
      }
      const info = css(el("div", {className:"wtc-publication-summary"},
        css(el("strong", { textContent: `Push ${nameOf(r)}` }), "font-size:12px"),
        css(el("div", { textContent: `${account} · ${snapshot.owner ?? ""}/${nameOf(r)}` }), "font-size:12px;overflow-wrap:anywhere"),
        css(el("div", { textContent: `${branch} → ${dest}${c.head || c.sourceHeadCommit ? ` · ${String(c.head ?? c.sourceHeadCommit).slice(0, 7)}` : ""}` }), "font-size:12px;overflow-wrap:anywhere;color:var(--muted,#888)"),
        css(el("div", { textContent: `${plural(count, "commit")}${snapshot.forge !== BITBUCKET && snapshot.dirty ? ` + ${plural(snapshot.dirty, "file")} to commit` : ""}${c.uncommitted ? `; ${plural(c.uncommitted, "uncommitted file")} left behind` : ""}. The gateway rechecks the captured request before publishing.` }), "font-size:11px;overflow-wrap:anywhere;color:var(--muted,#888)"),
        commits.length ? keepDisclosure(css(el("details", {}, el("summary", { textContent: `Inspect ${commits.length} captured commits` }), ...commits.map(commit => el("div", { textContent: typeof commit === "string" ? commit : commit.subject ?? commit.title ?? commit.message ?? commit.sha ?? "Commit" }))), "font-size:11px;overflow-wrap:anywhere"), `publication:${prepared.activityId}:commits`) : null),"display:grid;gap:4px;flex:none;min-width:0");
      const controls = css(el("div", {className:"wtc-publication-controls"}, confirm,
          nextButton("Cancel", `Cancel preparation for ${nameOf(r)}; nothing will be pushed`, () => { if (!active(prepared.conversationKey) || confirming.get(r.sessionKey) !== prepared) return; confirming.delete(r.sessionKey); if (prepared.activityId) recordActivity(prepared.logKey, prepared.activityId, { state: "cancelled", status: `${nameOf(r)}: publication cancelled.` }); v.say(`${nameOf(r)}: publication cancelled.`); v.rerender(); }, reserved(r))), "display:flex;align-items:center;gap:8px;padding-top:4px");
      return css(el("section", { className:"wtc-publication-confirmation", ariaLabel: `Confirm publication of ${nameOf(r)}` },
        ...(above ? [preview,scrollCue,info,controls] : [info,controls,preview]).filter(Boolean)),
        `display:flex;flex-direction:column;gap:8px;padding:10px 12px;border:1px solid var(--border-strong,rgba(128,128,128,.35));border-radius:8px;background:var(--bg,transparent);min-width:0;${above ? "max-height:min(50dvh,420px)" : ""}`);
    })), `display:grid;gap:8px;min-width:0;${above ? "max-height:min(50dvh,420px);overflow:auto;overscroll-behavior:contain" : ""}`);
    return above && pending.length > 1 ? css(el("div", {className:"wtc-prepared-publications"},
      css(el("div", {textContent:`${pending.length} prepared pushes · scroll to review each`}),"font-size:11px;color:var(--muted,#888)"), region),"display:grid;gap:4px;min-width:0") : region;
  }
  function bulkActions(repos, narrow, columns) {
    if (!repos.length) return null;
    const pushes = repos.filter(r => pushable(r) && PUSHES.has(r.forge) && mine[r.forge] !== false);
    const catches = repos.filter(r => canCatchUp(r));
    const push = nextButton("↑", `Push all ${pushes.length} eligible repositories; prepare each personal confirmation`, () => pushAll([...pushes]), !pushes.length || pushes.some(reserved) || batchIsRunning() || choosing);
    const catchAll = nextButton("↓", `Catch up all ${catches.length} eligible repositories`, () => catches.every(r => r.next && ["catch-up", "return-to-tip"].includes(r.next.step)) ? startTargets(catches.map(repo => ({ repo, step: repo.next }))) : catchUp([...catches]), !catches.length || catches.some(reserved) || batchIsRunning() || choosing);
    previewButton(catchAll, catches);
    catchAll.title += catches.length ? `: ${catches.map(nameOf).join(", ")}` : "";
    for (const [button, glyph] of [[push, "↑"], [catchAll, "↓"]]) {
      button.className += " wtc-count-control";
      button.replaceChildren(el("span", { className: "wtc-count-icon", textContent: glyph }), el("span", { className: "wtc-count-value" }));
      button.style.cssText += ";width:36px;min-width:0;justify-self:start";
    }
    const commitTargets = repos.filter(r => r.dirty && !r.error && !["running", "pending"].includes(requestFeedback.get(requestKey(r))?.phase) && repoActionOptions(r).some(o => o.label === "Commit changes" && !o.disabled));
    const commitAll = count("±", `Commit changes in all ${commitTargets.length} eligible repositories; review each first`, TONE.pending, () => commitGroup(commitTargets));
    commitAll.className += " wtc-count-control wtc-local-change wtc-column-commit";
    commitAll.replaceChildren(el("span", {className:"wtc-count-icon",textContent:"±"}), el("span", {className:"wtc-count-value"}));
    commitAll.disabled = !commitTargets.length || batchIsRunning();
    const label = css(el("span", { textContent: narrow ? "All" : "All repos" }), "color:var(--muted,#888);font-size:11px;white-space:nowrap;" + (narrow ? "width:20px;text-align:center" : "padding-left:26px"));
    const arrows = css(el("span", {}, commitAll, repoFacts.ahead ? push : null, repoFacts.behind ? catchAll : null), `display:grid;grid-template-columns:${factTracks().map(x => `${x}px`).join(" ")};gap:${FACT_GAP}px;align-items:baseline`);
    return css(el("div", { className: "wtc-bulk-actions", role: "group", ariaLabel: "All repositories" }, ...(narrow ? [label, arrows] : [label, el("span"), arrows, ...(repoFacts.pr ? [el("span")] : []), el("span")])), narrow
      ? `display:grid;grid-template-columns:${REPO_IDENTITY_INSET}px ${factWidth()}px minmax(0,1fr);box-sizing:border-box;column-gap:0;align-items:baseline;min-height:var(--wtc-control-height,20px);margin:0 0 2px;min-width:0`
      : `display:grid;grid-template-columns:${columns.map(c => c.width).join(" ")};column-gap:10px;align-items:baseline;min-height:var(--wtc-control-height,20px);margin:0 0 2px;min-width:0`);
  }
  // What a repository's push would send: its commits not on the forge yet,
  // or, before its first pull request, everything ahead of the base.
  const finished = (r) => r.pullRequest?.state === "merged";
  const toPush = (r) => finished(r) ? 0 : r.unpublished ?? (!r.pullRequest ? r.ahead ?? 0 : 0);
  const pushable = (r) => !r.error && !finished(r) && toPush(r) > 0;
  // A completed branch returns to the tip even if its commits are already
  // present there. Its PR leaves the repository row after that move.
  const toCatchUp = (r) => !r.error && (r.behind > 0 || finished(r));
  const catchUpChoice = r => choicesOf(r).find(a => a.available && ["catch-up", "return-to-tip"].includes(a.step));
  const canCatchUp = r => toCatchUp(r) && (r.choices ? !!catchUpChoice(r) : !r.dirty);

  const catchUpCell = (r) => {
    const button = finished(r)
    ? count(r.behind ? `↓ ${r.behind}` : "↓", `#${r.pullRequest.number} was merged. Return to ${baseOf(r)}'s tip, detached; keep it in the pull request list. The next change starts a new branch`, TONE.pending, () => catchUp([r]))
    : r.behind
    ? count(`↓ ${r.behind}`, `${r.behind} new on ${baseOf(r)}. Catch up locally by merging, without rebasing.${r.pullRequest?.state === "open" ? " Then Update PR / Push publishes the updated branch." : " Push is a separate action."}`, TONE.pending, () => catchUp([r]))
    : count("↓", `#${r.pullRequest.number} was merged. Return to ${baseOf(r)}'s tip; keep its completed PR in the pull request list`, TONE.pending, () => catchUp([r]));
    if (reserved(r)) { button.disabled = true; button.title += " Wait for the current action or inspect its unconfirmed result."; }
    else if (!canCatchUp(r)) { button.disabled = true; button.title = r.catchUpRecovery ? "Local changes need recovery; use Resolve conflicts or ask the conversation for help" : r.dirty ? "Commit local changes with ± first, or update the gateway to catch up while preserving edits" : "Catch-up is unavailable in the current repository plan; inspect its next action"; }
    return button;
  };
  const nameOf = (r) => r.repo ?? r.project ?? r.label;
  const baseOf = (r) => (r.base ? r.base.replace(/^origin\//, "") : "base");
  // Pushes waiting for the person's confirmation, by owner session.
  const confirming = new Map();
  // Whether the person has their own account on each forge: unknown until
  // asked, asked again at most every 30 seconds while something waits to be
  // pushed there. false for Bitbucket: the gateway has no publish for it.
  const mine = { github: undefined, bitbucket: undefined };
  const askedAt = { github: 0, bitbucket: 0 };
  let showConnect = null;
  const accountOf = {
    github: (r) => v.host.request("sessions.github.options", { sessionKey: r.sessionKey, agentId: v.agent() }).then(personalAccount),
    bitbucket: (r) => bitbucketAction(v.host, v.agent(), r, "publish-options", {}).then((o) => o?.account ?? null,
      (err) => (/no Bitbucket publish/.test(err.message) ? false : Promise.reject(err))),
  };
  function checkAccount(repos) {
    for (const forge of PUSHES) {
      const r = repos.find((x) => x.forge === forge && pushable(x));
      if (!r || mine[forge] === false || Date.now() - askedAt[forge] < 30000) continue;
      askedAt[forge] = Date.now();
      accountOf[forge](r).then((now) => {
        if (now && showConnect === forge) showConnect = null;
        if (!!now !== !!mine[forge] || mine[forge] === undefined || now === false) { mine[forge] = now; v.rerender(); }
      }, () => {});
    }
  }
  // How to connect it: shown once a push needed it, until it is there.
  function notice() {
    if (!showConnect || mine[showConnect]) return null;
    const bitbucket = showConnect === BITBUCKET;
    const open = el("a", bitbucket
      ? { textContent: "Open the Bitbucket page", href: BITBUCKET_CONNECT, target: "_blank", rel: "noopener" }
      : { textContent: "Open Settings → Profile", href: appHref("/settings/profile") });
    open.style.cssText = "color:var(--accent, inherit);font-weight:600";
    const close = smallButton("✕", "Hide", () => { showConnect = null; v.rerender(); });
    const steps = bitbucket
      ? [el("li", {}, open, " in its own tab (the Bitbucket tab in the sidebar shows it, but cannot take the form)."),
        el("li", { textContent: "Enter your Atlassian account e-mail and a Bitbucket API token with repositories and pull requests read and write." }),
        el("li", { textContent: "Connect. Then press ↑ again." })]
      : [el("li", {}, open, ", then GitHub connections → Connect."),
        el("li", { textContent: "Choose For me (not For the system), open github.com/login/device and enter the code shown." }),
        el("li", { textContent: "Approve, including workflow. Then press ↑ again." })];
    return css(el("div", { className: "wtc-connect" },
      css(el("div", {}, css(el("strong", { textContent: bitbucket ? "Pushing needs your own Bitbucket login" : "Pushing needs your own GitHub account" }), ""), close), "display:flex;justify-content:space-between;align-items:center;gap:8px"),
      el("div", { textContent: "The gateway pushes for you with it, after you confirm each push." }),
      css(el("ol", {}, ...steps), "margin:0;padding-left:18px;display:grid;gap:2px"),
      bitbucket ? null : css(el("div", { textContent: "A GitHub identity under Agents → Tools is the agent's own; it is not used for pushing." }), "color:var(--muted, #888)")),
    "font-size:12px;line-height:1.45;display:grid;gap:4px;padding:8px 10px;border:1px solid var(--border-strong, rgba(128,128,128,.4));border-radius:12px");
  }
  async function publish(r, conversationKey = v.key(), continueAllowed = () => true, confirmed = false, requestedActivityId = null) {
    const mayDispatch = () => active(conversationKey) && continueAllowed();
    if (!mayDispatch()) return { cancelled: true };
    if (requestUnresolved(r)) return { pending: true, reason: "commit", message: "A repository action is unresolved; inspect its result before publishing." };
    if (busy.has(r.sessionKey) || sharedPushBusy(r)) return { pending: true };
    if (!PUSHES.has(r.forge) || mine[r.forge] === false) return;
    if (confirming.has(r.sessionKey) && !confirmed) { revealPublication(); v.rerender(); return { pending: true }; }
    if (confirmed && !confirming.has(r.sessionKey)) { v.say("The publication preparation expired; prepare it again.", true); return; }
    const activityId = confirming.get(r.sessionKey)?.activityId ?? requestedActivityId ?? uuid();
    const logKey = v.collectionKey?.() ?? conversationKey;
    let lastFailed = false, lastText = "Preparing publication…", lastAction = null;
    const log = (state) => {
      if (!active(conversationKey) && state !== "running") feedbackStore.set(logKey, { text: lastText, failed: lastFailed, action: lastAction, activityId, at: Date.now() });
      recordActivity(logKey, activityId, { repo: nameOf(r), repoSessionKey: r.sessionKey, action: "push", phase: confirmed ? "publishing" : "preparing", inFlight: true, state, status: lastText, detail: undefined, followUp: lastAction });
    };
    const say = (text, failed = false, action = null) => {
      lastText = text; lastFailed = failed; lastAction = action;
      log(failed ? "failed" : "running");
      if (active(conversationKey)) v.say(text, failed, action, activityId);
    };
    const review = (repo, url, action) => reviewPRText(repo, url, conversationKey, action);
    busy.add(r.sessionKey);
    say(confirmed ? `Pushing ${nameOf(r)}…` : `Preparing to push ${nameOf(r)}…`);
    const resume = v.pause();
    v.rerender();
    try {
      const pending = confirming.get(r.sessionKey);
      confirming.delete(r.sessionKey);
      const bitbucket = r.forge === BITBUCKET;
      const next = pending
        ? await (bitbucket ? confirmBitbucket : confirmRepo)(v.host, v.agent(), r, pending, say, review, mayDispatch)
        : bitbucket
          ? await publishBitbucket(v.host, v.agent(), r, say, r.base ? baseOf(r) : undefined, mayDispatch)
          : await publishRepo(v.host, v.agent(), r, say, review, mayDispatch);
      if (next?.cancelled) { say(`${nameOf(r)}: Cancelled before publication dispatch; nothing was pushed.`); log("cancelled"); return next; }
      if (!active(conversationKey)) {
        if (next?.requestId) { lastText = `${nameOf(r)}: preparation discarded after conversation switch; nothing was pushed.`; log("cancelled"); }
        else log(next?.connect || next?.waiting ? "blocked" : lastFailed ? "failed" : next?.published ? "published" : "completed");
        return;
      }
      if (next?.connect) { log("blocked"); mine[r.forge] = null; showConnect = r.forge; v.reveal?.(); }
      else {
        // It is connected: look again rather than keep "not connected".
        if (mine[r.forge] === null) { mine[r.forge] = undefined; askedAt[r.forge] = 0; if (showConnect === r.forge) showConnect = null; }
        if (next?.requestId) { confirming.set(r.sessionKey, { ...next, activityId, conversationKey, logKey, repository: structuredClone(r) }); log("confirmation"); revealPublication(); }
      }
      if (!next?.connect && !next?.requestId) log(lastFailed ? "failed" : next?.waiting ? "blocked" : next?.published ? "published" : "completed");
      return next;
    } catch (err) {
      say(`${nameOf(r)}: ${String(err?.message ?? err)}`, true);
    } finally {
      resume?.();
      try { if (active(conversationKey)) await v.refresh(); }
      finally { busy.delete(r.sessionKey); recordActivity(logKey, activityId, { inFlight: false }); if (active(conversationKey)) v.rerender(); }
    }
  }
  async function withRepoRequests(repos, kind, perform) {
    const conversation = v.key(), collection = v.collectionKey?.() ?? conversation;
    const requests = [...new Map(repos.map(repo => [repo.sessionKey,repo])).values()].map(repo => ({repo, state:beginRepoRequest(repo, collection, kind)}));
    if (!requests.length || requests.some(t => !t.state)) { cancelUnsentRequests(requests.filter(t => t.state), collection); return; }
    const current = () => active(conversation) && requests.every(t => ownsRepoRequest(t.repo,t.state,collection));
    const send = (input, recoveryRef) => current() ? dispatchRepoRequests(requests,conversation,collection,CATCH_UP,input,recoveryRef) : {cancelled:true};
    const native = async operation => {
      if (!current()) return {cancelled:true};
      for (const {state} of requests) state.dispatchedAt = Date.now();
      try {
        const out = await operation();
        for (const {state} of requests) state.nativeFailed = !!out?.code;
        if (!current()) return {cancelled:true};
        return out;
      }
      catch (err) {
        for (const {repo,state} of requests) if (ownsRepoRequest(repo,state,collection)) {
          state.phase = "unconfirmed";
          recordActivity(collection,state.id,{state:"unknown",status:"Action response unavailable; outcome unconfirmed. Check its state before retrying",detail:String(err?.message ?? err)});
        }
        throw err;
      }
    };
    notifyFactFeedback(collection);
    try {
      await perform({conversation,collection,requests,current,send,native});
      for (const {repo,state} of requests) if (ownsRepoRequest(repo,state,collection) && !state.handoffAt && state.phase !== "unconfirmed") {
        state.phase = state.nativeFailed ? "failed" : "settled";
        recordActivity(collection,state.id,{state:state.nativeFailed ? "failed" : state.dispatchedAt ? "completed" : "cancelled",status:state.nativeFailed ? "Failed; open repository details" : state.dispatchedAt ? "Completed" : "Cancelled before dispatch"});
      }
    } finally {
      cancelUnsentRequests(requests,collection); notifyFactFeedback(collection);
      if (active(conversation)) await v.refresh();
    }
  }
  async function catchUp(list) {
    if (batchIsRunning()) return;
    const conversation = v.key();
    for (const r of list) {
      if (!active(conversation)) break;
      await withRepoRequests([r], "catch-up", async ({conversation,collection,requests,current,send,native}) => {
        const id = requests[0].state.id;
        const say = (text, failed = false, action = null) => {
          if (!current()) return;
          const item = recordActivity(collection,id,{detail:text,followUp:action});
          if (active(conversation) && !item.completedRecovery) v.say(text,failed,action,id);
        };
        try {
          const outcome = await catchUpRepo(v.host,v.agent(),r,say,send,() => native(() => call(v.host,"catch-up",r.sessionKey,v.agent())));
          if (outcome?.recoveryRef && current()) recordActivity(collection,id,{recoveryRef:outcome.recoveryRef});
        } catch (err) { if (current()) say(`${nameOf(r)}: ${String(err?.message ?? err)}`,true); }
      });
    }
  }
  async function pushAll(list) {
    if (batchIsRunning()) return;
    const conversationKey = v.key();
    for (const r of list) {
      if (!active(conversationKey)) break;
      if (!confirming.has(r.sessionKey)) await publish(r, conversationKey);
    }
  }
  // A sub-session's commits go into the collection's checkout of the repository, as a
  // catch-up from its branch; a conflict is the agent's, as on ↓.
  const taskRepo = r => store.get(v.key())?.data?.repos?.find(repo => repo.sessionKey === r.taskSessionKey)
    ?? { ...r, sessionKey:r.taskSessionKey, path:undefined, branch:r.taskBranch };
  async function mergeSub(w, r) {
    if (!r.taskSessionKey) return v.say(`${nameOf(r)}: the collection has no checkout of it to merge sub-session ${w.name} into`);
    return withRepoRequests([r,taskRepo(r)],"catch-up",async ({conversation,current,send,native}) => {
      const out = await native(() => call(v.host,"catch-up",r.taskSessionKey,v.agent(),{from:r.branch}));
      if (!current()) return;
      const conflicts = conflictsOf(out.message);
      if (conflicts.length) {
        const handoff = await send(`${nameOf(r)}: merging sub-session ${w.name}'s ${r.branch} (wtc catch-up --from ${r.branch}) conflicts in ${conflicts.join(", ")}.`);
        if (handoff?.handedOff && active(conversation)) v.say(`${nameOf(r)}: ${conflicts.join(", ")} changed in both; the conversation was asked to merge ${r.branch} and resolve them.`);
      } else v.say(`${nameOf(r)}: ${out.json?.summary ?? (wtcMessage(out.message) || "exit " + out.code)}`,out.code !== 0);
    });
  }
  async function catchUpSub(w, r) {
    return withRepoRequests(r.taskSessionKey ? [r,taskRepo(r)] : [r],"catch-up",async ({conversation,current,send,native}) => {
      const out = await native(() => call(v.host,"catch-up",r.sessionKey,v.agent(),r.taskBranch ? {from:r.taskBranch} : undefined));
      if (!current()) return;
      const conflicts = conflictsOf(out.message);
      if (conflicts.length) {
        const handoff = await send(`${nameOf(r)}: catching sub-session ${w.name} up with the collection (wtc catch-up in its checkout) conflicts in ${conflicts.join(", ")}.`);
        if (handoff?.handedOff && active(conversation)) v.say(`${nameOf(r)}: ${conflicts.join(", ")} changed in both; the conversation was asked to catch sub-session ${w.name} up and resolve them.`);
      } else v.say(`${nameOf(r)}: ${out.json?.summary ?? (wtcMessage(out.message) || "exit " + out.code)}`,out.code !== 0);
    });
  }
  // Taking over worker edits can change the parent checkout, so both owners
  // participate in the same lifecycle as a commit or native merge.
  async function reviewSub(w, r) {
    const repos = r.taskSessionKey ? [r,taskRepo(r)] : [r];
    return withRepoRequests(repos,"review",async ({conversation,collection,requests}) => {
      const differ = (r.uncommitted ?? []).filter(f => f.state !== "same").map(f => f.path);
      const input = `Sub-session ${w.name} left ${plural(r.dirty, "uncommitted file")} in its checkout of ${nameOf(r)} (${r.path}). ${differ.length ? `These differ from yours or only it has them: ${differ.join(", ")}. Compare them with yours and take over what is still wanted, in your checkout.` : "Yours has all of them the same."} Then tell me whether it can be discarded. Don't commit in its checkout, and don't push.`;
      const handoff = await dispatchRepoRequests(requests,conversation,collection,null,input);
      if (handoff?.handedOff && active(conversation)) v.say(`${nameOf(r)}: the conversation was asked to review what sub-session ${w.name} left.`);
    });
  }
  const armed = new Map();
  async function discardSub(w) {
    if (w.repos.some(reserved)) return;
    if (!armed.has(w.name)) {
      armed.set(w.name,setTimeout(() => {armed.delete(w.name);v.rerender();},5000)); v.rerender(); return;
    }
    clearTimeout(armed.get(w.name)); armed.delete(w.name);
    return withRepoRequests(w.repos,"discard",async ({conversation,current,native}) => {
      const kept = [];
      for (const r of w.repos) {
        if (!current()) break;
        const out = await native(() => v.host.request("sessions.delete",{key:r.sessionKey,agentId:v.agent()}));
        if (out?.worktreePreserved) kept.push(`${nameOf(r)} (${out.worktreePreserved.reason}, at ${out.worktreePreserved.path})`);
      }
      if (current()) v.say(kept.length ? `Sub-session ${w.name} discarded; OpenClaw kept the worktree of ${kept.join(", ")}.` : `Sub-session ${w.name} discarded.`);
    });
  }

  // PR and service tables retain their fact columns. Repository groups omit
  // unused columns while keeping the dimensions of actionable facts stable.
  const DOT = () => css(el("span", { textContent: "·" }), "color:var(--muted, #888)");
  const muted = (text, title) => css(el("span", { textContent: text, title: title ?? "" }), "color:var(--muted, #888)");
  const toned = (text, tone, title) => css(el("span", { textContent: text, title: title ?? "" }), `color:${tone}`);
  // A fact that is also an action: a count in a pill.
  const count = (text, title, tone, run) => {
    const b = css(el("button", { className: "wtc-action-control", type: "button", textContent: String(text), title, ariaLabel: title, disabled: batchIsRunning() }),
      `border:1px solid var(--wtc-control-border,var(--border, rgba(128,128,128,.4)));background:var(--wtc-control-bg,transparent);color:var(--wtc-control-text,${tone});cursor:pointer;font:inherit;font-size:12px;line-height:16px;min-height:var(--wtc-control-height,20px);min-width:var(--wtc-control-width,20px);padding:0 6px;border-radius:var(--radius-sm,6px);white-space:nowrap;font-variant-numeric:tabular-nums`);
    const parts = String(text).match(/^([±↑↓]) (\d+)$/);
    if (parts) {
      b.className += " wtc-count-control";
      b.replaceChildren(el("span", { className: "wtc-count-icon", textContent: parts[1] + " " }), el("span", { className: "wtc-count-value", textContent: parts[2] }));
    }
    b.onclick = async (e) => {
      if (b.disabled) return;
      e?.preventDefault();
      e?.stopPropagation();
      b.disabled = true;
      try { await run(); } catch (err) { v.say(String(err?.message ?? err), true); } finally { b.disabled = false; }
    };
    return b;
  };
  const working = (decorative = false) => {
    const svg = controlIcon("spinner", 12); svg.setAttribute("class", "wtc-header-glyph wtc-cell-spinner");
    svg.style.transformBox = "view-box";
    return css(el("span", decorative ? {ariaHidden:"true"} : {title:"Working",role:"status",ariaLabel:"Working"}, svg), "display:inline-flex;width:12px;height:12px;align-items:center;justify-content:center;vertical-align:middle");
  };
  // Repository mutations share one admission authority across views and routes.
  // A request retains its original owner and checkout until completion or explicit acknowledgement.
  const requestKey = (r, collection = v.collectionKey?.() ?? v.key()) => `${collection}|${r.sessionKey}`;
  const ownsRepoRequest = (r, state, collection) => state && requestFeedback.get(requestKey(r, collection)) === state && ["running", "pending", "unconfirmed"].includes(state.phase);
  function beginRepoRequest(r, collection, kind, id = uuid()) {
    if (r.error || reservedInCollection(r, collection) || recoveryQueues.has(collection) || kind === "commit" && commitBlocked(r)) return null;
    const state = { phase: "running", kind, before: toPush(r), dirty: r.dirty, branch: r.branch, path: r.path, id };
    const key = requestKey(r, collection);
    requestFeedback.set(key, state); limitRequestFeedback(key, state, collection);
    recordActivity(collection,id,{repo:nameOf(r),repoSessionKey:r.sessionKey,repoPath:r.path,action:kind,state:"running",status:kind === "commit" ? "Requesting local commits" : "Rechecking repository action"});
    return state;
  }
  const beginCommitRequest = (r, collection, id) => beginRepoRequest(r, collection, "commit", id);
  function cancelUnsentRequests(requests, collection, detail) {
    for (const {repo,state} of requests) {
      if (!ownsRepoRequest(repo,state,collection) || state.dispatchedAt) continue;
      state.phase = "settled";
      recordActivity(collection,state.id,{state:"cancelled",status:"Cancelled before action handoff",detail});
    }
  }
  async function dispatchRepoRequests(requests, conversation, collection, skill, input, recoveryRef) {
    if (!requests.length || !active(conversation) || !requests.every(({repo,state}) => ownsRepoRequest(repo, state, collection))) { cancelUnsentRequests(requests,collection); return {cancelled:true}; }
    const blocked = requests.map(({repo,state}) => repo.error || (repo.path !== state.path || repo.branch !== state.branch ? "The checkout changed before handoff; refresh before acting" : state.kind === "commit" ? commitBlocked(repo) : "")).find(Boolean);
    if (blocked) { cancelUnsentRequests(requests,collection,blocked); return {cancelled:true}; }
    if (requests.some(({state}) => state.handoffAt)) { cancelUnsentRequests(requests,collection); return {pending:true}; }
    for (const {state} of requests) state.handoffAt = state.dispatchedAt = Date.now();
    if (recoveryRef) for (const {state} of requests) recordActivity(collection,state.id,{recoveryRef});
    try {
      await sendToAuthor(conversation, skill, input);
      for (const {repo,state} of requests) {
        if (!ownsRepoRequest(repo,state,collection)) continue;
        if (state.phase !== "unconfirmed") state.phase = "pending";
        recordActivity(collection,state.id,{state:state.phase === "unconfirmed" ? "unknown" : "handed-off",status:state.phase === "unconfirmed" ? "Request accepted; outcome remains unconfirmed" : "Sent to conversation; completion pending"});
      }
      return {handedOff:true};
    } catch (err) {
      for (const {repo,state} of requests) {
        if (!ownsRepoRequest(repo,state,collection)) continue;
        state.phase = "unconfirmed"; state.error = String(err?.message ?? err);
        recordActivity(collection,state.id,{state:"unknown",status:"Action handoff outcome unconfirmed; check the conversation before retrying",detail:"Action handoff outcome unconfirmed: "+state.error});
      }
      return {unconfirmed:true};
    } finally { notifyFactFeedback(collection); }
  }
  function limitRequestFeedback(key, state, collection) {
    const timer = setTimeout(() => {
      if (requestFeedback.get(key) !== state || !["running", "pending"].includes(state.phase)) return;
      state.phase = "unconfirmed";
      recordActivity(collection,state.id,{state:"unknown",status:state.kind === "commit" ? "No commit observed yet; open action details to check the request or ask the agent for help" : "Action outcome unconfirmed; open action details to check the request or ask the agent for help"});
      notifyFactFeedback(collection);
    }, 90000); timer.unref?.();
  }
  async function commitGroup(repos) {
    if (!repos.length || batchIsRunning()) return;
    const conversation = v.key(), collection = v.collectionKey?.() ?? conversation;
    const admitted = repos.map(repo => ({repo,state:beginCommitRequest(repo,collection)})).filter(t => t.state);
    let targets = admitted.map(t => t.repo);
    if (!targets.length) return;
    const states = admitted.map(t => t.state);
    notifyFactFeedback(collection);
    try {
      targets = await recheckCommitTargets(targets, conversation, collection);
      if (!targets.every((r, i) => ownsRepoRequest(r, states[i], collection))) {
        states.forEach((state, i) => { if (ownsRepoRequest(targets[i], state, collection)) { state.phase = "settled"; recordActivity(collection,state.id,{state:"cancelled",status:"Cancelled before action handoff"}); } });
        return;
      }
      await dispatchRepoRequests(targets.map((repo,i) => ({repo,state:states[i]})), conversation, collection, "wtc", `Review and commit the intended local changes in these eligible repositories only: ${targets.map(nameOf).join(", ")}. Recheck each repository's current state and branch policy. Preserve intended work and report each result. Publication and follow-up actions are separate.`);
    } catch (err) { states.forEach((state, i) => { if (!ownsRepoRequest(targets[i], state, collection)) return; state.phase = "failed"; state.error = String(err?.message ?? err);recordActivity(collection,state.id,{state:"failed",status:"Commit request failed: "+state.error}); }); }
    finally { cancelUnsentRequests(targets.map((repo,i) => ({repo,state:states[i]})),collection); notifyFactFeedback(collection); if (active(conversation)) await v.refresh(); }
  }
  async function commitFromFact(r) {
    const collection = v.collectionKey?.() ?? v.key();
    const state = beginCommitRequest(r, collection); if (!state) return; notifyFactFeedback(collection);
    try {
      [r] = await recheckCommitTargets([r], v.key(), collection);
      if (!ownsRepoRequest(r, state, collection)) return;
      const result = r.next?.step === "commit" ? await actNext(r, state.id) : await askCommit(r, state);
      if (result?.unconfirmed || result?.cancelled) return;
      const last = activityStore.get(collection)?.get(state.id);
      if (!ownsRepoRequest(r, state, collection)) return;
      if (result?.error || last?.action === "commit" && activityFailed(last)) throw Error(result?.error ?? last.status);
    } catch (err) { state.phase = "failed"; state.error = String(err?.message ?? err);recordActivity(collection,state.id,{state:"failed",status:"Commit request failed: "+state.error}); }
    finally { cancelUnsentRequests([{repo:r,state}],collection); notifyFactFeedback(collection); }
  }
  function localChanges(r) {
    const collection = v.collectionKey?.() ?? v.key(), key = requestKey(r);
    const request = requestFeedback.get(key);
    const state = request?.kind === "commit" ? request : null;
    // New commits can be observed even when intentionally uncommitted files remain.
    // Partial progress does not prove the handed-off request has finished.
    // A lower dirty count alone never earns a commit-success mark.
    const progress = `${toPush(r)}|${r.dirty}`;
    if (["pending", "unconfirmed"].includes(state?.phase) && state.path === r.path && state.branch === r.branch && !r.error && (toPush(r) > state.before || r.dirty < state.dirty) && state.progress !== progress) {
      state.progress = progress;
      state.observedCommit = toPush(r) > state.before;
      state.phase = toPush(r) > state.before && !r.dirty ? "completed" : "unconfirmed";
      state.at = Date.now();
      const outcome = state.phase;
      queueMicrotask(() => recordActivity(collection,state.id,{state:outcome === "completed" ? "completed" : "unknown",status:outcome === "completed" ? "New local commits observed; working tree clean" : toPush(r) > state.before ? "Partial commit progress observed; request outcome unconfirmed. Check the conversation before releasing its reservation" : "Changes decreased without a new commit; request outcome unconfirmed. Check the conversation before releasing its reservation"}));
      const timer = setTimeout(() => { if (requestFeedback.get(key) === state && state.phase === "completed") state.phase = "settled"; notifyFactFeedback(collection); }, 5100); timer.unref?.();
    }
    if (state?.phase === "failed" && !r.dirty) state.phase = "settled";
    const done = state?.phase === "completed" && !r.dirty && Date.now() - state.at < 5000;
    if (!r.dirty && !done && !["pending", "running", "unconfirmed"].includes(state?.phase)) return null;
    const option = repoActionOptions(r).find(o => o.label === "Commit changes");
    const pending = ["pending", "running"].includes(state?.phase);
    const title = done ? "New local commits observed; working tree clean" : state?.phase === "unconfirmed" ? `${state.observedCommit ? "Some local commits observed; request outcome unconfirmed" : "No commit observed yet"}. Open action details to check the request or ask the agent for help` : state?.phase === "failed" ? `Commit request failed: ${state.error}. Retry` : pending ? "Sent to conversation; no commit observed yet. This repository stays reserved across views. Open action details for status or agent help" : option?.disabled ? option.detail : `${plural(r.dirty, "file")} changed. Commit changes for ${nameOf(r)}; publication is separate`;
    const button = count(`± ${r.dirty || 0}`, title, done ? TONE.success : state?.phase === "failed" ? TONE.failure : TONE.pending, () => pending || state?.phase === "unconfirmed" ? openActivity() : commitFromFact(r));
    button.className += " wtc-local-change";
    if(done)button.setAttribute("data-feedback","completed");
    button.disabled = done || !pending && state?.phase !== "unconfirmed" && (!option || option.disabled);
    const icon = button.children[0];
    if (icon && (pending || done || state?.phase === "failed" || state?.phase === "unconfirmed")) { if(pending) icon.replaceChildren(working(true)); else icon.textContent = done ? "✓" : state?.phase === "failed" ? "!" : "?"; }
    if (button.children[1] && r.dirty > 999) { button.children[1].textContent = new Intl.NumberFormat("en",{notation:"compact",maximumFractionDigits:0}).format(r.dirty);button.children[1].style.fontSize="10px"; }
    if (done && !r.dirty && button.children[1]) button.children[1].style.visibility = "hidden";
    if (pending) button.setAttribute("aria-busy", "true");
    return button;
  }
  const pendingPushCount = (n) => {
    const button = count(n ? `↑ ${n}` : "↑", "Publication in progress; the captured commit count stays visible", TONE.pending, () => {});
    button.disabled = true;
    button.style.opacity = ".65";
    return button;
  };
  // A fixed first-line reference keeps an empty fact from changing the row's
  // baseline when a count or action appears or disappears.
  const baselineStrut = () => css(el("span", { ariaHidden: "true" }), "display:inline-block;flex:0 0 0;width:0;height:calc(var(--wtc-control-height,20px) / 2 + 5px)");
  const factSlot = value => css(el("span", {}, baselineStrut(), value), "display:flex;align-items:baseline;min-width:0;min-height:calc(var(--wtc-control-height,20px) + 2px)");
  // columns: { head, title, width, align, under, overflow }. Narrow, an under column
  // (the service's repository) folds below its preceding cell. Repository
  // grouping is composed by repoTable. overflow lets its narrow facts extend
  // beneath Next by the action width plus the table column gap. blank leaves
  // unused cells empty, hides visual headings (retaining accessible names), and tightens the row gaps.
  const table = (columns, rows, narrow = false, blank = false) => {
    const align = (c) => c.align ? `justify-self:${c.align};` : "";
    const shown = columns.map((c, i) => i).filter((i) => !(narrow && columns[i].under));
    const grid = css(el("div", { className: "wtc-table" }),
      `display:grid;${blank ? "grid-auto-rows:minmax(var(--wtc-control-height,20px),auto);" : ""}grid-template-columns:${shown.map((i) => columns[i].width ?? "auto").join(" ")};column-gap:${blank ? REPO_COLUMN_GAP : 6}px;row-gap:${blank ? 1 : narrow ? 6 : 4}px;align-items:baseline;font-size:12px;font-variant-numeric:tabular-nums`);
    const empty = (value) => value == null || value === "" || value === 0;
    grid.setAttribute("data-columns", shown.length);
    // Repository field names remain accessible without occupying a visible row.
    grid.append(...shown.map((i) => css(el("span", { title: columns[i].title ?? "" }, columns[i].head ?? ""),
      `${blank ? "position:absolute;width:1px;height:1px;overflow:hidden;clip-path:inset(50%);" : ""}${align(columns[i])}font-size:10.5px;letter-spacing:normal;color:var(--muted, #888);white-space:nowrap`)));
    for (const [rowIndex,row] of rows.entries()) {
      grid.append(...shown.map((i) => {
        const c = columns[i];
        const below = narrow && columns[i + 1]?.under && !empty(row[i + 1]) ? row[i + 1] : null;
        const value = empty(row[i]) ? blank ? "" : DOT() : row[i];
        if (below) {
          return css(el("span", {}, css(el("span", {}, value), "display:flex;align-items:baseline;gap:6px;min-width:0;overflow:hidden;white-space:nowrap"),
            css(el("span", {}, below), "display:block;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-size:11px")),
          `${align(c)}min-width:0;display:grid;gap:1px`);
        }
        // Absolute icon rails keep their full hit area; text cells still clip.
        const overflow = c.overflow || value?.className === "wtc-lead-slot";
        const cell = css(el("span", {}, blank ? baselineStrut() : null, value), `${align(c)}${blank ? `width:100%;box-sizing:border-box;min-height:calc(var(--wtc-control-height,20px) + 2px);display:flex;align-items:baseline;align-self:stretch;z-index:1;grid-row:${rowIndex+1};grid-column:${i+1};${c.align === "end" ? "justify-content:flex-end;" : ""}` : ""}min-width:0;overflow:${overflow ? "visible" : "hidden"};text-overflow:ellipsis;white-space:nowrap`);
        if (row.sessionKey) cell.setAttribute("data-repo-session", row.sessionKey);
        return cell;
      }));
      if (blank && row.sessionKey) {
        // A single paint layer covers the row and its column gaps without sizing it.
        const wash = css(el("span", {className:"wtc-action-preview-wash",ariaHidden:"true"}), `grid-row:${rowIndex+1};grid-column:1/-1;align-self:stretch;margin-block:-.5px;min-height:0;min-width:0;z-index:0;pointer-events:none;border-radius:4px`);
        wash.setAttribute("data-repo-session",row.sessionKey);grid.append(wash);
      }
    }
    return grid;
  };

  const repoName = (r) => {
    const href = webUrlOf(r);
    const label = css(el(href ? "a" : "span", { className: "wtc-repo-label", textContent: nameOf(r) ?? "?", title: [r.owner ? `${r.owner}/${nameOf(r)}` : nameOf(r), r.path].filter(Boolean).join("\n") }),
      "color:var(--text-strong, inherit);font-weight:600;text-decoration:none;overflow:hidden;text-overflow:ellipsis;min-width:0");
    if (href) Object.assign(label, { href, target: "_blank", rel: "noopener noreferrer" });
    const check = choosing ? el("input", { type: "checkbox", checked: selection.has(r.sessionKey), disabled: batchIsRunning() || reserved(r) || !r.next || !choicesOf(r).some(a => a.available), title: r.next ? "Select this repository’s action" : "No selectable next step", ariaLabel: `Select ${nameOf(r)}` }) : null;
    if (check) {
      check.style.cssText = "align-self:center;width:15px;height:15px;margin:0;accent-color:var(--accent,#75c8c1);flex:none";
      check.onchange = () => { if (selection.has(r.sessionKey)) selection.delete(r.sessionKey); else selection.set(r.sessionKey, { repo: r, step: r.next }); batchPreview = null; v.rerender(); };
    }
    const last = lastActivity(r);
    const failure = activityFailed(last) || last?.state === "unknown" ? nextButton(last?.state === "unknown" ? "?" : "⚠", `${nameOf(r)}: ${last.status}. Open action details`, openActivity) : null;
    // Keep wide name tracks steady when action feedback appears.
    const warning = css(el("span", { className: "wtc-repo-warning" }, failure), "display:inline-flex;align-items:baseline;flex:0 0 24px;width:24px");
    return css(el("span", { className: "wtc-repo-name", title: label.title }, leadSlot(check ?? forgeIcon(r.forge, 13)), label, warning), "display:inline-flex;align-items:baseline;gap:6px;min-width:0;max-width:100%");
  };
  // ↑: the push, as a button only where the gateway pushes it (GitHub and,
  // with its plugin, Bitbucket, each with the person's own account); a
  // prepared arrow opens its review; Confirm push is a separate control.
  function pushCell(r, n) {
    if (!pushable(r)) return null;
    const label = n ? `↑ ${n}` : "↑";
    const what = [r.forge !== BITBUCKET && r.dirty ? plural(r.dirty, "uncommitted file") : "", n ? plural(n, "commit") : ""].filter(Boolean).join(" and ");
    const activity = `${what} to ${r.forge !== BITBUCKET && r.dirty ? "publish" : "push"}`;
    const account = r.forge === BITBUCKET ? "Bitbucket login" : "GitHub account";
    if (!PUSHES.has(r.forge) || mine[r.forge] === false) {
      const why = r.forge === BITBUCKET ? "The gateway has no Bitbucket publish (the openclaw-bitbucket plugin, 0.3.0 or later)" : `The gateway does not push ${r.forge ?? "this forge"}'s repositories`;
      return toned(label, TONE.pending, `${activity}. ${why}, and the card does not hand pushing to the agent.`);
    }
    const pending = confirming.get(r.sessionKey);
    if (pending) {
      const button = count(label, `Review ${nameOf(r)}'s prepared publication before confirming`, TONE.pending, () => { v.reveal?.(); v.showActions?.(); v.rerender(); });
      return button;
    }

    const left = r.forge === BITBUCKET && r.dirty ? " Uncommitted files stay behind: ask the conversation to commit them first." : "";
    const button = mine[r.forge] === null
      ? count(label, `${activity}. Pushing needs your own ${account}: click to push, or for how to connect it`, TONE.none, () => publish(r))
      : count(label, `${activity}. ${pushTitle(r.pullRequest, r.forge)}, with your own ${account}, after you confirm.${proposalTitle(r)}${left}`, TONE.pending, () => publish(r));
    if (requestUnresolved(r)) { button.disabled = true; button.title += " Wait for the current action or inspect its unconfirmed result."; }
    return button;
  }
  const openPRHistory = () => v.host.ui.openPanel("collection", { sessionKey: v.key(), agentId: v.agent(), focus: "pull-requests" });
  const otherPRs = (r) => [...new Map((r.pullRequests ?? []).filter((pr) => String(pr.number) !== String(r.pullRequest?.number)).map((pr) => [String(pr.number), pr])).values()];
  const historyButton = (...args) => { const button = nextButton(...args); button.className += " wtc-pr-history"; button.style.cssText += ";width:80px;max-width:80px;min-width:0;font-size:11px;padding:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;justify-content:flex-end;text-align:right"; return button; };
  function prHints(r) {
    const others = otherPRs(r);
    const archived = others.filter((pr) => pr.state === "merged" || pr.state === "closed");
    if (others.length && (r.pullRequest || others.length > 1 && others.some(pr => pr.state === "open"))) return historyButton(archived.length === others.length ? plural(archived.length, "archived PR") : plural(others.length, "other PR"), `${plural(others.length, "other PR")}: ${others.length - archived.length} open, ${archived.length} archived. Open collection PR history in the taba sidebar`, openPRHistory);
    const active = others.filter((pr) => pr.state === "open").map((pr) => {
      const hint = link(`#${pr.number}`, pr.url);
      return css(el("span", { title: `Other active PR #${pr.number}: ${pr.title ?? ""}` }, typeof hint === "object" ? css(hint, "color:var(--muted,#888);text-decoration:none") : hint), "color:var(--muted,#888)");
    });
    const history = archived.length ? historyButton(plural(archived.length, "archived PR"), `${archived.length} completed pull requests for ${nameOf(r)}. Open collection PR history in the taba sidebar`, openPRHistory) : null;
    if (history) { history.style.flexShrink = "1"; history.style.whiteSpace = "nowrap"; }
    return active.length || history ? css(el("span", {}, active.length ? css(el("span", {}, ...active), "display:inline-flex;gap:4px;min-width:0;overflow:hidden") : null, history), "display:inline-flex;align-items:baseline;gap:4px;min-width:0;overflow:hidden;white-space:nowrap") : null;
  }
  function repoRow(r) {
    const pr = r.pullRequest;
    const open = pr?.state === "open";
    const n = toPush(r);
    const ci = pr?.checks && pr.checks !== "none"
      ? toned({ success: "✓", failure: "✗", pending: "●" }[pr.checks] ?? "·", TONE[pr.checks] ?? TONE.none, `${ciText(pr)}: ${pr.passed ?? 0} passed, ${pr.failed ?? 0} failed, ${pr.running ?? 0} running`)
      : null;
    const conflict = open && pr.mergeable === "conflicting"
      ? count("✗", `#${pr.number} conflicts with ${baseOf(r)}. Resolve: ask the conversation to merge the base in and resolve the conflicts`, TONE.failure,
        () => withRepoRequests([r],"resolve",({send}) => send(`${nameOf(r)}: pull request #${pr.number} conflicts with ${baseOf(r)}.`)))
      : null;
    const review = open ? { approved: toned("✓", TONE.success, "approved"), changes_requested: toned("✗", TONE.failure, "changes requested"), review_required: muted("○", "review required") }[pr.review] ?? null : null;
    const errorLabel = r.error === "checkout unavailable" ? r.recovery?.state === "restorable" ? "Evicted" : r.recovery?.state === "unavailable" ? "Snapshot unavailable" : "No checkout" : r.error === "status unavailable" ? "No status" : r.error;
    const branch = r.error ? toned(errorLabel, r.recovery?.state === "restorable" ? TONE.pending : TONE.failure, r.recovery?.state === "restorable" ? "OpenClaw evicted this checkout; a recovery snapshot is recorded. Restore to resume." : r.recovery?.reason ?? r.error)
        : untouched(r)
          ? css(el("code", { textContent: `at ${baseOf(r)}`, title: `Checked out on branch ${r.branch}; no task commits ahead of ${baseOf(r)}` }), "font-size:12px;color:var(--muted, #888)")
          : css(el("code", { textContent: r.branch ?? `at ${baseOf(r)}`, title: r.branch ? `${r.branch}, ${r.ahead ?? 0} ahead of ${baseOf(r)}` : `Detached HEAD at ${baseOf(r)}; no branch is checked out` }), "font-size:12px;color:var(--muted, #888)");
    branch.className = "wtc-repo-branch";
    branch.style.cssText += ";display:block;width:100%;min-width:0;max-width:100%;overflow:hidden;text-overflow:ellipsis;white-space:nowrap";
    if (r.branch?.length > 8 && !untouched(r) && !r.error) {
      const expanded = expandedBranches.has(r.sessionKey);
      Object.assign(branch, { role: "button", tabIndex: 0, ariaExpanded: expanded, ariaLabel: `${r.branch}. ${expanded ? "Collapse" : "Show"} complete branch name` });
      branch.style.cssText += `;display:${expanded ? "flex" : "block"};align-items:baseline;white-space:${expanded ? "normal" : "nowrap"};overflow:hidden;text-overflow:ellipsis;overflow-wrap:anywhere;cursor:pointer;text-decoration:underline dotted;text-underline-offset:3px`;
      const toggle = e => { e?.preventDefault(); e?.stopPropagation(); if (expanded) expandedBranches.delete(r.sessionKey); else expandedBranches.add(r.sessionKey); v.rerender(); };
      branch.onclick = toggle;
      branch.onkeydown = e => { if (e.key === "Enter" || e.key === " ") toggle(e); };
    }
    const recent = lastActivity(r);
    const caughtUp = !toCatchUp(r) && recent?.action === "catch-up" && (recent.state === "completed" || recent.status === "Completed");
    return [
      repoName(r), branch,
      localChanges(r),
      busy.has(r.sessionKey) || sharedPushBusy(r) ? pendingPushCount(n) : pushCell(r, n),
      catchUpRunning(r) ? working() : toCatchUp(r) ? catchUpCell(r) : caughtUp ? toned("✓", TONE.success, "Caught up; no base changes remain in this snapshot") : null,
      pr ? css(el("span", { title: `${pr.draft && open ? "Draft PR. " : ""}${pr.title ?? ""}` }, link(`#${pr.number}`, pr.url), pr.draft && open && !otherPRs(r).length ? muted("draft") : pr.state !== "open" ? muted(pr.state) : null), "display:inline-flex;gap:4px") : null,
      ci, conflict, review, nextCell(r), prHints(r),
    ];
  }

  // ▶/■ NAME REPO STATE URL, then a column per way in (gateway, cloudflare…)
  // and the + that opens a new one.
  const slug = (url) => { try { return new URL(url).hostname.split(".")[0]; } catch { return url; } };
  function serviceControls(groups) {
    if (groups.length < 2) return null;
    if (v.svc.busy("*")) return muted("Working…");
    const button = (text, op) => {
      const b = css(el("button", { type: "button", className: "wtc-service-bulk", textContent: text, title: `${text} services`, ariaLabel: `${text} services` }), "display:inline-flex;align-items:center;min-height:var(--wtc-state-size,20px);border:0;padding:0 3px;border-radius:4px;background:transparent;color:var(--muted,#888);font:inherit;font-size:11px;line-height:16px;cursor:pointer;white-space:nowrap");
      b.onclick = e => { e?.preventDefault(); e?.stopPropagation(); return v.svc.run({ op }, `${op === "up" ? "Starting" : "Stopping"} all services…`); };
      return b;
    };
    return css(el("span", {}, groups.some(g => !g.running) ? button("Start all", "up") : null, groups.some(g => g.running) ? button("Stop all", "down") : null), "display:inline-flex;align-items:baseline;gap:6px");
  }
  function serviceTable(groups, data, narrow = false) {
    const ways = [];
    for (const g of groups) {
      if (portalsOf(g, data).length && !ways.includes("gateway")) ways.push("gateway");
      for (const t of g.tunnels) if (!ways.includes(tunnelName(t))) ways.push(tunnelName(t));
    }
    const foldWays = narrow && (ways.length > 1 || groups.some(g => g.loose));
    const visibleWays = foldWays ? [] : ways;
    const repair = (g) => count("Consolidate", "Ask the conversation to open it with wtc svc tunnel instead", "inherit", () => askConversation(v.host, v.key(), consolidate(g)));
    const wayNode = (g, w) => {
      if (w === "gateway") {
        const ps = portalsOf(g, data);
        return ps.length ? portalNode(g, ps[0], v.svc, foldWays ? "gateway" : "open") : null;
      }
      const t = g.tunnels.find(x => tunnelName(x) === w);
      return t ? tunnelNode(g, t, v.svc, {
        label: foldWays ? w : t.url ? slug(t.url) : "open",
        opener: (title, run) => {
          if (!foldWays) return leadSlot(stateGlyph("▶", title, TONE.none, run));
          const b = css(el("button", {type:"button",className:"wtc-tunnel-existing",title,ariaLabel:title},controlIcon("▶"),w), "display:inline-flex;align-items:center;gap:3px;min-height:24px;padding:0 3px;border:0;border-radius:4px;background:transparent;color:var(--muted,#888);font:inherit;cursor:pointer");
          b.onclick = e => { e.preventDefault(); e.stopPropagation(); return run(); };
          return b;
        },
      }) : null;
    };
    const columns = [
      { head: "", width: "20px" },
      { head: "Service", width: narrow ? `minmax(${visibleWays.length ? 48 : 64}px,1fr)` : "minmax(0,max-content)" },
      { head: "Repo", width: "minmax(0,max-content)", under: true },
      { head: "State", width: narrow ? "minmax(44px,56px)" : "78px" },
      { head: "URL", width: narrow ? "minmax(44px,56px)" : "80px", title: "Where it listens on the gateway" },
      ...visibleWays.map((w) => ({ head: css(el("span", { textContent: w }), "display:block;min-width:0;overflow:hidden;text-overflow:ellipsis"), width: narrow ? "minmax(24px,max-content)" : "minmax(14px,max-content)", title: w === "gateway" ? "OpenClaw portals" : `${w} tunnels` })),
      { head: "", width: !narrow && groups.some(g => g.loose) ? "minmax(32px,max-content)" : "32px", align: "center", overflow: true },
    ];
    const rows = groups.map((g) => {
      const stateText = g.state === "absent" || g.state === "idle" ? "stopped" : g.state;
      const logMembers = g.members.filter(s => !["endpoint", "resources", "configuration"].includes(s.kind) && s.state !== "unconfigured");
      const targets = logMembers.filter(s => s.path === g.group || s.path?.startsWith(g.group + "/")).map(s => s.path);
      const canReadLogs = !!g.group && (logMembers.length > 0 || g.members.some(s => s.kind === "endpoint" && s.state !== "unconfigured"));
      const logTitle = canReadLogs ? `View logs for ${g.label}` : "No process logs available yet";
      return [
        leadSlot(stateButton(g, v.svc)),
        Object.assign(css(el("button", { type: "button", className: "wtc-service-label", textContent: g.label, title: logTitle, ariaLabel: logTitle, disabled: !canReadLogs }), "display:block;min-width:0;max-width:100%;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font:inherit;line-height:inherit;font-weight:600;color:var(--text-strong, inherit);border:0;padding:0;background:transparent;text-align:left;cursor:pointer;text-decoration:underline dotted;text-underline-offset:3px"), { onclick: () => { if (canReadLogs) return loadServiceLogs(targets[0] ?? g.group, g.label, targets); } }),
        g.repo ? muted(g.repo) : null,
        g.loose ? toned(narrow ? "separate" : "separate tunnel", TONE.pending, "Started apart from the service it exposes")
          : toned(stateText, g.failed ? TONE.failure : g.running ? TONE.success : TONE.none, stateText),
        g.url && !g.loose ? address(g.url) : null,
        ...visibleWays.map(w => wayNode(g, w)),
        g.loose && !narrow ? repair(g) : leadSlot(g.loose ? null : newTunnel(g, v.svc, data, { compact: true }), 32),
    ];
    });
    const grid = table(columns, rows, narrow);
    grid.style.gridAutoRows = "minmax(20px,auto)";
    const cells = [...grid.children], width = columns.filter(c => !(narrow && c.under)).length;
    if (foldWays) groups.forEach((g, index) => {
      const links = ways.map(w => wayNode(g, w)).filter(Boolean);
      if (!links.length && !g.loose) return;
      const extra = css(el("span", {role:"group",ariaLabel:`Tunnels for ${g.label}`},muted("Tunnels"),...links,g.loose ? repair(g) : null), "grid-column:2 / -1;display:flex;align-items:center;flex-wrap:wrap;gap:4px 10px;min-width:0;margin-top:-4px;font-size:11px");
      extra.setAttribute("data-service-tunnels", "true");
      grid.insertBefore(extra, cells[(index + 2) * width] ?? null);
    });
    return grid;
  }

  // Collapsed, the same facts summed, in the same order: ± ↑ ↓, the pull
  // requests, the services. ↑ pushes and ↓ catches up eligible repositories.
  function totals(repos, groups) {
    // An action says what it is (↑ 3), a fact has its head before it (± 1).
    const slot = (head, value, title) => css(el("span", { title: title ?? "" }, value?.tagName === "BUTTON" ? null : muted(head), value ?? DOT()),
      "display:inline-flex;align-items:center;gap:4px;min-height:var(--wtc-control-height,20px);white-space:nowrap;font-size:12px;font-variant-numeric:tabular-nums");
    const files = repos.reduce((n, r) => n + (r.dirty ?? 0), 0);
    const ready = repos.filter((r) => pushable(r) && PUSHES.has(r.forge) && mine[r.forge] !== false);
    const commits = ready.reduce((n, r) => n + toPush(r), 0);
    const behind = repos.filter(canCatchUp);
    const prs = repos.filter((r) => r.pullRequest?.state === "open");
    const worst = prs.map((r) => r.pullRequest).sort((a, b) => [TONE.failure, TONE.pending, TONE.success, TONE.none].indexOf(prTone(a)) - [TONE.failure, TONE.pending, TONE.success, TONE.none].indexOf(prTone(b)))[0];
    const archivedCount = repos.reduce((n, r) => n + otherPRs(r).filter((pr) => pr.state === "merged" || pr.state === "closed").length, 0);
    const out = [
      slot("±", files ? toned(String(files), TONE.pending) : null, "Changed files not committed yet"),
      slot("↑", ready.some((r) => busy.has(r.sessionKey) || sharedPushBusy(r)) ? working() : ready.length
        ? count(commits ? `↑ ${commits}` : "↑", `Push ${ready.map(nameOf).join(", ")}, each to its pull request or a new draft one`, TONE.pending, () => pushAll(ready)) : null),
      slot("↓", behind.some((r) => catchUpRunning(r)) ? working() : behind.length
        ? count(behind.some((r) => r.behind) ? `↓ ${behind.reduce((n, r) => n + (r.behind ?? 0), 0)}` : "↓", `Catch up ${behind.map((r) => `${nameOf(r)} (${r.behind || `#${r.pullRequest.number} merged`})`).join(", ")}: merge locally into live branches and return completed branches to the tip, without rebasing. For an open PR, Push publishes the updated branch afterward`, TONE.pending, () => catchUp(behind)) : null),
      slot("PR", css(el("span", {}, prs.length ? toned(`${prs.length}${worst && prTone(worst) === TONE.failure ? " ✗" : worst && prTone(worst) === TONE.success ? " ✓" : ""}`, prTone(worst),
        prs.map((r) => `${nameOf(r)} #${r.pullRequest.number}: ${[r.pullRequest.title, ...prState(r.pullRequest)].filter(Boolean).join(", ")}`).join("\n")) : archivedCount ? null : DOT(),
        archivedCount ? nextButton(plural(archivedCount, "archived PR"), "Open completed pull requests in the collection's taba sidebar", openPRHistory) : null), "display:inline-flex;align-items:center;gap:4px")),
    ];
    if (groups.length) {
      const up = groups.filter((g) => g.running).length;
      const glyph = v.svc.busy("*") ? busyMark() : up < groups.length
        ? stateGlyph("▶", groups.length > 1 ? "Start all services" : `Start ${groups[0].label}`, TONE.none, () => v.svc.run({ op: "up" }, "Starting all services…"))
        : stateGlyph("■", groups.length > 1 ? "Stop all services" : `Stop ${groups[0].label}`, TONE.success, () => v.svc.run({ op: "down" }, "Stopping all services…"));
      out.push(css(el("span", { title: groups.map((g) => `${g.label}: ${g.state}`).join("\n") }, muted("svc"), glyph,
        toned(`${up}/${groups.length}`, groups.some((g) => g.failed) ? TONE.failure : up ? TONE.success : TONE.none)),
      "display:inline-flex;align-items:center;gap:4px;white-space:nowrap;font-size:12px;font-variant-numeric:tabular-nums"));
    }
    return out;
  }

  // A sub-session's checkout is a sub-row under its repository, with the same
  // columns: ± its uncommitted files, ↑ its commits the collection's branch lacks
  // (merged into the collection, never pushed to the forge), ↓ the collection's commits it
  // lacks. A sub-session has no pull request; ✕ discards it.
  const subFacts = (w) => {
    const dirty = w.repos.reduce((n, r) => n + (r.dirty ?? 0), 0);
    const unmerged = w.repos.reduce((n, r) => n + (r.toMerge ?? 0), 0);
    // Without the collection's status of a checkout, nobody knows what is merged.
    const unknown = w.repos.filter((r) => r.toMerge == null || r.mergeError).map(nameOf);
    const loss = [dirty ? plural(dirty, "uncommitted file") : "", unmerged ? `${plural(unmerged, "commit")} not merged` : ""].filter(Boolean).join(" and ");
    return [loss ? `It has ${loss}.` : "", unknown.length ? `Whether its commits in ${unknown.join(", ")} are merged is not known.` : "",
      !loss && !unknown.length ? "Everything in it is merged." : ""].filter(Boolean).join(" ");
  };
  // The uncommitted files, each against the collection's checkout.
  const filesTitle = (r) => {
    const by = { same: [], differs: [], only: [] };
    for (const f of r.uncommitted ?? []) (by[f.state] ?? by.differs).push(f.path);
    return [`${plural(r.dirty, "uncommitted file")}, compared with the collection's ${nameOf(r)}:`,
      by.differs.length ? `differs: ${by.differs.join(", ")}` : "", by.only.length ? `only here: ${by.only.join(", ")}` : "",
      by.same.length ? `the same in the collection: ${by.same.join(", ")}` : ""].filter(Boolean).join("\n");
  };
  function subRow(w, r) {
    const busyHere = reserved(r) || r.taskSessionKey && reserved(taskRepo(r));
    // ✕ ends the whole sub-session: not while any of its checkouts is busy.
    const discard = w.repos.some(reserved) ? null
      : armed.has(w.name)
        ? count("discard?", `Click again to discard sub-session ${w.name}. ${subFacts(w)}`, TONE.failure, () => discardSub(w))
        : smallButton("✕", `Discard sub-session ${w.name}${w.repos.length > 1 ? `, in all its ${w.repos.length} repositories` : ""}: deletes its sessions, and OpenClaw removes its worktrees. ${subFacts(w)}`, () => discardSub(w));
    const name = css(el("span", { title: `Sub-session ${w.name}: a session of this conversation that worked in a checkout of its own (${w.collection}). Its work merges into the collection; it never gets a pull request.` },
      muted("↳"), css(el("span", { textContent: w.name }), "font-weight:600"), discard), "display:inline-flex;align-items:center;gap:6px;padding-left:10px;min-width:0");
    const up = busyHere ? working()
      : r.mergeError ? toned("?", TONE.failure, r.mergeError)
      : r.toMerge > 0 ? count(`↑ ${r.toMerge}`, `${plural(r.toMerge, "commit")} on ${r.branch} not in the collection's ${nameOf(r)}. Merge them in (wtc catch-up --from ${r.branch}); never to the forge, never rebases`, TONE.pending, () => mergeSub(w, r))
      : null;
    // Uncommitted files keep a catch-up out (it would refuse): what to do
    // with them is the conversation's, so ± asks it to review them.
    const down = busyHere ? null
      : !(r.behind > 0) ? null
      : r.dirty ? toned(`↓ ${r.behind}`, TONE.pending, `${plural(r.behind, "commit")} of the collection's ${nameOf(r)} not on ${r.branch}. It has uncommitted files: review them first (±)`)
      : r.taskBranch ? count(`↓ ${r.behind}`, `${plural(r.behind, "commit")} of the collection's ${nameOf(r)} not on ${r.branch}. Catch the sub-session up with the collection; never rebases`, TONE.pending, () => catchUpSub(w, r))
      : toned(`↓ ${r.behind}`, TONE.pending, `${plural(r.behind, "commit")} of the collection's ${nameOf(r)} not on ${r.branch}`);
    const files = !r.dirty || busyHere ? null
      : count(String(r.dirty), `${filesTitle(r)}\n\nAsk the conversation to compare them with its own and take over what is still wanted`, TONE.pending, () => reviewSub(w, r));
    return [name, css(el("code", { textContent: r.branch ?? "", title: r.branch ?? "" }), "font-size:12px;color:var(--muted, #888)"),
      files, up, down, null, null, null, null];
  }

  function prTable(repos, narrow = false) {
    const open = pullRequestRows(repos);
    const columns = [
      { head: "PR", width: "28px" },
      { head: "Repo", width: "44px" },
      { head: "State", width: "44px" },
      { head: "C", width: "14px", title: "CI", align: "center" },
      { head: "M", width: "14px", title: "Merges with its base", align: "center" },
      { head: "R", width: "14px", title: "Review", align: "center" },
      { head: "Title", width: "minmax(0,1fr)" },
    ];
    return table(columns, open.map((r) => {
      const pr = r.pullRequest;
      const [, , , , , , ci, conflict, review] = repoRow(r);
      return [el("span", { title: `Pull request #${pr.number}` }, link(`#${pr.number}`, pr.url)), muted(nameOf(r), nameOf(r)), muted(pr.draft && pr.state === "open" ? "draft" : pr.state), ci, conflict, review,
        css(el("span", { textContent: pr.title ?? "", title: pr.title ?? "" }), "overflow:hidden;text-overflow:ellipsis")];
    }), narrow);
  }
  // Each repository's row, then its sub-sessions' sub-rows; a sub-session's checkout of
  // a repository the collection has none of comes last.
  function repoRows(repos, workers = []) {
    const sub = workers.flatMap((w) => w.repos.map((r) => ({ w, r })));
    const rows = [];
    for (const r of repos) {
      const row = repoRow(r); row.sessionKey = r.sessionKey; rows.push(row);
      for (const s of sub.filter((s) => s.r.repo && s.r.repo === r.repo)) rows.push(subRow(s.w, s.r));
    }
    for (const s of sub.filter((s) => !repos.some((r) => r.repo && r.repo === s.r.repo))) rows.push(subRow(s.w, s.r));
    return rows;
  }
  return { repositoryCompact, repositoryLayoutKey, table, checkAccount, notice, nextActions, actionToolbar, activityLog, publicationConfirmations, serviceLogs, clearServiceLogs, loadServiceLogs,
    dispose: () => { clearServiceLogs(); cancelPreparations("Preparation discarded when the view closed; nothing was pushed."); },
    repoTable: (repos, narrow, workers) => {
      syncConversation();
      // Retain observed columns so refresh/progress cannot make facts jump sideways.
      repoFacts.ahead ||= repos.some(r => r.dirty > 0 || toPush(r) > 0) || (workers ?? []).some(w => w.repos.some(r => r.toMerge > 0 || r.mergeError));
      repoFacts.behind ||= repos.some(r => toCatchUp(r)) || (workers ?? []).some(w => w.repos.some(r => r.behind > 0));
      repoFacts.pr ||= repos.some(r => r.pullRequest || otherPRs(r).length);
      actionColumns = Math.max(actionColumns, choosing ? 1 : 0, ...repos.map(r => repoActionOptions(r).filter(o => !o.disabled && o.label !== "Commit changes").length));
      const availableWidth = v.repoWidth?.() ?? v.width?.() ?? 0;
      narrow = repositoryCompact(availableWidth);
      inlineCapacity = Math.min(Math.max(1, actionColumns), narrow ? 1 : repoCapacity(availableWidth));
      const actionWidth = actionColumns ? inlineCapacity * 108 + 24 : 24;
      const columns = repositoryColumns(narrow, actionWidth, { width: factWidth(), pr: repoFacts.pr });
      const sharedGrid = !narrow && globalThis.CSS?.supports?.("grid-template-columns", "subgrid");
      // Without subgrid, a flexible branch track and fixed action width align
      // the independently sized header and rows from their shared right edge.
      if (!narrow && !sharedGrid) {
        columns[1].width = "minmax(64px,1fr)";
        columns.at(-1).width = `${actionWidth}px`;
      }
      const rows = repoRows(repos, workers).map((row) => {
        if (row[1]?.style) row[1].style.fontFamily = "inherit";
        const changes = css(el("span", { className: "wtc-change-slots" },
          css(el("span", {}, row[2]?.className?.includes("wtc-local-change") ? row[2] : row[2] ? el("span", {}, muted("± "), row[2]) : null), "min-width:0"),
          repoFacts.ahead ? css(el("span", {}, row[3]), "min-width:0") : null,
          repoFacts.behind ? css(el("span", {}, row[4]), "min-width:0") : null), `display:grid;grid-template-columns:${factTracks().map(x => `${x}px`).join(" ")};gap:${FACT_GAP}px;align-items:baseline;font-size:12px;font-variant-numeric:tabular-nums`);
        const currentPR = css(el("span", {}, row[5],
          row[6] || row[7] || row[8] ? css(el("span", {}, ...[row[6], row[7], row[8]].map((mark) => css(el("span", {}, mark), "display:inline-flex;justify-content:center;width:16px;flex:none"))), "display:inline-flex;align-items:baseline;gap:2px;flex:none") : null), "display:flex;gap:4px;align-items:baseline;min-width:0;overflow:hidden");
        const hint = row[10];
        if (hint) hint.style.cssText += ";max-width:80px;min-width:0;font-size:11px;padding:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap";
        const pr = css(el("span", { className: "wtc-repo-pr-slots" }, currentPR, hint ? css(el("span", { className: "wtc-repo-pr-history-slot" }, hint), "min-width:0;overflow:hidden") : null), `display:grid;grid-template-columns:${hint ? "minmax(0,1fr) 80px" : "minmax(0,1fr)"};gap:4px;align-items:baseline;white-space:nowrap;min-width:0;width:100%;flex:1 1 0`);
        const nameAndBranch = css(el("div", { className: "wtc-repo-identity" }, row[0], narrow ? row[1] : null), "display:flex;align-items:baseline;gap:8px;min-width:0;min-height:20px;white-space:nowrap");
        if (narrow && row[0]?.style) row[0].style.cssText += ";flex:0 1 auto;min-width:0";
        if (narrow && row[1]?.style) row[1].style.cssText += ";flex:1 1 0;min-width:32px;font-size:11px";
        const hasFacts = row.slice(2,9).some(Boolean) || !!row[10];
        const identity = css(el("div", {}, nameAndBranch,
          narrow && hasFacts ? css(el("div", { className: "wtc-repo-facts" }, factSlot(changes), row[5] || row[6] || row[7] || row[8] || row[10] ? factSlot(pr) : null), `display:grid;grid-template-columns:${availableWidth < REPO_IDENTITY_INSET + factWidth() + 84 ? "minmax(0,1fr)" : `${factWidth()}px minmax(0,1fr)`};width:calc(100% + ${actionWidth + REPO_COLUMN_GAP}px);padding-left:${REPO_IDENTITY_INSET}px;min-width:0;column-gap:4px;align-items:baseline;box-sizing:border-box`) : null), "min-width:0;padding:0;width:100%;flex:1 1 0");
        const rendered = narrow ? [identity, row[9]] : [row[0], row[1], changes, ...(repoFacts.pr ? [pr] : []), row[9]];
        rendered.sessionKey = row.sessionKey;
        return rendered;
      });
      const bulk = bulkActions(repos, narrow, columns), grid = table(columns, rows, false, true);
      if (sharedGrid) for (const child of [bulk, grid]) if (child) child.style.cssText += ";grid-column:1/-1;grid-template-columns:subgrid";
      previewRoot = css(el("div", { className: "wtc-repositories" }, bulk, grid), `min-width:0;--wtc-action-width:${actionWidth}px${sharedGrid ? `;display:grid;column-gap:${REPO_COLUMN_GAP}px;grid-template-columns:${columns.map(c => c.width).join(" ")}` : ""}`);
      return previewRoot;
    }, serviceTable, serviceControls, totals, prTable };
}

// The text of the latest message the person wrote, from chat.history.
// Heartbeat polls and wtc's pull request news arrive as user messages too,
// and are not theirs.
export function latestUserText(messages) {
  for (const m of [...(messages ?? [])].reverse()) {
    if (m?.role !== "user") continue;
    const text = typeof m.content === "string" ? m.content
      : Array.isArray(m.content) ? m.content.filter((c) => c?.type === "text").map((c) => c.text ?? "").join("\n") : "";
    if (!text.trim() || /^\[OpenClaw heartbeat poll\]|^wtc: pull request news/.test(text.trim())) continue;
    // What Start sent (marked "(wtc) ") was acted on: nothing to suggest
    // until the person writes again.
    if (/(^|\n)\(wtc\) /.test(text)) return "";
    return text.trim();
  }
  return "";
}
// The agent's main session (Home) routes work: it starts a conversation of
// its own for a task rather than becoming a collection itself.
export const isMainSession = (sessionKey) => /^agent:[^:]+:main$/.test(String(sessionKey ?? ""));
// What Start asks the conversation to do.
export const startMessage = (s, main = false) => main
  ? `Start a new conversation for this task in taba project ${s.project} with ${s.repos.join(", ")}: sessions_spawn with visible: true and a task that runs wtc add-repo ${s.repos.join(" ")} --project ${s.project} --worktree <a short name for this task> there, reads the project's AGENTS.md and adds what else the task needs. Send me its link.`
  : `Start this conversation's collection in taba project ${s.project} with ${s.repos.join(", ")}: wtc add-repo ${s.repos.join(" ")} --project ${s.project} --worktree <a short name for this task>. Then read the project's AGENTS.md and add what else the task needs.`;

function feedbackLabel(text, failed) {
  const name = String(text).match(/^([\w.-]+):/)?.[1];
  const prefix = name ? `${name}: ` : "";
  if (/Collection status refreshed/.test(text)) return "Refreshed";
  if (/cancelled/i.test(text)) return "Cancelled";
  if (failed) return "Failed" + (name ? ` · ${name}` : "");
  if (/confirm to push|confirm to publish/.test(text)) return prefix + "confirm push";
  if (/Catching up/.test(text)) return "Syncing…";
  if (/Preparing/.test(text)) return "Preparing…";
  if (/Pushing/.test(text)) return "Pushing…";
  if (/Asking/.test(text)) return "Sending…";
  if (/Starting|Stopping|Restarting/.test(text)) return String(text).match(/Starting|Stopping|Restarting/)[0] + "…";
  const pr = String(text).match(/(?:draft )?PR #(\d+)/)?.[1];
  if (pr && /created/.test(text)) return `PR #${pr} created` + (name ? ` · ${name}` : "");
  if (pr && /pushed/.test(text)) return `PR #${pr} updated` + (name ? ` · ${name}` : "");
  if (/caught up|up to date|returned to tip/i.test(text)) return "Caught up" + (name ? ` · ${name}` : "");
  if (/pushed/.test(text)) return prefix + "pushed";
  if (/asked the conversation|Sent to/.test(text)) return prefix + "sent to agent";
  return prefix + "action details";
}

function mountComposer(container, context) {
  let ctx = context;
  let disposed = false;
  let watch = null;
  let key = null;
  let entry = null;
  let note = "";
  let renderedStamp;

  const card = css(el("details", { className: "wtc-collection-card", open: remembered() }),
    "container-type:inline-size;container-name:wtc-card;box-sizing:border-box;width:calc(100% - var(--chat-composer-side-inset, 36px));max-width:var(--chat-thread-max-width, 48rem);margin:4px auto 0;background:var(--card, #fff);color:var(--text, inherit);border:1px solid var(--border, rgba(0,0,0,.08));border-radius:14px;font:inherit;font-size:14px");
  const extra = css(el("section", { className: "wtc-transient-actions", ariaLabel: "Collection action details" }),
    "box-sizing:border-box;width:calc(100% - var(--chat-composer-side-inset,36px));max-width:var(--chat-thread-max-width,48rem);margin:4px auto 0;padding:10px 12px;display:grid;gap:6px;max-height:min(40dvh,360px);overflow:auto;background:var(--card,#fff);color:var(--text,inherit);border:1px solid var(--border,rgba(0,0,0,.08));border-bottom:0;border-radius:14px 14px 0 0;font:inherit;font-size:14px");
  const summary = css(el("summary"), "list-style:none;display:flex;align-items:baseline;height:calc(var(--wtc-icon-size,24px) + 8px + var(--wtc-header-top-extra,6px));box-sizing:border-box;gap:6px;padding:calc(3px + var(--wtc-header-top-extra,6px)) 12px 3px;cursor:pointer;white-space:nowrap;overflow:hidden");
  const detail = css(el("div"), "padding:2px 12px 8px;display:grid;gap:6px");
  card.append(summary, detail, actionFeedback());
  card.addEventListener("toggle", () => remember(card.open));
  const composer = el("div");
  container.append(composer);
  const unmountDefault = ctx.mountDefault(composer);

  const iconButton = (text, title, run) => {
    const b = css(el("button", { type: "button", title, ariaLabel: title }, controlIcon(text)),
      "display:inline-flex;align-items:center;justify-content:center;border:0;background:transparent;color:var(--muted, #888);cursor:pointer;font:inherit;font-size:14px;line-height:20px;min-width:var(--wtc-icon-size,24px);min-height:var(--wtc-icon-size,24px);padding:0 3px;border-radius:4px");
    if (text === "↻") { b.className = "wtc-refresh-control"; b.setAttribute("aria-busy", String(!!entry?.inflight)); }
    b.onclick = (e) => { e.preventDefault(); e.stopPropagation(); return run(); };
    return b;
  };
  let noteFailed = false;
  let noteTimer;
  const say = (text, failed = false, action = null, activityId = null) => {
    clearTimeout(noteTimer);
    note = text; noteFailed = failed;
    if (text) feedbackStore.set(entry?.data?.collection?.sessionKey ?? key, { text, failed, action, activityId, at: Date.now() });
    if (entry) render(entry);
    // Errors, follow-ups and notes containing known progress words stay.
    // Row busy state and recorded outcomes remain available.
    if (text && !failed && !action && !/Preparing|Pushing|Catching up|Starting|Stopping|Restarting|Asking/.test(text)) noteTimer = setTimeout(() => {
      note = ""; if (!disposed && entry) render(entry);
    }, 8000);
  };
  let narrow = false, repoNarrow = false, contentWidth = 0;
  const sized = typeof ResizeObserver === "undefined" ? null : new ResizeObserver(([e]) => {
    const now = e.contentRect.width > 0 && e.contentRect.width < NARROW_PX;
    const compactRepos = tables.repositoryCompact(Math.max(0,e.contentRect.width - 24));
    const changed = tables.repositoryLayoutKey(Math.max(0,contentWidth - 24)) !== tables.repositoryLayoutKey(Math.max(0,e.contentRect.width - 24)) || headerActionCapacity(contentWidth) !== headerActionCapacity(e.contentRect.width);
    contentWidth = e.contentRect.width;
    if (now !== narrow || compactRepos !== repoNarrow || changed) { narrow = now; repoNarrow = compactRepos; if (entry) render(entry); }
  });
  sized?.observe(card);
  const svcBusy = new Set();
  const svc = {
    busy: (k) => svcBusy.has(k),
    ask: (text) => askConversation(ctx.host, key, text),
    async run(payload, doing, k = payload.group ?? "*") {
      const conversation = key;
      const report = (...args) => { if (!disposed && key === conversation) say(...args); };
      svcBusy.add(k);
      report(doing);
      try {
        const r = await trackedServiceRequest(ctx.host, conversation, agentOf(ctx), payload, doing, report, entry?.data?.collection?.sessionKey ?? conversation);
        return r;
      } catch (err) {
        report(String(err?.message ?? err), true);
      } finally {
        svcBusy.delete(k);
        if (!disposed && key === conversation) await watch?.refresh();
        if (entry) render(entry);
      }
    },
  };
  const tables = collectionTables({
    collectionKey: () => entry?.data?.collection?.sessionKey ?? key,
    openServiceLogs: (group, label, targets) => {
      serviceLogIntents.set(entry?.data?.collection?.sessionKey ?? key, { group, label, targets });
      if (serviceLogIntents.size > 128) serviceLogIntents.delete(serviceLogIntents.keys().next().value);
      ctx.host.ui.openPanel("collection", { sessionKey: key, agentId: agentOf(ctx) });
      for (const notify of serviceLogListeners) notify();
    },
    width: () => contentWidth, repoWidth: () => Math.max(0,contentWidth - 24),
    publicationBesideCollapsedBar: true,
    get host() { return ctx.host; }, alive: () => !disposed && !ctx.signal?.aborted, key: () => key, agent: () => agentOf(ctx), svc, say,
    // What a push needs (the how-to of connecting GitHub) shows open.
    reveal: () => { if (!card.open) { card.open = true; remember(true); } },
    refresh: async () => { const source = watch; await source?.refresh(); if (!disposed && watch === source && entry) render(entry); }, pause: () => pauseCollection(watch), rerender: () => entry && render(entry),
  });

  // A conversation without a collection: where the draft being typed, or
  // the person's latest message, asks to work, suggested by the gateway (its
  // decision model, OpenAI's asked directly, or the words) as a card to
  // start the collection from. The draft is asked about once typing pauses;
  // the latest message on each collection refresh, not more than every ten
  // seconds. Dismissed until the card is mounted again.
  let suggestion = null;
  let askedAt = 0;
  let draftTimer = null;
  let lastDraft = "";
  const dismissed = new Set();
  async function ask(sessionKey, text, fromDraft) {
    if (!text || dismissed.has(sessionKey) || (suggestion?.key === sessionKey && suggestion.text === text)) return;
    try {
      // A draft is matched by its words on the gateway; only a sent message
      // may go to a decision model.
      const res = await ctx.host.request("plugins.sessionAction", { pluginId: PLUGIN_ID, actionId: "suggest", sessionKey, ...(agentOf(ctx) ? { agentId: agentOf(ctx) } : {}), payload: { text, ...(fromDraft ? { draft: true } : {}) } });
      if (sessionKey !== key) return;
      suggestion = { key: sessionKey, text, fromDraft, result: res?.ok === false ? null : res?.result ?? null };
      if (entry) render(entry);
    } catch { /* no suggestion: the conversation still can */ }
  }
  async function suggestFor(sessionKey) {
    if (dismissed.has(sessionKey) || Date.now() - askedAt < 10000 || suggestion?.fromDraft) return;
    askedAt = Date.now();
    try {
      const history = await ctx.host.request("chat.history", { sessionKey, limit: 20 });
      await ask(sessionKey, latestUserText(history?.messages), false);
    } catch { /* as above */ }
  }
  // The draft, once typing pauses for a moment, while there is no collection.
  function draftChanged() {
    const draft = String(ctx.props?.draft ?? "").trim();
    if (draft === lastDraft) return;
    lastDraft = draft;
    clearTimeout(draftTimer);
    if (draft.length < 8) {
      if (suggestion?.fromDraft) { suggestion = null; if (entry) render(entry); }
      return;
    }
    if ((entry?.data?.repos ?? []).length) return;
    draftTimer = setTimeout(() => void ask(key, draft, true), 800);
  }
  const pill = (text, title, run, strong) => {
    const b = css(el("button", { type: "button", textContent: text, title }),
      `border:1px solid var(--border, rgba(128,128,128,.3));background:${strong ? "var(--accent, #2563eb)" : "transparent"};color:${strong ? "var(--accent-foreground, #fff)" : "inherit"};cursor:pointer;font:inherit;font-size:12px;padding:2px 10px;border-radius:999px`);
    if (text === "↻") { b.className = "wtc-refresh-control"; b.setAttribute("aria-busy", String(!!entry?.inflight)); }
    b.onclick = (e) => { e.preventDefault(); e.stopPropagation(); return run(); };
    return b;
  };
  function renderSuggestion(s) {
    extra.remove();
    summary.style.display = "flex";
    const main = isMainSession(key);
    const what = css(el("span", {}, main ? "Start a conversation in taba project " : "Work in taba project ", el("strong", { textContent: s.project }), ` with ${s.repos.join(", ")}?`),
      "flex:1;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap");
    what.title = `${s.source === "decision" ? "The decision model's" : "The words'"} suggestion for: ${suggestion.text}`;
    summary.replaceChildren(
      brand("14px", entry?.data?.version),
      what,
      css(el("span", {}, note ? noteNode(note, noteFailed) : null,
        pill("Start", main ? "Ask the agent to start a conversation for this task in that taba project" : "Ask the conversation to start this collection", () => {
          const fromDraft = suggestion?.fromDraft === true;
          dismissed.add(key);
          // Home never becomes a collection: once Start went out, it can be
          // suggested to again.
          const done = () => { if (main) { dismissed.delete(key); suggestion = null; } };
          note = "";
          card.remove();
          const draft = String(ctx.props?.draft ?? "").trim();
          const fail = (err) => {
            dismissed.delete(key);
            // The draft as the person wrote it, without what Start added.
            if (draft) { try { ctx.props?.setDraft?.(draft); } catch {} }
            say(String(err?.message ?? err), true);
          };
          // A draft goes along with what Start asks, through the composer's own send.
          if (fromDraft && draft && ctx.props?.setDraft && ctx.props?.send) {
            try {
              ctx.props.setDraft(`${draft}\n\n(wtc) ${startMessage(s, main)}`);
              void Promise.resolve(ctx.props.send()).then((sent) => { if (sent === false) fail(new Error("The message was not sent.")); else done(); }, fail);
            } catch (err) { fail(err); }
          } else {
            void askConversation(ctx.host, key, `(wtc) ${startMessage(s, main)}`).then(done, fail);
          }
        }, true),
        pill("Change…", "Pick another taba project or repositories in the Collection panel", () => ctx.host.ui.openPanel("collection", { sessionKey: key, agentId: agentOf(ctx) })),
        iconButton("✕", "Not now", () => { dismissed.add(key); card.remove(); })),
      "flex:none;margin-left:auto;display:inline-flex;align-items:center;gap:6px;white-space:nowrap"),
    );
    detail.replaceChildren();
    if (card.parentNode !== container) container.prepend(card);
  }

  function render(next) {
    entry = next;
    renderedStamp = collectionStamp(next, key);
    const repos = entry?.data?.repos ?? [];
    if (!ctx.presented || ctx.signal.aborted) {
      card.remove(); extra.remove();
      return;
    }
    // Only a collection that loaded and has nothing yet: a failed load is
    // no reason to start another one.
    if (repos.length === 0 && !entry?.data?.subSessions?.length) {
      extra.remove();
      if (entry?.data) void suggestFor(key);
      if (entry?.data && suggestion?.key === key && suggestion.result && !dismissed.has(key)) renderSuggestion(suggestion.result);
      else card.remove();
      return;
    }
    const groups = serviceGroups(entry.data.services ?? []);
    summary.style.display = "flex";
    summary.style.columnGap = "8px";
    const facts = css(el("span", { className: "wtc-summary-facts" }, ...(card.open || note ? [] : tables.totals(repos, groups))), "flex:1;min-width:0;height:var(--wtc-control-height,20px);line-height:var(--wtc-control-height,20px);overflow:hidden;display:flex;flex-wrap:wrap;align-items:baseline;align-content:flex-start;column-gap:8px;row-gap:8px");
    const headerNote = entry.error ? `Refresh failed: ${entry.error}` : note;
    const headerFailed = !!entry.error || noteFailed;
    const latestFeedback = feedbackStore.get(entry.data.collection?.sessionKey ?? key);
    const latestActivity = activityStore.get(entry.data.collection?.sessionKey ?? key)?.get(latestFeedback?.activityId);
    const publicationCount = [...(activityStore.get(entry.data.collection?.sessionKey ?? key)?.values() ?? [])].filter(item => item.action === "push" && item.inFlight).length;
    const publicationContext = publicationCount > 1 ? `${publicationCount} repos` : latestActivity?.repo;
    const headerLabel = !headerFailed && latestFeedback?.text === headerNote && latestActivity?.action === "push" && latestActivity.inFlight ? publicationContext : entry.error ? "Refresh failed" : feedbackLabel(headerNote, headerFailed);
    const feedback = headerNote ? css(el("button", { type: "button", className: "wtc-header-feedback wtc-action-control", role: headerFailed ? "alert" : "status", ariaLive: "polite", textContent: `${headerFailed ? "⚠ " : ""}${headerLabel}`, title: headerNote + " — Open full details", ariaLabel: headerNote + ". Open full details in the sidebar" }), `border:0;background:transparent;padding:0;text-align:left;font:inherit;font-size:12px;color:${headerFailed ? TONE.failure : "var(--muted,#888)"};min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;cursor:pointer;flex:1`) : null;
    if (feedback) feedback.onclick = (e) => { e.preventDefault(); e.stopPropagation(); ctx.host.ui.openPanel("collection", { sessionKey: key, agentId: agentOf(ctx), focus: "activity" }); };
    summary.replaceChildren(
      css(el("span", {}, brand("12px", entry.data.version, repoNarrow)), "flex:none;display:inline-flex;align-items:baseline"),
      feedback ?? facts,
      css(el("span", { className: "wtc-header-controls" }, tables.actionToolbar(repos, true, { failed: headerFailed, text: headerNote }),
        iconButton("↻", refreshTitle(entry), () => trackedRefresh(watch, entry?.data?.collection?.sessionKey ?? key, say, () => entry?.data?.collection?.sessionKey ?? key, () => !disposed)),
        iconButton("⤢", "Open the taba Collection panel", () => ctx.host.ui.openPanel("collection", { sessionKey: key, agentId: agentOf(ctx) })),
        css(el("span", { ariaHidden: "true" }, controlIcon(card.open ? "⌃" : "⌄")), "display:inline-flex;align-items:center;justify-content:center;color:var(--muted,#888);padding:0 2px")), "margin-left:auto;flex:none;display:flex;align-items:baseline;justify-content:flex-end;gap:4px;white-space:nowrap"));
    tables.checkAccount(repos);
    detail.replaceChildren(...[tables.repoTable(repos, repoNarrow, entry.data.subSessions),
      groups.length ? css(el("section", { className: "wtc-base-services", ariaLabel: "Collection services" }, css(el("div", {}, css(el("h3", { textContent: "Services" }), "margin:0;font-size:12px;line-height:16px;font-weight:600;color:var(--text-strong,inherit)"), tables.serviceControls(groups)), "display:flex;align-items:baseline;gap:10px;margin-bottom:4px"), tables.serviceTable(groups, entry.data, narrow)), "min-width:0;border-top:1px solid var(--border,rgba(128,128,128,.2));margin-top:4px;padding-top:8px") : null].filter(Boolean));
    const confirmations = tables.publicationConfirmations(repos,true);
    const temporary = [confirmations, tables.nextActions(repos), tables.notice()].filter(Boolean);
    extra.replaceChildren(...temporary);
    extra.style.maxHeight = confirmations ? "none" : "min(40dvh,360px)";
    extra.style.overflow = confirmations ? "visible" : "auto";
    const showTemporary = temporary.length > 0 && (card.open || !!confirmations);
    card.style.marginTop = showTemporary ? "0" : "4px";
    card.style.borderTopLeftRadius = card.style.borderTopRightRadius = showTemporary ? "0" : "14px";
    if (card.parentNode !== container) container.prepend(card);
    if (showTemporary) { if (extra.parentNode !== container) container.prepend(extra); } else extra.remove();
  }
  card.addEventListener("toggle", () => entry && render(entry));

  const keyOf = (c) => c.props?.sessionKey ?? c.props?.session?.key;
  const agentOf = (c) => c.props?.agentId ?? c.props?.session?.agentId;
  function follow(next) {
    const sessionKey = keyOf(next);
    if (sessionKey === key) return;
    serviceLogIntents.delete(entry?.data?.collection?.sessionKey ?? key);
    tables.clearServiceLogs();
    watch?.dispose();
    watch = null;
    key = sessionKey;
    entry = null;
    clearTimeout(noteTimer);
    note = "";
    noteFailed = false;
    card.remove(); extra.remove();
    renderedStamp = undefined;
    if (sessionKey) watch = watchCollection(next.host, sessionKey, agentOf(next), show);
  }
  function show(next) {
    const changed = renderedStamp !== collectionStamp(next, key);
    entry = next;
    if (!ctx.presented || ctx.signal.aborted) { card.remove(); extra.remove(); return; }
    if (changed) render(next);
    else {
      syncTimeLabels(card, next, { composer: true }); syncTimeLabels(extra, next);
      tables.checkAccount(next.data?.repos ?? []);
      if (next.data && !next.data.repos?.length && !next.data.subSessions?.length) void suggestFor(key);
    }
  }
  follow(ctx);
  const tick = setInterval(() => { if (entry && card.isConnected) { syncTimeLabels(card, entry, { composer: true }); syncTimeLabels(extra, entry); tables.checkAccount(entry.data?.repos ?? []); } }, 15000);
  return {
    update(next) {
      const visibilityChanged = next.presented !== ctx.presented || next.signal?.aborted !== ctx.signal?.aborted;
      ctx = next;
      follow(next);
      draftChanged();
      if (entry) visibilityChanged ? render(entry) : show(entry);
    },
    dispose() {
      disposed = true;
      tables.dispose();
      clearInterval(tick);
      clearTimeout(draftTimer);
      clearTimeout(noteTimer);
      sized?.disconnect();
      watch?.dispose();
      card.remove(); extra.remove();
      unmountDefault?.();
    },
  };
}

// The Collection panel beside a conversation: the whole collection at once —
// a card per repository with its branch, what is not published yet and its
// pull request, the services, and adding a repository.
function mountCollection(container, context) {
  let ctx = context;
  let disposed = false;
  let entry = null;
  let watch = null;

  const root = css(el("section", { className: "wtc-collection" }),
    "container-type:inline-size;container-name:wtc-panel;position:relative;width:100%;min-width:0;padding:14px 16px;font:inherit;font-size:14px;color:var(--text, inherit);overflow:auto;height:100%;box-sizing:border-box;display:grid;gap:14px;align-content:start");
  container.append(root);

  const muted = (text, extra = "") => css(el("span", { textContent: text }), `color:var(--muted, #888);font-size:12px;${extra}`);
  const mono = (text, title) => css(el("code", { textContent: text, title: title ?? text }),
    "font-family:var(--mono, ui-monospace, monospace);font-size:12px;color:var(--muted, #888);overflow:hidden;text-overflow:ellipsis;white-space:nowrap;min-width:0");
  const heading = sectionHeading;
  const row = (...kids) => css(el("div", {}, ...kids), "display:flex;align-items:baseline;gap:8px;min-width:0;flex-wrap:wrap");
  const button = (text, title, run, primary) => {
    const b = css(el("button", { textContent: text, type: "button", title }),
      `border:1px solid ${primary ? "transparent" : "var(--border-strong, rgba(128,128,128,.4))"};background:${primary ? "var(--accent, #bd4531)" : "transparent"};color:${primary ? "var(--accent-foreground, #fff)" : "inherit"};` +
      "font:inherit;font-size:12px;line-height:20px;padding:1px 10px;border-radius:var(--radius-full, 999px);cursor:pointer;white-space:nowrap");
    b.onclick = async () => {
      b.disabled = true;
      b.style.opacity = ".6";
      try { await run(); } catch (err) { say(String(err?.message ?? err), true); } finally { b.disabled = false; b.style.opacity = ""; }
    };
    return b;
  };
  const iconButton = (text, title, run) => {
    const b = css(el("button", { type: "button", title, ariaLabel: title }, controlIcon(text)),
      "display:inline-flex;align-items:center;justify-content:center;min-height:24px;border:0;background:transparent;color:var(--muted, #888);font:inherit;font-size:15px;padding:2px 6px;border-radius:8px;cursor:pointer");
    if (text === "↻") { b.className = "wtc-refresh-control"; b.setAttribute("aria-busy", String(!!entry?.inflight)); }
    b.onclick = run;
    return b;
  };

  const header = css(el("header"), "display:grid;gap:6px;min-width:0");
  const log = css(el("div"), "font-size:12px;color:var(--muted, #888);min-height:0");
  const body = css(el("div"), "display:grid;gap:16px");
  root.append(actionFeedback(), header, body);
  let note = "";
  let noteFailed = false;
  let noteAction = null;
  const say = (text, failed = false, action = null, activityId = null) => {
    note = text;
    noteFailed = failed;
    noteAction = action;
    if (text) feedbackStore.set(entry?.data?.collection?.sessionKey ?? ctx.props.sessionKey, { text, failed, action, activityId, at: Date.now() });
    log.replaceChildren(...[noteNode(text, failed, "white-space:normal;overflow-wrap:anywhere"), resultAction(action, say)].filter(Boolean));
    log.setAttribute("role", failed ? "alert" : "status");
    log.setAttribute("aria-live", "polite");
    log.style.color = failed ? TONE.failure : "var(--muted, #888)";
    log.style.minHeight = text ? "1em" : "0";
    if (entry?.data) render();
  };

  const sessionKeyOf = () => entry?.data?.collection?.sessionKey ?? ctx.props.sessionKey;
  const svcBusy = new Set();
  const svc = {
    busy: (k) => svcBusy.has(k),
    ask: (text) => askConversation(ctx.host, sessionKeyOf(), text),
    async run(payload, doing, k = payload.group ?? "*") {
      const conversation = sessionKeyOf();
      const report = (...args) => { if (!disposed && sessionKeyOf() === conversation) say(...args); };
      svcBusy.add(k);
      report(doing);
      render();
      try {
        const r = await trackedServiceRequest(ctx.host, conversation, ctx.props.agentId, payload, doing, report);
        return r;
      } catch (err) {
        report(String(err?.message ?? err), true);
      } finally {
        svcBusy.delete(k);
        if (!disposed && sessionKeyOf() === conversation) await watch?.refresh();
        render();
      }
    },
  };

  const tables = collectionTables({
    width: () => contentWidth,
    collectionKey: sessionKeyOf,
    showServiceLogs: () => { focusSection = "service-logs"; },
    showActions: () => { focusSection = "actions"; render(); },
    get host() { return ctx.host; }, alive: () => !disposed && !ctx.signal?.aborted, key: sessionKeyOf, agent: () => ctx.props.agentId, svc, say,
    refresh: async () => { const source = watch; await source?.refresh(); if (!disposed && watch === source && entry) render(); }, pause: () => pauseCollection(watch), rerender: () => render(),
  });
  let narrow = false, repoNarrow = false, contentWidth = 0;
  const sized = typeof ResizeObserver === "undefined" ? null : new ResizeObserver(([e]) => {
    const now = e.contentRect.width > 0 && e.contentRect.width < NARROW_PX;
    const compactRepos = tables.repositoryCompact(e.contentRect.width);
    const changed = tables.repositoryLayoutKey(contentWidth) !== tables.repositoryLayoutKey(e.contentRect.width) || headerActionCapacity(contentWidth) !== headerActionCapacity(e.contentRect.width);
    contentWidth = e.contentRect.width;
    if (now !== narrow || compactRepos !== repoNarrow || changed) { narrow = now; repoNarrow = compactRepos; render(); }
  });
  sized?.observe(root);

  // Adding a repository: what is typed is a GitHub owner/name, a GitHub or
  // Bitbucket URL, or a search. The search offers the projects the gateway
  // has, GitHub repositories (the gateway's own search, with the person's
  // GitHub account when they connected one) and, with a Bitbucket login, the
  // person's Bitbucket repositories. The repository is put on the gateway
  // first, then the collection's session is asked to take it in.
  const GITHUB = /^(?:https:\/\/github\.com\/|git@github\.com:)?([A-Za-z0-9][A-Za-z0-9-]*)\/([A-Za-z0-9._-]+?)(?:\.git)?\/?$/;
  const BITBUCKET = /^(?:https:\/\/(?:[^@/]+@)?|git@)?bitbucket\.org[/:]([A-Za-z0-9_.-]+)\/([A-Za-z0-9_.-]+?)(?:\.git)?\/?$/;
  const sourceOf = (spec) => {
    const text = spec.trim();
    let m = text.match(BITBUCKET);
    if (m) return { forge: "bitbucket", fullName: `${m[1]}/${m[2]}`, name: m[2], cloneUrl: `https://bitbucket.org/${m[1]}/${m[2]}.git` };
    m = text.match(GITHUB);
    if (m) return { forge: "github", fullName: `${m[1]}/${m[2]}`, name: m[2], cloneUrl: `https://github.com/${m[1]}/${m[2]}.git` };
    return null;
  };

  async function addRepository(source, branch) {
    const sessionKey = entry?.data?.collection?.sessionKey ?? ctx.props.sessionKey;
    let ask = source.forge === "project" ? source.name : source.cloneUrl.replace(/\.git$/, "");
    if (source.forge === "github") {
      say(`Adding ${source.fullName} to the gateway…`);
      await ctx.host.request("projects.add", { gitUrl: source.cloneUrl }, { timeoutMs: null });
    } else if (source.forge === "bitbucket") {
      say(`Cloning ${source.fullName} onto the gateway…`);
      const res = await ctx.host.request("plugins.sessionAction", { pluginId: PLUGIN_ID, actionId: "clone-repo", sessionKey, agentId: ctx.props.agentId, payload: { url: source.cloneUrl } }, { timeoutMs: null });
      if (res?.ok === false) throw new Error(res.error ?? "clone failed");
      const cloned = res?.result ?? res;
      try {
        await ctx.host.request("projects.register", { path: cloned.path, name: cloned.name });
      } catch (err) {
        // Already registered is fine; anything else is the person's to see.
        if (!/exist|already/i.test(String(err?.message ?? err))) throw err;
      }
    }
    const message = branch
      ? `Add ${ask} to this collection, on branch ${branch}: wtc add-repo ${ask} --base ${branch}`
      : `Add ${ask} to this collection.`;
    await ctx.host.request("sessions.send", { key: sessionKey, message });
    say(`${source.fullName ?? source.name} is on the gateway; the collection's session is adding it.`);
  }

  // Before anything is cloned or added: which repository this is, with a
  // link to check it, and the branch the collection works against.
  function confirmAdd(source) {
    const overlay = css(el("div"), "position:absolute;inset:0;background:color-mix(in srgb, var(--bg, #000) 55%, transparent);display:flex;align-items:flex-start;justify-content:center;padding:48px 16px;z-index:10");
    const dialog = css(el("div", { role: "dialog", ariaModal: "true", ariaLabel: "Add repository" }),
      "width:100%;max-width:420px;background:var(--card, #fff);color:var(--text, inherit);border:1px solid var(--border, rgba(128,128,128,.3));border-radius:var(--radius-lg, 14px);padding:16px;display:grid;gap:12px;box-shadow:0 12px 40px rgba(0,0,0,.25)");
    const close = () => { overlay.remove(); document.removeEventListener("keydown", onKey); };
    const onKey = (e) => { if (e.key === "Escape") close(); };
    document.addEventListener("keydown", onKey);
    overlay.onmousedown = (e) => { if (e.target === overlay) close(); };

    const [owner, name] = String(source.fullName ?? source.name).includes("/") ? String(source.fullName).split("/") : [null, source.name];
    const href = source.webUrl ?? (source.forge === "github" ? `https://github.com/${source.fullName}` : source.forge === "bitbucket" ? `https://bitbucket.org/${source.fullName}` : null);
    const where = { github: "GitHub", bitbucket: "Bitbucket", project: "already on the gateway" }[source.forge];
    const select = css(el("select", { disabled: true }, el("option", { textContent: "Reading branches…" })),
      "font:inherit;font-size:13px;color:inherit;background:var(--bg-elevated, transparent);border:1px solid var(--border, rgba(128,128,128,.3));border-radius:8px;padding:4px 8px");
    const status = muted("");
    let defaultBranch = null;
    const add = button("Add", "Put the repository on the gateway and add it to this collection", async () => {
      const branch = select.disabled ? null : select.value;
      close();
      await addRepository(source, branch && branch !== defaultBranch ? branch : null);
      await watch?.refresh();
    }, true);
    add.disabled = true;
    add.style.opacity = ".6";
    dialog.append(
      css(el("div", { textContent: "Add repository" }), "font-weight:600;font-size:15px;color:var(--text-strong, inherit)"),
      css(el("div", {},
        repoTitle({ forge: source.forge === "project" ? null : source.forge, owner, repo: name }),
        css(el("div", {}, muted([where, source.private === true ? "private" : source.private === false ? "public" : ""].filter(Boolean).join(" · ")),
          href ? css(el("span", {}, link("Open to check ↗", href)), "font-size:12px") : null), "display:flex;gap:10px;align-items:center;flex-wrap:wrap"),
        source.description ? css(el("div", { textContent: source.description }), "font-size:13px") : null),
      "display:grid;gap:4px"),
      css(el("label", {}, css(el("span", { textContent: "Default branch" }), "font-size:12px;font-weight:600;color:var(--muted, #888)"), select,
        muted("The collection's worktree starts from it, and is compared with, caught up from and published to it.")),
      "display:grid;gap:4px"),
      status,
      css(el("div", {}, button("Cancel", "", async () => close()), add), "display:flex;gap:8px;justify-content:flex-end"),
    );
    overlay.append(dialog);
    root.append(overlay);

    const payload = source.forge === "project" ? { project: source.name } : { url: source.cloneUrl };
    ctx.host.request("plugins.sessionAction", { pluginId: PLUGIN_ID, actionId: "repo-branches", sessionKey: sessionKeyOf(), agentId: ctx.props.agentId, payload })
      .then((res) => {
        if (res?.ok === false) throw new Error(res.error ?? "cannot read the branches");
        const { defaultBranch: head, branches } = res?.result ?? res;
        defaultBranch = head ?? source.defaultBranch ?? null;
        // The gateway's own working branches are not a base to choose.
        const sorted = [...new Set([defaultBranch, ...(branches ?? []).filter((b) => !b.startsWith("openclaw/"))].filter(Boolean))];
        select.replaceChildren(...sorted.map((b) => el("option", { value: b, textContent: b === defaultBranch ? `${b} (the repository's default)` : b, selected: b === defaultBranch })));
        select.disabled = sorted.length === 0;
        status.textContent = `${sorted.length} ${sorted.length === 1 ? "branch" : "branches"}`;
      })
      .catch((err) => {
        select.replaceChildren(el("option", { textContent: "the repository's default" }));
        status.textContent = `${String(err?.message ?? err)} The repository's default branch is used.`;
      })
      .finally(() => {
        const already = (entry?.data?.repos ?? []).some((r) => String(r.repo ?? r.project).toLowerCase() === String(name).toLowerCase()
          && (!owner || !r.owner || String(r.owner).toLowerCase() === String(owner).toLowerCase()));
        if (already) status.textContent = "Already in this collection.";
        else { add.disabled = false; add.style.opacity = ""; }
      });
  }

  const adder = (() => {
    const input = css(el("input", { placeholder: "Find or add a repository", type: "text" }),
      "flex:1;min-width:0;font:inherit;font-size:13px;color:inherit;background:var(--bg-elevated, transparent);border:1px solid var(--border, rgba(128,128,128,.3));border-radius:var(--radius-full, 999px);padding:4px 12px;outline:none");
    const results = css(el("div"), "display:none;border:1px solid var(--border, rgba(128,128,128,.25));border-radius:var(--radius-lg, 14px);background:var(--card, transparent);overflow:hidden");
    let options = [];
    let active = 0;
    let seq = 0;
    let timer = null;
    let projects = null;

    const choose = (source) => {
      results.style.display = "none";
      input.value = "";
      confirmAdd(source);
    };
    const forgeLabel = { github: "GitHub", bitbucket: "Bitbucket", project: "On the gateway" };
    function show(list, footer) {
      options = list;
      active = 0;
      const rows = list.map((o, i) => {
        const r = css(el("div", { title: o.description ?? "" },
          o.forge === "project" ? css(el("span", { textContent: "on gateway", title: forgeLabel.project }), "flex:none;font-size:11px;color:var(--muted, #888)") : forgeIcon(o.forge),
          css(el("span", { textContent: o.fullName ?? o.name }), "font-weight:600;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;min-width:0"),
          o.private ? muted("private") : null,
          o.description ? muted(o.description, "overflow:hidden;text-overflow:ellipsis;white-space:nowrap;min-width:0;flex:1") : null),
        `display:flex;align-items:center;gap:8px;padding:6px 12px;cursor:pointer;${i === active ? "background:var(--bg-hover, rgba(128,128,128,.12))" : ""}`);
        r.onmousedown = (e) => { e.preventDefault(); choose(o); };
        return r;
      });
      if (footer) rows.push(css(el("div", {}, muted(footer)), "padding:6px 12px"));
      results.replaceChildren(...rows);
      results.style.display = rows.length ? "block" : "none";
    }
    async function search(text) {
      const mine = ++seq;
      const direct = sourceOf(text);
      const q = text.trim().toLowerCase();
      if (q.length < 2) return show(direct ? [direct] : []);
      const sessionKey = entry?.data?.collection?.sessionKey ?? ctx.props.sessionKey;
      projects ??= ctx.host.request("projects.list", {}).then((r) => r?.projects ?? []).catch(() => []);
      const [onGateway, github, bitbucket] = await Promise.all([
        projects.then((all) => all.filter((p) => p.source !== "workspace" && String(p.displayName ?? p.id).toLowerCase().includes(q))
          .map((p) => ({ forge: "project", name: p.id, fullName: p.displayName ?? p.id }))),
        direct ? Promise.resolve(null) : ctx.host.request("projects.searchRemote", { query: text.trim() }).catch(() => null),
        ctx.host.request("plugins.sessionAction", { pluginId: PLUGIN_ID, actionId: "repo-search", sessionKey, agentId: ctx.props.agentId, payload: { query: text.trim() } })
          .then((r) => r?.result ?? r).catch(() => null),
      ]);
      if (mine !== seq) return;
      const have = new Set((entry?.data?.repos ?? []).map((r) => String(r.repo ?? r.project).toLowerCase()));
      const list = [
        ...(direct ? [direct] : []),
        ...onGateway,
        ...(bitbucket?.repos ?? []),
        ...(github?.projects ?? []).map((p) => ({ forge: "github", name: p.name, fullName: p.fullName, cloneUrl: p.cloneUrl, webUrl: p.webUrl, private: p.private, description: p.description, defaultBranch: p.defaultBranch })),
      ].filter((o, i, all) => !have.has(String(o.name).toLowerCase()) && all.findIndex((x) => x.forge === o.forge && x.fullName === o.fullName) === i).slice(0, 10);
      const notes = [];
      if (github?.credential === "missing") notes.push("GitHub: public repositories; connect your GitHub account to search yours");
      if (bitbucket && !bitbucket.bitbucket?.connected) notes.push("Bitbucket: connect your account on the Bitbucket tab to search yours");
      if (bitbucket?.bitbucket?.error) notes.push(`Bitbucket: ${bitbucket.bitbucket.error}`);
      show(list, notes.join(" · "));
    }
    input.oninput = () => {
      clearTimeout(timer);
      timer = setTimeout(() => void search(input.value), 300);
    };
    input.onkeydown = (e) => {
      if (e.key === "ArrowDown" || e.key === "ArrowUp") {
        e.preventDefault();
        if (!options.length) return;
        active = (active + (e.key === "ArrowDown" ? 1 : options.length - 1)) % options.length;
        [...results.children].forEach((r, i) => { if (i < options.length) r.style.background = i === active ? "var(--bg-hover, rgba(128,128,128,.12))" : ""; });
      } else if (e.key === "Enter") {
        const source = options[active] ?? sourceOf(input.value);
        if (source) choose(source);
        else say("Pick a repository from the list, or give owner/name or a GitHub or Bitbucket URL.");
      } else if (e.key === "Escape") results.style.display = "none";
    };
    input.onblur = () => setTimeout(() => { results.style.display = "none"; }, 150);
    input.onfocus = () => { if (options.length) results.style.display = "block"; };
    return css(el("div", {}, input, results), "display:grid;gap:6px");
  })();

  // Which plugin and wtc the gateway runs, for telling builds apart.
  const footer = (data) => (versionText(data.version) ? muted(versionText(data.version), "font-size:11px;display:block") : null);
  let focusSection = ctx.props?.focus;
  function render() {
    renderedStamp = collectionStamp(entry, sessionKeyOf());
    if (!entry?.data) {
      header.replaceChildren(css(el("div", { textContent: entry?.error ? `The collection could not be loaded: ${entry.error}` : "Loading…" }), "color:var(--muted, #888)"));
      body.replaceChildren();
      return;
    }
    const data = entry.data;
    const repos = data.repos ?? [];
    const services = data.services ?? [];
    const branch = collectionBranch(repos);
    const groups = serviceGroups(services);
    if (repos.length === 0 && groups.length === 0) {
      // No collection yet: the conversation's label says nothing about one.
      header.replaceChildren(
        row(brand("12px", data.version)),
        row(css(el("span", { textContent: "No collection yet" }), "font-size:16px;font-weight:600;color:var(--text-strong, inherit)"),
          css(el("span", {}, iconButton("↻", "Refresh", () => trackedRefresh(watch, sessionKeyOf(), say, sessionKeyOf, () => !disposed))), "margin-left:auto;display:flex;align-items:center;gap:2px")),
        muted("A collection is this conversation's worktrees of a taba project's repositories, for one task. Ask the conversation to work on a repository, or add one below.", "font-size:13px;display:block;margin-top:4px"),
      );
      const warnings = (data.warnings ?? []).map((w) => css(el("div", { textContent: w }), "font-size:12px;color:var(--warn, #bf8700)"));
      body.replaceChildren(...[css(el("div", {}, heading("Add a repository"), adder, note ? log : null), "display:grid;gap:10px"), ...warnings, footer(data)].filter(Boolean));
      return;
    }
    const toolbar = tables.actionToolbar(repos);
    header.replaceChildren(
      row(brand("14px", data.version), css(el("span", {}, css(timeLabel(entry.at, { prefix: "Updated ", updated: true }), "font-size:12px;color:var(--muted,#888)"),
        iconButton("↻", "Refresh", () => trackedRefresh(watch, sessionKeyOf(), say, sessionKeyOf, () => !disposed))), "margin-left:auto;display:flex;align-items:center;gap:2px")),
      css(el("div", { textContent: data.collection.label ?? data.collection.sessionKey }), "font-size:16px;font-weight:600;color:var(--text-strong, inherit);overflow-wrap:anywhere"),
      row(data.collection.project ? muted(data.collection.project === "ad-hoc" ? "ad-hoc taba project" : `taba project ${data.collection.project}`) : null,
        branch ? mono(branch, "The collection's branch; a repository on another one shows its own") : null,
        muted(`${repos.length} ${repos.length === 1 ? "repository" : "repositories"}${groups.length ? ` · ${groups.filter((g) => g.running).length}/${plural(groups.length, "service")} running` : ""}`)),
      // Not when there is no folder: replaceChildren writes a null as "null".
      ...(data.collection.folder ? [css(el("div", {}, mono(data.collection.folder, "The collection's folder: one link per repository to its worktree")), "display:flex;margin-top:2px")] : []),
      ...(toolbar ? [css(el("div", {}, toolbar), "display:flex;min-width:0;margin-top:4px")] : []),
    );

    const repoSection = css(el("div", { className: "wtc-repo-section" }, heading("Repositories")), "display:grid;gap:4px");
    if (repos.length === 0 && !data.subSessions?.length) repoSection.append(muted("No repositories yet. Ask in the conversation, or add one below.", "font-size:13px"));
    const confirmations = tables.publicationConfirmations(repos);
    const nextActions = tables.nextActions(repos);
    if (repos.length || data.subSessions?.length) repoSection.append(tables.repoTable(repos, repoNarrow, data.subSessions));
    tables.checkAccount(repos);
    const howTo = tables.notice();
    repoSection.append(adder);

    const prSection = pullRequestRows(repos).length ? el("div", { className: "wtc-pr-history" }, heading("Pull requests"), tables.prTable(repos, narrow)) : null;

    const serviceSection = el("div", { className: "wtc-base-services" }, heading("Services", tables.serviceControls(groups)));
    if (groups.length === 0) serviceSection.append(muted(data.servicesNote ?? "None running. Start them in the collection folder with wtc svc.", "font-size:13px"));
    else serviceSection.append(tables.serviceTable(groups, data, narrow));

    const warnings = (data.warnings ?? []).map((w) => css(el("div", { textContent: w }), "font-size:12px;color:var(--warn, #bf8700)"));
    const activitySection = tables.activityLog();
    const serviceLogSection = tables.serviceLogs();
    const temporary = [confirmations, nextActions, howTo].filter(Boolean);
    const actionSection = temporary.length ? css(el("section", { className: "wtc-transient-actions", ariaLabel: "Collection action details" }, ...temporary), "display:grid;gap:10px;min-width:0") : null;
    const sections = [repoSection, serviceSection, serviceLogSection, actionSection, activitySection, prSection, ...warnings, footer(data)].filter(Boolean);
    // Keep the unchanged log node connected: routine status ticks must preserve
    // its scroll position, keyboard focus and selection while the person reads.
    for (const child of [...body.children]) if (!sections.includes(child)) child.remove();
    sections.forEach((section, index) => { if (body.children[index] !== section) body.insertBefore(section, body.children[index] ?? null); });
    const destination = focusSection === "activity" ? activitySection : focusSection === "pull-requests" ? prSection : focusSection === "actions" ? actionSection : focusSection === "service-logs" ? serviceLogSection : null;
    if (focusSection) { const block = focusSection === "actions" || focusSection === "service-logs" ? "nearest" : "start"; focusSection = null; destination?.scrollIntoView?.({ block }); }
  }

  const consumeServiceLogIntent = () => {
    if (disposed || !entry?.data) return;
    const key = sessionKeyOf(), intent = serviceLogIntents.get(key);
    if (!intent) return;
    serviceLogIntents.delete(key);
    void tables.loadServiceLogs(intent.group, intent.label, intent.targets);
  };
  serviceLogListeners.add(consumeServiceLogIntent);
  let renderedStamp;
  const show = (next) => {
    if (ctx.signal.aborted) return;
    const changed = renderedStamp !== collectionStamp(next, sessionKeyOf());
    entry = next;
    if (changed) render();
    else { syncTimeLabels(root, next); tables.checkAccount(next.data?.repos ?? []); }
    consumeServiceLogIntent();
  };
  function follow() {
    tables.clearServiceLogs();
    watch?.dispose();
    watch = null;
    entry = null;
    renderedStamp = undefined;
    say("");
    const { sessionKey, agentId } = ctx.props ?? {};
    if (!sessionKey) {
      header.textContent = "No session selected.";
      return;
    }
    render();
    watch = watchCollection(ctx.host, sessionKey, agentId, show);
  }
  follow();
  const tick = setInterval(() => { if (entry?.data) { syncTimeLabels(root, entry); tables.checkAccount(entry.data.repos ?? []); } }, 15000);
  return {
    update(next) {
      const changed = next.props?.sessionKey !== ctx.props?.sessionKey;
      const focusChanged = next.props?.focus !== ctx.props?.focus || (!!next.presented && !ctx.presented);
      ctx = next;
      if (changed || focusChanged) focusSection = next.props?.focus;
      if (changed) follow();
      else if (focusChanged && next.props?.focus) { render(); consumeServiceLogIntent(); }
      else if (entry) show(entry);
    },
    dispose() {
      disposed = true;
      serviceLogListeners.delete(consumeServiceLogIntent);
      tables.dispose();
      clearInterval(tick);
      sized?.disconnect();
      watch?.dispose();
      root.remove();
    },
  };
}

export default {
  id: PLUGIN_ID,
  activate(host) {
    const disposers = [
      host.ui.registerPanel({ id: "collection", label: TITLE, icon: "folder", mount: mountCollection }),
      host.ui.registerReplacement({ id: "collection-composer", surface: "composer", label: "Composer with collection", mount: mountComposer }),
      // An entry in the session's menu. No header action: OpenClaw shows one
      // as a bar across every conversation, also one without a collection;
      // the card's ⤢ and the header's Panels menu open the panel too.
      host.ui.registerAction({
        id: "open-collection-session",
        label: TITLE,
        placement: "session",
        run: ({ sessionKey, agentId }) => host.ui.openPanel("collection", { sessionKey, agentId }),
      }),
    ];
    // The card lives with the message box, so this composer is selected on
    // activation. Customize UI → Built-in switches back for this browser.
    try { host.ui.selectReplacement("composer", "collection-composer"); } catch {}
    return () => disposers.forEach((d) => (typeof d === "function" ? d() : d?.dispose?.()));
  },
};
