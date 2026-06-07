package bot

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/nikogura/diagnostic-bot/pkg/claude"
	"github.com/nikogura/diagnostic-bot/pkg/investigations"
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

	// Health tracking
	healthMu        sync.RWMutex
	socketConnected bool
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
}

// NewBot creates a new diagnostic bot. The toolServer is the single in-process
// MCP tool surface, shared with the HTTP MCP server so every front-end drives
// one identical, gated toolset.
func NewBot(cfg Config, toolServer ToolDispatcher, logger *slog.Logger) (result *Bot, err error) {
	var skillLibrary *investigations.SkillLibrary
	var authResp *slack.AuthTestResponse

	// Load investigation skills
	skillLibrary, err = investigations.NewSkillLibrary(cfg.InvestigationDir)
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
	}

	return result, err
}

// Start starts the bot and begins listening for events.
func (b *Bot) Start(ctx context.Context) (err error) {
	b.logger.InfoContext(ctx, "starting diagnostic bot",
		slog.String("bot_user_id", b.botUserID))

	// Start cleanup goroutine
	go b.cleanupLoop(ctx)

	// Handle socket mode events
	go b.handleSocketMode(ctx)

	// Run socket mode client
	err = b.socketClient.RunContext(ctx)
	if err != nil {
		err = fmt.Errorf("running socket mode client: %w", err)
		return err
	}

	return err
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
				go b.handleEventsAPI(ctx, eventsAPI)

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

// cleanupLoop periodically cleans up expired conversations and old files.
func (b *Bot) cleanupLoop(ctx context.Context) {
	ticker := time.NewTicker(CleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-ticker.C:
			// Clean up expired conversations
			removed := b.conversations.CleanupExpired()
			if removed > 0 {
				b.logger.InfoContext(ctx, "cleaned up expired conversations",
					slog.Int("removed", removed))
			}

			// Clean up old generated files
			filesRemoved := b.cleanupOldFiles(ctx)
			if filesRemoved > 0 {
				b.logger.InfoContext(ctx, "cleaned up old files",
					slog.Int("removed", filesRemoved))
			}
		}
	}
}

// cleanupOldFiles removes PDF and markdown files older than fileRetention.
func (b *Bot) cleanupOldFiles(ctx context.Context) (result int) {
	searchDirs := []string{"/tmp", "/app/reports", "/app", "/home/bot"}
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
