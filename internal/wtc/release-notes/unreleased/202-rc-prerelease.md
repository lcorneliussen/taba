## Releases

- **Release candidates are published as GitHub prereleases.** A `v<version>-rc.N` tag no longer becomes GitHub's latest release, so `upgrade` without `--version` and the Homebrew formula stay on the stable release. `upgrade --version <version>-rc.N` installs a candidate and shows its notes; other prereleases are left out of the notes ([#202](https://github.com/lcorneliussen/wtc-cli/issues/202)).
