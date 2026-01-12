package main

import (
	"context"
	"fmt"
	"os"
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
	var mediaType string
	var workers int
	var maxErrors int

	cmd := &cobra.Command{
		Use:   "download",
		Short: "Download media for a message, chat, or all chats",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Validate flag combinations
			singleMode := chat != "" && id != ""
			batchMode := all || (chat != "" && id == "")
			
			if singleMode && batchMode {
				return fmt.Errorf("cannot combine single message download (--chat + --id) with batch download (--all or --chat without --id)")
			}
			if !singleMode && !batchMode {
				return fmt.Errorf("must specify either --chat and --id for single download, --chat for chat batch, or --all for all chats")
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

			// Single message download
			if singleMode {
				info, err := a.DB().GetMediaDownloadInfo(chat, id)
				if err != nil {
					return err
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
					"chat":          info.ChatJID,
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
			}

			// Batch download
			chatJID := ""
			if !all {
				chatJID = chat
			}

			// Validate media type if specified
			if mediaType != "" {
				validTypes := map[string]bool{"image": true, "video": true, "audio": true, "document": true}
				if !validTypes[mediaType] {
					return fmt.Errorf("invalid media type: %s (valid: image, video, audio, document)", mediaType)
				}
			}

			res, err := a.DownloadMediaBatch(ctx, app.DownloadMediaBatchOptions{
				BasePath:  outputPath,
				ChatJID:   chatJID,
				MediaType: mediaType,
				Workers:   workers,
				MaxErrors: maxErrors,
			})
			if err != nil {
				return err
			}

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{
					"total_found": res.TotalFound,
					"downloaded":  res.Downloaded,
					"skipped":     res.Skipped,
					"failed":      res.Failed,
				})
			}

			fmt.Fprintf(os.Stdout, "Media download complete: %d downloaded, %d skipped, %d failed (total: %d)\n",
				res.Downloaded, res.Skipped, res.Failed, res.TotalFound)
			return nil
		},
	}

	cmd.Flags().StringVar(&chat, "chat", "", "chat JID (for single message or chat batch)")
	cmd.Flags().StringVar(&id, "id", "", "message ID (for single message download)")
	cmd.Flags().StringVar(&outputPath, "output", "", "output directory (default: store media dir with chat subdirectories)")
	cmd.Flags().BoolVar(&all, "all", false, "download media from all chats")
	cmd.Flags().StringVar(&mediaType, "media-type", "", "filter by media type: image, video, audio, document (batch mode only)")
	cmd.Flags().IntVar(&workers, "workers", 4, "number of concurrent download workers (batch mode only)")
	cmd.Flags().IntVar(&maxErrors, "max-errors", 5, "maximum number of errors before aborting (batch mode only)")
	return cmd
}
