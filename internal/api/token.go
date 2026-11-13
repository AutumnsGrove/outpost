package api

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
)

// newFormToken is the anti-CSRF value the page puts in its POST bodies.
//
// It is not authentication — the tailnet is, and every other service on this box
// works the same way. It stops a page somewhere else on the internet from
// submitting a form that rents a server: a cross-origin form post cannot read
// this value first. Issued once per process, never written to disk.
func newFormToken() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return ""
	}
	return hex.EncodeToString(buf)
}

// checkToken compares the submitted token with the process's in constant time.
// An empty expected token (a failed random read) refuses everything rather than
// accepting everything.
func checkToken(got, want string) bool {
	if want == "" || got == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}
