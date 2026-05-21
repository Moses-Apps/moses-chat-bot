// Inbound is the message dispatch path: provider webhook → Moses Manager
// → response back through the provider. The Sender (outbound.go) handles
// the egress half; this file owns command dispatch and conversation
// resolution.
//
// Delivery model (supersedes the notifyLink-load-bearing model of commit
// 9f64861): the relay fires the streaming chat invocation
// (StreamChatMessage) to kick off a Moses Manager turn, then OBTAINS THE
// TURN REPLY ITSELF by polling the conversation's persisted messages for
// the assistant answer and delivering it via Sender.SendToLink. The
// streaming path is kept because it is what routes every AI-provider
// type, including Anthropic OAuth subscriptions, which the synchronous
// /api/v1/ai/chat path structurally cannot serve (CHAT-6j4in).
//
// Why polling works: StreamChatMessage hits /api/v1/ai/chat/stream, which
// returns immediately and runs the turn in processChatInBackground on a
// server-side background context — the turn completes regardless of the
// HTTP connection and PERSISTS the assistant message to the conversation.
// So after firing the turn the relay can poll the conversation messages
// until the new assistant reply appears.
//
// notifyLink reverts to async follow-ups ONLY (work that finishes after
// the turn ends — a deployment, an autopilot run). It is no longer in the
// critical path of the basic turn reply, so a missed notifyLink call can
// no longer leave the user silent.
//
// Concurrency model: HandleInbound is safe to invoke from many goroutines
// at once. Telegram serialises a 1:1 chat by design, so concurrent turns
// for the same link do not happen in practice.
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
//
// The relay invokes MM via StreamChatMessage: that POST returns once the
// platform has accepted the turn (the turn then runs in a server-side
// background goroutine, decoupled from the HTTP connection — see
// ai_chat_handlers.go StreamChatMessage). The streaming path routes every
// provider type including OAuth subscriptions; the synchronous path
// cannot (CHAT-6j4in). The relay then obtains the turn reply itself via
// GetConversationMessages — see dispatchToMM.
type PerKeyChatClient interface {
	CreateConversation(ctx context.Context, opts mosesclient.CreateConversationOpts) (*mosesclient.Conversation, error)
	StreamChatMessage(ctx context.Context, opts mosesclient.ChatStreamOpts) (*mosesclient.ChatStreamAck, error)
	GetConversationMessages(ctx context.Context, conversationID uuid.UUID, limit int) ([]mosesclient.ChatMessage, error)
}

// ChatClientFactory returns a PerKeyChatClient configured to authenticate
// outbound calls as the user identified by bearer. Production wires this
// to a closure around *mosesclient.Client.NewClient + BearerAuth.
type ChatClientFactory func(bearer string) PerKeyChatClient

// Polling defaults for harvesting the Moses Manager turn reply. The relay
// fires StreamChatMessage and then polls the conversation's persisted
// messages until the assistant answer for this turn appears.
const (
	// defaultPollInterval is the gap between conversation polls. A few
	// seconds keeps backend load low (one cheap GET per interval per
	// in-flight turn) while still feeling responsive on a chat UI.
	defaultPollInterval = 3 * time.Second

	// defaultPollTimeout caps how long the relay waits for the assistant
	// reply before giving the user a "still working" message. Moses
	// Manager turns are usually well under a minute; multi-tool agentic
	// loops can run longer, so we allow a few minutes. The turn keeps
	// running server-side past this — only the relay's wait ends.
	defaultPollTimeout = 4 * time.Minute
)

// InboundOpts configures the Inbound service.
type InboundOpts struct {
	// MaxConcurrent caps in-flight HandleInbound goroutines. The webhook
	// handler enforces the semaphore upstream; setting it here is
	// informational. Default 32.
	MaxConcurrent int

	// PollInterval is the gap between conversation polls while waiting
	// for the Moses Manager turn reply. Default defaultPollInterval.
	PollInterval time.Duration

	// PollTimeout caps the wait for the turn reply. On timeout the user
	// gets a brief "still working" message. Default defaultPollTimeout.
	PollTimeout time.Duration

	// Logger is required for diagnostics. main passes a configured
	// slog.Logger; tests may pass slog.New(slog.NewTextHandler(io.Discard, nil)).
	Logger *slog.Logger
}

// Inbound is the inbound dispatch service.
type Inbound struct {
	Store       InboundStore
	Sender      *Sender
	Envelope    *crypto.Envelope
	Linker      *linker.Linker
	Registry    *provider.Registry
	ChatFactory ChatClientFactory
	Logger      *slog.Logger

	// PollInterval / PollTimeout govern how the relay harvests the Moses
	// Manager turn reply from the conversation. Defaulted in NewInbound.
	PollInterval time.Duration
	PollTimeout  time.Duration

	// Autopilot is optional — when nil the /autopilot command surface
	// degrades to a "not configured" reply. main.go wires this; tests
	// substitute a fake.
	Autopilot AutopilotService
}

// NewInbound constructs the inbound service. ChatFactory is required for
// production; tests inject a fake to avoid network I/O.
func NewInbound(
	store InboundStore,
	sender *Sender,
	env *crypto.Envelope,
	link *linker.Linker,
	registry *provider.Registry,
	chatFactory ChatClientFactory,
	opts InboundOpts,
) *Inbound {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	pollInterval := opts.PollInterval
	if pollInterval <= 0 {
		pollInterval = defaultPollInterval
	}
	pollTimeout := opts.PollTimeout
	if pollTimeout <= 0 {
		pollTimeout = defaultPollTimeout
	}
	return &Inbound{
		Store:        store,
		Sender:       sender,
		Envelope:     env,
		Linker:       link,
		Registry:     registry,
		ChatFactory:  chatFactory,
		Logger:       logger,
		PollInterval: pollInterval,
		PollTimeout:  pollTimeout,
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
	// A clean parse (parseErr == nil) AND a recognised verb with bad args
	// (ErrInvalidArgs — e.g. "/autopilot wat") both carry a usable Verb, so
	// both are dispatched: the user gets a command reply (a usage hint for
	// bad args) instead of having a "/command ..." message silently
	// forwarded to Moses Manager.
	if parseErr == nil || errors.Is(parseErr, commands.ErrInvalidArgs) {
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
		// Unknown slash command (ErrUnknownCommand) — let MM interpret it.
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
		// Lowercase the subcommand — Parse keeps args verbatim and mobile
		// keyboards autocapitalise ("/autopilot Start").
		switch strings.ToLower(cmd.Args[0]) {
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
// for Moses Manager. The relay obtains this turn's reply ITSELF — it polls
// the conversation for the persisted assistant message and delivers it — so
// MM should just answer normally. MM is told to use the chat-bot app's
// `notifyLink` workspace tool ONLY for async follow-ups: progress or results
// for work that finishes AFTER this turn ends (a deployment, an autopilot
// run). Without the link ID, MM has no way to address the chat for those
// later updates; without the instruction, it does not know the surface
// exists. Using notifyLink for the immediate turn reply would post twice.
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

// dispatchToMM resolves the per-chat conversation, fires a streaming Moses
// Manager turn, then OBTAINS THE TURN REPLY ITSELF by polling the
// conversation's persisted messages and delivers it via Sender.SendToLink.
//
// The streaming invocation (StreamChatMessage) is mandatory: it routes
// every AI-provider type, whereas the synchronous /api/v1/ai/chat path
// 500s for Anthropic OAuth subscriptions (CHAT-6j4in), the primary case
// the relay must serve. The platform acknowledges the POST immediately
// and runs the agentic loop in a server-side background goroutine,
// persisting the assistant turn to the conversation when it completes.
//
// The relay no longer depends on MM calling the notifyLink workspace tool
// for the basic reply (that was the model in commit 9f64861, which put a
// human grant-approval step in the critical path of every reply and risked
// total silence). notifyLink is now for async follow-ups only.
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

	// Record a baseline BEFORE firing the turn: the newest message already
	// in the conversation. The turn reply is the first assistant message
	// that appears after this baseline. For a fresh conversation the
	// baseline is the zero time, so any assistant message qualifies.
	baseline, err := i.latestMessageTime(ctx, chatClient, conversationID)
	if err != nil {
		if errors.Is(err, mosesclient.ErrUnauthorized) {
			i.handleUnauthorized(ctx, link, logger)
			return err
		}
		// A non-fatal baseline read failure (transient 5xx, fresh
		// conversation 404) must not block the turn — fall back to the
		// zero time and let the poller pick the first assistant message.
		logger.Warn("baseline read failed; using zero baseline", slog.String("err", err.Error()))
		baseline = time.Time{}
	}

	// What MM receives: the user's text wrapped with relay context.
	relayPrompt := buildRelayPrompt(link, msg)

	// Fire the streaming turn. The platform returns 200 as soon as it has
	// accepted the turn; the agentic loop then runs in its own background
	// goroutine and persists the assistant message on completion. A failure
	// HERE means the turn never started, so the user gets nothing unless we
	// tell them.
	_, err = chatClient.StreamChatMessage(ctx, mosesclient.ChatStreamOpts{
		Message:        relayPrompt,
		ConversationID: conversationID.String(),
	})
	if err != nil {
		if errors.Is(err, mosesclient.ErrUnauthorized) {
			i.handleUnauthorized(ctx, link, logger)
			return err
		}
		logger.Error("start MM turn failed", slog.String("err", err.Error()))
		_, _ = i.Sender.SendToLink(ctx, link, provider.OutboundMessage{
			Text: "Couldn't reach Moses just now — your message wasn't delivered. Please try again in a moment.",
		}, &conversationID)
		return err
	}
	logger.Info("MM turn started; polling conversation for reply")

	// Poll the conversation until the assistant reply lands or the timeout
	// fires. The turn keeps running server-side past the timeout — only the
	// relay's wait ends.
	reply, err := i.pollForReply(ctx, chatClient, link, conversationID, baseline, logger)
	if err != nil {
		if errors.Is(err, mosesclient.ErrUnauthorized) {
			i.handleUnauthorized(ctx, link, logger)
			return err
		}
		if errors.Is(err, errPollTimeout) {
			logger.Warn("MM reply poll timed out")
			_, _ = i.Sender.SendToLink(ctx, link, provider.OutboundMessage{
				Text: "Moses is still working on this — I'll send the answer here as soon as it's ready.",
			}, &conversationID)
			return nil
		}
		// Context cancellation or a persistent poll error.
		logger.Error("poll for MM reply failed", slog.String("err", err.Error()))
		return err
	}

	if _, err := i.Sender.SendToLink(ctx, link, provider.OutboundMessage{Text: reply}, &conversationID); err != nil {
		logger.Error("deliver MM reply failed", slog.String("err", err.Error()))
		return err
	}
	logger.Info("MM reply delivered")
	return nil
}

// errPollTimeout is the sentinel pollForReply returns when the assistant
// reply does not appear before PollTimeout. dispatchToMM maps it to a
// brief "still working" message rather than treating it as an error.
var errPollTimeout = errors.New("relay: timed out waiting for MM reply")

// latestMessageTime returns the CreatedAt of the newest message currently
// in the conversation, or the zero time when the conversation is empty or
// does not exist yet (404). It is the pre-turn baseline: the assistant
// reply is the first message newer than this.
func (i *Inbound) latestMessageTime(
	ctx context.Context,
	chatClient PerKeyChatClient,
	conversationID uuid.UUID,
) (time.Time, error) {
	// limit=1 is enough — messages come back chronologically, so a 1-row
	// page is the single newest message.
	msgs, err := chatClient.GetConversationMessages(ctx, conversationID, 1)
	if err != nil {
		if errors.Is(err, mosesclient.ErrNotFound) {
			return time.Time{}, nil
		}
		return time.Time{}, err
	}
	if len(msgs) == 0 {
		return time.Time{}, nil
	}
	return msgs[len(msgs)-1].CreatedAt, nil
}

// pollForReply polls the conversation on PollInterval until an assistant
// message strictly newer than baseline appears, then returns its text. It
// returns errPollTimeout when PollTimeout elapses first, ErrUnauthorized
// when the key is revoked mid-poll, or a context error on cancellation.
//
// Transient poll errors (5xx, network blips) are logged and retried — a
// single bad GET must not abandon a turn whose reply may already be on the
// way. Only a revoked key or context cancellation stops the loop early.
func (i *Inbound) pollForReply(
	ctx context.Context,
	chatClient PerKeyChatClient,
	link *db.ChatRelayLink,
	conversationID uuid.UUID,
	baseline time.Time,
	logger *slog.Logger,
) (string, error) {
	pollCtx, cancel := context.WithTimeout(ctx, i.PollTimeout)
	defer cancel()

	ticker := time.NewTicker(i.PollInterval)
	defer ticker.Stop()

	for {
		// Check immediately on entry and then once per tick — the turn is
		// already running, and a fast reply should not wait a full interval.
		msgs, err := chatClient.GetConversationMessages(pollCtx, conversationID, 50)
		if err != nil {
			if errors.Is(err, mosesclient.ErrUnauthorized) {
				return "", err
			}
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				// Distinguish "our poll timeout" from the parent context.
				if ctx.Err() == nil {
					return "", errPollTimeout
				}
				return "", ctx.Err()
			}
			logger.Warn("conversation poll error; retrying", slog.String("err", err.Error()))
		} else if reply, ok := newestAssistantAfter(msgs, baseline); ok {
			return reply, nil
		}

		select {
		case <-pollCtx.Done():
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			return "", errPollTimeout
		case <-ticker.C:
		}
	}
}

// newestAssistantAfter scans a chronologically-ordered message slice and
// returns the text of the newest assistant message strictly newer than
// baseline. ok is false when no such message exists.
func newestAssistantAfter(msgs []mosesclient.ChatMessage, baseline time.Time) (string, bool) {
	for idx := len(msgs) - 1; idx >= 0; idx-- {
		m := msgs[idx]
		if m.Role != "assistant" {
			continue
		}
		if m.CreatedAt.After(baseline) {
			return m.Content, true
		}
		// Older than the baseline — and everything before it is older
		// still, so no qualifying assistant message exists.
		return "", false
	}
	return "", false
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
