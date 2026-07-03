package store

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "wacli.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func countRows(t *testing.T, db *sql.DB, q string, args ...any) int {
	t.Helper()
	row := db.QueryRow(q, args...)
	var n int
	if err := row.Scan(&n); err != nil {
		t.Fatalf("countRows scan: %v", err)
	}
	return n
}

func TestListMediaDownloadInfos(t *testing.T) {
	db := openTestDB(t)

	chat := "123@s.whatsapp.net"
	if err := db.UpsertChat(chat, "dm", "Alice", time.Now()); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}

	ts := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	if err := db.UpsertMessage(UpsertMessageParams{
		ChatJID:       chat,
		MsgID:         "m1",
		SenderJID:     chat,
		SenderName:    "Alice",
		Timestamp:     ts,
		FromMe:        false,
		MediaType:     "image",
		Filename:      "a.jpg",
		MimeType:      "image/jpeg",
		DirectPath:    "/direct/1",
		MediaKey:      []byte{1, 2, 3},
		FileSHA256:    []byte{1},
		FileEncSHA256: []byte{2},
		FileLength:    100,
	}); err != nil {
		t.Fatalf("UpsertMessage m1: %v", err)
	}
	if err := db.UpsertMessage(UpsertMessageParams{
		ChatJID:       chat,
		MsgID:         "m2",
		SenderJID:     chat,
		SenderName:    "Alice",
		Timestamp:     ts.Add(1 * time.Second),
		FromMe:        false,
		MediaType:     "image",
		Filename:      "b.jpg",
		MimeType:      "image/jpeg",
		DirectPath:    "/direct/2",
		MediaKey:      []byte{4, 5, 6},
		FileSHA256:    []byte{3},
		FileEncSHA256: []byte{4},
		FileLength:    100,
	}); err != nil {
		t.Fatalf("UpsertMessage m2: %v", err)
	}
	if err := db.MarkMediaDownloaded(chat, "m2", "/tmp/m2.jpg", ts.Add(2*time.Second)); err != nil {
		t.Fatalf("MarkMediaDownloaded: %v", err)
	}

	pending, err := db.ListMediaDownloadInfos(chat, 0, false)
	if err != nil {
		t.Fatalf("ListMediaDownloadInfos pending: %v", err)
	}
	if len(pending) != 1 || pending[0].MsgID != "m1" {
		t.Fatalf("expected only pending m1, got %+v", pending)
	}

	all, err := db.ListMediaDownloadInfos(chat, 0, true)
	if err != nil {
		t.Fatalf("ListMediaDownloadInfos all: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 media entries, got %d", len(all))
	}

	one, err := db.ListMediaDownloadInfos(chat, 1, true)
	if err != nil {
		t.Fatalf("ListMediaDownloadInfos limit: %v", err)
	}
	if len(one) != 1 || one[0].MsgID != "m1" {
		t.Fatalf("expected first media item m1, got %+v", one)
	}
}
