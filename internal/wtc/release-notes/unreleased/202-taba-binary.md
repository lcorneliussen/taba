## Rename

- **Releases ship a `taba` executable next to `wtc`.** Archives are now named `taba_<version>_<os>_<arch>.tar.gz` and contain both names; existing mise pins and `wtc upgrade` keep working through `wtc`. Running `wtc` in a terminal prints a one-line rename notice on stderr; it is skipped for `--json`, non-terminal stderr and shell completion, and `TABA_NO_RENAME_NOTICE=1` hides it. See [invocation names](https://github.com/lcorneliussen/wtc-cli/blob/main/docs/rename-invocation.md#both-executables) ([#202](https://github.com/lcorneliussen/wtc-cli/issues/202)).
