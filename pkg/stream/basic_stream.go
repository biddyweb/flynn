package stream

import (
	"sync"
	"sync/atomic"
)

/*
	Initializer for a Basic Stream.

	Suggested usage is to only return the 'Stream' interface from
	your worker method (see also the package examples); however,
	you'll need the stream.Basic type reference available in your
	worker method so that it may modify the error status and
	select on the stop chan.
*/
func New() *Basic {
	b := &Basic{
		StopCh: make(chan struct{}),
	}
	b.closed.Store(false)
	return b
}

/*
	Basic is a common implementation of Stream.

	Internally it contains:

	- a channel that indicates stopping, which the producer side
	of the stream should use in a select (see the package examples),
	- an error field that the producer ride of the stream should
	set in case of problems (just before closing the associated
	data channel).
*/
type Basic struct {
	StopCh chan struct{}
	Error  error

	closed    atomic.Value // bool
	closeOnce sync.Once
}

func (s Basic) Close() error {
	s.closeOnce.Do(func() {
		s.closed.Store(true)
		close(s.StopCh)
	})
	return nil
}
func (s Basic) IsClosed() bool {
	return s.closed.Load().(bool)
}

func (s Basic) Err() error {
	return s.Error
}
