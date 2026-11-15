## Upgrade rehearsal

- **Report failed observations and verify release selection.** Rehearsals now fail when a required snapshot command fails, retain its diagnostics, and validate release versions and executable paths inside the private mise installs tree before running them. Invalid target releases skip dependent catch-up and registry follow-up steps. Hooks and caller authentication retain their existing trust boundary. ([#332](https://github.com/lcorneliussen/taba/issues/332)).
