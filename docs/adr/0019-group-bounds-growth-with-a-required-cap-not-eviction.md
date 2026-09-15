# ADR-0019: Group bounds growth with a required cap and an explicit error, not eviction

## Status
Accepted

## Context

FR-10 gives named breakers; it gives no way to *get one by name*. The shape
almost every real consumer needs — one breaker per downstream host, per
tenant, per endpoint, created on first use — means every consumer today
writes the same `map[string]*Breaker` plus a mutex plus a double-checked
get-or-create, making their own concurrency mistakes in it (issue #56).

**This is not IR-03's forbidden global state, and the record should say so
directly rather than leave the wording to be misread.** IR-03 forbids
*package-level* mutable configuration, a registry, an `init()` — state no
caller chose to create and cannot avoid sharing. A `Group` a caller
constructs and holds is an ordinary value with an ordinary lifetime, exactly
like a `*Breaker` itself: nothing here is reachable except through a value
the caller owns, and two different `Group`s share nothing. "Two breakers
sharing a name are still two independent breakers" stays true — a `Group`
only means one *caller* chose to have one `*Breaker` answer to one key,
which is a decision the caller made, not one the library imposed.

**The real question, and the reason this needs an ADR rather than a map:**
a `Group` keyed by something attacker-influenced — a tenant id, a `Host`
header, anything reachable from outside — grows without limit if nothing
bounds it. Each `*Breaker` is small and fixed-size, but "small times
unbounded" is still unbounded, and a memory-exhaustion vector reachable from
outside is exactly the kind of failure `SECURITY.md` already asks this
library to take seriously even though it holds no secret of its own.

## Decision

`NewGroup` takes a required cap, not an optional one — `maxKeys` is a
positional argument, not a `GroupOption` behind a default a caller could
overlook:

```go
func NewGroup(maxKeys int, opts ...Option) (*Group, error)
```

`opts` reuses [`Option`](../../options.go) exactly — the same values `New` itself
takes — applied to every `*Breaker` the group creates; there is no separate
`GroupOption` type. `maxKeys <= 0` and an invalid `Option` both return
[`ErrInvalidConfig`](../../errors.go), validated once at construction (the same
"assemble, validate, fail before anything runs" discipline `New` already
follows) rather than deferred to the first `Get` that happens to trip over
it.

`Get` creates a `*Breaker` on first use, named after the key (FR-10 already
gives per-breaker identity in hook events — a `Group` needs no separate
mechanism to make a key distinguishable in the event stream, `ev.Name` is
already it). Once the group already holds `maxKeys` breakers, `Get` for a
**new** key returns `ErrGroupFull` rather than creating one — the cap is
enforced as a hard, visible failure a caller must handle, not a silent
eviction of whichever breaker happened to be used least recently:

```go
func (g *Group) Get(key string) (*Breaker, error)
func (g *Group) Delete(key string)
func (g *Group) Len() int
```

`Delete` exists so a caller who *does* know a key has left for good — a
tenant offboarded, a host decommissioned — can free its slot deliberately,
complementing the cap rather than replacing it. `Len` exists for the same
reason `Counts` exists on `Breaker` (ADR-0015): a caller can poll "how close
is this to its own limit" without having to catch a specific error first.

**`SECURITY.md` states the cap is a backstop, not the primary defense.** A
`Group` with `MaxKeys=10000` keyed directly by an unauthenticated `Host`
header still lets an attacker occupy all 10000 slots with garbage keys,
denying legitimate ones — a cap bounds the *library's* memory, it does not
make an attacker-controlled key space safe to use unfiltered. The
recommended, stronger mitigation is a caller-validated key space (an
allowlist drawn from configuration, not from the request) wherever the key
would otherwise come from an untrusted source; the cap is what keeps a
bug or an oversight in that validation from becoming unbounded rather than
merely bounded-and-large.

Every `Group` member shares one `Option` set. Per-key configuration —
tenant A gets a different `FailureThreshold` than tenant B — is not
supported directly: a caller with that need constructs multiple `Group`s,
one per configuration class, and picks which one to `Get` from at the call
site. This composes with what already exists rather than adding a new
per-key-override mechanism to `Group` itself for a need no consumer has
stated yet.

`Group` exposes only per-key state (`Get(key).Counts()`,
`Get(key).State()`) plus `Len`. No aggregate rollup across members — a
"total consecutive failures across every tenant" number mixes unrelated
circuits into a figure nothing in this library's stated use case asks for,
and a caller who wants one can already compute it by iterating their own
known key set and calling `Counts()` per key.

Concurrency: one `sync.Mutex` guards the map for the full duration of a
get-or-create. `New` is cheap and does not block, so there is no benefit to
a more elaborate per-key locking scheme, and a plain mutex trivially
guarantees that concurrent `Get` calls for the same new key create exactly
one `*Breaker` and every caller observes the same instance — the property
issue #56 asks to be tested under `-race`, not merely assumed.

## The alternative that was rejected

**LRU eviction instead of a hard cap.** Rejected: a `*Breaker`'s value is
the evidence it has accumulated — evicting it silently under load discards
that evidence and re-admits the next call into a fresh `StateClosed`
breaker with no memory of why the old one tripped, which is a confusing,
hard-to-explain support question ("why did this circuit reset itself") for
a mitigation whose whole point was safety. An explicit error a caller must
handle is a worse ergonomic experience than silent eviction in the common
case and a strictly better one the one time it actually matters.

**Keys restricted to a caller-supplied allowlist, enforced by `Group`
itself.** Considered, and folded into the `SECURITY.md` guidance as the
*recommended* practice rather than an enforced mechanism: `Group` has no way
to know what a caller's legitimate key space is, so enforcing an allowlist
inside the library would mean adding configuration (the allowlist itself,
its update path) for something a caller can already do perfectly well one
layer up, before a key ever reaches `Get`.

## Consequences

- A caller who wants effectively unbounded growth can still set `maxKeys` to
  a very large number — but has to write that number down and mean it,
  rather than getting it as an implicit default.
- `ErrGroupFull` is a new sentinel a `Group` consumer must handle, the same
  way `ErrOpenState`/`ErrTooManyRequests` are already sentinels an `Execute`
  consumer handles.
- `New`'s own option-assembly-and-validation is extracted into a small
  unexported helper both `New` and `NewGroup` call, so the two can never
  silently drift on what counts as a valid `Breaker` configuration.

## Reopening criterion

A real workload where a hard cap with an explicit error is measurably worse
than eviction would have been — not a preference for eviction's ergonomics
in the common case, which this ADR already weighs against the one time it
matters. Today, none has been reported.
