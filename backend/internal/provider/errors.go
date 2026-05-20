package provider

import "errors"

var (
	ErrSignatureInvalid    = errors.New("provider: webhook signature invalid")
	ErrRateLimited         = errors.New("provider: rate limited by upstream")
	ErrProviderUnavailable = errors.New("provider: upstream unavailable")
	ErrUnknownProvider     = errors.New("provider: unknown provider name")
	ErrDuplicateProvider   = errors.New("provider: provider already registered with this name")
	// ErrUnauthorized signals the upstream provider rejected the bot's
	// credentials (e.g. a revoked Telegram token). It is terminal: the poll
	// loop stops rather than backing off, because retrying cannot recover.
	ErrUnauthorized = errors.New("provider: upstream rejected the bot credentials")
)
