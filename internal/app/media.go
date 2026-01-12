package app

import (
	"context"
	"database/sql"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/steipete/wacli/internal/pathutil"
	"github.com/steipete/wacli/internal/store"
)

type mediaJob struct {
	chatJID string
	msgID   string
}

func (a *App) ResolveMediaOutputPath(info store.MediaDownloadInfo, requested string) (string, error) {
	filename := mediaFilename(info)

	if strings.TrimSpace(requested) != "" {
		out := requested
		if !filepath.IsAbs(out) {
			if abs, err := filepath.Abs(out); err == nil {
				out = abs
			}
		}
		if st, err := os.Stat(out); err == nil && st.IsDir() {
			return filepath.Join(out, filename), nil
		}
		if strings.HasSuffix(out, string(os.PathSeparator)) {
			return filepath.Join(out, filename), nil
		}
		return out, nil
	}

	baseDir := filepath.Join(a.opts.StoreDir, "media", pathutil.SanitizeSegment(info.ChatJID), pathutil.SanitizeSegment(info.MsgID))
	if info.MediaType != "" {
		baseDir = filepath.Join(baseDir, pathutil.SanitizeSegment(info.MediaType))
	}
	if abs, err := filepath.Abs(baseDir); err == nil {
		baseDir = abs
	}
	return filepath.Join(baseDir, filename), nil
}

func mediaFilename(info store.MediaDownloadInfo) string {
	name := strings.TrimSpace(info.Filename)
	ext := ""
	if strings.TrimSpace(info.MimeType) != "" {
		if exts, err := mime.ExtensionsByType(info.MimeType); err == nil && len(exts) > 0 {
			ext = exts[0]
		}
	}

	if name == "" {
		base := "message-" + pathutil.SanitizeSegment(info.MsgID)
		if ext == "" {
			ext = ".bin"
		}
		return pathutil.SanitizeFilename(base + ext)
	}

	name = pathutil.SanitizeFilename(name)
	if ext != "" && filepath.Ext(name) == "" {
		name += ext
	}
	return name
}

func (a *App) runMediaWorkers(ctx context.Context, jobs <-chan mediaJob, workers int) (func(), error) {
	if workers <= 0 {
		workers = 2
	}
	if jobs == nil {
		return func() {}, nil
	}

	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case job := <-jobs:
					if strings.TrimSpace(job.chatJID) == "" || strings.TrimSpace(job.msgID) == "" {
						continue
					}
					if err := a.downloadMediaJob(ctx, job); err != nil {
						fmt.Fprintf(os.Stderr, "media download failed for %s/%s: %v\n", job.chatJID, job.msgID, err)
					}
				}
			}
		}()
	}

	stop := func() {
		cancel()
		wg.Wait()
	}
	return stop, nil
}

func (a *App) downloadMediaJob(ctx context.Context, job mediaJob) error {
	info, err := a.db.GetMediaDownloadInfo(job.chatJID, job.msgID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		return err
	}
	if strings.TrimSpace(info.LocalPath) != "" {
		return nil
	}
	if strings.TrimSpace(info.MediaType) == "" || strings.TrimSpace(info.DirectPath) == "" || len(info.MediaKey) == 0 {
		return nil
	}

	targetPath, err := a.ResolveMediaOutputPath(info, "")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), 0700); err != nil {
		return err
	}

	if _, err := a.wa.DownloadMediaToFile(ctx, info.DirectPath, info.FileEncSHA256, info.FileSHA256, info.MediaKey, info.FileLength, info.MediaType, "", targetPath); err != nil {
		return err
	}

	now := time.Now().UTC()
	return a.db.MarkMediaDownloaded(info.ChatJID, info.MsgID, targetPath, now)
}

type DownloadMediaBatchOptions struct {
	BasePath       string
	ChatJID        string
	MediaType      string
	Workers        int
	MaxErrors      int
}

type DownloadMediaBatchResult struct {
	TotalFound     int
	Downloaded     int
	Skipped        int
	Failed         int
}

func (a *App) DownloadMediaBatch(ctx context.Context, opts DownloadMediaBatchOptions) (DownloadMediaBatchResult, error) {
	if opts.Workers <= 0 {
		opts.Workers = 4
	}
	if opts.MaxErrors <= 0 {
		opts.MaxErrors = 5
	}

	// Ensure authenticated
	if err := a.EnsureAuthed(); err != nil {
		return DownloadMediaBatchResult{}, err
	}
	if err := a.OpenWA(); err != nil {
		return DownloadMediaBatchResult{}, err
	}

	// Get all messages with media
	messages, err := a.db.ListMessagesWithMedia(opts.ChatJID, opts.MediaType)
	if err != nil {
		return DownloadMediaBatchResult{}, fmt.Errorf("list messages with media: %w", err)
	}

	if len(messages) == 0 {
		return DownloadMediaBatchResult{}, fmt.Errorf("no undownloaded media found")
	}

	chatDesc := "all chats"
	if opts.ChatJID != "" {
		chatDesc = opts.ChatJID
	}
	mediaDesc := "all media types"
	if opts.MediaType != "" {
		mediaDesc = opts.MediaType
	}
	fmt.Fprintf(os.Stderr, "Starting download of %d media files from %s (%s) with %d worker(s)...\n",
		len(messages), chatDesc, mediaDesc, opts.Workers)

	// Atomic counters
	var processed, downloaded, skipped, failed atomic.Int64
	var mu sync.Mutex

	// Create job channel
	jobs := make(chan store.MediaDownloadInfo, len(messages))
	for _, msg := range messages {
		jobs <- msg
	}
	close(jobs)

	// Worker function
	worker := func() error {
		for info := range jobs {
			// Check if we've exceeded max errors
			if int(failed.Load()) >= opts.MaxErrors {
				return fmt.Errorf("maximum error count (%d) reached", opts.MaxErrors)
			}

			// Skip if already downloaded
			if strings.TrimSpace(info.LocalPath) != "" {
				skipped.Add(1)
				processed.Add(1)
				continue
			}

			// Skip if no media metadata
			if strings.TrimSpace(info.MediaType) == "" || strings.TrimSpace(info.DirectPath) == "" || len(info.MediaKey) == 0 {
				skipped.Add(1)
				processed.Add(1)
				continue
			}

			// Resolve output path with chat-based structure
			basePath := opts.BasePath
			if basePath == "" {
				basePath = filepath.Join(a.opts.StoreDir, "media")
			}
			
			// Create chat-based subdirectory
			chatDir := filepath.Join(basePath, pathutil.SanitizeSegment(info.ChatJID))
			filename := mediaFilename(info)
			targetPath := filepath.Join(chatDir, filename)

			if err := os.MkdirAll(chatDir, 0700); err != nil {
				mu.Lock()
				failed.Add(1)
				fmt.Fprintf(os.Stderr, "\rError creating directory for %s/%s: %v\n", info.ChatJID, info.MsgID, err)
				mu.Unlock()
				processed.Add(1)
				continue
			}

			// Download media
			if _, err := a.wa.DownloadMediaToFile(ctx, info.DirectPath, info.FileEncSHA256, info.FileSHA256, info.MediaKey, info.FileLength, info.MediaType, "", targetPath); err != nil {
				mu.Lock()
				failed.Add(1)
				fmt.Fprintf(os.Stderr, "\rError downloading %s/%s: %v\n", info.ChatJID, info.MsgID, err)
				mu.Unlock()
				processed.Add(1)
				continue
			}

			// Mark as downloaded
			now := time.Now().UTC()
			if err := a.db.MarkMediaDownloaded(info.ChatJID, info.MsgID, targetPath, now); err != nil {
				mu.Lock()
				failed.Add(1)
				fmt.Fprintf(os.Stderr, "\rError marking downloaded %s/%s: %v\n", info.ChatJID, info.MsgID, err)
				mu.Unlock()
				processed.Add(1)
				continue
			}

			mu.Lock()
			dl := downloaded.Add(1)
			proc := processed.Add(1)
			if proc%10 == 0 {
				fmt.Fprintf(os.Stderr, "\rProgress: %d/%d processed, %d downloaded, %d skipped, %d failed...",
					proc, len(messages), dl, skipped.Load(), failed.Load())
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

	fmt.Fprintf(os.Stderr, "\nMedia download complete: %d processed, %d downloaded, %d skipped, %d failed\n",
		processed.Load(), downloaded.Load(), skipped.Load(), failed.Load())

	result := DownloadMediaBatchResult{
		TotalFound: len(messages),
		Downloaded: int(downloaded.Load()),
		Skipped:    int(skipped.Load()),
		Failed:     int(failed.Load()),
	}

	if workerErr != nil {
		return result, workerErr
	}

	return result, nil
}
