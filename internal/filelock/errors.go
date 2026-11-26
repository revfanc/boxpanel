package filelock

import "errors"

// ErrBusy reports that another live process holds the lock.
var ErrBusy = errors.New("lock held by another process")
