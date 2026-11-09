## Releases

- **`go install` builds know their version.** `go install taba.sh/taba/cmd/taba@v<version>` now reports that version in `--version`, `doctor` and compatibility checks, instead of a `-dev` label. Builds from a checkout and from untagged commits are still dev builds ([#202](https://github.com/lcorneliussen/taba/issues/202)).
