//go:build !cgo || !sqlite_fts5

package buildprofile

// Complete is false for a plain `go run .` or any build missing CGO/FTS5.
const Complete = false
