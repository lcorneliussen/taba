## Status

- **Scrolling the status view responds promptly again.** Wheel, arrow and page
  keys appeared only on the next once-per-second paint, so each step lagged by
  up to a second. The view now paints at up to 30 frames per second; idle
  frames with an unchanged view write nothing, and scrolling starts no provider
  or Git work.
  ([#481](https://github.com/lcorneliussen/taba/issues/481)).
