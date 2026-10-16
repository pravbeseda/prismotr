# Spec: <subsystem>

- **Status:** draft | approved (reviewed and in force) | superseded
- **Owns:** the packages this spec governs, e.g. `internal/config`
- **Decisions:** ADRs this behaviour follows from

## Purpose

One paragraph: what this subsystem is responsible for, and what it explicitly is not.

## Behaviour

The table is the contract. One row = one test. Keep rows observable: an input the caller
can produce, an outcome the caller can see. A row that names a package, a goroutine or a
timeout is a test, not a spec; such constraints belong in the tests.

| Given | When | Then |
|---|---|---|
| a file with no `sites` | the daemon starts | the menu is empty and the daemon runs |

Anchor each group of rows with a heading so tests can cite it (`spec: config.md#reload`).

## Invariants

Statements that must hold after every operation, whatever the sequence of events.

## Edge cases

The situations that are easy to leave undecided: a missing file, a reload that fails, a
value at the boundary.

## Out of scope

What this subsystem deliberately does not handle, and which spec handles it instead.

## Open questions

Empty in a spec that is in force.
