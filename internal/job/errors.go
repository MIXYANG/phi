package job

import "errors"

// Sentinel errors returned by job operations.
var (
	ErrInvalid       = errors.New("job: invalid request")
	ErrNotFound      = errors.New("job: not found")
	ErrDepth         = errors.New("job: nesting depth exceeded")
	ErrBusy          = errors.New("job: concurrency limit reached")
	ErrClosed        = errors.New("job: manager closed")
	ErrNotRunning    = errors.New("job: not running")
	ErrWaitTimeout   = errors.New("job: wait timed out")
	ErrMailboxFull   = errors.New("job: steering mailbox full")
	ErrSteeringLimit = errors.New("job: steering round limit reached")
)
