## Status

- **A keychain gh login shares status observations.** Status panes using a
  gh login whose token is in the system keychain now share cached PR
  observations, keyed by the gh configuration directory, host and user. Before,
  each pass threw its rows away and read again
  ([#562](https://github.com/lcorneliussen/taba/issues/562)).
