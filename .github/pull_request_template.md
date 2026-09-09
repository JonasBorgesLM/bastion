<!--
Title must be a Conventional Commit -- it becomes the subject on a squash merge:
  feat(breaker): open the circuit on the Nth consecutive failure
-->

## What and why

<!-- The diff says what changed. Say why it needed to. -->

## Traceability

- Requirements: <!-- FR-01, NFR-05 -- or "none" with a reason -->
- Decisions: <!-- ADR-0002 -- or "none" -->
- Closes: <!-- #12 -->

## Correctness checklist

Delete the lines that do not apply; do not delete the ones that do.

- [ ] Every new state transition has a test that has been **seen to fail**
      before it was seen to pass. The negative control is noted in a comment
      above the test.
- [ ] No test asserts a transition by sleeping. Time moves because the fake
      clock was advanced (NFR-05).
- [ ] `go test -race` passes, and any new shared state is held under the mutex
      that owns it (NFR-01).
- [ ] A context cancelled by the caller is still counted as neither a success
      nor a failure of the remote dependency (FR-05).
- [ ] No new dependency, and nothing new reaching `net/http` (NFR-03, IR-01).
- [ ] Hooks are still called synchronously, and a nil handler is still a no-op
      rather than a panic (IR-02).

## Documentation

- [ ] Exported identifiers have doc comments stating the contract, not the
      signature.
- [ ] A structural decision here has an ADR, written **before** the code.
- [ ] An existing ADR was amended, never rewritten.
- [ ] `./.github/scripts/check-docs.sh` passes locally.
