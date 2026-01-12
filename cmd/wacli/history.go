package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/steipete/wacli/internal/app"
	"github.com/steipete/wacli/internal/out"
)

func newHistoryCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "history",
		Short: "History backfill (best-effort; requires prior auth)",
	}
	cmd.AddCommand(newHistoryBackfillCmd(flags))
	cmd.AddCommand(newHistoryBackfillAllCmd(flags))
	return cmd
}

func newHistoryBackfillCmd(flags *rootFlags) *cobra.Command {
	var chat string
	var count int
	var requests int
	var wait time.Duration
	var idleExit time.Duration

	cmd := &cobra.Command{
		Use:   "backfill",
		Short: "Request older messages for a chat from your primary device (on-demand history sync)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if chat == "" {
				return fmt.Errorf("--chat is required")
			}

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			a, lk, err := newApp(ctx, flags, true, false)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			res, err := a.BackfillHistory(ctx, app.BackfillOptions{
				ChatJID:        chat,
				Count:          count,
				Requests:       requests,
				WaitPerRequest: wait,
				IdleExit:       idleExit,
			})
			if err != nil {
				return err
			}

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{
					"chat":            res.ChatJID,
					"requests_sent":   res.RequestsSent,
					"responses_seen":  res.ResponsesSeen,
					"messages_added":  res.MessagesAdded,
					"messages_synced": res.MessagesSynced,
				})
			}

			fmt.Fprintf(os.Stdout, "Backfill complete for %s. Added %d messages (%d requests).\n", res.ChatJID, res.MessagesAdded, res.RequestsSent)
			return nil
		},
	}

	cmd.Flags().StringVar(&chat, "chat", "", "chat JID")
	cmd.Flags().IntVar(&count, "count", 50, "number of messages to request per on-demand sync (recommended: 50)")
	cmd.Flags().IntVar(&requests, "requests", 1, "number of on-demand requests to attempt")
	cmd.Flags().DurationVar(&wait, "wait", 60*time.Second, "time to wait for an on-demand response per request")
	cmd.Flags().DurationVar(&idleExit, "idle-exit", 5*time.Second, "exit after being idle (after backfill requests)")
	return cmd
}

func newHistoryBackfillAllCmd(flags *rootFlags) *cobra.Command {
	var count int
	var requests int
	var wait time.Duration
	var idleExit time.Duration
	var workers int
	var maxErrors int

	cmd := &cobra.Command{
		Use:   "backfill-all",
		Short: "Request older messages for all chats from your primary device",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			a, lk, err := newApp(ctx, flags, true, false)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			res, err := a.BackfillAllChats(ctx, app.BackfillAllOptions{
				Count:          count,
				Requests:       requests,
				WaitPerRequest: wait,
				IdleExit:       idleExit,
				Workers:        workers,
				MaxErrors:      maxErrors,
			})
			if err != nil {
				return err
			}

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{
					"chats_processed": res.ChatsProcessed,
					"chats_success":   res.ChatsSuccess,
					"chats_failed":    res.ChatsFailed,
					"total_requests":  res.TotalRequests,
					"total_messages":  res.TotalMessages,
				})
			}

			fmt.Fprintf(os.Stdout, "Backfill complete: %d chats processed (%d successful, %d failed), %d messages added\n",
				res.ChatsProcessed, res.ChatsSuccess, res.ChatsFailed, res.TotalMessages)
			return nil
		},
	}

	cmd.Flags().IntVar(&count, "count", 50, "number of messages to request per on-demand sync (recommended: 50)")
	cmd.Flags().IntVar(&requests, "requests", 1, "number of on-demand requests to attempt per chat")
	cmd.Flags().DurationVar(&wait, "wait", 60*time.Second, "time to wait for an on-demand response per request")
	cmd.Flags().DurationVar(&idleExit, "idle-exit", 5*time.Second, "exit after being idle (after backfill requests)")
	cmd.Flags().IntVar(&workers, "workers", 1, "number of concurrent workers for backfilling chats")
	cmd.Flags().IntVar(&maxErrors, "max-errors", 5, "maximum number of errors before aborting")
	return cmd
}
