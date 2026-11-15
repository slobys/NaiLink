// SPDX-License-Identifier: AGPL-3.0-only
package dae

import (
	"errors"
	"fmt"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestRouterDaeServeRetainsNamespaceFailure(t *testing.T) {
	cause := errors.New("synthetic namespace entry failure")
	var called bool
	err := routerDaeServe(func(func() error) error { return cause }, func(chan<- bool) error {
		called = true
		return nil
	}, func() { t.Error("failed namespace became ready") })
	var failure *routerDaeRunError
	if called || !errors.Is(err, cause) || !errors.As(err, &failure) || failure.stage != "namespace" {
		t.Fatalf("lost namespace failure: called=%v error=%v", called, err)
	}
}

func TestRouterDaeServeRetainsListenFailureWithoutReadySignal(t *testing.T) {
	cause := errors.New("synthetic listener open failure")
	err := routerDaeServe(func(run func() error) error { return run() }, func(chan<- bool) error {
		return cause
	}, func() { t.Error("failed listener became ready") })
	var failure *routerDaeRunError
	if !errors.Is(err, cause) || !errors.As(err, &failure) || failure.stage != "listen" {
		t.Fatalf("lost listener failure: %v", err)
	}
}

func TestRouterDaeServeJoinsReadinessBeforeReturning(t *testing.T) {
	for _, ready := range []bool{false, true} {
		t.Run(map[bool]string{false: "rejected", true: "serving"}[ready], func(t *testing.T) {
			var calls atomic.Int32
			marked := make(chan struct{})
			release := make(chan struct{})
			done := make(chan error, 1)
			go func() {
				done <- routerDaeServe(func(run func() error) error { return run() }, func(signal chan<- bool) error {
					signal <- ready
					if ready {
						<-marked
					}
					return nil
				}, func() { calls.Add(1); close(marked); <-release })
			}()
			if ready {
				select {
				case <-marked:
				case <-time.After(time.Second):
					t.Fatal("readiness was not delivered")
				}
				select {
				case <-done:
					t.Fatal("serve returned with readiness callback still running")
				default:
				}
			}
			close(release)
			select {
			case err := <-done:
				if err != nil || calls.Load() != map[bool]int32{false: 0, true: 1}[ready] {
					t.Fatal("unexpected serve result", calls.Load(), err)
				}
			case <-time.After(time.Second):
				t.Fatal("readiness waiter did not finish")
			}
		})
	}
}

func TestRouterDaeServePreservesErrnoThroughNamespaceFormatting(t *testing.T) {
	cause := fmt.Errorf("synthetic listener bind: %w", syscall.EADDRINUSE)
	err := routerDaeServe(func(run func() error) error {
		if err := run(); err != nil {
			// Match the real DaeNetns.With wrapper, which does not use %w.
			return fmt.Errorf("failed to run func in dae netns: %v", err)
		}
		return nil
	}, func(chan<- bool) error { return cause }, func() { t.Error("failed listener became ready") })
	var failure *routerDaeRunError
	if !errors.Is(err, syscall.EADDRINUSE) || !errors.Is(err, cause) || !errors.As(err, &failure) || failure.stage != "listen" {
		t.Fatalf("namespace wrapper discarded listener errno: %v", err)
	}
}
