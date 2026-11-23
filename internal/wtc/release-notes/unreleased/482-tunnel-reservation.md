## Services

- **An unrelated problem in another collection no longer blocks Tailscale
  tunnels.** Before reserving a Tailscale listener, a named tunnel reads every
  collection's tunnel journal in the workspace. It used to open each of those
  collections in full, so a stale `.env.collection` in any one of them refused
  every Tailscale tunnel with `cannot verify local tunnel reservation owner`,
  even when no collection had a tunnel. The check now reads each journal from
  the collection directory without validating that collection's environment.
  It still refuses when a journal holds a conflicting reservation, or cannot be
  read, parsed or verified as that collection's own. The refusal now names the
  collection and the reason.
  ([#482](https://github.com/lcorneliussen/taba/issues/482)).
