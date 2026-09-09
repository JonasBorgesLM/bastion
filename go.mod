module github.com/JonasBorgesLM/bastion

// Go 1.24 is the floor, and it is the *lowest viable* version rather than the
// newest available (NFR-07). A library's `go` directive is a compatibility
// promise about who may import it: raising it strands consumers without
// patching anything for them. It moves only when something concrete makes it
// non-viable, and that reasoning is written as an ADR before this line changes.
//
// The floor comes from the ecosystem rather than from this module's own needs:
// the generics this library is built on landed in 1.18 and nothing here reaches
// past it. It matches moat and cairn so a service importing all three has a
// single floor to satisfy.
//
// This module has no `require` block and is not supposed to acquire one
// (NFR-03). The `zero-dependencies` job in .github/workflows/ci.yml enforces
// that; a policy that is not checked is a preference.
go 1.24
