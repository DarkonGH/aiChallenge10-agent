package main

import (
	"context"
	"testing"
)

// TestInterruptTurnCancels checks the turn slot behind Ctrl-C: while a turn is
// armed, the signal handler cancels its context and reports the signal as
// consumed; after disarm there is nothing to cancel and the default SIGINT
// behaviour is restored.
func TestInterruptTurnCancels(t *testing.T) {
	a := &app{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a.armInterrupt(cancel)
	if !a.interruptTurn() {
		t.Fatal("interruptTurn() = false with an armed turn")
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("context is not cancelled")
	}
	if ctx.Err() != context.Canceled {
		t.Fatalf("ctx.Err() = %v, want context.Canceled", ctx.Err())
	}

	// A second Ctrl-C while the turn is unwinding is harmless: the cancel
	// function is idempotent and the signal is still not allowed to kill the
	// process.
	if !a.interruptTurn() {
		t.Fatal("second interruptTurn() = false, want the turn still armed")
	}

	a.disarmInterrupt()
	if a.interruptTurn() {
		t.Fatal("interruptTurn() = true after disarm, want false")
	}
}
