# Set up wtc

For an agent asked to bring wtc into an environment. Work out the three
answers below, from what you can see first and from the person only where
you cannot, then follow the guide each answer points at. Say which path you
took and why before changing anything.

Read this from the binary you will run, `wtc docs setup.md`, so that it
matches that version. Without wtc yet, install a
[release](https://github.com/lcorneliussen/taba/releases) first: the
`taba_<version>_<os>_<arch>.tar.gz` for the machine (`wtc_…` before the
taba releases; it holds both `taba` and `wtc`), checked against
`checksums.txt`, or `mise use -g github:lcorneliussen/taba@<version>`.

## 1. Where does wtc run?

| Runtime | How to tell | What a collection is |
| --- | --- | --- |
| **Terminal** | `wtc host` prints `terminal` | a folder of worktrees, used from any shell or editor |
| **herdr** | `wtc host` prints `herdr`, or the person works in herdr panes | the same folder, opened as a herdr workspace with `wtc open` |
| **OpenClaw** | `wtc host` prints `openclaw`, or the target is an OpenClaw gateway | managed worktrees of the gateway, one conversation per task, with a card and a Collection panel in the Control UI |

`wtc host` describes the machine you run on. If you are preparing another
machine, such as a gateway's image in a repository that CI/CD builds, ask
which runtime that machine is for.

## 2. Which harness, if any?

A harness is a Git repository with the workspace's registry of repositories,
instructions, skills, hooks and tool pins. Look for one before asking: a
`harness/` beside the current checkout, a `.harness-repos.yml`, or a
repository the person's organisation names as its harness.

| Answer | Terminal or herdr | OpenClaw |
| --- | --- | --- |
| **None**: a task across a few repositories | `docs/getting-started.md`, "Ad-hoc collection, no harness" | The default. Collections are ad hoc; repositories are named as `owner/name`, URLs or gateway projects. |
| **An existing harness** | `wtc docs bootstrap.md`, from step 2 with its remote | Add the harness as a gateway project and configure it under `plugins.entries.wtc.config.harnesses` ([Harnesses](https://github.com/lcorneliussen/taba/blob/main/docs/openclaw.md#harnesses)). Collections then resolve repositories through it. |
| **A new harness** | `wtc docs bootstrap.md`, from step 1. The person creates the remote. | As for an existing one, once the remote exists. |

With several harnesses, ask which one this environment belongs to. Never
create a harness remote, publish a repository, or change another team's
harness without the person's go.

## 3. Who maintains the environment?

- **By hand, on this machine**: follow the guide as written.
- **Built by CI/CD**, such as a gateway image or a provisioning repository:
  make the change there, as code, and let the pipeline apply it. Pin every
  version (wtc, plugins). An upgrade is then a pin bump.
  For OpenClaw, follow "Gateway image and CI/CD" in
  [`docs/openclaw.md`](https://github.com/lcorneliussen/taba/blob/main/docs/openclaw.md#gateway-image-and-cicd).

## Then

- **Terminal or herdr**: `wtc doctor` in the first collection checks it.
- **OpenClaw**: follow the
  [install](https://github.com/lcorneliussen/taba/blob/main/docs/openclaw.md#install).
  Then run `wtc doctor --openclaw` on the gateway host (from an agent's
  command, plain `wtc doctor` does the same). It checks Git, `gh`, the
  plugin and its version, the Control UI setting, and whether each agent's
  `AGENTS.md` mentions wtc.
- Report what you set up, which versions you pinned, and what is left for
  the person: their own forge logins, a harness remote they still have to
  create, a pipeline run.
