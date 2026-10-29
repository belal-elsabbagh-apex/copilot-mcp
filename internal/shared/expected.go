// Package shared holds the tiny cross-domain helpers every other package may import. It imports
// nothing from this module, so it can never be part of an import cycle.
package shared

import (
	"errors"
	"fmt"
)

// Expected marks a known, user-actionable failure — not a bug. The feedback layer classifies any
// error whose chain holds one as expected by identity (no reportIssue block), independent of its
// message wording. Port of the TS ExpectedError base class: a TS subclass becomes a Go type that
// implements this interface (usually by embedding ExpectedError).
type Expected interface {
	error
	Expected()
}

// ExpectedError is the plain Expected: a message and nothing else. Embed it in a richer error
// type to make that type Expected.
type ExpectedError struct {
	Msg string
}

func (e *ExpectedError) Error() string { return e.Msg }

// Expected implements the Expected marker.
func (*ExpectedError) Expected() {}

// Expectedf originates a plain expected error.
func Expectedf(format string, args ...any) error {
	return &ExpectedError{Msg: fmt.Sprintf(format, args...)}
}

// IsExpected reports whether any error in err's chain is Expected.
func IsExpected(err error) bool {
	_, ok := errors.AsType[Expected](err)
	return ok
}
