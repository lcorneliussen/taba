# Named tunnels in `taba doctor`

`taba doctor` checks each named tunnel configured for the collection, so a
tunnel that cannot resolve shows up before someone tries to share a service.
The `named-tunnels` row is advisory: it never fails doctor. Without any tunnel
policy it reads `none configured`.

For each effective entry, from the project and collection tunnel policies,
doctor reports one of these states:

- `ok`: the definition resolves as far as doctor can check locally.
- `problem`: something would stop the tunnel; each problem comes with a fix.
- `disabled`: the entry is `enabled = false`, which also hides an inherited one.

It checks:

- **The service and its local target.** The service must be declared in the
  repository's task declarations. A literal `x-wtc.url` must be a loopback URL,
  with an explicit port for Tailscale Serve and an IPv4 or `localhost` HTTP
  port without a path for devtunnel. A target from `x-wtc.port_env` or
  `x-wtc.url_env` is shown by variable name, for example
  `http://127.0.0.1:$WIDGET_PORT`. A service with none of these gets
  "declare `x-wtc.port_env` or `x-wtc.url` on `widget/web`".
- **The repository identity.** The tunnel's repository must be checked out
  once in the collection under its registered name.
- **The provider inputs that can be checked without provider I/O.** Each mode
  needs these inputs:

  | Mode | Inputs |
  |---|---|
  | `cloudflare/named` | `executable`, `hosting_file`, `management_file`, `cleanup_file` |
  | `cloudflare/quick` | `executable` |
  | `devtunnel/persistent` | `node`, `hosting_file`, `management_file`, `cleanup_file` |
  | `devtunnel/temporary` | `node`, `management_file`, `cleanup_file` |
  | `ngrok/domain` | `hosting_file`; with `reserve`, also `management_file` and `cleanup_file` |
  | `tailscale/serve` | `socket`, `node_id`, `dns_name` |

  An executable or Node path must be an executable file, a socket must exist
  as a socket, and a credential file must be a non-empty file of at most
  16 KiB inside the control root.

Doctor is observational. It does not source the collection env files, which
are receiver-owned shell code; only an explicit tunnel action reads
`port_env` and `url_env` values. Provider inputs are checked with `stat`
alone: doctor reads no credential file, connects to no socket, runs no
provider executable, hook or probe, makes no provider, account or network
request, and writes nothing. Diagnostics name provider input fields, never
their values or paths.

So an `ok` row does not mean the tunnel will start: the env value, the
daemon, the account and the credentials themselves are only checked when the
tunnel runs. In `--json`, the row carries the per-tunnel results under
`tunnels`.

Running doctor after an upgrade is separate work under
[#450](https://github.com/lcorneliussen/taba/issues/450).
