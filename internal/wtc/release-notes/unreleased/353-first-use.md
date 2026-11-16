## Services

- **Keep first-use tunnel controls usable.** Disabling a named tunnel before the first service start no longer leaves a notification file that blocks runtime creation. Normal setup also recovers the empty notification left beside a valid owned tunnel journal by 0.2.5; unrelated or unsafe state remains refused. Unconfigured or invalid runtime state waits for the next status poll; configured runtimes still notify immediately. ([#353](https://github.com/lcorneliussen/taba/issues/353), [#401](https://github.com/lcorneliussen/taba/pull/401))
