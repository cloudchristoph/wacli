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
	"time"

	"github.com/steipete/wacli/internal/pathutil"
	"github.com/steipete/wacli/internal/store"
)

type mediaJob struct {
	chatJID string
	msgID   string
}

type DownloadChatMediaOptions struct {
	ChatJID           string
	Limit             int
	OutputDir         string
	IncludeDownloaded bool
}

type DownloadChatMediaResult struct {
	ChatJID     string
	KnownMedia  int
	Downloaded  int
	Skipped     int
	Failed      int
	Bytes       int64
	ResolvedDir string
	Attempts    []ChatMediaDownloadAttempt
}

type ChatMediaDownloadAttempt struct {
	MsgID      string
	MediaType  string
	Filename   string
	TargetPath string
	Status     string // downloaded|failed|skipped
	Bytes      int64
	Error      string
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

func (a *App) DownloadChatMedia(ctx context.Context, opts DownloadChatMediaOptions) (DownloadChatMediaResult, error) {
	chatJID := strings.TrimSpace(opts.ChatJID)
	if chatJID == "" {
		return DownloadChatMediaResult{}, fmt.Errorf("chat JID is required")
	}

	infos, err := a.db.ListMediaDownloadInfos(chatJID, opts.Limit, opts.IncludeDownloaded)
	if err != nil {
		return DownloadChatMediaResult{}, err
	}

	res := DownloadChatMediaResult{
		ChatJID:    chatJID,
		KnownMedia: len(infos),
	}

	outDir := strings.TrimSpace(opts.OutputDir)
	requested := ""
	if outDir != "" {
		if !filepath.IsAbs(outDir) {
			if abs, err := filepath.Abs(outDir); err == nil {
				outDir = abs
			}
		}
		if st, err := os.Stat(outDir); err == nil {
			if !st.IsDir() {
				return DownloadChatMediaResult{}, fmt.Errorf("--output must be a directory when downloading chat media in bulk")
			}
		} else if !os.IsNotExist(err) {
			return DownloadChatMediaResult{}, err
		}
		if err := os.MkdirAll(outDir, 0700); err != nil {
			return DownloadChatMediaResult{}, err
		}
		requested = outDir + string(os.PathSeparator)
		res.ResolvedDir = outDir
	}

	for _, info := range infos {
		attempt := ChatMediaDownloadAttempt{
			MsgID:     info.MsgID,
			MediaType: info.MediaType,
			Filename:  info.Filename,
		}
		if strings.TrimSpace(info.MediaType) == "" || strings.TrimSpace(info.DirectPath) == "" || len(info.MediaKey) == 0 {
			attempt.Status = "skipped"
			attempt.Error = "missing media metadata"
			res.Skipped++
			res.Attempts = append(res.Attempts, attempt)
			continue
		}

		targetPath, err := a.ResolveMediaOutputPath(info, requested)
		if err != nil {
			attempt.Status = "failed"
			attempt.Error = err.Error()
			res.Failed++
			res.Attempts = append(res.Attempts, attempt)
			continue
		}
		attempt.TargetPath = targetPath
		if err := os.MkdirAll(filepath.Dir(targetPath), 0700); err != nil {
			attempt.Status = "failed"
			attempt.Error = err.Error()
			res.Failed++
			res.Attempts = append(res.Attempts, attempt)
			continue
		}

		bytes, err := a.wa.DownloadMediaToFile(ctx, info.DirectPath, info.FileEncSHA256, info.FileSHA256, info.MediaKey, info.FileLength, info.MediaType, "", targetPath)
		if err != nil {
			attempt.Status = "failed"
			attempt.Error = err.Error()
			res.Failed++
			res.Attempts = append(res.Attempts, attempt)
			continue
		}
		now := time.Now().UTC()
		if err := a.db.MarkMediaDownloaded(info.ChatJID, info.MsgID, targetPath, now); err != nil {
			attempt.Status = "failed"
			attempt.Error = err.Error()
			res.Failed++
			res.Attempts = append(res.Attempts, attempt)
			continue
		}
		attempt.Status = "downloaded"
		attempt.Bytes = bytes
		res.Downloaded++
		res.Bytes += bytes
		res.Attempts = append(res.Attempts, attempt)
	}

	return res, nil
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
