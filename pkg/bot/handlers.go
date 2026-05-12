package bot

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nikogura/diagnostic-bot/pkg/metrics"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
)

// handleAppMention handles app mention events (when bot is @mentioned).
func (b *Bot) handleAppMention(ctx context.Context, event *slackevents.AppMentionEvent) {
	// Ignore our own messages to prevent self-triggering loops
	if event.User == b.botUserID {
		return
	}

	b.logger.InfoContext(ctx, "handling app mention",
		slog.String("user", event.User),
		slog.String("channel", event.Channel),
		slog.String("text", event.Text))

	// Strip mention and get clean message
	message := b.stripMention(event.Text)

	// Check for help command
	if strings.ToLower(strings.TrimSpace(message)) == "help" {
		b.sendHelpMessage(event.Channel, event.TimeStamp)
		return
	}

	// Check if this is a thread reply (continuing conversation)
	if event.ThreadTimeStamp != "" && event.ThreadTimeStamp != event.TimeStamp {
		b.handleThreadReply(ctx, event.Channel, event.ThreadTimeStamp, event.User, message)
		return
	}

	// New investigation
	b.startInvestigation(ctx, event.Channel, event.TimeStamp, event.User, message)
}

// handleMessage handles regular message events in threads.
func (b *Bot) handleMessage(ctx context.Context, event *slackevents.MessageEvent) {
	// Only handle thread messages
	if event.ThreadTimeStamp == "" || event.User == b.botUserID {
		return
	}

	// Check if this is a conversation we're tracking
	_, exists := b.conversations.Get(event.ThreadTimeStamp)
	if !exists {
		return
	}

	b.logger.InfoContext(ctx, "handling thread message",
		slog.String("user", event.User),
		slog.String("channel", event.Channel),
		slog.String("thread_ts", event.ThreadTimeStamp))

	b.handleThreadReply(ctx, event.Channel, event.ThreadTimeStamp, event.User, event.Text)
}

// sendHelpMessage sends a help message listing available investigations.
func (b *Bot) sendHelpMessage(channel string, threadTS string) {
	helpText := b.matcher.FormatAvailableInvestigations()

	helpMessage := fmt.Sprintf("# Diagnostic Bot\n\n%s\n\nTo start an investigation, mention me with a description of the issue.", helpText)

	_, _, err := b.slackClient.PostMessage(
		channel,
		slack.MsgOptionText(helpMessage, false),
		slack.MsgOptionTS(threadTS),
	)
	if err != nil {
		b.logger.Error("failed to send help message", slog.String("error", err.Error()))
	}
}

// startInvestigation starts a new investigation based on user message.
func (b *Bot) startInvestigation(ctx context.Context, channel string, threadTS string, userID string, message string) {
	// Prevent duplicate investigations for the same user in the same channel.
	// This stops loops caused by queued Slack events being processed after an investigation completes.
	key := InvestigationKey(channel, userID)
	if !b.tracker.TryStart(key) {
		b.logger.InfoContext(ctx, "investigation already active, skipping duplicate",
			slog.String("user", userID),
			slog.String("channel", channel))

		return
	}

	defer b.tracker.Done(key)

	// Add reaction to show we're working
	err := b.slackClient.AddReaction("eyes", slack.ItemRef{
		Channel:   channel,
		Timestamp: threadTS,
	})
	if err != nil {
		b.logger.WarnContext(ctx, "failed to add reaction", slog.String("error", err.Error()))
	}

	// Get channel name for context-aware matching
	channelName := b.getChannelName(channel)

	// Match message to investigation template with channel context
	matchResult := b.matcher.MatchWithChannel(message, channelName)

	if !matchResult.Matched {
		b.sendErrorMessage(channel, threadTS, "I couldn't determine what type of investigation you need. "+
			"Try being more specific or use `@bot help` to see available investigation types.")
		return
	}

	// Create conversation
	conv := b.conversations.Create(threadTS, channel, userID, matchResult.InvestigationType)

	// Record metrics
	metrics.InvestigationsStartedTotal.WithLabelValues(string(matchResult.InvestigationType)).Inc()
	metrics.ConversationsActive.Set(float64(b.conversations.Count()))

	b.logger.InfoContext(ctx, "starting investigation",
		slog.String("type", string(matchResult.InvestigationType)),
		slog.String("user", userID),
		slog.String("channel", channel))

	// Inform user which investigation is starting
	investigationType := string(matchResult.InvestigationType)
	investigationMessage := fmt.Sprintf("Starting **%s** investigation: *%s*",
		investigationType, matchResult.Skill.Name)

	_, _, err = b.slackClient.PostMessage(
		channel,
		slack.MsgOptionText(investigationMessage, false),
		slack.MsgOptionTS(threadTS),
	)
	if err != nil {
		b.logger.WarnContext(ctx, "failed to send investigation type message", slog.String("error", err.Error()))
	}

	// Store original user message for thread context
	conv.OriginalUserMessage = message

	// Run investigation via Claude Code
	investigationResult, err := b.claudeCodeRunner.RunInvestigation(ctx, matchResult.Skill, message)
	if err != nil {
		b.sendErrorMessage(channel, threadTS, fmt.Sprintf("Error starting investigation: %v", err))
		return
	}

	// Store output for thread follow-ups
	conv.LastOutput = investigationResult

	// Send result to Slack
	err = b.sendFormattedMessage(channel, threadTS, investigationResult)
	if err != nil {
		b.logger.ErrorContext(ctx, "failed to send investigation result", slog.String("error", err.Error()))
		return
	}

	// Check for and upload any generated PDF files
	b.scanAndUploadPDFs(channel, threadTS)

	// Update reaction to show we're done
	err = b.slackClient.RemoveReaction("eyes", slack.ItemRef{
		Channel:   channel,
		Timestamp: threadTS,
	})
	if err != nil {
		b.logger.WarnContext(ctx, "failed to remove reaction", slog.String("error", err.Error()))
	}

	err = b.slackClient.AddReaction("white_check_mark", slack.ItemRef{
		Channel:   channel,
		Timestamp: threadTS,
	})
	if err != nil {
		b.logger.WarnContext(ctx, "failed to add completion reaction", slog.String("error", err.Error()))
	}

	// Record completion
	metrics.InvestigationsResolvedTotal.WithLabelValues(string(conv.InvestigationType)).Inc()
}

// handleThreadReply handles a reply in an existing investigation thread.
func (b *Bot) handleThreadReply(ctx context.Context, channel string, threadTS string, userID string, message string) {
	// Prevent duplicate thread replies for the same user in the same channel.
	key := InvestigationKey(channel, userID)
	if !b.tracker.TryStart(key) {
		b.logger.InfoContext(ctx, "investigation already active for thread reply, skipping",
			slog.String("user", userID),
			slog.String("channel", channel),
			slog.String("thread_ts", threadTS))

		return
	}

	defer b.tracker.Done(key)

	conv, exists := b.conversations.Get(threadTS)
	if !exists {
		b.sendErrorMessage(channel, threadTS, "I couldn't find an active conversation for this thread.")
		return
	}

	// Add reaction to show we're working
	err := b.slackClient.AddReaction("thinking_face", slack.ItemRef{
		Channel:   channel,
		Timestamp: threadTS,
	})
	if err != nil {
		b.logger.WarnContext(ctx, "failed to add reaction", slog.String("error", err.Error()))
	}

	b.logger.InfoContext(ctx, "handling thread reply",
		slog.String("type", string(conv.InvestigationType)),
		slog.String("user", userID))

	// Get the investigation skill
	skill, err := b.skillLibrary.GetSkill(conv.InvestigationType)
	if err != nil {
		b.sendErrorMessage(channel, threadTS, fmt.Sprintf("Error loading skill: %v", err))
		return
	}

	// Build follow-up prompt with prior context
	followUpMessage := message
	if conv.LastOutput != "" {
		// Truncate prior output to avoid exceeding context limits
		priorOutput := conv.LastOutput
		const maxContextLen = 30000
		if len(priorOutput) > maxContextLen {
			priorOutput = priorOutput[:maxContextLen] + "\n\n[Prior output truncated]"
		}

		followUpMessage = fmt.Sprintf("## Prior Investigation Context\n\nOriginal question: %s\n\nPrior investigation output:\n\n%s\n\n## Follow-Up Question\n\n%s",
			conv.OriginalUserMessage, priorOutput, message)
	}

	// Run follow-up investigation via Claude Code
	investigationResult, err := b.claudeCodeRunner.RunInvestigation(ctx, skill, followUpMessage)
	if err != nil {
		b.sendErrorMessage(channel, threadTS, fmt.Sprintf("Error processing follow-up: %v", err))
		return
	}

	// Update stored output for subsequent follow-ups
	conv.LastOutput = investigationResult

	// Send result to Slack
	err = b.sendFormattedMessage(channel, threadTS, investigationResult)
	if err != nil {
		b.logger.ErrorContext(ctx, "failed to send follow-up result", slog.String("error", err.Error()))
		return
	}

	// Check for and upload any generated PDF files
	b.scanAndUploadPDFs(channel, threadTS)

	// Remove working reaction
	err = b.slackClient.RemoveReaction("thinking_face", slack.ItemRef{
		Channel:   channel,
		Timestamp: threadTS,
	})
	if err != nil {
		b.logger.WarnContext(ctx, "failed to remove reaction", slog.String("error", err.Error()))
	}
}

// sendFormattedMessage sends a formatted message to Slack.
func (b *Bot) sendFormattedMessage(channel string, threadTS string, text string) (err error) {
	textPreview := text
	if len(textPreview) > 200 {
		textPreview = text[:200] + "... (truncated)"
	}

	b.logger.Info("sending message to Slack",
		slog.String("channel", channel),
		slog.String("thread", threadTS),
		slog.Int("message_length", len(text)),
		slog.String("preview", textPreview))

	_, _, err = b.slackClient.PostMessage(
		channel,
		slack.MsgOptionText(text, false),
		slack.MsgOptionTS(threadTS),
	)

	if err != nil {
		b.logger.Error("failed to send Slack message",
			slog.String("error", err.Error()))
	} else {
		b.logger.Info("Slack message sent successfully")
	}

	return err
}

// sendErrorMessage sends an error message to Slack.
func (b *Bot) sendErrorMessage(channel string, threadTS string, errorText string) {
	message := fmt.Sprintf("❌ Error: %s", errorText)

	_, _, err := b.slackClient.PostMessage(
		channel,
		slack.MsgOptionText(message, false),
		slack.MsgOptionTS(threadTS),
	)
	if err != nil {
		b.logger.Error("failed to send error message", slog.String("error", err.Error()))
	}
}

// uploadFile uploads a file to a Slack thread.
func (b *Bot) uploadFile(channel string, threadTS string, filePath string, title string, comment string) (err error) {
	b.logger.Info("uploading file to Slack",
		slog.String("channel", channel),
		slog.String("thread", threadTS),
		slog.String("file_path", filePath),
		slog.String("title", title))

	// Get file size
	var fileInfo os.FileInfo

	fileInfo, err = os.Stat(filePath)
	if err != nil {
		b.logger.Error("failed to stat file for upload",
			slog.String("error", err.Error()),
			slog.String("file", filePath))
		err = fmt.Errorf("stating file: %w", err)
		return err
	}

	params := slack.UploadFileV2Parameters{
		Channel:         channel,
		Filename:        filepath.Base(filePath),
		File:            filePath,
		FileSize:        int(fileInfo.Size()),
		Title:           title,
		ThreadTimestamp: threadTS,
	}

	if comment != "" {
		params.InitialComment = comment
	}

	b.logger.Info("uploading file to Slack with size",
		slog.String("file", filePath),
		slog.Int("size_bytes", int(fileInfo.Size())))

	_, uploadErr := b.slackClient.UploadFileV2(params)
	if uploadErr != nil {
		b.logger.Error("failed to upload file to Slack",
			slog.String("error", uploadErr.Error()),
			slog.String("file", filePath))
		err = fmt.Errorf("uploading file to Slack: %w", uploadErr)
		return err
	}

	b.logger.Info("file uploaded to Slack successfully",
		slog.String("file", filePath))

	return err
}

// waitForFileStable waits for a file to stop changing (indicating write completion).
// Returns os.FileInfo when stable, or error if timeout or file issues.
func (b *Bot) waitForFileStable(filePath string, timeout time.Duration) (result os.FileInfo, err error) {
	const pollInterval = 200 * time.Millisecond
	deadline := time.Now().Add(timeout)

	var lastSize int64 = -1
	var lastModTime time.Time
	var stableCount int

	for time.Now().Before(deadline) {
		result, err = os.Stat(filePath)
		if err != nil {
			err = fmt.Errorf("stat file: %w", err)
			return result, err
		}

		// File must be non-empty
		if result.Size() == 0 {
			b.logger.Debug("waiting for file content",
				slog.String("path", filePath),
				slog.Int64("size", result.Size()))
			time.Sleep(pollInterval)
			continue
		}

		// Check if size and mod time are stable
		if result.Size() == lastSize && result.ModTime().Equal(lastModTime) {
			stableCount++
			// Consider stable after 3 consecutive checks (600ms total)
			if stableCount >= 3 {
				b.logger.Info("file stable and ready",
					slog.String("path", filePath),
					slog.Int64("size", result.Size()))
				return result, err
			}
		} else {
			// File changed, reset counter
			stableCount = 0
			lastSize = result.Size()
			lastModTime = result.ModTime()
			b.logger.Debug("file still changing",
				slog.String("path", filePath),
				slog.Int64("size", result.Size()))
		}

		time.Sleep(pollInterval)
	}

	err = fmt.Errorf("timeout waiting for file stability after %v", timeout)
	return result, err
}

// scanAndUploadPDFs scans common directories for PDF files and uploads them to Slack.
// This is called after Claude Code investigations complete to automatically upload any generated reports.
func (b *Bot) scanAndUploadPDFs(channel string, threadTS string) {
	searchDirs := []string{
		"/tmp",
		"/app/reports",
		"/app",      // Claude might create PDFs in working directory
		"/home/bot", // Claude's home directory
	}

	b.logger.Info("scanning for PDF files to upload",
		slog.Any("search_dirs", searchDirs),
		slog.String("channel", channel),
		slog.String("thread_ts", threadTS))

	var foundPDFs []string

	for _, dir := range searchDirs {
		matches, err := filepath.Glob(filepath.Join(dir, "*.pdf"))
		if err != nil {
			b.logger.Warn("failed to scan directory for PDFs",
				slog.String("dir", dir),
				slog.String("error", err.Error()))
			continue
		}
		b.logger.Info("scanned directory for PDFs",
			slog.String("dir", dir),
			slog.Int("pdf_count", len(matches)),
			slog.Any("matches", matches))
		foundPDFs = append(foundPDFs, matches...)
	}

	if len(foundPDFs) == 0 {
		b.logger.Warn("no PDF files found to upload after scanning all directories")
		return
	}

	b.logger.Info("found PDF files to upload",
		slog.Int("count", len(foundPDFs)),
		slog.Any("files", foundPDFs))

	for _, pdfPath := range foundPDFs {
		// Wait for file to be complete (not being written to)
		fileInfo, waitErr := b.waitForFileStable(pdfPath, 10*time.Second)
		if waitErr != nil {
			b.logger.Error("failed to wait for PDF file stability",
				slog.String("path", pdfPath),
				slog.String("error", waitErr.Error()))
			continue
		}

		fileName := fileInfo.Name()
		title := strings.TrimSuffix(fileName, filepath.Ext(fileName))

		// Upload the PDF
		uploadErr := b.uploadFile(channel, threadTS, pdfPath, title, "📄 Generated report")
		if uploadErr != nil {
			b.logger.Error("failed to upload PDF",
				slog.String("path", pdfPath),
				slog.String("error", uploadErr.Error()))
			// Continue to try other files even if one fails
			continue
		}

		// Clean up the uploaded file
		removeErr := os.Remove(pdfPath)
		if removeErr != nil {
			b.logger.Warn("failed to remove PDF after upload",
				slog.String("path", pdfPath),
				slog.String("error", removeErr.Error()))
		}
	}
}

// getChannelName fetches the channel name from a channel ID.
// Returns empty string if unable to retrieve.
func (b *Bot) getChannelName(channelID string) (result string) {
	// Try to get channel info from Slack API
	channel, err := b.slackClient.GetConversationInfo(&slack.GetConversationInfoInput{
		ChannelID: channelID,
	})
	if err != nil {
		b.logger.Warn("failed to get channel name",
			slog.String("channel_id", channelID),
			slog.String("error", err.Error()))
		result = ""
		return result
	}

	result = channel.Name
	return result
}
