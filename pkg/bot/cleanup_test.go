package bot

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/nikogura/diagnostic-bot/pkg/investigations"
	"github.com/nikogura/diagnostic-bot/pkg/metrics"
)

// TestRunCleanupSyncsActiveConversationsGauge is a regression test for the
// conversations_active gauge drifting high. CleanupExpired shrinks the store, but
// the gauge was only ever set on conversation creation, so it never ticked down
// as threads went idle. runCleanup must re-sync the gauge to the live store size.
func TestRunCleanupSyncsActiveConversationsGauge(t *testing.T) {
	// Not parallel: this asserts on the process-global conversations_active gauge,
	// which other tests in this package also mutate.
	ctx := context.Background()

	bot := &Bot{
		conversations: NewConversationStore(24 * time.Hour),
		logger:        slog.New(slog.DiscardHandler),
		// Huge retention so cleanupOldFiles deletes nothing from /tmp during the test.
		fileRetention: 1_000_000 * time.Hour,
	}

	// Two live conversations → the gauge tracks the store.
	bot.conversations.Create("100.1", "C1", "U1", investigations.InvestigationType("test"))
	stale := bot.conversations.Create("200.2", "C1", "U2", investigations.InvestigationType("test"))
	bot.syncConversationsActive()

	if got := metrics.GetConversationsActive(); got != 2 {
		t.Fatalf("gauge after creating 2 conversations = %d, want 2", got)
	}

	// Age one conversation past the expiry window so the next pass evicts it.
	stale.LastActivity = time.Now().Add(-48 * time.Hour)

	bot.runCleanup(ctx)

	if got := bot.conversations.Count(); got != 1 {
		t.Fatalf("store count after cleanup = %d, want 1", got)
	}

	if got := metrics.GetConversationsActive(); got != 1 {
		t.Errorf("gauge after cleanup = %d, want 1 (gauge drifted from the store)", got)
	}
}
