package app

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
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

type BackfillAllOptions struct {
	Count          int
	Requests       int
	WaitPerRequest time.Duration
	IdleExit       time.Duration
	Workers        int
	MaxErrors      int
}

type BackfillAllResult struct {
	ChatsProcessed int
	ChatsSuccess   int
	ChatsFailed    int
	TotalRequests  int
	TotalMessages  int64
}

type backfillJob struct {
	chatJID string
}

func (a *App) BackfillAllChats(ctx context.Context, opts BackfillAllOptions) (BackfillAllResult, error) {
	if opts.Workers <= 0 {
		opts.Workers = 1
	}
	if opts.MaxErrors <= 0 {
		opts.MaxErrors = 5
	}
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

	// Get all chats
	chats, err := a.db.ListChats("", 0)
	if err != nil {
		return BackfillAllResult{}, fmt.Errorf("list chats: %w", err)
	}

	if len(chats) == 0 {
		return BackfillAllResult{}, fmt.Errorf("no chats found in database")
	}

	fmt.Fprintf(os.Stderr, "Starting backfill for %d chats with %d worker(s)...\n", len(chats), opts.Workers)

	// Create job channel
	jobs := make(chan backfillJob, len(chats))
	for _, chat := range chats {
		jobs <- backfillJob{chatJID: chat.JID}
	}
	close(jobs)

	// Atomic counters
	var processed, success, failed atomic.Int64
	var totalRequests, totalMessages atomic.Int64
	var mu sync.Mutex

	// Worker function
	worker := func() error {
		for job := range jobs {
			// Check if we've exceeded max errors
			if int(failed.Load()) >= opts.MaxErrors {
				return fmt.Errorf("maximum error count (%d) reached", opts.MaxErrors)
			}

			result, err := a.BackfillHistory(ctx, BackfillOptions{
				ChatJID:        job.chatJID,
				Count:          opts.Count,
				Requests:       opts.Requests,
				WaitPerRequest: opts.WaitPerRequest,
				IdleExit:       opts.IdleExit,
			})

			mu.Lock()
			proc := processed.Add(1)
			if err != nil {
				failed.Add(1)
				fmt.Fprintf(os.Stderr, "\rError backfilling %s: %v\n", job.chatJID, err)
			} else {
				success.Add(1)
				totalRequests.Add(int64(result.RequestsSent))
				totalMessages.Add(result.MessagesAdded)
				if proc%10 == 0 {
					fmt.Fprintf(os.Stderr, "\rProgress: %d/%d chats processed, %d successful, %d failed...",
						proc, len(chats), success.Load(), failed.Load())
				}
			}
			mu.Unlock()
		}
		return nil
	}

	// Start workers
	var wg sync.WaitGroup
	errCh := make(chan error, opts.Workers)

	for i := 0; i < opts.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := worker(); err != nil {
				select {
				case errCh <- err:
				default:
				}
			}
		}()
	}

	wg.Wait()
	close(errCh)

	// Check for worker errors
	var workerErr error
	select {
	case workerErr = <-errCh:
	default:
	}

	fmt.Fprintf(os.Stderr, "\nBackfill complete: %d chats processed, %d successful, %d failed\n",
		processed.Load(), success.Load(), failed.Load())
	fmt.Fprintf(os.Stderr, "Total: %d requests, %d messages added\n",
		totalRequests.Load(), totalMessages.Load())

	result := BackfillAllResult{
		ChatsProcessed: int(processed.Load()),
		ChatsSuccess:   int(success.Load()),
		ChatsFailed:    int(failed.Load()),
		TotalRequests:  int(totalRequests.Load()),
		TotalMessages:  totalMessages.Load(),
	}

	if workerErr != nil {
		return result, workerErr
	}

	return result, nil
}
