# Canonical configuration requirements (v1)

The declarative requirements parser accepts explicitly selected dotenv examples
named `<actual filename>.example`. Supported names are `.env`, `.env.<suffix>`
and `<name>.env`, with `.example` appended. `.env.example` and
`.env.local.example` follow the same rule. There is no hidden-file crawl and no
requirement to rename an application's configuration files.

`taba doctor`, `taba config list`, and explicit `taba secrets list --requirements`
use one inventory. The default `secrets list` remains a filename/link-state view
and does not inspect contents.

By default, only Git-tracked root `.env.example` and `.env.local.example` files
are considered. A `# taba:requirements v1` header opts the file into the schema.
Unannotated legacy examples remain hints, not mandatory-key declarations.
Explicit `--example repo:path` selects nonstandard owned paths instead of the
default selection; repeat it to select several independent groups. Selected
examples cannot claim the same actual source or key namespace. There is no
separate manifest or duplicated authoritative variable list.

```sh
taba doctor
taba config list --json
taba secrets list --requirements --json
taba doctor --example web:config/dev.env.example
taba config lint --json
```

`config lint` validates declarations, references, source ownership, provided
owned key names and dotenv syntax. It reports effective value states, but those
states do not impose readiness requirements on static lint. For example, an
out-of-choices selector is reported as `malformed`; doctor fails that mandatory
selector while static lint can still pass its syntax and key-name checks. It does **not** fail merely because a required credential,
actual configuration file or optional integration is absent. A repository can
require this command in local validation and CI without supplying real secrets.
In a standalone Git checkout, use `taba config lint --example .env.example` or
run `taba config lint` for bounded default discovery. Standalone lint cannot
authorize project control-root inputs, so it does not open them; its provided-key
checks cover selected repository-local inputs. In a collection, approved project
inputs are also inspected when provided. Malformed provided dotenv files,
unsafe config sources, contradictory schemas and undocumented owned keys fail
lint. Missing, empty, placeholder and malformed effective-value states alone do
not fail static lint. Required opaque-file presence and content readiness belong
to doctor, not static CI lint.

Doctor diagnoses readiness: missing, empty, placeholder, malformed and unverified
mandatory requirements are not healthy; optional missing and inapplicable
requirements do not fail. Unknown selection remains unknown. A local connection
can remove a remote credential requirement while local-service readiness remains
unverified. Neither inventory listing nor doctor proves authentication or starts
services.

One canonical example contains the variable declarations for one configuration
scope. The source annotation maps multiple actual files to that schema in the
application's documented order, lowest precedence first. An empty higher-priority
assignment overrides a lower-priority value. Example assignments document values;
they never supply actual values to the inventory.

```dotenv
# taba:requirements v1
# taba:source {"files":[{"path":".env","scope":"project","format":"dotenv"},{"path":".env.local","scope":"collection","format":"dotenv"}],"env_names":["WEB_CONNECTION"],"env_prefixes":["WEB_TEST_"]}
# taba:require {"kind":"config","purpose":"Choose the test connection","affects":"web tests","required":"required","scope":"collection","example":"nonsecret-default","hint":"Choose local or remote","doc":"docs/testing.md","choices":["local","remote"]}
WEB_CONNECTION=remote
# taba:require {"kind":"secret","purpose":"Authenticate remote tests","affects":"remote tests","required":"conditional","scope":"project","example":"placeholder","hint":"Request a development credential from the service owner","doc":"docs/testing.md","when":{"key":"WEB_CONNECTION","equals":"remote"}}
WEB_TEST_TOKEN=replace-me
```

All annotations start with `# taba:`. Only version `v1`, `source`, `require` and
`file` are accepted. Annotation objects use closed JSON: unknown fields,
duplicate fields, duplicate declarations, unsupported formats and contradictory
conditions fail validation. An unannotated example is not a mandatory-key list;
it does not meet this explicit schema contract.

Each key declaration immediately precedes its literal `NAME=value` example
assignment (ordinary comments and blank lines may intervene). `kind` is `config`
or `secret`; `required` is `required`, `optional` or `conditional`; `scope` is
`project` or `collection`. `purpose`, `affects`, `hint` and `doc` are mandatory
non-secret guidance. `example` is `placeholder` or `nonsecret-default`.
Secret examples must be classified as placeholders. An actual value equal to a
nonempty placeholder example is reported as `placeholder`.

Conditional declarations require `when`, containing exactly a declared selector
`key` and literal `equals`. The selector must be unconditional configuration with
a finite `choices` list containing the comparison value. A missing, empty,
unverified or invalid selector leaves applicability unknown. Conditions do not
evaluate expressions or run commands. A selected local connection can remove a
remote credential requirement; it provides no evidence that the local service is
configured or running. Repository checkout alone supplies no selector value.

The optional file annotation declares a presence-only requirement:

```dotenv
# taba:file {"file":"certs/test.pem","kind":"secret","purpose":"Authenticate integration tests","affects":"full integration","required":"optional","scope":"collection","example":"placeholder","hint":"Request a development certificate from the service owner","doc":"docs/testing.md"}
```

File presence is separate from validation of its contents or authentication.
The adapter reports only presence for opaque file declarations; it does not
validate their contents. Supported dotenv file contents are checked through key
declarations. Unsupported content formats are not silently validated.

The source list explicitly selects actual dotenv files. Process input ownership
uses exact `env_names` and bounded `env_prefixes`. Only owned process names can
participate in precedence or undocumented-input reporting. Unrelated inherited
variables such as `HOME` and `PATH` do not participate. Process values participate
only when the invocation explicitly adds
`--process-environment`; declaring ownership alone does not enable inspection.
Uninspected owned process inputs remain unverified where they could affect the
effective configuration. Inherited credentials never silently satisfy a
file-mapped requirement. Standalone static lint does not inspect process inputs.

Project sources resolve under the validated receiving control root in the
registered repository namespace. Collection sources resolve relative to that
repository's selected worktree. The inventory reports declaration, effective
source scope and provenance separately. Process storage scope is reported as
`process`, not assumed to be project-shared or collection-isolated. Existing worktree secret links may
resolve inside that exact approved project/repository namespace. Cross-project,
cross-repository and arbitrary external links are rejected. Examples cannot be
symlinks; all inspected files must be regular, owned by the current user, not
writable by others and within the size bound. Inspection uses rooted read-only
opens and never creates, copies, links or migrates configuration. Ambiguous
receiving identity produces an unverified diagnostic without exposing paths or
values.

The shared inventory reports names, classification, purpose, scope, effective
source, condition, applicability and presence. Key and file targets have distinct
identities. Presence states include `present`,
`missing`, `empty`, `placeholder`, `malformed`, `unverified`, `not-required` and
`undocumented`. A present value does not prove valid authentication. Optional
missing inputs remain distinguishable from missing mandatory inputs. A required
remote credential remains required when remote is explicitly selected, including
when a local service repository is also checked out.

Input is bounded to 256 KiB per file, 16 KiB per line, 1,024 keys, 16 sources and
16 owned process prefixes. Paths use ASCII letters, digits, `_`, `.`, `/` and `-`;
they are relative and
cannot traverse parents. Actual source paths cannot end in `.example`.
Dotenv syntax is deliberately literal: `NAME=value`, blank lines and whole-line
comments. One matching pair of single or double quotes is removed. There is no
shell expansion, escape processing, interpolation, multiline assignment, `export`
syntax or inline-comment interpretation. Shell substitutions are inert data.
Malformed actual inputs are rejected as a whole. Diagnostics contain only fixed
codes and line numbers; raw input and decoder error strings are omitted.

This declarative slice does not implement executable checks or permissions
probes. It does not prove that arbitrary dynamic application code uses only
declared inputs. Reusable validation can enforce supported declarations and
explicitly owned selected inputs; static application-input lint requires a
separately specified supported language and policy.
