## Status

- **Text in the status view can be selected and copied.** Press `v` for a
  selection view: it writes the complete current output, including rows
  outside the viewport, to the terminal's normal screen with mouse reporting
  off, so ordinary terminal or multiplexer selection, scrollback and copy work.
  Enter, `q` or `v` returns; refreshes already running continue and keep their
  observation times. Click targets now act on release at the pressed row and
  cell, so a drag that starts on a link no longer opens it.
  ([#473](https://github.com/lcorneliussen/taba/issues/473)).
