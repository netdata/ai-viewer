package canonical

import "errors"

// FatalScanError marks a Scan failure that makes Tail unsafe for this process
// run. Source supervisors record scan_failed and do not enter Tail.
type FatalScanError struct {
	Err error
}

func (e *FatalScanError) Error() string {
	if e == nil || e.Err == nil {
		return "fatal scan error"
	}
	return e.Err.Error()
}

func (e *FatalScanError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// NewFatalScanError wraps err in the FatalScanError marker.
func NewFatalScanError(err error) error {
	return &FatalScanError{Err: err}
}

// IsFatalScanError reports whether err contains a FatalScanError marker.
func IsFatalScanError(err error) bool {
	var fatal *FatalScanError
	return errors.As(err, &fatal)
}
