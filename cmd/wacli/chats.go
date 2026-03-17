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
	cmd.AddCommand(newChatsConsolidateIdentitiesCmd(flags))
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

func newChatsConsolidateIdentitiesCmd(flags *rootFlags) *cobra.Command {
	var dryRun bool
	var apply bool
	var limit int

	cmd := &cobra.Command{
		Use:     "consolidate-identities",
		Aliases: []string{"consolidate-lid"},
		Short:   "Consolidate alternate chat identities into canonical phone JIDs and print a merge report",
		RunE: func(cmd *cobra.Command, args []string) error {
			if apply {
				dryRun = false
			}
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

			mode := "dry-run"
			if !dryRun {
				mode = "apply"
			}
			fmt.Fprintf(os.Stdout, "Identity consolidation (%s)\n", mode)
			fmt.Fprintf(os.Stdout, "  Mappings found: %d\n", res.MappingsFound)
			fmt.Fprintf(os.Stdout, "  Mappings tried: %d\n", res.MappingsTried)
			fmt.Fprintf(os.Stdout, "  Chats merged: %d\n", res.ChatsMerged)
			fmt.Fprintf(os.Stdout, "  Messages moved: %d\n", res.MessagesMoved)
			fmt.Fprintf(os.Stdout, "  Skipped invalid: %d\n", res.SkippedInvalid)
			fmt.Fprintf(os.Stdout, "  Skipped unmapped: %d\n", res.SkippedUnmapped)

			if len(res.Details) > 0 {
				w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
				fmt.Fprintln(w, "ACTION\tFROM\tTO\tMOVED\tNOTE")
				for _, d := range res.Details {
					action := "skip"
					if d.Merged {
						action = "merge"
					}
					note := d.SkippedReason
					if note == "" && dryRun {
						note = "candidate"
					}
					fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\n", action, truncate(d.FromJID, 30), truncate(d.ToJID, 30), d.MessagesMoved, note)
				}
				_ = w.Flush()
			}

			if dryRun {
				fmt.Fprintln(os.Stdout, "No data changed. Re-run with --apply to perform the merges.")
				return nil
			}

			fmt.Fprintln(os.Stdout, "Consolidation complete.")
			return nil
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", true, "show candidate merges without modifying data")
	cmd.Flags().BoolVar(&apply, "apply", false, "perform the merges (equivalent to --dry-run=false)")
	cmd.Flags().IntVar(&limit, "limit", 0, "max number of mappings to process (0 = all)")
	return cmd
}
