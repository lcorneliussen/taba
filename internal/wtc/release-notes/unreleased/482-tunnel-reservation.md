## Services

- **An unrelated problem in another collection no longer blocks Tailscale
  tunnels.** The workspace-wide reservation check now reads other collections'
  tunnel journals without validating their environment, so a stale
  `.env.collection` elsewhere no longer refuses with `cannot verify local tunnel
  reservation owner`. Conflicting or unreadable reservation evidence still
  refuses, and the refusal now names the collection and the reason.
  ([#482](https://github.com/lcorneliussen/taba/issues/482)).
