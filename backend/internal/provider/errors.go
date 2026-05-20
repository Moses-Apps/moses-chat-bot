package provider

import "errors"

var (
	ErrSignatureInvalid    = errors.New("provider: webhook signature invalid")
	ErrRateLimited         = errors.New("provider: rate limited by upstream")
	ErrProviderUnavailable = errors.New("provider: upstream unavailable")
	ErrUnknownProvider     = errors.New("provider: unknown provider name")
	ErrDuplicateProvider   = errors.New("provider: provider already registered with this name")
)
