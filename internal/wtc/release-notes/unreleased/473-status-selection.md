## Status

- **Status text can be selected with an ordinary drag.** The live view no
  longer captures the mouse by default, so dragging and double-clicking select
  the rows on screen with the terminal or multiplexer, and links open with the
  terminal's modifier-click. Where the terminal reports alternate scroll
  (DEC mode 1007) off, status turns it on while it runs so that the wheel
  scrolls one row per step, and turns it off again on exit. A terminal that
  already has the mode on, or does not answer, keeps its own setting.
  `--mouse` keeps the previous in-app wheel, click targets and hover text;
  those click targets now act on release, so a drag that starts on a link no
  longer opens it. Press `v` to copy beyond the visible rows: the complete
  output is written to the terminal's normal screen for ordinary selection
  and scrollback, and Enter, `q` or `v` returns.
  ([#473](https://github.com/lcorneliussen/taba/issues/473)).
