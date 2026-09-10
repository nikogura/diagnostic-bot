package bot

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/nikogura/diagnostic-bot/pkg/claude"
	"github.com/nikogura/diagnostic-bot/pkg/investigations"
	"github.com/nikogura/diagnostic-bot/pkg/metrics"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
)

const (
	// ConversationExpiry is how long conversations remain active.
	ConversationExpiry = 24 * time.Hour

	// CleanupInterval is how often to cleanup expired conversations.
	CleanupInterval = 1 * time.Hour

	// DefaultFileRetention is how long to keep generated files before deletion.
	DefaultFileRetention = 24 * time.Hour

	// DefaultSlackRetryInitial and DefaultSlackRetryMax bound the exponential
	// backoff between Slack socket-mode reconnection attempts.
	DefaultSlackRetryInitial = 1 * time.Second
	DefaultSlackRetryMax     = 5 * time.Minute

	// slackStableRun is how long a connection must survive before its failure
	// is treated as a fresh incident rather than a continuing one. Without
	// this, a connection that works for hours and then drops would resume at
	// whatever backoff the last outage ended on.
	slackStableRun = 2 * time.Minute
)

// Bot represents the Slack diagnostic bot.
type Bot struct {
	slackClient   *slack.Client
	socketClient  *socketmode.Client
	runner        *InvestigationRunner
	skillLibrary  *investigations.SkillLibrary
	matcher       *investigations.Matcher
	conversations *ConversationStore
	tracker       *InvestigationTracker
	logger        *slog.Logger
	botUserID     string
	fileRetention time.Duration
	pdfDisabled   bool

	// Health tracking
	healthMu        sync.RWMutex
	socketConnected bool
	retryInitial    time.Duration
	retryMax        time.Duration
	lastEventTime   time.Time
}

// Config holds the bot configuration.
type Config struct {
	SlackBotToken    string
	SlackAppToken    string
	AnthropicAPIKey  string
	InvestigationDir string
	FileRetention    time.Duration // How long to keep generated files (0 = use default)
	GitHubToken      string        // GitHub personal access token for repository access
	ClaudeModel      string        // Claude model to use (e.g., "claude-sonnet-4-5-20250929")
	PDFDisabled      bool          // Globally disable PDF report generation

	// SlackRetryInitial and SlackRetryMax bound the reconnection backoff.
	// Zero uses the defaults.
	SlackRetryInitial time.Duration
	SlackRetryMax     time.Duration

	// SkillLibrary is the shared investigation library. When nil the bot loads
	// its own from InvestigationDir; supplying it lets the MCP surface and the
	// Slack surface serve the same objects rather than two independent reads.
	SkillLibrary *investigations.SkillLibrary

	// ContextDocuments names the documents and documents whose full text is
	// prepended to every investigation prompt. The same list drives the MCP
	// server's instructions, so both front-ends carry one picture of the
	// environment.
	ContextDocuments []string
}

// NewBot creates a new diagnostic bot. The toolServer is the single in-process
// MCP tool surface, shared with the HTTP MCP server so every front-end drives
// one identical, gated toolset.
func NewBot(cfg Config, toolServer ToolDispatcher, logger *slog.Logger) (result *Bot, err error) {
	var skillLibrary *investigations.SkillLibrary
	var authResp *slack.AuthTestResponse

	// Use the shared library when one was supplied, so the Slack and MCP
	// surfaces serve the same objects; otherwise load our own.
	skillLibrary = cfg.SkillLibrary
	if skillLibrary == nil {
		skillLibrary, err = investigations.NewSkillLibrary(cfg.InvestigationDir)
	}

	if err != nil {
		err = fmt.Errorf("loading investigation skills: %w", err)
		return result, err
	}

	matcher := investigations.NewMatcher(skillLibrary)

	// Initialize clients
	slackClient := slack.New(
		cfg.SlackBotToken,
		slack.OptionDebug(false),
		slack.OptionLog(slog.NewLogLogger(logger.Handler(), slog.LevelDebug)),
		slack.OptionAppLevelToken(cfg.SlackAppToken),
	)

	socketClient := socketmode.New(
		slackClient,
		socketmode.OptionDebug(false),
		socketmode.OptionLog(slog.NewLogLogger(logger.Handler(), slog.LevelDebug)),
	)

	// Create the in-process investigation runner: an agent loop driving the
	// shared MCP tool surface directly via the Anthropic API. No subprocess,
	// no --dangerously-skip-permissions, no environment hand-off.
	model := claude.NewClient(cfg.AnthropicAPIKey, cfg.ClaudeModel, logger)
	runner := NewInvestigationRunner(model, toolServer, logger)

	// The same operator knowledge the MCP transport publishes in its server
	// instructions is prepended to every Slack investigation, so a question
	// asked in Slack and one asked through MCP are answered against identical
	// facts about this environment.
	knowledge := investigations.RenderContext(skillLibrary, cfg.ContextDocuments, 0)
	runner.SetKnowledge(knowledge.Text)

	logKnowledge(logger, knowledge)

	// Get bot user ID
	authResp, err = slackClient.AuthTest()
	if err != nil {
		err = fmt.Errorf("authenticating with Slack: %w", err)
		return result, err
	}

	// Use configured retention or default
	fileRetention := cfg.FileRetention
	if fileRetention == 0 {
		fileRetention = DefaultFileRetention
	}

	result = &Bot{
		slackClient:   slackClient,
		socketClient:  socketClient,
		runner:        runner,
		skillLibrary:  skillLibrary,
		matcher:       matcher,
		conversations: NewConversationStore(ConversationExpiry),
		tracker:       NewInvestigationTracker(),
		logger:        logger,
		botUserID:     authResp.UserID,
		fileRetention: fileRetention,
		pdfDisabled:   cfg.PDFDisabled,
		retryInitial:  orDuration(cfg.SlackRetryInitial, DefaultSlackRetryInitial),
		retryMax:      orDuration(cfg.SlackRetryMax, DefaultSlackRetryMax),
	}

	return result, err
}

// Start starts the bot and begins listening for events. It returns only when
// the context is cancelled, and returns nil on that graceful shutdown.
func (b *Bot) Start(ctx context.Context) (err error) {
	b.logger.InfoContext(ctx, "starting diagnostic bot",
		slog.String("bot_user_id", b.botUserID))

	// Start cleanup goroutine
	b.safeGo(ctx, "cleanup_loop", func() { b.cleanupLoop(ctx) })

	// Handle socket mode events
	b.safeGo(ctx, "socket_mode", func() { b.handleSocketMode(ctx) })

	err = b.runSocketMode(ctx)

	return err
}

// runSocketMode keeps the Slack connection up for the life of the context.
//
// RunContext retries transient disconnects internally and returns only on a
// fatal error, so a return here means Slack is unusable — a bad token, a
// rejected handshake, a sustained outage. That is a dependency failure, not a
// reason to end the process: the MCP tool surface and the metrics endpoint are
// independent of Slack and stay useful while it is down.
//
// So the connection is retried with exponential backoff, IsSocketConnected
// reports false meanwhile so readiness tells the truth, and the bot recovers on
// its own when Slack returns. Exiting instead would drop two healthy listeners
// and land the pod in CrashLoopBackOff, which fixes nothing.
func (b *Bot) runSocketMode(ctx context.Context) (err error) {
	err = b.reconnectLoop(ctx, b.socketClient)
	return err
}

// socketRunner is the sliver of the Slack socket-mode client the reconnect
// loop depends on. Taking the interface rather than the concrete client is what
// lets the retry behaviour be tested without a Slack connection.
type socketRunner interface {
	RunContext(ctx context.Context) (err error)
}

// reconnectLoop runs the socket client, retrying until the context is done.
func (b *Bot) reconnectLoop(ctx context.Context, runner socketRunner) (err error) {
	delay := b.retryInitial

	for {
		started := time.Now()
		runErr := runner.RunContext(ctx)
		lasted := time.Since(started)

		b.setSocketConnected(false)

		// A cancelled context is a graceful shutdown, not a failure, so the
		// error RunContext returned is discarded rather than reported.
		if contextDone(ctx) {
			b.logger.InfoContext(ctx, "slack socket mode stopped")
			return err
		}

		// A connection that stayed up is a new incident, not a continuing one.
		if lasted >= slackStableRun {
			delay = b.retryInitial
		}

		metrics.RecordSlackReconnect(ctx)

		b.logger.ErrorContext(ctx, "slack socket mode ended; retrying",
			slog.String("error", errorText(runErr)),
			slog.Duration("connected_for", lasted.Round(time.Second)),
			slog.Duration("retry_in", delay))

		select {
		case <-ctx.Done():
			return err
		case <-time.After(delay):
		}

		delay *= 2
		if delay > b.retryMax {
			delay = b.retryMax
		}
	}
}

// contextDone reports whether the context has been cancelled or has expired.
// Expressed as a predicate rather than an error comparison because the answer
// drives a shutdown decision, not error handling: the caller is asking whether
// to stop, not whether something went wrong.
func contextDone(ctx context.Context) (done bool) {
	done = ctx.Err() != nil
	return done
}

// errorText renders an error for logging, tolerating a nil.
func errorText(err error) (text string) {
	if err == nil {
		text = "none"
		return text
	}

	text = err.Error()

	return text
}

// handleSocketMode handles incoming socket mode events.
func (b *Bot) handleSocketMode(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			b.setSocketConnected(false)
			return

		case evt := <-b.socketClient.Events:
			b.recordEvent()

			switch evt.Type { //nolint:exhaustive // Only handling core event types, others are ignored
			case socketmode.EventTypeConnected:
				b.setSocketConnected(true)
				b.logger.InfoContext(ctx, "slack socket mode connected")

			case socketmode.EventTypeConnectionError:
				b.setSocketConnected(false)
				b.logger.ErrorContext(ctx, "slack socket mode connection error")

			case socketmode.EventTypeDisconnect:
				b.setSocketConnected(false)
				b.logger.WarnContext(ctx, "slack socket mode disconnected")

			case socketmode.EventTypeEventsAPI:
				eventsAPI, ok := evt.Data.(slackevents.EventsAPIEvent)
				if !ok {
					b.logger.WarnContext(ctx, "failed to cast event to EventsAPIEvent")
					continue
				}

				b.socketClient.Ack(*evt.Request)
				b.safeGo(ctx, "events_api", func() { b.handleEventsAPI(ctx, eventsAPI) })

			case socketmode.EventTypeInteractive:
				// Handle interactive events (buttons, etc.) if needed
				b.socketClient.Ack(*evt.Request)

			case socketmode.EventTypeSlashCommand:
				// Handle slash commands if needed
				b.socketClient.Ack(*evt.Request)

			default:
				// Ignore other event types
			}
		}
	}
}

// handleEventsAPI handles Events API events.
func (b *Bot) handleEventsAPI(ctx context.Context, event slackevents.EventsAPIEvent) {
	switch event.Type {
	case slackevents.CallbackEvent:
		innerEvent := event.InnerEvent

		switch ev := innerEvent.Data.(type) {
		case *slackevents.AppMentionEvent:
			b.handleAppMention(ctx, ev)

		case *slackevents.MessageEvent:
			b.handleMessage(ctx, ev)
		}
	}
}

// safeGo runs fn in a new goroutine guarded by a panic recovery boundary. An
// unrecovered panic in any goroutine crashes the whole process; every goroutine
// the bot launches must go through here so one bad investigation degrades to a
// single failed request instead of a CrashLoopBackOff. site names the goroutine
// for logs and the panics_recovered_total metric.
func (b *Bot) safeGo(ctx context.Context, site string, fn func()) {
	go func() {
		defer b.recoverPanic(ctx, site)
		fn()
	}()
}

// recoverPanic contains a panic in a spawned goroutine: it records the panic on
// the panics_recovered_total counter and logs it with a stack trace, then
// returns so the process keeps serving. Deferred at the top of every goroutine
// started via safeGo.
func (b *Bot) recoverPanic(ctx context.Context, site string) {
	r := recover()
	if r == nil {
		return
	}

	metrics.RecordPanicRecovered(ctx, site)
	b.logger.ErrorContext(ctx, "recovered from panic in goroutine",
		slog.String("site", site),
		slog.Any("panic", r),
		slog.String("stack", string(debug.Stack())))
}

// syncConversationsActive refreshes the conversations_active gauge to match the
// store. Call it after any operation that can change the count — creating a
// conversation or expiring stale ones — so the gauge never drifts above the real
// number of live conversations.
func (b *Bot) syncConversationsActive() {
	metrics.SetConversationsActive(int64(b.conversations.Count()))
}

// cleanupLoop periodically cleans up expired conversations and old files.
func (b *Bot) cleanupLoop(ctx context.Context) {
	ticker := time.NewTicker(CleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-ticker.C:
			b.runCleanup(ctx)
		}
	}
}

// runCleanup performs one cleanup pass: expire idle conversations, re-sync the
// active-conversation gauge to the store, and remove old generated files. Split
// out of cleanupLoop so a pass can be exercised in tests without the ticker.
func (b *Bot) runCleanup(ctx context.Context) {
	// Clean up expired conversations
	removed := b.conversations.CleanupExpired()
	if removed > 0 {
		b.logger.InfoContext(ctx, "cleaned up expired conversations",
			slog.Int("removed", removed))
	}

	// CleanupExpired shrinks the store, so re-sync the gauge every pass —
	// otherwise it stays pinned at its last create-time value and reads high.
	b.syncConversationsActive()

	// Clean up old generated files
	filesRemoved := b.cleanupOldFiles(ctx)
	if filesRemoved > 0 {
		b.logger.InfoContext(ctx, "cleaned up old files",
			slog.Int("removed", filesRemoved))
	}
}

// cleanupOldFiles removes PDF and markdown files older than fileRetention.
// Generated reports live in /tmp.
func (b *Bot) cleanupOldFiles(ctx context.Context) (result int) {
	searchDirs := []string{"/tmp"}
	patterns := []string{"*.pdf", "*.md"}
	cutoff := time.Now().Add(-b.fileRetention)

	for _, dir := range searchDirs {
		for _, pattern := range patterns {
			removed := b.cleanupFilesInDirectory(ctx, dir, pattern, cutoff)
			result += removed
		}
	}

	return result
}

// cleanupFilesInDirectory removes old files matching pattern in a directory.
func (b *Bot) cleanupFilesInDirectory(ctx context.Context, dir string, pattern string, cutoff time.Time) (result int) {
	matches, globErr := filepath.Glob(filepath.Join(dir, pattern))
	if globErr != nil {
		b.logger.WarnContext(ctx, "failed to glob files for cleanup",
			slog.String("dir", dir),
			slog.String("pattern", pattern),
			slog.String("error", globErr.Error()))
		return result
	}

	for _, filePath := range matches {
		removed := b.removeOldFile(ctx, filePath, cutoff)
		if removed {
			result++
		}
	}

	return result
}

// removeOldFile removes a single file if it's older than cutoff.
func (b *Bot) removeOldFile(ctx context.Context, filePath string, cutoff time.Time) (result bool) {
	fileInfo, statErr := os.Stat(filePath)
	if statErr != nil {
		b.logger.WarnContext(ctx, "failed to stat file for cleanup",
			slog.String("path", filePath),
			slog.String("error", statErr.Error()))
		return result
	}

	// Check if file is older than retention period
	if !fileInfo.ModTime().Before(cutoff) {
		return result
	}

	removeErr := os.Remove(filePath)
	if removeErr != nil {
		b.logger.WarnContext(ctx, "failed to remove old file",
			slog.String("path", filePath),
			slog.Time("mod_time", fileInfo.ModTime()),
			slog.String("error", removeErr.Error()))
		return result
	}

	b.logger.DebugContext(ctx, "removed old file",
		slog.String("path", filePath),
		slog.Time("mod_time", fileInfo.ModTime()),
		slog.Duration("age", time.Since(fileInfo.ModTime())))
	result = true

	return result
}

// stripMention removes bot mention from message text.
func (b *Bot) stripMention(text string) (result string) {
	result = strings.TrimSpace(strings.ReplaceAll(text, fmt.Sprintf("<@%s>", b.botUserID), ""))
	return result
}

// IsSocketConnected returns whether the Slack socket mode connection is active.
func (b *Bot) IsSocketConnected() (connected bool) {
	b.healthMu.RLock()
	defer b.healthMu.RUnlock()

	connected = b.socketConnected
	return connected
}

// LastEvent returns the time of the last received socket mode event.
func (b *Bot) LastEvent() (lastEvent time.Time) {
	b.healthMu.RLock()
	defer b.healthMu.RUnlock()

	lastEvent = b.lastEventTime
	return lastEvent
}

// setSocketConnected updates the socket connection state.
func (b *Bot) setSocketConnected(connected bool) {
	b.healthMu.Lock()
	defer b.healthMu.Unlock()

	b.socketConnected = connected
}

// recordEvent records the time of the last received event.
func (b *Bot) recordEvent() {
	b.healthMu.Lock()
	defer b.healthMu.Unlock()

	b.lastEventTime = time.Now()
}

// orDuration returns value when set, otherwise fallback.
func orDuration(value time.Duration, fallback time.Duration) (result time.Duration) {
	result = value
	if result <= 0 {
		result = fallback
	}

	return result
}

// logKnowledge reports what operator knowledge reached the Slack front-end, and
// says so out loud when a configured name was wrong. A misnamed preload is
// otherwise invisible: the operator believes they published something that no
// investigation ever receives.
func logKnowledge(logger *slog.Logger, knowledge investigations.Context) {
	logger.Info("investigation prompts carry operator knowledge",
		slog.Int("documents", len(knowledge.Names)),
		slog.Int("bytes", len(knowledge.Text)))

	if len(knowledge.Missing) > 0 {
		logger.Error("configured context document does not exist; Slack investigations will not receive it",
			slog.Any("missing", knowledge.Missing))
	}
}
