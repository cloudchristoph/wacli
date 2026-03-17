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
	var chatDelay time.Duration
	var limit int
	var skipOnError bool

	cmd := &cobra.Command{
		Use:   "backfill-all",
		Short: "Request older messages for ALL locally-known chats (sequential on-demand history sync)",
		Long: `Iterates every chat that already has at least one message stored locally and
requests additional history via on-demand sync. Progress and an ETA estimate
are printed to stderr. Use --limit to cap the number of chats processed.`,
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
				Limit:          limit,
				ChatDelay:      chatDelay,
				SkipOnError:    skipOnError,
			})
			if err != nil {
				return err
			}

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{
					"processed":      res.Processed,
					"skipped":        res.Skipped,
					"messages_added": res.MessagesAdded,
					"elapsed_s":      res.Elapsed.Seconds(),
				})
			}

			fmt.Fprintf(os.Stdout, "Backfill-all complete. Processed: %d, Skipped: %d, Messages added: %d, Elapsed: %s\n",
				res.Processed, res.Skipped, res.MessagesAdded, res.Elapsed.Round(time.Second))
			return nil
		},
	}

	cmd.Flags().IntVar(&count, "count", 50, "messages to request per on-demand sync per chat")
	cmd.Flags().IntVar(&requests, "requests", 1, "on-demand requests per chat")
	cmd.Flags().DurationVar(&wait, "wait", 60*time.Second, "time to wait for each on-demand response")
	cmd.Flags().DurationVar(&idleExit, "idle-exit", 5*time.Second, "idle timeout after backfill requests per chat")
	cmd.Flags().DurationVar(&chatDelay, "chat-delay", 5*time.Second, "pause between chats to avoid flooding")
	cmd.Flags().IntVar(&limit, "limit", 0, "max number of chats to process (0 = all)")
	cmd.Flags().BoolVar(&skipOnError, "skip-on-error", true, "log errors and continue instead of aborting")
	return cmd
}
