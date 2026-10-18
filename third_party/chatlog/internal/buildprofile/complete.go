//go:build cgo && sqlite_fts5

package buildprofile

// Complete reports whether the executable carries Chatlog's required runtime
// profile. The sqlite_fts5 tag also compiles the MMFtsTokenizer registration.
const Complete = true
