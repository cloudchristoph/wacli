package main

import (
	"context"
	"fmt"
	"os"

	"github.com/openclaw/wacli/internal/out"
	"github.com/openclaw/wacli/internal/wa"
	"github.com/spf13/cobra"
)

func newDeleteCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete messages",
	}
	cmd.AddCommand(newDeleteMessageCmd(flags))
	return cmd
}

// newDeleteMessageCmd is a lightweight, direct-connection alternative to
// `wacli messages delete` / `wacli messages revoke`. It does not go through
// the upstream send-delegate IPC mechanism (send_ipc.go has no revoke/delete
// request kind), so it always connects directly. Prefer `wacli messages
// delete --for-me` / `wacli messages revoke` for local-media cleanup,
// revoke-eligibility checks, and retry-receipt handling.
func newDeleteMessageCmd(flags *rootFlags) *cobra.Command {
	var chat string
	var msgID string
	var forEveryone bool

	cmd := &cobra.Command{
		Use:   "message",
		Short: "Delete a message (revoke)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if chat == "" || msgID == "" {
				return fmt.Errorf("--chat and --id are required")
			}
			if err := flags.requireWritable(); err != nil {
				return err
			}

			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			a, lk, err := newApp(ctx, flags, true, false)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			if err := a.EnsureAuthed(); err != nil {
				return err
			}
			if err := a.Connect(ctx, false, nil); err != nil {
				return err
			}

			chatJID, err := wa.ParseUserOrJID(chat)
			if err != nil {
				return fmt.Errorf("parse chat: %w", err)
			}

			// Type assert to get the concrete client for RevokeMessageForEveryone.
			waClient, ok := a.WA().(*wa.Client)
			if !ok {
				return fmt.Errorf("unexpected WA client type")
			}

			if err := waClient.RevokeMessageForEveryone(ctx, chatJID, msgID, forEveryone); err != nil {
				return fmt.Errorf("revoke: %w", err)
			}

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{
					"deleted":     true,
					"chat":        chatJID.String(),
					"id":          msgID,
					"forEveryone": forEveryone,
				})
			}
			fmt.Fprintf(os.Stdout, "Deleted message %s from %s\n", msgID, chatJID.String())
			return nil
		},
	}

	cmd.Flags().StringVar(&chat, "chat", "", "chat JID (e.g., 1234567890@s.whatsapp.net)")
	cmd.Flags().StringVar(&msgID, "id", "", "message ID to delete")
	cmd.Flags().BoolVar(&forEveryone, "for-everyone", true, "delete for everyone (not just locally)")
	return cmd
}
