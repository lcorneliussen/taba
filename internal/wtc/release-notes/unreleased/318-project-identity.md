## OpenClaw

- Reuse existing gateway project IDs after a verified repository rename, even
  when clone folders and older collection registries retain their old names.
  Registry aliases and permitted sandbox metadata resolve old/new names without
  creating another clone or changing existing worktree paths.
  ([#318](https://github.com/lcorneliussen/wtc-cli/issues/318)).
