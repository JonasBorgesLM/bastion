# ADR-0013: Provenance attests a reproducible source tarball, not a compiled artifact

## Status
Accepted

## Context

`release.yml` already refuses to publish an unsigned tag (`git verify-tag`
against `.github/allowed_signers`). That answers one question: *did the
maintainer authorize this tag.* It does not answer a different one a consumer
resolving `github.com/JonasBorgesLM/bastion@v0.1.0` might reasonably ask: *was
this the exact source that passed the stated checks* — `go vet`, `go test
-race`, `govulncheck`, the zero-dependency and no-`net/http` boundary, before
being published — rather than something assembled by hand on a laptop and
tagged after the fact. Tag signing proves authorization; it says nothing about
which pipeline, if any, verified the content before the signature was made.
`actions/attest-build-provenance` is the tool for that second question (issue
#51).

**The tool assumes a shape this project does not have.** Every example and
most of the tooling around build provenance targets a build step that
transforms source into a binary or container image — the interesting claim is
"this binary came from this source, via this pipeline," closing a gap between
what a human can read and what actually runs. bastion has no build step. It is
imported as source; the thing a consumer's `go build` compiles is the same
`.go` files a reader already sees on GitHub. Attesting a Go binary bastion
does not produce, to answer a build-provenance question bastion does not have,
would be attesting the wrong thing to look complete.

**What a Go consumer actually resolves isn't produced by this pipeline
either.** `go get` does not download a GitHub Release asset. It asks
`proxy.golang.org`, which serves a zip it assembled itself from the tagged git
tree, and checks it against `sum.golang.org`'s append-only transparency log.
Nothing `release.yml` uploads is in that path. Content-integrity — "the bytes
I got are the bytes everyone else got, unmodified since first fetch" — is
already handled, for every consumer, by the checksum database, with no action
on this repository's part. An attestation cannot add to that; it answers a
different question the checksum database does not: *did this content pass
bastion's own stated gates before being tagged*, which is a claim about
process, not about bit-for-bit integrity after the fact.

## Decision

**Adopt it, scoped to that process claim.** `release.yml`'s `release` job
produces a plain (uncompressed) tarball of the exact tagged tree —
`git archive --format=tar --prefix=bastion-${TAG}/ -o bastion-${TAG}.tar
${TAG}` — and attests that file with `actions/attest-build-provenance`. The
tarball is also uploaded as a GitHub Release asset, so verifying it is a
download plus one command, not a reproduction step, though reproduction is
also possible (see Consequences).

The claim this attestation actually carries: *this exact tarball, digest
`sha256:…`, was produced by this specific `release.yml` workflow run, at this
commit, after `verify` passed* — `go build`, `go vet`, `go test -race`,
`govulncheck`, the zero-dependency and `net/http` boundary checks, and
`check-docs.sh`, all gated ahead of it as `needs: verify`. It is deliberately
not framed as "this binary is authentic" (there is no binary) or "these are
the exact bytes the Go proxy will serve you" (the proxy assembles its own zip
independently; this tarball is not it). It is a verifiable claim about which
process touched this content before it was tagged, complementary to — not a
replacement for — the tag signature's claim about who authorized it.

`SECURITY.md` documents both, side by side, since they answer different
questions a consumer might have:

```bash
# Was this tag authorized by the maintainer?
git config gpg.ssh.allowedSignersFile .github/allowed_signers
git verify-tag v0.1.0

# Did this content pass bastion's own CI gates before being tagged?
gh attestation verify bastion-v0.1.0.tar -R JonasBorgesLM/bastion
```

## The alternative that was rejected

**Attesting a build artifact anyway** — `go build ./...`'s output, or a
`go vet`/coverage report, standing in for "the thing that was built." Rejected
because a library ships no binary a consumer runs; attesting one manufactured
only to have something to attest would be answering a question nobody asked
("is this binary authentic") while dodging the one this repository actually
has an answer for ("did this source pass the stated checks").

**Skipping provenance entirely, on the grounds that `sum.golang.org` already
covers integrity.** Considered seriously, and rejected on the distinction
drawn above: the checksum database proves the content has not changed since
first resolution — it says nothing about whether that content ever passed a
test, a vulnerability scan, or the dependency boundary this library's whole
value proposition rests on. For a library whose stated property is "audit the
dependency surface, not the binary," a verifiable record that its own CI
actually checked what it claims to check is exactly the kind of evidence this
project's stated priorities should produce, once the tooling to do so exists
and its cost is one workflow step.

## Consequences

- A consumer who wants the *reproducibility* guarantee, not merely the
  attested-by-CI one, can run the identical `git archive` command themselves
  against the tagged commit and compare the resulting tarball's digest to the
  one the attestation names. This is expected to hold in practice, but is not
  a hard cryptographic guarantee the way a hermetic, fully pinned build
  process gives one for a compiled artifact: `git archive`'s exact byte
  output for a given tree has historically been stable across ordinary git
  versions, but nothing in this ADR proves it is invariant across every past
  and future git release. State this honestly rather than implying a stronger
  guarantee than what was actually checked.
- The attested tarball is a GitHub Release asset, not part of what `go get`
  resolves. A consumer who only ever runs `go get` never sees it and is
  unaffected either way; it exists for the consumer who specifically wants to
  verify provenance before vendoring or auditing a release.
- `release`'s job permissions gain `id-token: write` and
  `attestations: write`, alongside the existing `contents: write`. Both are
  scoped to the one job that already runs only on a tag push gated by
  `verify`.

## Reopening criterion

Not deferred. If a future consumer need surfaces that this scope does not
serve — verifying the exact bytes `proxy.golang.org` serves, say — that is
new evidence this ADR's boundary was drawn in the wrong place, and reopens it.
