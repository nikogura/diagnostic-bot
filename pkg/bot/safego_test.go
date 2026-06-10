package bot

import (
	"context"
	"log/slog"
	"testing"
	"time"
)

// TestRecoverPanicContainsPanic verifies the recovery boundary swallows a panic
// instead of letting it unwind. Without recoverPanic this test goroutine would
// crash the whole test binary — which is exactly what would happen to the bot
// process in production.
func TestRecoverPanicContainsPanic(t *testing.T) {
	t.Parallel()

	bot := &Bot{logger: slog.New(slog.DiscardHandler)}

	// If recoverPanic works, control returns here normally. If it doesn't, the
	// panic propagates and crashes the test binary.
	func() {
		defer bot.recoverPanic(context.Background(), "test")
		panic("boom")
	}()
}

// TestSafeGoRunsFnAndContainsPanic verifies safeGo both runs its function and
// contains a panic in it without taking down the process.
func TestSafeGoRunsFnAndContainsPanic(t *testing.T) {
	t.Parallel()

	bot := &Bot{logger: slog.New(slog.DiscardHandler)}
	ctx := context.Background()

	// A normal function runs to completion.
	ran := make(chan struct{})
	bot.safeGo(ctx, "ok", func() { close(ran) })

	select {
	case <-ran:
	case <-time.After(2 * time.Second):
		t.Fatal("safeGo did not run the function")
	}

	// A panicking function is contained: the deferred signal fires during unwind,
	// recoverPanic swallows the panic, and the process stays alive.
	reached := make(chan struct{})
	bot.safeGo(ctx, "boom", func() {
		defer close(reached)
		panic("boom")
	})

	select {
	case <-reached:
	case <-time.After(2 * time.Second):
		t.Fatal("panicking safeGo function did not run")
	}
}
