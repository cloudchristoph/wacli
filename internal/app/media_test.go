package app

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/steipete/wacli/internal/store"
)

func TestDownloadMediaJobMarksDownloaded(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	chat := "123@s.whatsapp.net"
	if err := a.db.UpsertChat(chat, "dm", "Alice", time.Now()); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	if err := a.db.UpsertMessage(store.UpsertMessageParams{
		ChatJID:       chat,
		MsgID:         "mid",
		SenderJID:     chat,
		SenderName:    "Alice",
		Timestamp:     time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC),
		FromMe:        false,
		Text:          "",
		MediaType:     "image",
		MediaCaption:  "cap",
		Filename:      "pic.jpg",
		MimeType:      "image/jpeg",
		DirectPath:    "/direct/path",
		MediaKey:      []byte{1, 2, 3},
		FileSHA256:    []byte{4, 5},
		FileEncSHA256: []byte{6, 7},
		FileLength:    123,
	}); err != nil {
		t.Fatalf("UpsertMessage: %v", err)
	}

	if err := a.downloadMediaJob(context.Background(), mediaJob{chatJID: chat, msgID: "mid"}); err != nil {
		t.Fatalf("downloadMediaJob: %v", err)
	}

	info, err := a.db.GetMediaDownloadInfo(chat, "mid")
	if err != nil {
		t.Fatalf("GetMediaDownloadInfo: %v", err)
	}
	if info.LocalPath == "" {
		t.Fatalf("expected LocalPath to be set")
	}
	if _, err := os.Stat(info.LocalPath); err != nil {
		t.Fatalf("expected downloaded file to exist: %v", err)
	}
}

func TestDownloadChatMediaBulk(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	chat := "123@s.whatsapp.net"
	if err := a.db.UpsertChat(chat, "dm", "Alice", time.Now()); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}

	base := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	if err := a.db.UpsertMessage(store.UpsertMessageParams{
		ChatJID:       chat,
		MsgID:         "m1",
		SenderJID:     chat,
		SenderName:    "Alice",
		Timestamp:     base,
		FromMe:        false,
		MediaType:     "image",
		Filename:      "a.jpg",
		MimeType:      "image/jpeg",
		DirectPath:    "/direct/1",
		MediaKey:      []byte{1, 2, 3},
		FileSHA256:    []byte{1},
		FileEncSHA256: []byte{2},
		FileLength:    10,
	}); err != nil {
		t.Fatalf("UpsertMessage m1: %v", err)
	}
	if err := a.db.UpsertMessage(store.UpsertMessageParams{
		ChatJID:       chat,
		MsgID:         "m2",
		SenderJID:     chat,
		SenderName:    "Alice",
		Timestamp:     base.Add(1 * time.Second),
		FromMe:        false,
		MediaType:     "document",
		Filename:      "b.pdf",
		MimeType:      "application/pdf",
		DirectPath:    "/direct/2",
		MediaKey:      []byte{4, 5, 6},
		FileSHA256:    []byte{3},
		FileEncSHA256: []byte{4},
		FileLength:    20,
	}); err != nil {
		t.Fatalf("UpsertMessage m2: %v", err)
	}

	res, err := a.DownloadChatMedia(context.Background(), DownloadChatMediaOptions{ChatJID: chat})
	if err != nil {
		t.Fatalf("DownloadChatMedia: %v", err)
	}
	if res.KnownMedia != 2 {
		t.Fatalf("expected KnownMedia=2, got %d", res.KnownMedia)
	}
	if res.Downloaded != 2 || res.Failed != 0 {
		t.Fatalf("unexpected result after first run: %+v", res)
	}
	if len(res.Attempts) != 2 {
		t.Fatalf("expected 2 attempts, got %d", len(res.Attempts))
	}
	for _, a := range res.Attempts {
		if a.Status != "downloaded" {
			t.Fatalf("expected downloaded attempt, got %+v", a)
		}
		if a.TargetPath == "" {
			t.Fatalf("expected target path in attempt, got %+v", a)
		}
	}

	res2, err := a.DownloadChatMedia(context.Background(), DownloadChatMediaOptions{ChatJID: chat})
	if err != nil {
		t.Fatalf("DownloadChatMedia second run: %v", err)
	}
	if res2.KnownMedia != 0 || res2.Downloaded != 0 {
		t.Fatalf("expected no pending media on second run, got %+v", res2)
	}
	if len(res2.Attempts) != 0 {
		t.Fatalf("expected 0 attempts on second run, got %d", len(res2.Attempts))
	}

	res3, err := a.DownloadChatMedia(context.Background(), DownloadChatMediaOptions{ChatJID: chat, IncludeDownloaded: true})
	if err != nil {
		t.Fatalf("DownloadChatMedia include downloaded: %v", err)
	}
	if res3.KnownMedia != 2 || res3.Downloaded != 2 {
		t.Fatalf("expected redownload of 2 media items, got %+v", res3)
	}
	if len(res3.Attempts) != 2 {
		t.Fatalf("expected 2 attempts on redownload, got %d", len(res3.Attempts))
	}
}
