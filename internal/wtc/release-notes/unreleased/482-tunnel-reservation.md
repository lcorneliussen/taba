## Services

- **A configuration problem in another collection no longer blocks Tailscale
  tunnels.** The workspace-wide listener check reads other collections' tunnel
  journals without validating their environment, and still refuses conflicting
  or unreadable reservations, now naming the collection and the reason.
  ([#482](https://github.com/lcorneliussen/taba/issues/482)).
