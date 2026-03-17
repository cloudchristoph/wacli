package main

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/steipete/wacli/internal/app"
	"github.com/steipete/wacli/internal/out"
)

func newChatsCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "chats",
		Short: "List chats from the local DB",
	}
	cmd.AddCommand(newChatsListCmd(flags))
	cmd.AddCommand(newChatsShowCmd(flags))
	cmd.AddCommand(newChatsConsolidateLIDCmd(flags))
	return cmd
}

func newChatsListCmd(flags *rootFlags) *cobra.Command {
	var query string
	var limit int
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List chats",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			a, lk, err := newApp(ctx, flags, false, false)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			chats, err := a.DB().ListChats(query, limit)
			if err != nil {
				return err
			}
			if flags.asJSON {
				return out.WriteJSON(os.Stdout, chats)
			}

			w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
			fmt.Fprintln(w, "KIND\tNAME\tJID\tLAST")
			for _, c := range chats {
				name := c.Name
				if name == "" {
					name = c.JID
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", c.Kind, truncate(name, 28), c.JID, c.LastMessageTS.Local().Format("2006-01-02 15:04:05"))
			}
			_ = w.Flush()
			return nil
		},
	}
	cmd.Flags().StringVar(&query, "query", "", "search query")
	cmd.Flags().IntVar(&limit, "limit", 50, "limit")
	return cmd
}

func newChatsShowCmd(flags *rootFlags) *cobra.Command {
	var jid string
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Show one chat",
		RunE: func(cmd *cobra.Command, args []string) error {
			if jid == "" {
				return fmt.Errorf("--jid is required")
			}
			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			a, lk, err := newApp(ctx, flags, false, false)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			c, err := a.DB().GetChat(jid)
			if err != nil {
				return err
			}
			if flags.asJSON {
				return out.WriteJSON(os.Stdout, c)
			}
			fmt.Fprintf(os.Stdout, "JID: %s\nKind: %s\nName: %s\nLast: %s\n", c.JID, c.Kind, c.Name, c.LastMessageTS.Local().Format(time.RFC3339))
			return nil
		},
	}
	cmd.Flags().StringVar(&jid, "jid", "", "chat JID")
	return cmd
}

func newChatsConsolidateLIDCmd(flags *rootFlags) *cobra.Command {
	var dryRun bool
	var limit int

	cmd := &cobra.Command{
		Use:   "consolidate-lid",
		Short: "Consolidate @lid chats into phone JIDs using session mappings (lossless)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			a, lk, err := newApp(ctx, flags, false, false)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			res, err := a.ConsolidateLIDChats(ctx, app.ConsolidateLIDOptions{
				DryRun: dryRun,
				Limit:  limit,
			})
			if err != nil {
				return err
			}

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, res)
			}

			if dryRun {
				fmt.Fprintf(os.Stdout, "Dry-run: %d mappings found, %d candidates.\n", res.MappingsFound, res.MappingsTried)
				for _, p := range res.Pairs {
					fmt.Fprintf(os.Stdout, "  %s\n", p)
				}
				return nil
			}

			fmt.Fprintf(os.Stdout,
				"Consolidation complete: merged %d chats, moved %d messages (%d mappings found, %d tried).\n",
				res.ChatsMerged, res.MessagesMoved, res.MappingsFound, res.MappingsTried)
			return nil
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", true, "show candidate merges without modifying data")
	cmd.Flags().IntVar(&limit, "limit", 0, "max number of mappings to process (0 = all)")
	return cmd
}
