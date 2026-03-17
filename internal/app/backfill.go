package app

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

type BackfillOptions struct {
	ChatJID        string
	Count          int
	Requests       int
	WaitPerRequest time.Duration
	IdleExit       time.Duration
}

type BackfillResult struct {
	ChatJID        string
	RequestsSent   int
	ResponsesSeen  int
	MessagesAdded  int64
	MessagesSynced int64
}

type onDemandResponse struct {
	conversations int
	messages      int
	endType       waHistorySync.Conversation_EndOfHistoryTransferType
}

func (a *App) BackfillHistory(ctx context.Context, opts BackfillOptions) (BackfillResult, error) {
	chatStr := strings.TrimSpace(opts.ChatJID)
	if chatStr == "" {
		return BackfillResult{}, fmt.Errorf("--chat is required")
	}
	chat, err := types.ParseJID(chatStr)
	if err != nil {
		return BackfillResult{}, fmt.Errorf("parse chat JID: %w", err)
	}
	chatStr = chat.String()

	if opts.Count <= 0 {
		opts.Count = 50
	}
	if opts.Requests <= 0 {
		opts.Requests = 1
	}
	if opts.WaitPerRequest <= 0 {
		opts.WaitPerRequest = 60 * time.Second
	}
	if opts.IdleExit <= 0 {
		opts.IdleExit = 5 * time.Second
	}

	if err := a.EnsureAuthed(); err != nil {
		return BackfillResult{}, err
	}
	if err := a.OpenWA(); err != nil {
		return BackfillResult{}, err
	}

	beforeCount, _ := a.db.CountMessages()

	var mu sync.Mutex
	var waitCh chan onDemandResponse
	handlerID := a.wa.AddEventHandler(func(evt interface{}) {
		hs, ok := evt.(*events.HistorySync)
		if !ok || hs == nil || hs.Data == nil {
			return
		}
		if hs.Data.GetSyncType() != waHistorySync.HistorySync_ON_DEMAND {
			return
		}

		for _, conv := range hs.Data.GetConversations() {
			if strings.TrimSpace(conv.GetID()) != chatStr {
				continue
			}
			mu.Lock()
			ch := waitCh
			mu.Unlock()
			if ch == nil {
				return
			}
			resp := onDemandResponse{
				conversations: len(hs.Data.GetConversations()),
				messages:      len(conv.GetMessages()),
				endType:       conv.GetEndOfHistoryTransferType(),
			}
			select {
			case ch <- resp:
			default:
			}
			return
		}
	})
	defer a.wa.RemoveEventHandler(handlerID)

	var requestsSent int
	var responsesSeen int

	syncRes, err := a.Sync(ctx, SyncOptions{
		Mode:     SyncModeOnce,
		AllowQR:  false,
		IdleExit: opts.IdleExit,
		AfterConnect: func(ctx context.Context) error {
			for i := 0; i < opts.Requests; i++ {
				oldest, err := a.db.GetOldestMessageInfo(chatStr)
				if err != nil {
					if err == sql.ErrNoRows {
						return fmt.Errorf("no messages for %s in local DB; run `wacli sync` first", chatStr)
					}
					return err
				}

				reqInfo := types.MessageInfo{
					MessageSource: types.MessageSource{
						Chat:     chat,
						IsFromMe: oldest.FromMe,
					},
					ID:        types.MessageID(oldest.MsgID),
					Timestamp: oldest.Timestamp,
				}

				ch := make(chan onDemandResponse, 4)
				mu.Lock()
				waitCh = ch
				mu.Unlock()

				requestsSent++
				fmt.Fprintf(os.Stderr, "Requesting %d older messages for %s...\n", opts.Count, chatStr)
				if _, err := a.wa.RequestHistorySyncOnDemand(ctx, reqInfo, opts.Count); err != nil {
					return err
				}

				var resp onDemandResponse
				select {
				case <-ctx.Done():
					return ctx.Err()
				case resp = <-ch:
					responsesSeen++
				case <-time.After(opts.WaitPerRequest):
					return fmt.Errorf("timed out waiting for on-demand history sync response")
				}

				mu.Lock()
				if waitCh == ch {
					waitCh = nil
				}
				mu.Unlock()

				fmt.Fprintf(os.Stderr, "On-demand history sync: %d conversations, %d messages.\n", resp.conversations, resp.messages)

				newOldest, err := a.db.GetOldestMessageInfo(chatStr)
				if err == nil && newOldest.MsgID == oldest.MsgID {
					fmt.Fprintln(os.Stderr, "No older messages were added (stopping).")
					return nil
				}
				if resp.messages <= 0 {
					fmt.Fprintln(os.Stderr, "No messages returned (stopping).")
					return nil
				}
				if resp.endType == waHistorySync.Conversation_COMPLETE_AND_NO_MORE_MESSAGE_REMAIN_ON_PRIMARY {
					fmt.Fprintln(os.Stderr, "Reached start of chat history (stopping).")
					return nil
				}
			}
			return nil
		},
	})
	if err != nil {
		return BackfillResult{}, err
	}

	afterCount, _ := a.db.CountMessages()

	return BackfillResult{
		ChatJID:        chatStr,
		RequestsSent:   requestsSent,
		ResponsesSeen:  responsesSeen,
		MessagesAdded:  afterCount - beforeCount,
		MessagesSynced: syncRes.MessagesStored,
	}, nil
}

// BackfillAllOptions controls BackfillAllChats behaviour.
type BackfillAllOptions struct {
	// Per-request options forwarded to BackfillHistory.
	Count          int
	Requests       int
	WaitPerRequest time.Duration
	IdleExit       time.Duration

	// Limit caps the number of chats processed (0 = all).
	Limit int

	// ChatDelay is how long to pause between chats.
	ChatDelay time.Duration

	// SkipOnError: if true, log and continue; otherwise abort.
	SkipOnError bool
}

// BackfillAllResult is the aggregate result of BackfillAllChats.
type BackfillAllResult struct {
	Processed     int
	Skipped       int
	MessagesAdded int64
	Elapsed       time.Duration
}

// BackfillAllChats iterates every locally-known chat that already has at least
// one message and requests additional history for each one sequentially.
// Progress lines are written to stderr.
func (a *App) BackfillAllChats(ctx context.Context, opts BackfillAllOptions) (BackfillAllResult, error) {
	chats, err := a.db.ListChatsWithMessages(opts.Limit)
	if err != nil {
		return BackfillAllResult{}, fmt.Errorf("list chats: %w", err)
	}
	total := len(chats)
	if total == 0 {
		fmt.Fprintln(os.Stderr, "No chats with messages found.")
		return BackfillAllResult{}, nil
	}

	var (
		start         = time.Now()
		totalAdded    int64
		skipped       int
		durations     []time.Duration // elapsed time per completed chat
	)

	for i, chat := range chats {
		if ctx.Err() != nil {
			break
		}

		// Skip broadcast lists and LID-addressed chats — WhatsApp never responds
		// to on-demand history sync requests for these.
		jidLower := strings.ToLower(chat.JID)
		if strings.Contains(jidLower, "@broadcast") || strings.Contains(jidLower, "@lid") {
			fmt.Fprintf(os.Stderr, "[%d/%d] Skipping %s (unsupported JID type)\n", i+1, total, chat.JID)
			skipped++
			continue
		}

		// Compute ETA from rolling average of completed chats.
		var etaStr string
		if i > 0 {
			var sum time.Duration
			for _, d := range durations {
				sum += d
			}
			avg := sum / time.Duration(i)
			remaining := avg * time.Duration(total-i)
			etaStr = fmt.Sprintf(", ~%s remaining", remaining.Round(time.Second))
		}

		label := chat.Name
		if label == "" {
			label = chat.JID
		}
		fmt.Fprintf(os.Stderr, "[%d/%d] %s (%s) — elapsed %s%s\n",
			i+1, total, label, chat.JID,
			time.Since(start).Round(time.Second), etaStr)

		chatStart := time.Now()
		res, err := a.BackfillHistory(ctx, BackfillOptions{
			ChatJID:        chat.JID,
			Count:          opts.Count,
			Requests:       opts.Requests,
			WaitPerRequest: opts.WaitPerRequest,
			IdleExit:       opts.IdleExit,
		})
		chatElapsed := time.Since(chatStart)

		if err != nil {
			if opts.SkipOnError {
				fmt.Fprintf(os.Stderr, "  WARNING: skipping %s: %v\n", chat.JID, err)
				skipped++
			} else {
				return BackfillAllResult{
					Processed:     i,
					Skipped:       skipped,
					MessagesAdded: totalAdded,
					Elapsed:       time.Since(start),
				}, fmt.Errorf("backfill %s: %w", chat.JID, err)
			}
		} else {
			totalAdded += res.MessagesAdded
			durations = append(durations, chatElapsed)
		}

		// Pause between chats (unless it's the last one).
		if opts.ChatDelay > 0 && i < total-1 {
			select {
			case <-ctx.Done():
			case <-time.After(opts.ChatDelay):
			}
		}
	}

	result := BackfillAllResult{
		Processed:     total - skipped,
		Skipped:       skipped,
		MessagesAdded: totalAdded,
		Elapsed:       time.Since(start),
	}
	fmt.Fprintf(os.Stderr, "\nDone: %d chats processed, %d skipped, %d messages added (%s total)\n",
		result.Processed, result.Skipped, result.MessagesAdded, result.Elapsed.Round(time.Second))
	return result, nil
}
