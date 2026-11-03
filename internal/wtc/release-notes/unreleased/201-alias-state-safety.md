## Fixes

- **Unsafe alias state is refused without modifying linked targets or losing retirement guards.** Malformed retirement markers remain leftovers, selected runtime and service directories are checked before planning or edits, and refused commands do not write service-change notifications. Safe partial service changes still notify status ([#201](https://github.com/lcorneliussen/wtc-cli/issues/201)).
