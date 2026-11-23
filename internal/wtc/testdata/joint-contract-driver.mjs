// Joint taba <-> plugin contract driver (#471): runs the pinned, merged
// openclaw-bitbucket pr-writes module against a request taba wrote, with a
// fake Bitbucket injected and the global fetch poisoned, then prints what the
// plugin answered and which Bitbucket calls it made.
//
//   node joint-contract-driver.mjs <plugin-dir> <state-dir> <worktree> <fake-state> [fail]
//
// fake-state: a JSON file that keeps the fake Bitbucket's pull request and
// comments from one run to the next, as the real one would.
// fail, optional: "<METHOD> <route-suffix> <status>" (an HTTP answer) or
// "<METHOD> <route-suffix> drop" (no answer) for the first matching call.
import { existsSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { pathToFileURL } from "node:url";

const [pluginDir, stateDir, worktree, fakeState, fail = ""] = process.argv.slice(2);
globalThis.fetch = async (url) => { throw new Error(`network escape in the joint contract test: ${url}`); };

const { createPullRequestWriter } = await import(pathToFileURL(path.join(pluginDir, "lib", "pr-writes.js")).href);
const { writeLogin } = await import(pathToFileURL(path.join(pluginDir, "lib", "logins.js")).href);

const person = { profile: "synthetic-person", user: "person@example.invalid", name: "Synthetic Person", token: "synthetic-joint-token", stored: "2026-01-01T00:00:00.000Z" };
await writeLogin(stateDir, person);

// One open draft pull request in acme/alpha, as Bitbucket would keep it.
const saved = existsSync(fakeState) ? JSON.parse(readFileSync(fakeState, "utf8")) : null;
const pr = saved?.pr ?? { id: 7, state: "OPEN", draft: true, title: "Synthetic", description: "", source: { commit: { hash: "aaaaaaaaaaaa" } }, destination: { repository: { full_name: "acme/alpha" } } };
const comments = new Map(saved?.comments ?? [[901, { id: 901, pullrequest: { id: 7 }, user: { uuid: "{person}" } }], [902, { id: 902, pullrequest: { id: 7 }, user: { uuid: "{person}" } }]]);
let next = saved?.next ?? 950;
const calls = [];
let [failMethod, failSuffix, failHow] = fail.split(" ");
const json = (value, status = 200) => new Response(JSON.stringify(value), { status, headers: { "content-type": "application/json" } });
const fetchImpl = async (url, init) => {
  const route = url.replace("https://api.test/2.0", "");
  const body = init.body ? JSON.parse(init.body) : undefined;
  calls.push({ method: init.method, route, body });
  if (!url.startsWith("https://api.test/2.0/")) throw new Error("unexpected host");
  if (init.headers.authorization !== "Basic " + Buffer.from(`${person.user}:${person.token}`).toString("base64")) return json({}, 401);
  if (failMethod && init.method === failMethod && route.endsWith(failSuffix)) {
    failMethod = "";
    if (failHow === "drop") throw new TypeError("fetch failed");
    return json({}, Number(failHow));
  }
  if (route === "/user") return json({ uuid: "{person}" });
  const m = route.match(/^\/repositories\/acme\/alpha\/pullrequests\/7(\/comments(?:\/(\d+))?(\/resolve)?)?$/);
  if (!m) return json({}, 404);
  if (!m[1]) {
    if (init.method === "PUT") Object.assign(pr, body);
    return json(pr);
  }
  if (m[3]) {
    comments.get(Number(m[2])).resolved = true;
    return new Response(null, { status: 204 });
  }
  if (m[2]) {
    const c = comments.get(Number(m[2]));
    if (!c) return json({}, 404);
    if (init.method === "PUT") c.content = body.content;
    return json(c);
  }
  const created = { id: next++, pullrequest: { id: 7 }, user: { uuid: "{person}" }, content: body.content, inline: body.inline, parent: body.parent };
  comments.set(created.id, created);
  return json(created, 201);
};

const writer = createPullRequestWriter({
  stateDir,
  getSessionEntry: async ({ sessionKey }) => (sessionKey === "repo-session" ? { worktree: { branch: "feature" }, workspaceDir: worktree } : undefined),
  currentProfile: () => ({ profileId: person.profile }),
  fetchImpl,
  apiUrl: "https://api.test/2.0",
});
const asked = await writer.ask({ sessionKey: "repo-session" });
let confirmed = null;
if (asked.ok && asked.result?.status === "needs_confirmation") {
  confirmed = await writer.confirm({ sessionKey: "repo-session", payload: { requestId: asked.result.requestId, requestDigest: asked.result.confirmation.requestDigest } });
}
writeFileSync(fakeState, JSON.stringify({ pr, comments: [...comments], next }));
process.stdout.write(JSON.stringify({ asked, confirmed, calls, pr: { draft: pr.draft }, comments: [...comments.values()] }));
