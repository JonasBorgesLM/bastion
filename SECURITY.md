# Security

## Reporting

Report a vulnerability through GitHub's private advisory form on this
repository: **Security → Report a vulnerability**. Please do not open a public
issue for something exploitable.

Include what you did, what happened, and what you expected. A reproducer beats a
description.

## What bastion is, from a security point of view

**bastion is an availability control, not a security control.** It holds no
secret, stores nothing, validates no input from an untrusted party, and makes no
authentication or authorization decision. There is deliberately no threat model
in this repository, and that absence is a statement rather than an omission —
see [`.github/scripts/check-docs.sh`](.github/scripts/check-docs.sh), where the
threat-model checks are switched off on purpose.

That makes the interesting failure modes different from `moat`'s or `cairn`'s.
The ones worth reporting here are the ones where the library becomes the
outage:

- **A deadlock.** Any path that can leave `b.mu` held — a panic through a held
  lock above all (FR-11) — blocks every subsequent call to that dependency
  forever.
- **A stranded circuit.** Any state the breaker can enter and not leave, such as
  a half-open probe that never returns taking the last allowance with it
  (FR-12).
- **A goroutine or timer the host cannot stop.** bastion is supposed to own
  neither.
- **Unbounded growth.** Anything that accumulates per call or per breaker name.

A denial of service caused by the component installed to prevent one is the
serious bug in a library of this shape, and it is what this file is asking you
to look for.

## Scope

- **In scope:** the code in this module.
- **Out of scope:** how a host composes bastion with anything else, including
  the response mapping and rate-limit interaction described in
  [`REQUIREMENTS.md`](REQUIREMENTS.md) §5.1 — those are the host's code. Report
  those to the service that owns them.

## Supported versions

Pre-1.0. Only the latest tag receives fixes. There are no backports, and there
is nothing to back-port to yet.
