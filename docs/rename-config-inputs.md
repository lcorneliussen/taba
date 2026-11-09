# Configuration inputs for the taba rename

This is a phase-0 increment of [#201](https://github.com/lcorneliussen/taba/issues/201), preparing existing `wtc 0.1.x` clients for the rename. Use the target release's notes to determine whether it has shipped. There is no migration command or need to rename files yet.

## Accepted names

| Input | Legacy name | Alternate name |
| --- | --- | --- |
| Harness configuration | `wtc.toml` | `taba.toml` |
| Harness CLI version pin | `.wtc-cli-version` | `.taba-version` |
| Control-root defaults | `wtc.env` | `taba.env` |
| User configuration directory | `$XDG_CONFIG_HOME/wtc` | `$XDG_CONFIG_HOME/taba` |
| User data directory | `$XDG_DATA_HOME/wtc` | `$XDG_DATA_HOME/taba` |
| Collection task context | `WTC-SCOPE.md` | `SCOPE.md` |
| Skill overlay base digest | `.wtc-base.sha256` | `.taba-base.sha256` |

Configuration and pin files stay in the harness directory. Ad-hoc configuration accepts `taba.toml` inside the selected configuration directory. [Collection directory inputs](rename-collection-directories.md) add `.taba/config` when `.wtc/config` is absent.

When XDG variables are unset, user directories use `~/.config` and `~/.local/share` as before. Explicit config/data-root settings keep their existing precedence over these defaults; both `TABA_` and `WTC_` prefixes are accepted as described in [environment compatibility](rename-compatibility.md).

## Selection and writes

For each pair, an existing legacy path wins. Only when that path is absent does the alternate name become eligible. Older clients still write legacy files, so this order prevents a newer client from silently selecting a second configuration. An invalid or dangling legacy input is not bypassed in favor of an alternate file. The files are not combined.

This differs from environment variable spelling: **within the selected source**, `TABA_X` wins over `WTC_X`. For example, if both defaults files exist, only `wtc.env` is read; a `TABA_STATUS_WATCH` assignment inside that file wins over `WTC_STATUS_WATCH` there. Environment settings still precede file defaults.

Reads do not create, rename, copy or symlink the selected inputs. When neither path exists, fresh scaffolding and generated output keep their legacy names. Existing `SCOPE.md` task context is preserved by scope seeding. Harness initialization refuses to create legacy files beside existing alternate config/pin files.

The embedded agent instructions and start/follow/add-repo skills read and edit the selected scope file. Harness-authored instruction and skill overrides remain unchanged; update those overrides to the same selection rule before adopting `SCOPE.md`.

An explicit `wtc upgrade` updates the selected pin and configuration **in place**, including alternate filenames. Its dry-run report and dirty-file protection refer to those actual files. It does not create legacy files that would shadow them. Forking an ad-hoc collection carries over the selected configuration's contents but writes the fresh sibling's usual `wtc.toml`.

For user config/data roots, the existing legacy directory likewise wins over an alternate directory. If neither exists, the default stays `wtc`; lookup does not create either directory. Explicit root overrides remain available when a particular location must be selected.

Toolchain caches fingerprint the selected config path and contents, so switching filenames or selecting an older file cannot retain stale tool paths. Older caches without a fingerprint are rebuilt once. The generated `.env.toolchain` filename stays the same; from the first taba release it carries `TABA_TOOLCHAIN_PATH` beside `WTC_TOOLCHAIN_PATH`.

## Later increments

[Collection-state compatibility](rename-collection-state.md) adds alternate reads for PR enlistments, status snapshots, review records, watcher registrations and runtime state while retaining legacy output names. One runtime home and shared locks keep clients from starting separate runners for one collection. [Invocation-aware command names](rename-invocation.md) are a separate preparation increment; eventual default-name changes follow the [rename plan](https://taba.sh/wtc-is-now-taba/).
