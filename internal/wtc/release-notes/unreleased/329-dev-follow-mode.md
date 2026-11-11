## Added

- Collection-local `dev mode auto|on|off` follows selected source with fresh builds, persistent exit and idle status-TUI reload. Project conventions are explicit; review verification and the existing dev-build bypass remain isolated. One-shot installs retain their release-version fallback.
- `TABA_DEV_BUILD=off` bypasses local builds for both command names, with `WTC_DEV_BUILD` compatibility. OFF in either spelling wins; conflicting nonempty values warn. Upgrades do not opt a checkout into following, and older running status processes still need an explicit initial restart.
