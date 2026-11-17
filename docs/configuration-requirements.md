# Canonical configuration requirements (v1 draft)

The declarative requirements parser accepts explicitly selected dotenv examples
named `<actual filename>.example`. Supported names are `.env`, `.env.<suffix>`
and `<name>.env`, with `.example` appended. `.env.example` and
`.env.local.example` follow the same rule. There is no hidden-file crawl and no
requirement to rename an application's configuration files.

The parser and inventory evaluator are currently library components. Command
selection, filesystem authorization and CLI output are pending integration.
This document describes the draft data contract, not commands already available.

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
Unsupported file-content checks remain unverified. The pure evaluator accepts
only observations supplied by an authorized adapter; it opens no files itself.

The source list explicitly selects actual dotenv files. Process input ownership
uses exact `env_names` and bounded `env_prefixes`. Only owned process names can
participate in precedence or undocumented-input reporting. Unrelated inherited
variables such as `HOME` and `PATH` do not participate. The adapter must establish
receiving-project ownership before reading files or supplying process inputs;
these declarations do not grant arbitrary filesystem access or establish that an
inherited credential belongs to this project.

The shared inventory reports names, classification, purpose, scope, effective
source, condition, applicability and presence. Presence states include `present`,
`missing`, `empty`, `placeholder`, `malformed`, `unverified`, `not-required` and
`undocumented`. A present value does not prove valid authentication. Optional
missing inputs remain distinguishable from missing mandatory inputs. A required
remote credential remains required when remote is explicitly selected, including
when a local service repository is also checked out.

Input is bounded to 256 KiB per file, 16 KiB per line, 1,024 keys, 16 sources and
16 owned process prefixes. Paths are relative and cannot traverse parents.
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
