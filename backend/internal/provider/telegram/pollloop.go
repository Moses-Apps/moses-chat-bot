package telegram

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"moses-chat-bot/backend/internal/provider"
)

// InboundDispatcher is the narrow surface the poll loop needs from the relay.
// relay.Inbound.HandleInbound satisfies it — the loop feeds the SAME
// InboundMessage values the webhook handler does, so the relay stays entirely
// mode-agnostic (it already dedups by provider_message_id).
type InboundDispatcher interface {
	HandleInbound(ctx context.Context, msg provider.InboundMessage) error
}

// Backoff bounds for the poll loop's transient-error retry. A network blip or
// a Telegram 5xx backs off starting at pollBackoffMin, doubling up to
// pollBackoffMax; the delay resets to zero after any successful poll.
const (
	pollBackoffMin = 1 * time.Second
	pollBackoffMax = 30 * time.Second
)

// PollLoop drives inbound Telegram traffic by repeatedly long-polling
// getUpdates and dispatching each decoded message into the relay.
//
// It is the default ingress mode (webhook is the opt-in alternative): a poll
// loop is purely outbound, so it works behind the Moses per-app auth gate, on
// localhost, with no public URL and no tunnel.
//
// Lifecycle is owned by the botconfig service: Run is launched in a goroutine
// on Connect / LoadAtStartup and stopped by cancelling the context handed to
// it. A revoked token (provider.ErrUnauthorized) stops the loop on its own.
//
// Offset is held in memory only. On restart the loop re-fetches from offset 0,
// but the relay dedups by provider_message_id, so at-most-once delivery still
// holds — persisting the offset would be over-engineering for that gain.
//
// Single-consumer constraint: getUpdates hands each update to exactly one
// caller, so exactly one PollLoop may run per bot token. The deployment keeps
// replicas: 1 for this reason; scaling out would need a webhook or a
// leader-elected single poller (a future bead).
type PollLoop struct {
	adapter    *Adapter
	dispatcher InboundDispatcher
	logger     *slog.Logger

	// dispatchCtx is the parent context for HandleInbound calls. It must
	// outlive a single poll iteration (an MM round-trip can take minutes);
	// the loop's own ctx only governs whether to fetch the NEXT batch.
	dispatchCtx context.Context
}

// NewPollLoop constructs a poll loop. dispatchCtx is the long-lived context
// passed to HandleInbound (typically the process root ctx) so a slow MM
// dispatch is not killed when the loop's own ctx is cancelled at shutdown.
func NewPollLoop(adapter *Adapter, dispatcher InboundDispatcher, dispatchCtx context.Context, logger *slog.Logger) *PollLoop {
	if logger == nil {
		logger = slog.Default()
	}
	if dispatchCtx == nil {
		dispatchCtx = context.Background()
	}
	return &PollLoop{
		adapter:     adapter,
		dispatcher:  dispatcher,
		logger:      logger,
		dispatchCtx: dispatchCtx,
	}
}

// Run blocks until ctx is cancelled or the bot token is revoked. It first
// deletes any active webhook (getUpdates 409s while a webhook is registered),
// then long-polls in a loop, dispatching every inbound message.
//
// Run is intended to be launched in its own goroutine. It never panics on a
// transient error: it backs off and retries. It returns nil on a clean
// ctx-cancel and the terminal error on a revoked token.
func (p *PollLoop) Run(ctx context.Context) error {
	// getUpdates and a webhook are mutually exclusive. Drop any webhook the
	// bot may carry from a prior webhook-mode deploy before the first poll.
	if err := p.adapter.DeleteWebhook(ctx); err != nil {
		// Non-fatal: if a webhook was never set this is a harmless no-op on
		// Telegram's side; if it genuinely failed the first getUpdates will
		// surface the 409 and the backoff path will retry.
		p.logger.Warn("telegram poll: deleteWebhook before polling failed",
			slog.String("err", err.Error()))
	}

	p.logger.Info("telegram poll: loop started")
	var (
		offset  int64
		backoff time.Duration
	)
	for {
		if ctx.Err() != nil {
			p.logger.Info("telegram poll: loop stopped (context cancelled)")
			return nil
		}

		messages, next, err := p.adapter.Poll(ctx, offset)
		if err != nil {
			if errors.Is(err, provider.ErrUnauthorized) {
				p.logger.Error("telegram poll: bot token revoked; stopping loop",
					slog.String("err", err.Error()))
				return err
			}
			if ctx.Err() != nil {
				// The error is just the cancelled context unwinding the
				// in-flight HTTP call — a clean shutdown, not a failure.
				p.logger.Info("telegram poll: loop stopped (context cancelled)")
				return nil
			}
			backoff = nextBackoff(backoff)
			p.logger.Warn("telegram poll: getUpdates failed; backing off",
				slog.String("err", err.Error()),
				slog.Duration("backoff", backoff))
			if !sleepCtx(ctx, backoff) {
				p.logger.Info("telegram poll: loop stopped (context cancelled)")
				return nil
			}
			continue
		}

		// Successful poll — reset the backoff and advance the offset.
		backoff = 0
		offset = next

		for _, msg := range messages {
			if derr := p.dispatcher.HandleInbound(p.dispatchCtx, msg); derr != nil {
				// Dispatch errors are already logged inside the relay; the
				// poll loop must not abort the batch or stop polling.
				p.logger.Warn("telegram poll: HandleInbound returned error",
					slog.String("provider_message_id", msg.ProviderMessageID),
					slog.String("err", derr.Error()))
			}
		}
	}
}

// nextBackoff doubles cur, clamped to [pollBackoffMin, pollBackoffMax].
func nextBackoff(cur time.Duration) time.Duration {
	if cur < pollBackoffMin {
		return pollBackoffMin
	}
	next := cur * 2
	if next > pollBackoffMax {
		return pollBackoffMax
	}
	return next
}

// sleepCtx blocks for d or until ctx is done. It returns true if the full
// duration elapsed, false if ctx was cancelled first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
