package bot

import (
	"sync"
	"time"

	anthropic "github.com/liushuangls/go-anthropic/v2"
	"github.com/nikogura/diagnostic-bot/pkg/investigations"
)

// ConversationState represents the state of a conversation.
type ConversationState string

const (
	ConversationStateActive    ConversationState = "active"
	ConversationStateResolved  ConversationState = "resolved"
	ConversationStateAbandoned ConversationState = "abandoned"
)

// Conversation tracks the state of an investigation conversation.
type Conversation struct {
	ID                  string
	InvestigationType   investigations.InvestigationType
	SlackThreadTS       string
	SlackChannel        string
	UserID              string
	StartedAt           time.Time
	LastActivity        time.Time
	MessageHistory      []anthropic.Message
	KubernetesContext   map[string]interface{}
	State               ConversationState
	LastOutput          string // Most recent investigation/follow-up output
	OriginalUserMessage string // The message that triggered the investigation
}

// ConversationStore manages active conversations.
type ConversationStore struct {
	mu             sync.RWMutex
	conversations  map[string]*Conversation // key: thread timestamp
	expiryDuration time.Duration
}

// NewConversationStore creates a new conversation store.
func NewConversationStore(expiryDuration time.Duration) (result *ConversationStore) {
	result = &ConversationStore{
		conversations:  make(map[string]*Conversation),
		expiryDuration: expiryDuration,
	}

	return result
}

// Create creates a new conversation.
func (cs *ConversationStore) Create(threadTS string, channel string, userID string, invType investigations.InvestigationType) (result *Conversation) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	conv := &Conversation{
		ID:                threadTS,
		InvestigationType: invType,
		SlackThreadTS:     threadTS,
		SlackChannel:      channel,
		UserID:            userID,
		StartedAt:         time.Now(),
		LastActivity:      time.Now(),
		MessageHistory:    make([]anthropic.Message, 0),
		KubernetesContext: make(map[string]interface{}),
		State:             ConversationStateActive,
	}

	cs.conversations[threadTS] = conv

	result = conv
	return result
}

// Get retrieves a conversation by thread timestamp.
func (cs *ConversationStore) Get(threadTS string) (result *Conversation, exists bool) {
	var conv *Conversation

	cs.mu.RLock()
	defer cs.mu.RUnlock()

	conv, exists = cs.conversations[threadTS]
	result = conv

	return result, exists
}

// Update updates a conversation's last activity time.
func (cs *ConversationStore) Update(threadTS string, messages []anthropic.Message) (err error) {
	var conv *Conversation
	var exists bool

	cs.mu.Lock()
	defer cs.mu.Unlock()

	conv, exists = cs.conversations[threadTS]
	if !exists {
		return err
	}

	conv.MessageHistory = messages
	conv.LastActivity = time.Now()

	return err
}

// SetState updates a conversation's state.
func (cs *ConversationStore) SetState(threadTS string, state ConversationState) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	if conv, exists := cs.conversations[threadTS]; exists {
		conv.State = state
		conv.LastActivity = time.Now()
	}
}

// CleanupExpired removes conversations that have been inactive for longer than the expiry duration.
func (cs *ConversationStore) CleanupExpired() (removed int) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	now := time.Now()

	for threadTS, conv := range cs.conversations {
		if now.Sub(conv.LastActivity) > cs.expiryDuration {
			delete(cs.conversations, threadTS)
			removed++
		}
	}

	return removed
}

// Count returns the number of active conversations.
func (cs *ConversationStore) Count() (result int) {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	result = len(cs.conversations)
	return result
}

// List returns all conversations (for debugging/admin purposes).
func (cs *ConversationStore) List() (result []*Conversation) {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	result = make([]*Conversation, 0, len(cs.conversations))

	for _, conv := range cs.conversations {
		result = append(result, conv)
	}

	return result
}
