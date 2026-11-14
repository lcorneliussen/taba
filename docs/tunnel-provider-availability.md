# Gateway tunnel client lookup

The OpenClaw collection response retains `tunnelProviders`, listing clients
that resolve to regular files with execute access on the gateway Node process's
PATH. It also includes `tunnelProviderAvailability`, keyed by provider, with
`scope: "gateway-path"` and one of these states:

- `available`: a regular file passes execute-access checking, following symlinks.
- `missing`: no candidate was found in the searched nonempty PATH entries.
- `unusable`: a candidate is a directory, a dangling symlink, or inaccessible for execution.
- `unknown`: PATH is unavailable/empty or lookup encountered another filesystem error.

A usable later PATH candidate wins. Otherwise unknown errors take precedence
over unusable candidates, which take precedence over missing candidates.
The lookup skips empty PATH components; it does not search the working directory
implicitly. Results are fresh on collection reads and add/setup clicks. Setup
returns the current `availability` and guidance to inspect existing collection
policy inputs before installing anything. No probe executes a provider.

This is gateway evidence, not collection runtime capability. The service action
runs `wtc svc tunnel` through the host command API from the collection folder.
Current runtime policy can select an absolute Cloudflare executable, an explicit
Node helper for devtunnel, or a socket/API for other provider operations. Tunnel
hosts use a minimal environment with `PATH=/usr/bin:/bin`, rather than loading a
collection tool environment. Gateway lookup cannot validate these policy inputs.
It therefore does not block start/reopen actions based on gateway PATH; runtime
policy validation remains authoritative. Existing definition selection and
public-exposure approval requirements still apply.

Execute access does not guarantee successful process launch, authentication,
network reachability or a running tunnel. Collection-scoped capability reporting
and UI guidance based on that capability remain follow-up work under
[#365](https://github.com/lcorneliussen/taba/issues/365).
