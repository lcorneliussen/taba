## Status

- **One plain request budget per forge.** Every provider read takes one token
  from the forge's shared bucket or is deferred. Bitbucket confirmations no
  longer queue or hold tokens back, so no token stays unspent while a read
  waits; a rate-limit response still pauses the forge for its `Retry-After`.
  Manual `r` waits for the next refill without priority over background reads.
  The Bitbucket open-PR list is reused for 180 seconds instead of 90 (GitHub
  keeps 90); `WTC_FORGE_CACHE_AGE` still sets one age for both. Status panes
  still on an older release keep deferring their own reads behind their queue;
  restart them on this release
  ([#562](https://github.com/lcorneliussen/taba/issues/562),
  [#564](https://github.com/lcorneliussen/taba/pull/564)).
