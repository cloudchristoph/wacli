package app

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/steipete/wacli/internal/store"
)

func TestConsolidateLIDChatsReport(t *testing.T) {
	a := newTestApp(t)
	seedLIDMapForConsolidate(t, filepath.Join(a.StoreDir(), "session.db"), "lid-alice", "491234567890", "lid-unused", "499999999999")

	oldTS := time.Date(2024, 3, 1, 8, 0, 0, 0, time.UTC)
	if err := a.DB().UpsertChat("lid-alice@lid", "dm", "Alice via LID", oldTS); err != nil {
		t.Fatalf("UpsertChat lid: %v", err)
	}
	if err := a.DB().UpsertMessage(store.UpsertMessageParams{
		ChatJID:    "lid-alice@lid",
		ChatName:   "Alice via LID",
		MsgID:      "old-lid-msg",
		SenderJID:  "lid-alice@lid",
		SenderName: "Alice via LID",
		Timestamp:  oldTS,
		Text:       "preexisting lid message",
	}); err != nil {
		t.Fatalf("UpsertMessage lid: %v", err)
	}

	dryRun, err := a.ConsolidateLIDChats(context.Background(), ConsolidateLIDOptions{DryRun: true})
	if err != nil {
		t.Fatalf("ConsolidateLIDChats dry-run: %v", err)
	}
	if len(dryRun.Details) != 2 {
		t.Fatalf("expected 2 detail rows in dry-run, got %+v", dryRun)
	}
	if dryRun.Details[0].SkippedReason != "dry-run" {
		t.Fatalf("expected dry-run detail reason, got %+v", dryRun.Details[0])
	}

	res, err := a.ConsolidateLIDChats(context.Background(), ConsolidateLIDOptions{DryRun: false})
	if err != nil {
		t.Fatalf("ConsolidateLIDChats: %v", err)
	}
	if res.ChatsMerged != 1 || res.MessagesMoved != 1 {
		t.Fatalf("expected one merged chat / message moved, got %+v", res)
	}
	if len(res.Details) != 2 {
		t.Fatalf("expected 2 detail rows, got %+v", res)
	}
	if !res.Details[0].Merged || res.Details[0].FromJID != "lid-alice@lid" || res.Details[0].ToJID != "491234567890@s.whatsapp.net" {
		t.Fatalf("unexpected merged detail: %+v", res.Details[0])
	}
	if res.Details[1].SkippedReason != "no-source-chat" {
		t.Fatalf("expected no-source-chat detail, got %+v", res.Details[1])
	}
	if _, err := a.DB().GetChat("lid-alice@lid"); !store.IsNotFound(err) {
		t.Fatalf("expected lid chat to be merged away, err=%v", err)
	}
}

func seedLIDMapForConsolidate(t *testing.T, sessionDBPath string, kv ...string) {
	t.Helper()
	db, err := sql.Open("sqlite3", sessionDBPath)
	if err != nil {
		t.Fatalf("sql.Open session: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE whatsmeow_lid_map (lid TEXT, pn TEXT)`); err != nil {
		t.Fatalf("create whatsmeow_lid_map: %v", err)
	}
	for i := 0; i+1 < len(kv); i += 2 {
		if _, err := db.Exec(`INSERT INTO whatsmeow_lid_map (lid, pn) VALUES (?, ?)`, kv[i], kv[i+1]); err != nil {
			t.Fatalf("insert whatsmeow_lid_map: %v", err)
		}
	}
}
