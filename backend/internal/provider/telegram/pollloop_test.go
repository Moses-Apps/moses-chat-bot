package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"moses-chat-bot/backend/internal/provider"
)

// updatesResponse renders a getUpdates ok-envelope for the given updates.
func updatesResponse(t *testing.T, updates []Update) string {
	t.Helper()
	raw, err := json.Marshal(updates)
	if err != nil {
		t.Fatalf("marshal updates: %v", err)
	}
	return fmt.Sprintf(`{"ok":true,"result":%s}`, raw)
}

// textUpdate builds an Update carrying a plain text message.
func textUpdate(id int64, chatID int64, userID int64, text string) Update {
	return Update{
		UpdateID: id,
		Message: &Message{
			MessageID: id,
			From:      &User{ID: userID, IsBot: false, Username: "tester"},
			Chat:      Chat{ID: chatID, Type: "private"},
			Date:      time.Now().Unix(),
			Text:      text,
		},
	}
}

// --- Poll (single getUpdates call) -----------------------------------------

func TestPoll_DecodesMessagesAndAdvancesOffset(t *testing.T) {
	a, _ := newTestAdapter(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/getUpdates") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		fmt.Fprint(w, updatesResponse(t, []Update{
			textUpdate(100, 7, 9, "hello"),
			textUpdate(101, 7, 9, "world"),
		}))
	})

	msgs, next, err := a.Poll(context.Background(), 0)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2", len(msgs))
	}
	if next != 102 {
		t.Fatalf("next offset = %d, want 102 (max update_id + 1)", next)
	}
	if msgs[0].Text != "hello" || msgs[1].Text != "world" {
		t.Fatalf("unexpected message texts: %q, %q", msgs[0].Text, msgs[1].Text)
	}
	if msgs[0].ProviderMessageID != "100" {
		t.Fatalf("ProviderMessageID = %q, want 100", msgs[0].ProviderMessageID)
	}
	if msgs[0].Provider != ProviderName {
		t.Fatalf("Provider = %q, want %q", msgs[0].Provider, ProviderName)
	}
}

func TestPoll_EmptyBatchKeepsOffset(t *testing.T) {
	a, _ := newTestAdapter(t, Config{}, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, updatesResponse(t, nil))
	})
	msgs, next, err := a.Poll(context.Background(), 55)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("got %d messages, want 0", len(msgs))
	}
	if next != 55 {
		t.Fatalf("next offset = %d, want 55 (unchanged)", next)
	}
}

func TestPoll_NonMessageUpdateAdvancesOffsetButYieldsNoMessage(t *testing.T) {
	a, _ := newTestAdapter(t, Config{}, func(w http.ResponseWriter, _ *http.Request) {
		// An update with no Message (e.g. an edited_message) — must still
		// advance the offset so it is not re-fetched forever.
		fmt.Fprint(w, updatesResponse(t, []Update{{UpdateID: 200}}))
	})
	msgs, next, err := a.Poll(context.Background(), 0)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("got %d messages, want 0", len(msgs))
	}
	if next != 201 {
		t.Fatalf("next offset = %d, want 201", next)
	}
}

func TestPoll_UnauthorizedIsTerminal(t *testing.T) {
	a, _ := newTestAdapter(t, Config{}, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"ok":false,"error_code":401,"description":"Unauthorized"}`)
	})
	_, _, err := a.Poll(context.Background(), 0)
	if !errors.Is(err, provider.ErrUnauthorized) {
		t.Fatalf("got %v, want provider.ErrUnauthorized", err)
	}
}

func TestPoll_ServerErrorIsTransient(t *testing.T) {
	a, _ := newTestAdapter(t, Config{}, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"ok":false,"error_code":500,"description":"Internal"}`)
	})
	_, _, err := a.Poll(context.Background(), 0)
	if err == nil {
		t.Fatal("expected error for 500")
	}
	if errors.Is(err, provider.ErrUnauthorized) {
		t.Fatal("500 must NOT be classified as ErrUnauthorized")
	}
}

// TestPoll_WebhookAndPollingYieldIdenticalInboundMessage proves the shared
// updateToInbound conversion produces a byte-identical InboundMessage whether
// a message arrives via the webhook or via long-polling.
func TestPoll_WebhookAndPollingYieldIdenticalInboundMessage(t *testing.T) {
	upd := textUpdate(300, 12, 34, "consistency check")

	// Webhook path: HandleWebhook decodes a single-update body.
	body, err := json.Marshal(upd)
	if err != nil {
		t.Fatalf("marshal update: %v", err)
	}
	whAdapter, _ := newTestAdapter(t, Config{}, nil)
	whMsgs, err := whAdapter.HandleWebhook(context.Background(), body, nil)
	if err != nil {
		t.Fatalf("HandleWebhook: %v", err)
	}
	if len(whMsgs) != 1 {
		t.Fatalf("webhook: got %d messages, want 1", len(whMsgs))
	}

	// Polling path: Poll decodes the same update from a getUpdates batch.
	pollAdapter, _ := newTestAdapter(t, Config{}, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, updatesResponse(t, []Update{upd}))
	})
	pollMsgs, _, err := pollAdapter.Poll(context.Background(), 0)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(pollMsgs) != 1 {
		t.Fatalf("poll: got %d messages, want 1", len(pollMsgs))
	}

	wh, pl := whMsgs[0], pollMsgs[0]
	// ReceivedAt is wall-clock and intentionally differs; compare every other
	// field. RawJSON differs only in byte layout (webhook passes the request
	// body, poll re-marshals) but decodes to the same Update — assert that.
	if wh.Provider != pl.Provider ||
		wh.ProviderUserID != pl.ProviderUserID ||
		wh.ProviderChatID != pl.ProviderChatID ||
		wh.Text != pl.Text ||
		wh.ProviderMessageID != pl.ProviderMessageID {
		t.Fatalf("webhook vs poll InboundMessage diverged:\n  webhook=%+v\n  poll=%+v", wh, pl)
	}
	var whU, plU Update
	if err := json.Unmarshal(wh.RawJSON, &whU); err != nil {
		t.Fatalf("decode webhook RawJSON: %v", err)
	}
	if err := json.Unmarshal(pl.RawJSON, &plU); err != nil {
		t.Fatalf("decode poll RawJSON: %v", err)
	}
	if whU.UpdateID != plU.UpdateID || whU.Message.Text != plU.Message.Text {
		t.Fatalf("RawJSON decoded to different updates: %+v vs %+v", whU, plU)
	}
}

// --- PollLoop (the long-running loop) ---------------------------------------

// stubDispatcher records every InboundMessage HandleInbound receives.
type stubDispatcher struct {
	mu   sync.Mutex
	msgs []provider.InboundMessage
	err  error
}

func (d *stubDispatcher) HandleInbound(_ context.Context, msg provider.InboundMessage) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.msgs = append(d.msgs, msg)
	return d.err
}

func (d *stubDispatcher) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.msgs)
}

func (d *stubDispatcher) snapshot() []provider.InboundMessage {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]provider.InboundMessage(nil), d.msgs...)
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestPollLoop_DrivesRelayAndAdvancesOffset runs the loop against a stub
// getUpdates that returns one batch then empties out, and asserts every
// message reached the dispatcher and the offset advanced (no re-delivery).
func TestPollLoop_DrivesRelayAndAdvancesOffset(t *testing.T) {
	var getUpdatesCalls int32
	var maxSeenOffset int64
	var offMu sync.Mutex

	a, _ := newTestAdapter(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/deleteWebhook"):
			fmt.Fprint(w, `{"ok":true,"result":true}`)
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			n := atomic.AddInt32(&getUpdatesCalls, 1)
			var body GetUpdatesParams
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &body)
			offMu.Lock()
			if body.Offset > maxSeenOffset {
				maxSeenOffset = body.Offset
			}
			offMu.Unlock()
			if n == 1 {
				fmt.Fprint(w, updatesResponse(t, []Update{
					textUpdate(10, 1, 2, "first"),
					textUpdate(11, 1, 2, "second"),
				}))
				return
			}
			// Subsequent polls: empty batch.
			fmt.Fprint(w, updatesResponse(t, nil))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	disp := &stubDispatcher{}
	loop := NewPollLoop(a, disp, context.Background(), discardLogger())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = loop.Run(ctx)
		close(done)
	}()

	// Wait until both messages were dispatched and at least one follow-up poll
	// happened (proving the offset advanced past the delivered batch).
	deadline := time.After(3 * time.Second)
	for {
		if disp.count() >= 2 && atomic.LoadInt32(&getUpdatesCalls) >= 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("timed out: dispatched=%d getUpdates=%d", disp.count(), getUpdatesCalls)
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	<-done

	if disp.count() != 2 {
		t.Fatalf("dispatched %d messages, want exactly 2 (no re-delivery)", disp.count())
	}
	offMu.Lock()
	defer offMu.Unlock()
	if maxSeenOffset != 12 {
		t.Fatalf("max getUpdates offset = %d, want 12 (advanced past update 11)", maxSeenOffset)
	}
}

// TestPollLoop_StopsOnContextCancel proves a cancelled context cleanly stops
// the loop (Run returns nil).
func TestPollLoop_StopsOnContextCancel(t *testing.T) {
	a, _ := newTestAdapter(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/deleteWebhook"):
			fmt.Fprint(w, `{"ok":true,"result":true}`)
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			fmt.Fprint(w, updatesResponse(t, nil))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	loop := NewPollLoop(a, &stubDispatcher{}, context.Background(), discardLogger())
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- loop.Run(ctx) }()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run returned %v on clean cancel, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after context cancel")
	}
}

// TestPollLoop_StopsOnRevokedToken proves a 401 from getUpdates terminates the
// loop (no infinite backoff) and Run returns the terminal error.
func TestPollLoop_StopsOnRevokedToken(t *testing.T) {
	a, _ := newTestAdapter(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/deleteWebhook"):
			fmt.Fprint(w, `{"ok":true,"result":true}`)
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"ok":false,"error_code":401,"description":"Unauthorized"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	loop := NewPollLoop(a, &stubDispatcher{}, context.Background(), discardLogger())
	errCh := make(chan error, 1)
	go func() { errCh <- loop.Run(context.Background()) }()

	select {
	case err := <-errCh:
		if !errors.Is(err, provider.ErrUnauthorized) {
			t.Fatalf("Run returned %v, want provider.ErrUnauthorized", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop on a revoked token")
	}
}

// TestPollLoop_BacksOffOnTransientError proves a transient (5xx) error does not
// kill the loop — it keeps polling and recovers once getUpdates succeeds.
func TestPollLoop_BacksOffOnTransientError(t *testing.T) {
	var calls int32
	a, _ := newTestAdapter(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/deleteWebhook"):
			fmt.Fprint(w, `{"ok":true,"result":true}`)
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			n := atomic.AddInt32(&calls, 1)
			if n == 1 {
				// First poll fails transiently.
				w.WriteHeader(http.StatusInternalServerError)
				fmt.Fprint(w, `{"ok":false,"error_code":500,"description":"Internal"}`)
				return
			}
			if n == 2 {
				// Recovery: deliver a message.
				fmt.Fprint(w, updatesResponse(t, []Update{textUpdate(1, 5, 6, "recovered")}))
				return
			}
			fmt.Fprint(w, updatesResponse(t, nil))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	disp := &stubDispatcher{}
	loop := NewPollLoop(a, disp, context.Background(), discardLogger())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = loop.Run(ctx)
		close(done)
	}()

	// The first poll fails; the loop backs off pollBackoffMin (1s) then retries
	// and delivers. Allow generous headroom over the 1s backoff.
	deadline := time.After(5 * time.Second)
	for disp.count() == 0 {
		select {
		case <-deadline:
			t.Fatalf("loop did not recover from transient error; getUpdates calls=%d", atomic.LoadInt32(&calls))
		case <-time.After(20 * time.Millisecond):
		}
	}
	cancel()
	<-done

	got := disp.snapshot()
	if len(got) == 0 || got[0].Text != "recovered" {
		t.Fatalf("expected the post-backoff message to be delivered, got %+v", got)
	}
	if atomic.LoadInt32(&calls) < 2 {
		t.Fatalf("getUpdates called %d times, want >= 2 (retry after backoff)", calls)
	}
}

// TestPollLoop_DeletesWebhookBeforePolling proves the loop drops any active
// webhook before its first getUpdates (the two are mutually exclusive).
func TestPollLoop_DeletesWebhookBeforePolling(t *testing.T) {
	var deletedBeforeFirstPoll atomic.Bool
	var deleteCalled atomic.Bool

	a, _ := newTestAdapter(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/deleteWebhook"):
			deleteCalled.Store(true)
			fmt.Fprint(w, `{"ok":true,"result":true}`)
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			if deleteCalled.Load() {
				deletedBeforeFirstPoll.Store(true)
			}
			fmt.Fprint(w, updatesResponse(t, nil))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	loop := NewPollLoop(a, &stubDispatcher{}, context.Background(), discardLogger())
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = loop.Run(ctx) }()

	deadline := time.After(2 * time.Second)
	for !deletedBeforeFirstPoll.Load() {
		select {
		case <-deadline:
			cancel()
			t.Fatalf("deleteWebhook was not called before the first getUpdates (deleteCalled=%v)", deleteCalled.Load())
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
}
