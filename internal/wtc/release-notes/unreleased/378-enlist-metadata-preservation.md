## Fixes

- Re-enlisting a PR preserves its existing title when empty and its URL when omitted, including URL-only references and verified repository aliases. Explicit values still replace metadata, and omitted branches retain their existing checkout defaults. Native publication retries preserve an already recorded title. ([#378](https://github.com/lcorneliussen/taba/issues/378), [#413](https://github.com/lcorneliussen/taba/pull/413)).
