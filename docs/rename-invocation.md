# Invocation-aware command names

Phase 0 of the taba rename ([#201](https://github.com/lcorneliussen/wtc-cli/issues/201)) made the executable invocation-aware; phase 1 ([#202](https://github.com/lcorneliussen/wtc-cli/issues/202)) ships it under both names. Use a release's notes to determine whether it includes this groundwork. Packaging both executable names is the phase-1 work in [#202](https://github.com/lcorneliussen/wtc-cli/issues/202), described under [Both executables](#both-executables).

The command reads its invoked executable name from `argv[0]`. Invoking the same binary through a `taba` symlink or renamed executable selects `taba` for command paths in help, examples, diagnostic prefixes, command hints and terminal headings. Invoking it as `wtc` keeps the existing text. The corresponding `.exe` names are recognized too; unknown executable names retain `wtc`. A shell alias that executes `wtc` still supplies `wtc` as the executable name.

A pinned executable that hands over to a collection-local dev build retains the caller's invocation name, even though the dev artifact remains `.wtc/dev/wtc`.

Only explicitly marked, authored text follows the invocation. Substitutions happen before user values are formatted into messages. User paths, repository names, error details and JSON payload values are not passed through a text-rewriting filter. Human-readable summaries can include the selected command name.

Technical identities retain their current spellings: environment variables, file paths, mise backend, the dev-build artifact name, OpenClaw plugin ID, skill names, protocol/schema markers, and machine action commands. For example, help invoked through `taba` still correctly says `openclaw plugins reload wtc`; the argument is a plugin ID. Generated scripts, embedded documents and historical release notes also retain their existing content. There is no automatic migration.

Environment-prefix and persistent-input compatibility are documented separately in [environment compatibility](rename-compatibility.md) and [configuration inputs](rename-config-inputs.md). Runtime/generated-state aliases remain further phase-0 work; default-name changes follow the [rename timeline](https://taba.sh/wtc-is-now-taba/).

## Both executables

This part is phase 1 of the rename. Release archives are named `taba_<version>_<os>_<arch>.tar.gz` and contain two executables built from the same program: `taba` and `wtc`. Both run the same commands. Existing mise pins on the `github:lcorneliussen/wtc-cli` backend pick up the renamed archive by OS and architecture and put both names on `PATH`, so scripts, hooks and `wtc upgrade` (which runs `wtc release-notes` from the target release) keep calling `wtc`. The Go module path and `go install ./cmd/wtc` still produce only `wtc`; the module and `cmd/taba` move with the repository rename.

When a person runs `wtc` in a terminal, it prints one line on stderr saying the command is now `taba`, or that the rename is under way when no `taba` is on `PATH`. The notice never goes to stdout and is skipped when stderr is not a terminal, with `--json`, for shell completion, and for `taba`. Set `TABA_NO_RENAME_NOTICE=1` to hide it. Every invocation sets that variable for its own hooks, services and nested calls, so one command shows it at most once.
