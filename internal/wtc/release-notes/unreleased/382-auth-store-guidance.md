## Documentation

- **Clarify tool config scoping.** Explain directory-presence opt-in, environment precedence and the limits of filesystem routing: token variables and secure stores can remain eligible. Preserve stores when changing configuration instead of deleting them. Generator, authentication and migration behavior are unchanged; broader credential isolation remains open. ([#382](https://github.com/lcorneliussen/taba/issues/382)).
