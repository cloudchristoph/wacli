package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steipete/wacli/internal/app"
	"github.com/steipete/wacli/internal/out"
)

func newMediaCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "media",
		Short: "Media download",
	}
	cmd.AddCommand(newMediaDownloadCmd(flags))
	return cmd
}

func newMediaDownloadCmd(flags *rootFlags) *cobra.Command {
	var chat string
	var id string
	var outputPath string
	var all bool
	var limit int
	var redownload bool

	cmd := &cobra.Command{
		Use:   "download",
		Short: "Download media for a message or in bulk for a chat",
		RunE: func(cmd *cobra.Command, args []string) error {
			if chat == "" {
				return fmt.Errorf("--chat is required")
			}
			if !all && id == "" {
				return fmt.Errorf("--id is required (or use --all)")
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

			resolvedChat := chat
			candidates, _ := a.ResolveChatJIDCandidates(ctx, chat)
			if len(candidates) > 0 {
				resolvedChat = candidates[0]
				if resolvedChat != chat {
					fmt.Fprintf(os.Stderr, "Note: resolved --chat %s -> %s\n", chat, resolvedChat)
				}
			}

			if all {
				if err := a.Connect(ctx, false, nil); err != nil {
					return err
				}

				bulkResult, err := a.DownloadChatMedia(ctx, app.DownloadChatMediaOptions{
					ChatJID:           resolvedChat,
					Limit:             limit,
					OutputDir:         outputPath,
					IncludeDownloaded: redownload,
				})
				if err != nil {
					return err
				}

				if bulkResult.KnownMedia == 0 && len(candidates) > 1 {
					for _, alt := range candidates[1:] {
						altResult, altErr := a.DownloadChatMedia(ctx, app.DownloadChatMediaOptions{
							ChatJID:           alt,
							Limit:             limit,
							OutputDir:         outputPath,
							IncludeDownloaded: redownload,
						})
						if altErr != nil {
							continue
						}
						if altResult.KnownMedia > 0 {
							bulkResult = altResult
							resolvedChat = alt
							fmt.Fprintf(os.Stderr, "Note: resolved --chat %s -> %s\n", chat, alt)
							break
						}
					}
				}

				resp := map[string]any{
					"chat":               resolvedChat,
					"bulk":               true,
					"known_media":        bulkResult.KnownMedia,
					"downloaded":         bulkResult.Downloaded,
					"skipped":            bulkResult.Skipped,
					"failed":             bulkResult.Failed,
					"bytes":              bulkResult.Bytes,
					"include_downloaded": redownload,
					"attempts":           bulkResult.Attempts,
				}
				if bulkResult.ResolvedDir != "" {
					resp["output_dir"] = bulkResult.ResolvedDir
				}
				if flags.asJSON {
					return out.WriteJSON(os.Stdout, resp)
				}

				fmt.Fprintf(os.Stdout, "chat=%s known=%d downloaded=%d skipped=%d failed=%d bytes=%d\n",
					resolvedChat,
					bulkResult.KnownMedia,
					bulkResult.Downloaded,
					bulkResult.Skipped,
					bulkResult.Failed,
					bulkResult.Bytes,
				)
				for _, a := range bulkResult.Attempts {
					switch a.Status {
					case "downloaded":
						fmt.Fprintf(os.Stdout, "OK   %s %s (%d bytes)\n", truncate(a.MsgID, 18), truncateForDisplay(a.TargetPath, 96, flags.fullOutput), a.Bytes)
					case "failed":
						errText := strings.TrimSpace(a.Error)
						if errText == "" {
							errText = "unknown error"
						}
						fmt.Fprintf(os.Stdout, "FAIL %s %s\n", truncate(a.MsgID, 18), errText)
					case "skipped":
						reason := strings.TrimSpace(a.Error)
						if reason == "" {
							reason = "skipped"
						}
						fmt.Fprintf(os.Stdout, "SKIP %s %s\n", truncate(a.MsgID, 18), reason)
					}
				}
				return nil
			}

			info, err := a.DB().GetMediaDownloadInfo(resolvedChat, id)
			if err != nil {
				if len(candidates) > 1 {
					for _, alt := range candidates[1:] {
						altInfo, altErr := a.DB().GetMediaDownloadInfo(alt, id)
						if altErr == nil {
							resolvedChat = alt
							info = altInfo
							fmt.Fprintf(os.Stderr, "Note: resolved --chat %s -> %s\n", chat, alt)
							err = nil
							break
						}
					}
				}
				if err != nil {
					return err
				}
			}
			if info.ChatJID != "" {
				resolvedChat = info.ChatJID
			}
			if info.MediaType == "" || info.DirectPath == "" || len(info.MediaKey) == 0 {
				return fmt.Errorf("message has no downloadable media metadata (run `wacli sync` first)")
			}

			target, err := a.ResolveMediaOutputPath(info, outputPath)
			if err != nil {
				return err
			}

			if err := a.Connect(ctx, false, nil); err != nil {
				return err
			}

			bytes, err := a.WA().DownloadMediaToFile(ctx, info.DirectPath, info.FileEncSHA256, info.FileSHA256, info.MediaKey, info.FileLength, info.MediaType, "", target)
			if err != nil {
				return err
			}
			now := time.Now().UTC()
			_ = a.DB().MarkMediaDownloaded(info.ChatJID, info.MsgID, target, now)

			resp := map[string]any{
				"chat":          resolvedChat,
				"id":            info.MsgID,
				"path":          target,
				"bytes":         bytes,
				"media_type":    info.MediaType,
				"mime_type":     info.MimeType,
				"downloaded":    true,
				"downloaded_at": now.Format(time.RFC3339Nano),
			}
			if flags.asJSON {
				return out.WriteJSON(os.Stdout, resp)
			}
			fmt.Fprintf(os.Stdout, "%s (%d bytes)\n", target, bytes)
			return nil
		},
	}

	cmd.Flags().StringVar(&chat, "chat", "", "chat JID")
	cmd.Flags().StringVar(&id, "id", "", "message ID")
	cmd.Flags().StringVar(&outputPath, "output", "", "output file or directory (default: store media dir)")
	cmd.Flags().BoolVar(&all, "all", false, "download all known media from the chat")
	cmd.Flags().IntVar(&limit, "limit", 0, "limit number of media files to download in bulk mode (0 = all)")
	cmd.Flags().BoolVar(&redownload, "redownload", false, "include already downloaded media in bulk mode")
	_ = cmd.MarkFlagRequired("chat")
	return cmd
}
