package bot

import (
	"context"

	"github.com/nikogura/diagnostic-bot/pkg/authz"
)

// withPrincipal attaches an authz principal to ctx so the tool layer can
// authorize tool calls by role. Slack identity is bound to the immutable Slack
// user ID — never the user-editable profile email — so a user cannot evade
// authorization by changing their email. When no policy is configured the
// principal is ignored and every tool remains available.
func (b *Bot) withPrincipal(ctx context.Context, userID string) (newCtx context.Context) {
	newCtx = authz.NewContext(ctx, authz.Principal{SlackID: userID, Source: authz.SourceSlack})
	return newCtx
}
