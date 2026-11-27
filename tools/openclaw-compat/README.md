# OpenClaw API audit

Check a plugin before upgrading its OpenClaw host. The audit reads JavaScript
and TypeScript syntax, the selected host's package exports, and its own
compatibility registry. It does not execute either the plugin or the registry,
contact accounts, install a plugin, or change a gateway.

```sh
npm ci --ignore-scripts --prefix tools/openclaw-compat
node tools/openclaw-compat/audit.mjs \
  --host /path/to/clean-openclaw-release-checkout \
  --plugin openclaw-plugin

# Add another plugin package, or save a machine-readable report.
node tools/openclaw-compat/audit.mjs \
  --host /path/to/clean-openclaw-release-checkout \
  --plugin openclaw-plugin --plugin /path/to/another-plugin --json > audit.json

node --test tools/openclaw-compat/audit.test.mjs
```

Select an exact published host tag and verify that checkout before using the
report. The host input must include `package.json` and `src/plugins/compat/`.
The first qualified registry format is OpenClaw **2026.11.10**. A future
registry syntax this reader cannot understand stops the audit with exit **2**;
it never substitutes an empty list. The output records the host version, a
fingerprint of the export map and registry, and hashes of the scanned files.
Treat reports as private until inspected: filenames and API names can identify
the inspected package.

The read-only `OpenClaw API audit` workflow runs the unit tests on Node 22 and
24, then audits Taba against that published host's pinned source commit. It
does not start a gateway or use account credentials. Update the host pin and
repeat the real-host checks when qualifying another release.

## Reading the result

- **Error:** an SDK subpath is absent from this host's export map, or a matched
  registry surface has status `removed`. Exit **1**.
- **Warning:** a matched surface is `deprecated` or `removal-pending`. Exit
  **0**, or **1** with `--fail-on-warning`.
- **Review date:** the registry's `removeAfter`. Reaching that date does not
  change `removal-pending` into `removed`. Read its replacement and removal
  conditions. Different API families have different windows.
- **Enclosing condition:** source context for a candidate, including an older
  host fallback. It is not proof the branch executes on the selected host.
- **Inventory and gaps:** JSON includes SDK imports, injected API references,
  simple aliases, literal RPC candidates, and computed imports/member accesses
  that need manual inspection. A literal RPC candidate is not schema validation.

The JavaScript/TypeScript scan includes shipped `dist/` code and skips tests, dot directories,
dependencies and symlinks. No finding means no match in this limited analysis.
It does **not** certify compatibility. Aliases are resolved file-wide; scopes,
reassignment, cross-file wrappers, dynamic dispatch, named exports and registry
surfaces written as prose/signatures need review. Go-side CLI/RPC calls are not
scanned. The host's separate Doctor
migration registry is outside this SDK audit.

## Also check the real host

In a disposable gateway, run the host's own commands:

```sh
openclaw plugins lint --root /path/to/plugin --json
openclaw gateway call plugins.list --json
```

The first checks runtime hazards without executing plugin code. Installation
checks the manifest and load contract. For packages using the newer tool/feature
authoring metadata, also run `openclaw plugins validate --root /path/to/plugin
--json`; this authoring validator rejects ordinary runtime entries that do not
expose that metadata, even when installation and loading succeed.
The live gateway's `plugins.list` includes
`compatibilityNotices` observed by **that process**. Exercise the relevant
session, UI and confirmation routes, then read it again. A fresh CLI process's
plugin list does not contain every observation from the running gateway.
An unexecuted path can have no notice while still using a deprecated API.
Keep account and publication tests separate from credential-free smoke tests.

OpenClaw defines these contracts in its [compatibility registry and usage
diagnostics](https://docs.openclaw.ai/plugins/compatibility), [runtime migration
guide](https://docs.openclaw.ai/plugins/sdk-runtime), and [removed surfaces
guide](https://docs.openclaw.ai/plugins/sdk-migration/removed-surfaces). Its
[runtime compatibility lint](https://docs.openclaw.ai/plugins/runtime-compatibility)
complements this source audit.
