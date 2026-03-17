package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steipete/wacli/internal/app"
	"github.com/steipete/wacli/internal/out"
)

func newImportCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "import",
		Short: "Import data into the local wacli store",
	}
	cmd.AddCommand(newImportIPhoneBackupCmd(flags))
	return cmd
}

func newImportIPhoneBackupCmd(flags *rootFlags) *cobra.Command {
	var path string
	var includeStatus bool

	cmd := &cobra.Command{
		Use:     "iphone-backup",
		Aliases: []string{"ios-backup"},
		Short:   "Import chats, contacts, groups, and messages from an extracted iPhone WhatsApp backup",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(path) == "" {
				return fmt.Errorf("--path is required")
			}

			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			a, lk, err := newApp(ctx, flags, true, true)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			res, err := a.ImportIPhoneBackup(ctx, path, app.IPhoneBackupImportOptions{IncludeStatus: includeStatus})
			if err != nil {
				return err
			}

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, res)
			}

			fmt.Fprintf(os.Stdout, "Imported iPhone backup from %s\n", res.BackupPath)
			fmt.Fprintf(os.Stdout, "  Contacts: %d\n", res.ContactsImported)
			fmt.Fprintf(os.Stdout, "  Chats: %d\n", res.ChatsImported)
			fmt.Fprintf(os.Stdout, "  Groups: %d\n", res.GroupsImported)
			fmt.Fprintf(os.Stdout, "  Participants: %d\n", res.GroupParticipantsImported)
			fmt.Fprintf(os.Stdout, "  Messages: %d\n", res.MessagesImported)
			fmt.Fprintf(os.Stdout, "  Starred: %d\n", res.StarredImported)
			fmt.Fprintf(os.Stdout, "  Media messages: %d\n", res.MediaMessagesImported)
			if res.SkippedStatusChats > 0 || res.SkippedStatusMessages > 0 {
				fmt.Fprintf(os.Stdout, "  Skipped status chats/messages: %d/%d\n", res.SkippedStatusChats, res.SkippedStatusMessages)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&path, "path", "", "path to extracted WhatsApp iPhone backup folder")
	cmd.Flags().BoolVar(&includeStatus, "include-status", false, "include WhatsApp status/broadcast threads")
	return cmd
}
