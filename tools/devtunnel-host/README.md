# Embedded devtunnel host

This helper hosts an exact, already registered devtunnel using an explicitly
supplied host token. Registration and persistent-resource cleanup use separate
management credentials. Named service commands run this embedded helper through
the foreground `svc tunnel-host` task and its owning tunnel lifecycle.

Dev tunnel hosting requires an installed Node 22 or newer. Node 22.14.0 is the
pinned build and test version. The helper is bundled into the Go executable;
running it requires no npm packages, package download, native addon, login or
global credential configuration. Other providers and status do not require
Node. The receiving caller supplies the Node path and environment.

The helper reads one bounded JSON frame from standard input and emits JSON
`started` and `stopped` events. Credentials never belong in arguments or those
events. SIGTERM and SIGINT request disposal; failed or interrupted disposal
requires reconciliation by the owning lifecycle. This is explicit credential
binding, not process sandboxing or an exactly-once remote-effect guarantee.

## Build verification

From the repository root, with the pinned Node distribution and its npm:

```sh
node tools/devtunnel-host/check.mjs
```

The checker installs the integrity-pinned public lockfile with
`npm ci --ignore-scripts`, using temporary HOME, npm configuration and cache.
It rebuilds and compares the embedded CJS, notices and checksums, runs fake
transport tests, and compares the assets again. To deliberately regenerate
assets after changing helper source, run `node build.mjs` from this directory
after that confined dependency installation, then run the checker again.

The SDK's optional WebSocket accelerators use their upstream functional
JavaScript fallbacks. The obsolete RSA fallback for Node without native
key-generation support is unavailable. Tests exercise the real pinned SDK's
native host-key generation, endpoint setup, denied relay and exact endpoint
cleanup, including the standalone CJS with no external package resolution or
real network access. License text is required for each bundled package;
missing text fails the build even when package metadata declares a license.

All provider transports in these tests are fake. The checker does not contact
provider accounts, create tunnels, or inspect credential stores.
