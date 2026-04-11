// Package version exposes immutable build metadata for a running service.
//
// It is intentionally narrow in scope: the package reports version, commit,
// build date, and Go toolchain information, but does not implement release
// management or update distribution.
//
// Most applications use version.Get() directly or wire Get().Handler() into an
// HTTP endpoint.
package version
