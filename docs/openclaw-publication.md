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
The card reports **Tracking pending**. A private gateway receipt is retained
under the plugin's state directory, and a later Collection refresh retries only
that metadata write for the same verified session checkout. Do not push again
to repair tracking. If linking or identity remains ambiguous, ask the owning
conversation to inspect the collection's tracking. Local receipt retention
cannot recover a native publisher response that was never returned or persisted;
inspect the native publication outcome before retrying an ambiguous operation.

The proposed PR text opens as a formatted, collapsible preview. In the
conversation it expands above the short summary and confirmation buttons; in
the sidebar it follows them. Explicit collapse survives refresh. **Source**
shows the unchanged body. Raw HTML stays literal and images become explicit
links, so opening a preview does not execute HTML or load remote resources.
Formatting does not change the title/body sent to the publisher. Existing PR
text is retained by the native publisher.

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
