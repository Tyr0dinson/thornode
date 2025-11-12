package common

import (
	"errors"
	"fmt"
)

// FatalReason identifiers used by chain clients.
const FailReasonXRPDestinationTagNeeded = "xrp_destination_tag_required"

// FatalError represents an error condition that should not be retried because
// it is expected to fail deterministically.
type FatalError struct {
	Reason string
	Err    error
}

func (e FatalError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("fatal error: %s", e.Reason)
	}
	return e.Err.Error()
}

func (e FatalError) Unwrap() error {
	return e.Err
}

// IsFatal returns true when the provided error (or any wrapped error) is a
// FatalError.
func IsFatal(err error) bool {
	var fatal FatalError
	return errors.As(err, &fatal)
}

// FatalReason extracts the fail reason from the supplied error if it is a
// FatalError. When the error is not fatal an empty string is returned.
func FatalReason(err error) string {
	var fatal FatalError
	if errors.As(err, &fatal) {
		return fatal.Reason
	}
	return ""
}

// NewFatalError wraps the provided error in a FatalError with the supplied
// reason. If the err is already a FatalError the existing reason is preserved
// when it is non-empty.
func NewFatalError(reason string, err error) FatalError {
	if fatal, ok := err.(FatalError); ok {
		if fatal.Reason == "" && reason != "" {
			fatal.Reason = reason
		}
		return fatal
	}

	return FatalError{
		Reason: reason,
		Err:    err,
	}
}
