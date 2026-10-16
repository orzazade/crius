package store

import (
	"errors"
	"fmt"
)

// Refused is bad input: nothing was written.
type Refused struct{ msg string }

func (e *Refused) Error() string { return e.msg }

func refused(format string, args ...any) error { return &Refused{fmt.Sprintf(format, args...)} }

// Partial means the main write landed but a follow-up write did not; the message says what to do.
type Partial struct{ msg string }

func (e *Partial) Error() string { return e.msg }

func IsRefused(err error) bool { var r *Refused; return errors.As(err, &r) }

func IsPartial(err error) bool { var p *Partial; return errors.As(err, &p) }
