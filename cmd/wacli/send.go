package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steipete/wacli/internal/config"
	"github.com/steipete/wacli/internal/ipc"
	"github.com/steipete/wacli/internal/out"
	"github.com/steipete/wacli/internal/store"
	"github.com/steipete/wacli/internal/wa"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
)

func newSendCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "send",
		Short: "Send messages",
	}
	cmd.AddCommand(newSendTextCmd(flags))
	cmd.AddCommand(newSendFileCmd(flags))
	return cmd
}

func newSendTextCmd(flags *rootFlags) *cobra.Command {
	var to string
	var message string
	var replyTo string
	var replyChat string
	var noIPC bool

	cmd := &cobra.Command{
		Use:   "text",
		Short: "Send a text message",
		RunE: func(cmd *cobra.Command, args []string) error {
			if to == "" || message == "" {
				return fmt.Errorf("--to and --message are required")
			}

			// Resolve store directory
			storeDir := flags.storeDir
			if storeDir == "" {
				storeDir = config.DefaultStoreDir()
			}
			storeDir, _ = filepath.Abs(storeDir)

			// Try IPC first if not disabled
			if !noIPC {
				client := ipc.NewClient(storeDir)
				if client.IsAvailable() {
					result, err := client.SendText(to, message)
					if err != nil {
						// IPC failed, but socket exists - maybe sync is starting up
						// Fall through to direct mode
						fmt.Fprintf(os.Stderr, "IPC send failed (%v), trying direct mode...\n", err)
					} else {
						// Success via IPC
						if flags.asJSON {
							return out.WriteJSON(os.Stdout, map[string]any{
								"sent": true,
								"to":   result.To,
								"id":   result.MsgID,
								"via":  "ipc",
							})
						}
						fmt.Fprintf(os.Stdout, "Sent to %s (id %s) via daemon\n", result.To, result.MsgID)
						return nil
					}
				}
			}

			// Direct mode - acquire lock and connect
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

			toJID, err := wa.ParseUserOrJID(to)
			if err != nil {
				return err
			}

			var msgID types.MessageID
			if strings.TrimSpace(replyTo) != "" {
				// Build a quoted reply (reply-to). We fetch the quoted message from the local DB.
				chatJID := toJID.String()
				if strings.TrimSpace(replyChat) != "" {
					cj, err := wa.ParseUserOrJID(replyChat)
					if err != nil {
						return fmt.Errorf("invalid --reply-chat: %w", err)
					}
					chatJID = cj.String()
				}

				qm, err := a.DB().GetMessage(chatJID, replyTo)
				if err != nil {
					if err == sql.ErrNoRows {
						return fmt.Errorf("reply-to message not found in local DB (chat=%s id=%s). Run `wacli sync --follow` or `wacli history backfill --chat %s` and try again", chatJID, replyTo, chatJID)
					}
					return err
				}

				// Participant is required for group replies (sender of quoted message).
				var participant *types.JID
				if wa.IsGroupJID(toJID) {
					if strings.TrimSpace(qm.SenderJID) == "" {
						return fmt.Errorf("reply-to in groups requires sender JID in local DB (missing SenderJID). Run `wacli sync --follow` or history backfill for %s", chatJID)
					}
					pj, err := types.ParseJID(qm.SenderJID)
					if err != nil {
						return fmt.Errorf("reply-to in groups requires a valid sender JID; got %q (parse error: %v)", qm.SenderJID, err)
					}
					participant = &pj
				}

				quotedText := strings.TrimSpace(qm.Text)
				if quotedText == "" {
					quotedText = strings.TrimSpace(qm.DisplayText)
				}
				quotedMsg := &waProto.Message{Conversation: &quotedText}

				id, err := a.WA().SendTextReply(ctx, toJID, message, replyTo, participant, quotedMsg)
				if err != nil {
					return err
				}
				msgID = id
			} else {
				id, err := a.WA().SendText(ctx, toJID, message)
				if err != nil {
					return err
				}
				msgID = id
			}

			now := time.Now().UTC()
			chat := toJID
			chatName := a.WA().ResolveChatName(ctx, chat, "")
			kind := chatKindFromJID(chat)
			_ = a.DB().UpsertChat(chat.String(), kind, chatName, now)
			_ = a.DB().UpsertMessage(store.UpsertMessageParams{
				ChatJID:    chat.String(),
				ChatName:   chatName,
				MsgID:      string(msgID),
				SenderJID:  "",
				SenderName: "me",
				Timestamp:  now,
				FromMe:     true,
				Text:       message,
			})

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{
					"sent": true,
					"to":   chat.String(),
					"id":   msgID,
					"via":  "direct",
				})
			}
			fmt.Fprintf(os.Stdout, "Sent to %s (id %s)\n", chat.String(), msgID)
			return nil
		},
	}

	cmd.Flags().StringVar(&to, "to", "", "recipient phone number or JID")
	cmd.Flags().StringVar(&message, "message", "", "message text")
	cmd.Flags().StringVar(&replyTo, "reply-to", "", "message id to reply to (stanza id)")
	cmd.Flags().StringVar(&replyChat, "reply-chat", "", "chat JID/number where the reply-to message lives (defaults to --to)")
	cmd.Flags().BoolVar(&noIPC, "no-ipc", false, "skip IPC and use direct connection")
	return cmd
}
