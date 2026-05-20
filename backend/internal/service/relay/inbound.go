// Inbound is the message dispatch path: provider webhook → Moses Manager
// → response back through the provider. The Sender (outbound.go) handles
// the egress half; this file owns command dispatch, conversation
// resolution, WS subscription, and stream aggregation.
//
// Concurrency model: HandleInbound is safe to invoke from many goroutines
// at once. The wsConnPool serialises per-link connection setup. The
// WS event loop for any given (link, conversation) runs inline inside
// HandleInbound; aggregated chunks for the same conversation arriving
// concurrently are unspecified (Telegram serialises a 1:1 chat by
// design so this doesn't happen in practice).
package relay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"moses-chat-bot/backend/internal/db"
	"moses-chat-bot/backend/internal/mosesclient"
	"moses-chat-bot/backend/internal/provider"
	"moses-chat-bot/backend/internal/provider/commands"
	"moses-chat-bot/backend/internal/service/crypto"
	"moses-chat-bot/backend/internal/service/linker"
)

// AutopilotService is the narrow surface inbound dispatch needs from the
// autopilot package. Declared here (rather than imported) to keep the
// relay → autopilot dependency one-directional — autopilot already pulls
// relay's Sender interface, so a reverse import would form a cycle.
type AutopilotService interface {
	Start(ctx context.Context, link *db.ChatRelayLink, providerChatID string) (string, error)
	Stop(ctx context.Context, link *db.ChatRelayLink, providerChatID string) (string, error)
	Status(ctx context.Context, link *db.ChatRelayLink, providerChatID string) (string, error)
}

// InboundStore is the narrow DB interface the inbound relay depends on.
// The concrete *db.Store satisfies it (compile-time check below).
type InboundStore interface {
	IsDuplicateInbound(ctx context.Context, linkID uuid.UUID, providerMessageID string) (bool, error)
	GetActiveLinkByProviderUser(ctx context.Context, providerName, providerUserID string) (*db.ChatRelayLink, error)
	DeactivateLink(ctx context.Context, tenantID uuid.UUID, id uuid.UUID, reason string) error
	InsertMessage(
		ctx context.Context,
		linkID uuid.UUID,
		direction string,
		providerMessageID *string,
		mosesConversationID *uuid.UUID,
		text string,
		metadata []byte,
		errMsg *string,
	) (uuid.UUID, error)
	GetOrCreate(ctx context.Context, linkID uuid.UUID, providerChatID string) (*db.ProviderChatState, error)
	UpdateConversationID(ctx context.Context, linkID uuid.UUID, providerChatID string, conversationID uuid.UUID) error
	ClearConversationID(ctx context.Context, linkID uuid.UUID, providerChatID string) error
	TouchLastUsed(ctx context.Context, tenantID uuid.UUID, id uuid.UUID) error
}

// Compile-time guarantee that the concrete *db.Store satisfies the
// narrow InboundStore interface — drift in either side fails the build.
var _ InboundStore = (*db.Store)(nil)

// PerKeyChatClient builds an authenticated mosesclient for a single
// user's bearer key. Different links carry different keys, so we cannot
// reuse one client across users — but we DO reuse the underlying *http.Client.
type PerKeyChatClient interface {
	CreateConversation(ctx context.Context, opts mosesclient.CreateConversationOpts) (*mosesclient.Conversation, error)
	StreamChatMessage(ctx context.Context, opts mosesclient.ChatStreamOpts) (*mosesclient.ChatStreamAck, error)
	SendChatMessageSync(ctx context.Context, opts mosesclient.ChatSyncOpts) (*mosesclient.ChatSyncResponse, error)
}

// ChatClientFactory returns a PerKeyChatClient configured to authenticate
// outbound calls as the user identified by bearer. Production wires this
// to a closure around *mosesclient.Client.NewClient + BearerAuth.
type ChatClientFactory func(bearer string) PerKeyChatClient

// InboundOpts configures the Inbound service.
type InboundOpts struct {
	// StreamTimeout caps how long HandleInbound waits for
	// assistant_message_complete on the WS before sending the user a
	// retry message. Default 5min.
	StreamTimeout time.Duration

	// MaxConcurrent caps in-flight HandleInbound goroutines. The webhook
	// handler enforces the semaphore upstream; setting it here is
	// informational. Default 32.
	MaxConcurrent int

	// Logger is required for diagnostics. main passes a configured
	// slog.Logger; tests may pass slog.New(slog.NewTextHandler(io.Discard, nil)).
	Logger *slog.Logger
}

// Inbound is the inbound dispatch service.
type Inbound struct {
	Store         InboundStore
	Sender        *Sender
	Envelope      *crypto.Envelope
	Linker        *linker.Linker
	Registry      *provider.Registry
	ChatFactory   ChatClientFactory
	WSPool        *wsConnPool
	StreamTimeout time.Duration
	Logger        *slog.Logger

	// Autopilot is optional — when nil the /autopilot command surface
	// degrades to a "not configured" reply. main.go wires this; tests
	// substitute a fake.
	Autopilot AutopilotService
}

// NewInbound constructs the inbound service. ChatFactory and WSPool are
// required for production; tests inject fakes to avoid network I/O.
func NewInbound(
	store InboundStore,
	sender *Sender,
	env *crypto.Envelope,
	link *linker.Linker,
	registry *provider.Registry,
	chatFactory ChatClientFactory,
	wsPool *wsConnPool,
	opts InboundOpts,
) *Inbound {
	if opts.StreamTimeout <= 0 {
		opts.StreamTimeout = 5 * time.Minute
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Inbound{
		Store:         store,
		Sender:        sender,
		Envelope:      env,
		Linker:        link,
		Registry:      registry,
		ChatFactory:   chatFactory,
		WSPool:        wsPool,
		StreamTimeout: opts.StreamTimeout,
		Logger:        logger,
	}
}

// HandleInbound is the core dispatch function. It is fire-and-forget from
// the webhook handler's perspective: errors here only flow to the
// structured log and (when meaningful to the user) to a chat reply. The
// HTTP webhook has already returned 200 to the provider.
func (i *Inbound) HandleInbound(ctx context.Context, msg provider.InboundMessage) error {
	logger := i.Logger.With(
		slog.String("provider", msg.Provider),
		slog.String("provider_user_id", msg.ProviderUserID),
		slog.String("provider_message_id", msg.ProviderMessageID),
	)

	// Parse the command up front — /start and /link are BOOTSTRAP commands
	// that must work before any link exists (/link is what creates one).
	cmd, parseErr := commands.Parse(msg.Text)

	// 1. Resolve link
	link, err := i.Store.GetActiveLinkByProviderUser(ctx, msg.Provider, msg.ProviderUserID)
	if err != nil && !db.IsNoRows(err) {
		logger.Error("resolve link", slog.String("err", err.Error()))
		return fmt.Errorf("relay: resolve link: %w", err)
	}

	// 2. No link yet — only the bootstrap commands /start and /link are
	//    actionable. /link is what CREATES the link, so it cannot itself
	//    require one to already exist.
	if link == nil {
		return i.handleUnlinked(ctx, msg, cmd, parseErr, logger)
	}
	logger = logger.With(slog.String("link_id", link.ID.String()))

	// 2. Dedup
	if msg.ProviderMessageID != "" {
		dup, err := i.Store.IsDuplicateInbound(ctx, link.ID, msg.ProviderMessageID)
		if err != nil {
			logger.Warn("dedup check failed", slog.String("err", err.Error()))
		} else if dup {
			logger.Info("inbound dedup skip")
			return nil
		}
	}

	// 3. Persist inbound row up front — audit anchor.
	inboundMeta := buildInboundMetadata(msg)
	pmid := msg.ProviderMessageID
	var pmidPtr *string
	if pmid != "" {
		pmidPtr = &pmid
	}
	if _, err := i.Store.InsertMessage(ctx, link.ID, "in", pmidPtr, nil, msg.Text, inboundMeta, nil); err != nil {
		logger.Warn("persist inbound row failed", slog.String("err", err.Error()))
		// continue anyway — losing audit must not block the user response.
	}
	_ = i.Store.TouchLastUsed(ctx, link.TenantID, link.ID)

	// 4. Slash command dispatch (cmd/parseErr were parsed at the top).
	if parseErr == nil {
		handled, err := i.dispatchCommand(ctx, link, msg, cmd)
		if err != nil {
			logger.Warn("command dispatch failed", slog.String("verb", cmd.Verb), slog.String("err", err.Error()))
		}
		if handled {
			return nil
		}
		// fall through (e.g. /tickets, /status — converted to MM prompts)
	} else if errors.Is(parseErr, commands.ErrNotACommand) {
		// not a command — fall through to MM
	} else {
		// Unknown / invalid command — let MM interpret it.
		logger.Debug("command parse non-fatal", slog.String("err", parseErr.Error()))
	}

	// 5. MM dispatch
	return i.dispatchToMM(ctx, link, msg, logger)
}

// dispatchCommand handles slash commands. Returns (handled=true) when the
// command produced its own reply and MM should NOT be invoked; returns
// (handled=false) when the command falls through to MM (e.g. /tickets,
// /status are formatted as MM prompts).
func (i *Inbound) dispatchCommand(
	ctx context.Context,
	link *db.ChatRelayLink,
	msg provider.InboundMessage,
	cmd commands.Command,
) (bool, error) {
	switch cmd.Verb {
	case "/start":
		i.Linker.RegisterKnown(msg.Provider, msg.ProviderUserID)
		return true, i.replyText(ctx, link, "Welcome! Send `/link <code>` from your Moses UI to connect this chat to your account.")

	case "/link":
		// Already linked (we resolved link above), but support relink-by-code
		// anyway: the linker call below will error if already-linked.
		return true, i.handleLinkCommand(ctx, link, msg, cmd)

	case "/unlink":
		if err := i.Linker.Unlink(ctx, link.TenantID, link.MosesUserID, link.ID); err != nil {
			_ = i.replyText(ctx, link, "Failed to unlink. Please try again or remove the link from your Moses UI.")
			return true, err
		}
		return true, i.replyText(ctx, link, "Unlinked. Your messages will no longer reach Moses until you relink.")

	case "/help":
		return true, i.replyText(ctx, link, helpText())

	case "/clear":
		// Ensure the chat-state row exists, then null its conversation id
		// so the next inbound message opens a fresh Moses thread.
		if _, err := i.Store.GetOrCreate(ctx, link.ID, msg.ProviderChatID); err != nil {
			return true, fmt.Errorf("relay: /clear getOrCreate: %w", err)
		}
		if err := i.Store.ClearConversationID(ctx, link.ID, msg.ProviderChatID); err != nil {
			return true, fmt.Errorf("relay: /clear reset conv: %w", err)
		}
		return true, i.replyText(ctx, link, "Fresh conversation started. Your next message will open a new Moses thread.")

	case "/use":
		return true, i.replyText(ctx, link, "Multi-tenant switching is not yet supported. Re-link from the target workspace's Moses UI to switch.")

	case "/dnd":
		return true, i.replyText(ctx, link, fmt.Sprintf("Do-not-disturb wiring is pending (T-PUSH-1 / T-AUTOPILOT-1). Args were: %s", strings.Join(cmd.Args, " ")))

	case "/autopilot":
		if i.Autopilot == nil {
			return true, i.replyText(ctx, link, "Autopilot service not configured.")
		}
		if len(cmd.Args) == 0 {
			return true, i.replyText(ctx, link, "Usage: /autopilot start|stop|status")
		}
		var (
			reply string
			err   error
		)
		switch cmd.Args[0] {
		case "start":
			reply, err = i.Autopilot.Start(ctx, link, msg.ProviderChatID)
		case "stop":
			reply, err = i.Autopilot.Stop(ctx, link, msg.ProviderChatID)
		case "status":
			reply, err = i.Autopilot.Status(ctx, link, msg.ProviderChatID)
		default:
			return true, i.replyText(ctx, link, "Usage: /autopilot start|stop|status")
		}
		if err != nil {
			// Surface the error to the user; the sweeper retries on
			// terminal-state observations so we don't need to here.
			return true, i.replyText(ctx, link, "Autopilot error: "+err.Error())
		}
		return true, i.replyText(ctx, link, reply)

	case "/tickets":
		// Rewrite as an MM prompt.
		msg.Text = "List my open tickets."
		return false, nil

	case "/status":
		msg.Text = "Show me my Moses workspace status (active deployments, in-flight tickets, autopilot sessions)."
		return false, nil
	}
	// Unknown / unparsed: let MM see it.
	return false, nil
}

// handleLinkCommand processes /link <code>. The user is already linked
// (we resolved a link before dispatch), but Telegram users may resend the
// command — return a friendly explanation rather than an error.
func (i *Inbound) handleLinkCommand(
	ctx context.Context,
	link *db.ChatRelayLink,
	_ provider.InboundMessage,
	_ commands.Command,
) error {
	return i.replyText(ctx, link, "You're already linked. Send `/unlink` first if you want to relink to a different account.")
}

// handleUnlinked processes a message from a provider user with no active
// link. Only the bootstrap commands /start and /link are actionable —
// /link is what mints the link. Anything else gets linking instructions.
func (i *Inbound) handleUnlinked(
	ctx context.Context,
	msg provider.InboundMessage,
	cmd commands.Command,
	parseErr error,
	logger *slog.Logger,
) error {
	if parseErr == nil && cmd.Verb == "/start" {
		i.Linker.RegisterKnown(msg.Provider, msg.ProviderUserID)
		i.replyUnlinked(ctx, msg, "Welcome! Generate a 6-digit code in your Moses chat-bot app, then send `/link <code>` here to connect this chat to your Moses account.")
		return nil
	}
	if parseErr == nil && cmd.Verb == "/link" {
		if len(cmd.Args) == 0 {
			i.replyUnlinked(ctx, msg, "Usage: `/link <code>`. Generate the 6-digit code in your Moses chat-bot app first.")
			return nil
		}
		i.replyUnlinked(ctx, msg, i.completeLink(ctx, msg, cmd.Args[0], logger))
		return nil
	}
	i.sendNoLinkReply(ctx, msg)
	return nil
}

// completeLink runs the /link code-claim for a not-yet-linked provider user
// and returns the user-facing reply. linker.CompleteLink does the real work
// (known-user gate, lockout, code validation, pending→link copy).
func (i *Inbound) completeLink(
	ctx context.Context,
	msg provider.InboundMessage,
	code string,
	logger *slog.Logger,
) string {
	link, err := i.Linker.CompleteLink(ctx, code, msg.Provider, msg.ProviderUserID)
	if err != nil {
		switch {
		case errors.Is(err, linker.ErrUnknownUser):
			return "Send `/start` first, then `/link <code>`."
		case errors.Is(err, linker.ErrLockedOut):
			return "Too many failed attempts. Please wait a few minutes, then try again."
		case errors.Is(err, linker.ErrInvalidCode):
			return "That code is invalid. Generate a fresh one in your Moses chat-bot app and send `/link <code>` again."
		case errors.Is(err, linker.ErrExpired):
			return "That code has expired — codes last 60 seconds. Generate a fresh one and send `/link <code>` again."
		case errors.Is(err, linker.ErrAlreadyLinked):
			return "This Telegram account is already linked. Send `/unlink` first to relink to a different account."
		default:
			logger.Error("complete link", slog.String("err", err.Error()))
			return "Something went wrong linking your account. Please try again in a moment."
		}
	}
	logger.Info("link completed via /link", slog.String("link_id", link.ID.String()))
	return "Linked! You can now message Moses from this chat — send anything to talk to your Moses Manager."
}

// replyText is the shared helper every command branch funnels through to
// send a plain-text response. Centralising the OutboundMessage construction
// keeps the dispatch loop terse and ensures any future cross-cutting
// concern (markdown rendering, reply-threading) has one call site to
// touch.
func (i *Inbound) replyText(ctx context.Context, link *db.ChatRelayLink, text string) error {
	_, err := i.Sender.SendToLink(ctx, link, provider.OutboundMessage{Text: text}, nil)
	return err
}

// buildRelayPrompt wraps the user's provider-chat message with relay context
// for Moses Manager. It tells MM (a) the turn reply is delivered back to the
// chat automatically so it should just answer normally, and (b) how to send
// asynchronous follow-up messages to this SAME chat after the turn ends —
// via the chat-bot app's `notifyLink` workspace tool, keyed by this link's
// ID — for work that outlives the turn (deployments, autopilot runs, long
// tasks). Without the link ID, MM has no way to address the chat for a
// later push; without the instruction, it does not know the surface exists.
func buildRelayPrompt(link *db.ChatRelayLink, msg provider.InboundMessage) string {
	return fmt.Sprintf(`[moses-chat-bot relay context]
This message was relayed from a %s chat. Your reply to this turn is delivered
back to that chat automatically — just answer normally; do NOT call a tool to
send your immediate reply (it would post twice).

If you start work that finishes AFTER this turn ends (a deployment, an
autopilot run, any long-running task), send progress or result updates to
this same chat later with the chat-bot app's "notifyLink" workspace tool:
  - chat link id: %s
  - arguments: {"id": "%s", "text": "<your update>"}

User's message:
%s`, capitalize(msg.Provider), link.ID, link.ID, msg.Text)
}

// dispatchToMM resolves the per-chat conversation, subscribes to the WS,
// fires the streaming chat request, aggregates events, and replies.
func (i *Inbound) dispatchToMM(
	ctx context.Context,
	link *db.ChatRelayLink,
	msg provider.InboundMessage,
	logger *slog.Logger,
) error {
	// Decrypt the user's API key.
	plaintext, err := i.Envelope.Decrypt(link.TenantID, link.EncryptedAPIKey, link.EncryptionKeyID)
	if err != nil {
		logger.Error("decrypt api key", slog.String("err", err.Error()))
		_, _ = i.Sender.SendToLink(ctx, link, provider.OutboundMessage{
			Text: "Internal error decrypting your stored credentials. Please re-link from the Moses UI.",
		}, nil)
		return err
	}
	bearer := string(plaintext)

	chatClient := i.ChatFactory(bearer)

	// Resolve or create conversation.
	state, err := i.Store.GetOrCreate(ctx, link.ID, msg.ProviderChatID)
	if err != nil {
		return fmt.Errorf("relay: get/create chat state: %w", err)
	}

	var conversationID uuid.UUID
	if state.MosesConversationID != nil {
		conversationID = *state.MosesConversationID
	} else {
		title := fmt.Sprintf("%s: %s", capitalize(msg.Provider), msg.ProviderUserID)
		conv, err := chatClient.CreateConversation(ctx, mosesclient.CreateConversationOpts{
			Title:  &title,
			Source: "chat-bot-bridge",
		})
		if err != nil {
			if errors.Is(err, mosesclient.ErrUnauthorized) {
				i.handleUnauthorized(ctx, link, logger)
				return err
			}
			return fmt.Errorf("relay: create conversation: %w", err)
		}
		conversationID = conv.ID
		if err := i.Store.UpdateConversationID(ctx, link.ID, msg.ProviderChatID, conversationID); err != nil {
			logger.Warn("persist conversation id", slog.String("err", err.Error()))
		}
	}
	logger = logger.With(slog.String("conversation_id", conversationID.String()))

	// What MM actually receives: the user's text wrapped with relay context
	// (chat link id + how to push async follow-ups). Built once and used for
	// both the streaming path and the sync fallback.
	relayPrompt := buildRelayPrompt(link, msg)

	// Subscribe BEFORE firing the stream so we don't lose early chunks.
	sub, err := i.WSPool.Get(ctx, link.ID, bearer, conversationID)
	if err != nil {
		logger.Warn("ws subscribe failed; falling back to sync", slog.String("err", err.Error()))
		return i.syncFallback(ctx, link, chatClient, conversationID, relayPrompt, "ws_subscribe_failed", logger)
	}
	i.WSPool.Touch(link.ID)

	// Fire the stream request.
	if _, err := chatClient.StreamChatMessage(ctx, mosesclient.ChatStreamOpts{
		Message:        relayPrompt,
		ConversationID: conversationID.String(),
	}); err != nil {
		if errors.Is(err, mosesclient.ErrUnauthorized) {
			i.handleUnauthorized(ctx, link, logger)
			return err
		}
		logger.Warn("stream rpc failed; falling back to sync", slog.String("err", err.Error()))
		return i.syncFallback(ctx, link, chatClient, conversationID, relayPrompt, "stream_dispatch_failed", logger)
	}

	// Aggregate events.
	aggregated, status, err := i.aggregateStream(ctx, sub, conversationID, logger)
	if err != nil {
		return err
	}

	switch status {
	case streamStatusComplete:
		_, err := i.Sender.SendToLink(ctx, link, provider.OutboundMessage{Text: aggregated}, &conversationID)
		return err
	case streamStatusTimeout:
		_, err := i.Sender.SendToLink(ctx, link, provider.OutboundMessage{
			Text: "Moses is still working on this — try again in a moment.",
		}, &conversationID)
		return err
	case streamStatusDisconnected:
		logger.Warn("ws disconnect mid-stream; switching to sync fallback")
		return i.syncFallback(ctx, link, chatClient, conversationID, msg.Text, "ws_disconnected", logger)
	default:
		return fmt.Errorf("relay: unexpected stream status %q", status)
	}
}

type streamStatus string

const (
	streamStatusComplete     streamStatus = "complete"
	streamStatusTimeout      streamStatus = "timeout"
	streamStatusDisconnected streamStatus = "disconnected"
)

// aggregateStream reads from the subscriber's Events() channel, filtering
// by conversation_id, until it sees assistant_message_complete OR a
// terminal disconnect signal OR the StreamTimeout fires.
func (i *Inbound) aggregateStream(
	ctx context.Context,
	sub Subscriber,
	conversationID uuid.UUID,
	logger *slog.Logger,
) (string, streamStatus, error) {
	convStr := conversationID.String()
	var buf strings.Builder
	timer := time.NewTimer(i.StreamTimeout)
	defer timer.Stop()

	events := sub.Events()
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return buf.String(), streamStatusDisconnected, nil
			}
			if ev.Type == "error" {
				// Terminal disconnect emitted by WSSubscriber.run.
				return buf.String(), streamStatusDisconnected, nil
			}
			if ev.ConversationID != "" && ev.ConversationID != convStr {
				continue
			}
			switch ev.Type {
			case "assistant_message_chunk":
				// Decode {"content":"..."} or {"text":"..."}.
				if chunk := extractChunkText(ev.Message); chunk != "" {
					buf.WriteString(chunk)
				}
			case "assistant_message_complete":
				return buf.String(), streamStatusComplete, nil
			default:
				// Subscription ack, domain_event, etc. — ignore.
			}
		case <-timer.C:
			logger.Warn("stream timeout", slog.Duration("after", i.StreamTimeout))
			return buf.String(), streamStatusTimeout, nil
		case <-ctx.Done():
			return buf.String(), streamStatusTimeout, ctx.Err()
		}
	}
}

// extractChunkText pulls the assistant text out of the assistant_message_chunk
// Message envelope. The platform sometimes uses {"content": "..."} and
// sometimes {"text": "..."} — accept either.
func extractChunkText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var probe map[string]interface{}
	if err := json.Unmarshal(raw, &probe); err != nil {
		// Bare string?
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
		return ""
	}
	for _, k := range []string{"content", "text", "chunk", "delta"} {
		if v, ok := probe[k]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
	}
	return ""
}

// syncFallback calls SendChatMessageSync as a degraded-mode path. The
// reason string is stamped into outbound message metadata.
func (i *Inbound) syncFallback(
	ctx context.Context,
	link *db.ChatRelayLink,
	chatClient PerKeyChatClient,
	conversationID uuid.UUID,
	prompt string,
	reason string,
	logger *slog.Logger,
) error {
	resp, err := chatClient.SendChatMessageSync(ctx, mosesclient.ChatSyncOpts{
		Message:        prompt,
		ConversationID: conversationID.String(),
	})
	if err != nil {
		if errors.Is(err, mosesclient.ErrUnauthorized) {
			i.handleUnauthorized(ctx, link, logger)
			return err
		}
		logger.Error("sync fallback failed", slog.String("err", err.Error()))
		_, _ = i.Sender.SendToLink(ctx, link, provider.OutboundMessage{
			Text: "Moses is temporarily unreachable. Please try again in a moment.",
		}, &conversationID)
		return err
	}
	out := provider.OutboundMessage{Text: resp.Message}
	rowID, sendErr := i.Sender.SendToLink(ctx, link, out, &conversationID)
	logger.Info("sync fallback delivered",
		slog.String("reason", reason),
		slog.String("audit_row", rowID.String()),
	)
	return sendErr
}

// handleUnauthorized marks the link inactive and tells the user to relink.
// Best-effort: persist-failures are logged but do not propagate so the
// user always sees the friendly message.
func (i *Inbound) handleUnauthorized(ctx context.Context, link *db.ChatRelayLink, logger *slog.Logger) {
	if err := i.Store.DeactivateLink(ctx, link.TenantID, link.ID, "platform_401"); err != nil {
		logger.Warn("deactivate link", slog.String("err", err.Error()))
	}
	_, _ = i.Sender.SendToLink(ctx, link, provider.OutboundMessage{
		Text: "Your Moses key was revoked — please /unlink and re-link from the web UI.",
	}, nil)
}

// replyUnlinked sends a plain-text reply to a provider user who has no link
// row to anchor to. NOT persisted — there is no link to attach the audit
// row to. Goes straight to the provider adapter.
func (i *Inbound) replyUnlinked(ctx context.Context, msg provider.InboundMessage, text string) {
	p, ok := i.Registry.Get(msg.Provider)
	if !ok {
		i.Logger.Warn("unlinked reply but provider not registered", slog.String("provider", msg.Provider))
		return
	}
	chat := provider.ChatRef{Provider: msg.Provider, ProviderChatID: msg.ProviderChatID}
	if err := p.SendMessage(ctx, chat, provider.OutboundMessage{Text: text}); err != nil {
		i.Logger.Warn("unlinked reply failed", slog.String("err", err.Error()))
	}
}

// sendNoLinkReply replies to an unrecognised provider user with the linking
// instructions: register with /start, then claim a code with /link.
func (i *Inbound) sendNoLinkReply(ctx context.Context, msg provider.InboundMessage) {
	i.replyUnlinked(ctx, msg, "I don't recognise this chat yet. Send `/start`, then generate a 6-digit code in your Moses chat-bot app and send `/link <code>` here to connect.")
}

// buildInboundMetadata captures provider context that may be useful for
// later audit/debugging. Attachments and the raw JSON go here.
func buildInboundMetadata(msg provider.InboundMessage) []byte {
	m := map[string]interface{}{
		"received_at":      msg.ReceivedAt.UTC().Format(time.RFC3339Nano),
		"provider_chat_id": msg.ProviderChatID,
	}
	if len(msg.Attachments) > 0 {
		m["attachments"] = msg.Attachments
	}
	b, err := json.Marshal(m)
	if err != nil {
		return []byte("{}")
	}
	return b
}

// capitalize uppercases the first ASCII letter of s. Sufficient for the
// short provider names we have today; we avoid golang.org/x/text/cases to
// keep the dependency surface minimal.
func capitalize(s string) string {
	if s == "" {
		return s
	}
	if s[0] >= 'a' && s[0] <= 'z' {
		return string(s[0]-32) + s[1:]
	}
	return s
}

func helpText() string {
	return strings.Join([]string{
		"Commands:",
		"/start — greeting + linking instructions",
		"/link <code> — link this chat to your Moses account",
		"/unlink — disconnect this chat from Moses",
		"/clear — start a fresh Moses conversation",
		"/tickets — list your open tickets (via Moses Manager)",
		"/status — show workspace status",
		"/help — show this list",
		"Anything else: forwarded to Moses Manager.",
	}, "\n")
}
