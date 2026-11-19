# TWG Bitbucket pull-request fixtures

Synthetic, sanitized fixtures for `twg bitbucket pull-requests get` as
documented by TWG 1.4.4 help (`twg help describe "bitbucket pull-requests get"`
and the agent-output reference). No fixture was captured from a live account:
the resource body follows the public Bitbucket Cloud pull-request resource, and
the batch (`items`/`partial`) and failure (`ok:false` with `error.code`) shapes
follow the TWG help contract. Error codes such as `TWG_AUTH_REQUIRED` are
placeholders, not documented TWG codes; the reader treats any failure as
unavailable and only uses code/kind to choose a fixed diagnostic category.
See docs/forges.md.
