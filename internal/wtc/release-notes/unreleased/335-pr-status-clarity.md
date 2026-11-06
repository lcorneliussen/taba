## Status

- **PR signals line up and explain their meaning.** Status tables pad PR numbers across the listed rows, reserve link space, and keep checks, mergeability, and reviews in a consistent order. Wide PR tables use readable headings; keyboard help explains the symbols, including unknown facts, pending checks, comments, and requested changes ([#335](https://github.com/lcorneliussen/wtc-cli/issues/335)).

- Repository and PR table cells in the terminal status view now share the same symbols: clean mergeability shows ✓, waiting reviews use ●, requested changes use ✗, and blocked mergeability uses the failure tone. Comments remain ✎ and unknown facts remain ·. Markdown output is unchanged.

- Compact repository rows reserve draft/conflict markers and full PR numbers before descriptive columns, and retain checks, mergeability, and review signals whenever the complete cell fits.

- A status guide explains complete Git, PR, review and build representations, provider coverage, build containment, freshness and compact layouts.

- Wide repository rows place the conflict label and draft marker after the fixed checks/merge/review slots. Compact rows can place draft state beside the number and use the merge signal for conflict to retain aligned C/M/R; prefix markers preserve state and identity when optional signals cannot fit.
