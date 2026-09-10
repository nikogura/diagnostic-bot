// Copyright © 2026 Nik Ogura <nik.ogura@gmail.com>
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// White-box tests for the Slack reconnect loop.
package bot

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeRunner stands in for the Slack socket-mode client.
type fakeRunner struct {
	calls    atomic.Int32
	err      error
	blockFor time.Duration
	release  chan struct{}
}

// RunContext records a call and returns the configured error.
func (f *fakeRunner) RunContext(ctx context.Context) (err error) {
	f.calls.Add(1)

	if f.release != nil {
		select {
		case <-f.release:
		case <-ctx.Done():
			err = ctx.Err()
			return err
		}
	}

	if f.blockFor > 0 {
		select {
		case <-time.After(f.blockFor):
		case <-ctx.Done():
			err = ctx.Err()
			return err
		}
	}

	err = f.err

	return err
}

// testBot builds a Bot with fast retry timings and no Slack dependency.
func testBot() (bot *Bot) {
	bot = &Bot{
		logger:       slog.New(slog.DiscardHandler),
		retryInitial: 5 * time.Millisecond,
		retryMax:     20 * time.Millisecond,
	}

	return bot
}

// TestReconnectLoopReturnsNilOnShutdown pins the graceful path. The socket
// client returns ctx.Err() when cancelled, and treating that as a failure is
// what made Start unable to ever return nil.
func TestReconnectLoopReturnsNilOnShutdown(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := testBot().reconnectLoop(ctx, &fakeRunner{err: context.Canceled})
	assert.NoError(t, err, "a cancelled context is a shutdown, not a failure")
}

// TestReconnectLoopRetriesFatalErrors is the self-heal contract: a Slack
// failure must not end the process, because the MCP tool surface and the
// metrics endpoint do not depend on Slack.
func TestReconnectLoopRetriesFatalErrors(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{err: errors.New("slack: invalid handshake")}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)

	go func() {
		done <- testBot().reconnectLoop(ctx, runner)
	}()

	select {
	case err := <-done:
		require.NoError(t, err, "the loop must exit cleanly when the context expires")
	case <-time.After(3 * time.Second):
		t.Fatal("reconnect loop did not stop when the context expired")
	}

	assert.Greater(t, runner.calls.Load(), int32(1),
		"a fatal Slack error must be retried, not returned")
}

// TestReconnectLoopMarksSocketDisconnected proves readiness tells the truth
// while Slack is unreachable, which is what makes staying alive honest rather
// than merely convenient.
func TestReconnectLoopMarksSocketDisconnected(t *testing.T) {
	t.Parallel()

	bot := testBot()
	bot.setSocketConnected(true)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()

	_ = bot.reconnectLoop(ctx, &fakeRunner{err: errors.New("dropped")})

	assert.False(t, bot.IsSocketConnected(),
		"health must report the socket as down while it is down")
}

// TestReconnectLoopStopsPromptlyDuringBackoff proves a shutdown signal is not
// held hostage by a pending retry delay.
func TestReconnectLoopStopsPromptlyDuringBackoff(t *testing.T) {
	t.Parallel()

	bot := testBot()
	bot.retryInitial = 10 * time.Second
	bot.retryMax = 10 * time.Second

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)

	go func() {
		done <- bot.reconnectLoop(ctx, &fakeRunner{err: errors.New("dropped")})
	}()

	// Let the first attempt fail and the loop enter its backoff wait.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation must interrupt the backoff wait, not wait it out")
	}
}
