// SPDX-License-Identifier: AGPL-3.0-only
package dae

// routerDaeServe owns the readiness waiter even when entering the namespace or
// opening the listener fails before Serve can signal readiness. Joining it
// before returning also prevents a retired generation from marking itself ready.
func routerDaeServe(with func(func() error) error, serve func(chan<- bool) error, onReady func()) error {
	ready := make(chan bool, 1)
	finished := make(chan struct{})
	waiterDone := make(chan struct{})
	go func() {
		defer close(waiterDone)
		select {
		case ok := <-ready:
			if ok {
				onReady()
			}
		case <-finished:
		}
	}()
	stage := "namespace"
	var serveErr error
	err := with(func() error {
		stage = "listen"
		serveErr = serve(ready)
		return serveErr
	})
	// DaeNetns.With adds context with %v, so retain the callback error
	// itself to preserve errors.Is/As (including listener errno values).
	if serveErr != nil {
		err = serveErr
	}
	close(finished)
	<-waiterDone
	if err != nil {
		return &routerDaeRunError{stage: stage, err: err}
	}
	return nil
}
