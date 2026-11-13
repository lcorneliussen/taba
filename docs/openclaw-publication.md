# Native publication and collection tracking

The Collection controls publish through the person's native GitHub connection
or the Bitbucket plugin, with the publisher's existing confirmation. Taba's
wrapper forwards only those fixed methods using the authenticated request's
original identity. It does not select a shared gateway login or bypass the
publisher's account, digest, permission or idempotency checks.

On a successful result, Taba records the returned PR URL and captured branch in
the linked collection's ordinary PR registry. The metadata command verifies the
actual managed checkout, unique collection membership, registry/checkout origin
and exact forge URL. It runs no collection hooks, fetch, commit or publication.
Other PR rows are preserved; a later checkout branch change does not change the
published branch recorded by the receipt.

A failed metadata write does not turn a successful push into a failed push.
The card reports **Tracking pending**. A saved private gateway receipt lets a
later Collection refresh retry only that metadata write for the same verified
session checkout. If gateway storage prevents saving the receipt, keep the
returned PR URL and enlist it after storage is repaired; refresh cannot replay
an unsaved receipt. Do not push again to repair tracking. If linking or identity
remains ambiguous, ask the owning conversation to inspect collection tracking.
Local receipt retention
cannot recover a native publisher response that was never returned or persisted;
inspect the native publication outcome before retrying an ambiguous operation.

The proposed PR text opens as a formatted, collapsible preview. In the
conversation it expands above the short summary and confirmation buttons; in
the sidebar it follows them. Explicit collapse survives refresh. **Source**
shows the unchanged body. Raw HTML stays literal and images become explicit
links, so opening a preview does not execute HTML or load remote resources.
Formatting does not change the title/body sent to the publisher. Existing PR
text is retained by the native publisher. Multiple prepared publications share
a bounded, keyboard-scrollable chat region; the sidebar uses its panel scroll.

## Update and compatibility

This uses the supported authenticated gateway dispatch contract available in
OpenClaw 2026.10.2, the plugin's existing minimum host version. It declares
`contracts.gatewayMethodDispatch: ["authenticated-request"]` and session-scoped
write admission, then uses `openclaw/plugin-sdk/gateway-method-runtime`.
The host still enforces each native publisher's authorization.

Install the matching Taba CLI and plugin together with `taba openclaw install`,
then reload open Control UI tabs. A previously loaded tab keeps its old code;
this change does not claim to upgrade a running gateway automatically. No
registry or publishing service is added. The bundled Markdown lexer is pinned,
licensed and served locally; its provenance is in the vendor directory.

Contract references: [authenticated dispatcher](https://github.com/openclaw/openclaw/blob/b8952d477f0c932d8ddc6da5c89cab5a169b6ff1/src/plugin-sdk/gateway-method-runtime.ts),
[session-scoped method admission](https://github.com/openclaw/openclaw/blob/b8952d477f0c932d8ddc6da5c89cab5a169b6ff1/src/gateway/methods/descriptor.ts).

The pinned host package exports that dispatcher, whose implementation requires
an admitted real client (`disableSyntheticClient` and `requireScopedClient`).
The Bitbucket plugin's 0.3.4 result includes the captured branch and PR URL:
[host package export](https://github.com/openclaw/openclaw/blob/b8952d477f0c932d8ddc6da5c89cab5a169b6ff1/package.json),
[Bitbucket publication result](https://github.com/lcorneliussen/openclaw-bitbucket/blob/9d645a8f50cd2164c094f86f871dbdc2972f3f77/lib/publish.js#L244).
These are source-contract checks; they are not a live-account test.

## Reproduce the preview check

Install Playwright separately; its Chromium browser must already be available.
The unit tests do not require it. For an isolated QA directory:

```sh
npm install --prefix /tmp/taba-preview-qa --ignore-scripts playwright@1.64.1
node openclaw-plugin/tools/check-publication-preview.mjs /tmp/taba-preview-qa/node_modules/playwright/index.mjs /tmp/taba-preview-qa/output
```

The successful run prints:

```text
24 publication preview scenarios passed
```

The cases cover single and multiple preparations, narrow and short viewports,
and keyboard access to every Confirm/Cancel pair without confirming publication.
The output directory contains synthetic screenshots and a JSON geometry report.
The script contacts no account or gateway and never confirms publication.
