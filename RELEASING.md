# Releasing

A library's version number is its contract. This file exists because the
ecosystem has already paid for getting a release wrong once, and the fix was
never "be more careful".

## The rule that costs the most to learn

**A tag is never deleted or moved once it has been fetched.** The Go module
proxy caches an immutable copy the first time anyone resolves it, including a CI
runner. Moving the tag afterwards means two different trees answer to one
version, and the proxy will keep serving the first one to everybody who is not
you.

A release that turns out to be wrong is retracted and superseded, never
rewritten. `cairn`'s `go.mod` carries a `retract` line for exactly this reason.

## Before tagging

```bash
go build ./... && go vet ./... && go test -race ./...
golangci-lint run ./...
./.github/scripts/check-docs.sh
gofmt -l .                      # must print nothing
```

Then check the things a green build does not:

- **The `boundaries` job's claims still hold.** No `require`, no `go.sum`,
  nothing reaching `net/http`. A dependency acquired in a release is a
  dependency every consumer inherits permanently.
- **No open question has been silently resolved in code.** If the diff since the
  last tag decided something listed in [`docs/adr/`](docs/adr/README.md), the
  ADR is written first — after the code is a record of what happened, not a
  decision.
- **Every exported identifier added since the last tag has a doc comment that
  states its contract.** The lint job checks that one exists; it cannot check
  that it is true.

## Versioning

Semantic versioning, and **pre-1.0 does not mean the version is decorative**:

- `v0.x.y` — the API may change between minor versions, and that is stated in
  the README rather than assumed.
- A breaking change to an exported identifier bumps the minor while below 1.0,
  and is called out in the release notes with the migration.
- **Renaming or changing the signature of the entry point is a breaking change
  for every caller there is.** It is the reason that decision is an ADR before
  B1 rather than a preference discovered at B7.

`v1.0.0` says the API is stable and will not break without a major bump. Do not
tag it while any question in [`docs/adr/`](docs/adr/README.md) is still open.

## Tagging

```bash
git checkout main && git pull
git tag -s v0.1.0 -m "bastion v0.1.0"
git push origin v0.1.0
```

Tags are signed. Then create the GitHub Release from the tag, with notes that
say what changed and what it breaks — not a list of commit subjects, which the
reader can already get from `git log`.

## If a release is wrong

1. Do not delete the tag. Do not move it.
2. Fix forward: `retract` the bad version in `go.mod`, with a comment saying
   why, and tag the next patch.
3. The retraction comment is read by humans running `go list -m -retracted`, so
   write it for them: what was broken, not what the fix was.

## Not yet automated

## The release workflow

`.github/workflows/release.yml` runs on every `v*` tag push. It fails closed
at every step it can, deliberately including the very first release:

- Rebuilds, vets, tests and `govulncheck`s the tag exactly as a consumer
  resolves it, and re-checks the zero-dependency and no-`net/http` claims
  `ci.yml`'s `boundaries` job already checks on every push — asserted again
  here because a release, unlike a branch, cannot be fixed by force-pushing.
- Generates the API diff against the previous tag with `gorelease`, and
  uploads it as an artifact the `release` job assembles into the GitHub
  Release notes — a changelog generated from what actually changed, not
  written from memory.
- **Refuses to publish an unsigned tag.** `.github/allowed_signers` ships
  empty on purpose: `git verify-tag` fails until a human adds their own SSH
  public key there, per the comment in that file. An unsigned release is one
  nobody can attribute, and a check that only warns gates nothing — this one
  has not yet been watched succeed, because there has not yet been a key to
  verify it against.

This has not been seen to publish a real release. It has been checked with
`actionlint` and each shell step's syntax verified independently, and its
logic mirrors `cairn`'s own `release.yml` closely enough to inherit that
one's track record — but "verified by inspection" and "watched succeed" are
different claims, and only the second one is true once the first tag has
actually gone through it.
