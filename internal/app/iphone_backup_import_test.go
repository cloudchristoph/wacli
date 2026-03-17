package app

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/steipete/wacli/internal/store"
)

func TestImportIPhoneBackup(t *testing.T) {
	ctx := context.Background()
	backupDir := t.TempDir()
	chatDBPath := filepath.Join(backupDir, "ChatStorage.sqlite")
	contactsDBPath := filepath.Join(backupDir, "ContactsV2.sqlite")
	mediaDir := filepath.Join(backupDir, "Message", "Media")
	if err := os.MkdirAll(mediaDir, 0o755); err != nil {
		t.Fatalf("MkdirAll mediaDir: %v", err)
	}
	mediaPath := filepath.Join(mediaDir, "photo.jpg")
	if err := os.WriteFile(mediaPath, []byte("jpg"), 0o644); err != nil {
		t.Fatalf("WriteFile mediaPath: %v", err)
	}

	seedBackupDatabases(t, chatDBPath, contactsDBPath)

	appDir := t.TempDir()
	a, err := New(Options{StoreDir: appDir, AllowUnauthed: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer a.Close()

	res, err := a.ImportIPhoneBackup(ctx, backupDir, IPhoneBackupImportOptions{})
	if err != nil {
		t.Fatalf("ImportIPhoneBackup: %v", err)
	}
	if res.ChatsImported != 2 {
		t.Fatalf("expected 2 imported chats, got %+v", res)
	}
	if res.MessagesImported != 2 {
		t.Fatalf("expected 2 imported messages, got %+v", res)
	}
	if res.StarredImported != 1 {
		t.Fatalf("expected 1 starred message, got %+v", res)
	}
	if res.SkippedStatusChats != 1 || res.SkippedStatusMessages != 0 {
		t.Fatalf("expected 1 skipped status chat/message, got %+v", res)
	}

	chats, err := a.DB().ListChats("", 10)
	if err != nil {
		t.Fatalf("ListChats: %v", err)
	}
	if len(chats) != 2 {
		t.Fatalf("expected 2 chats in store, got %d", len(chats))
	}

	dm, err := a.DB().GetMessage("491234567890@s.whatsapp.net", "m1")
	if err != nil {
		t.Fatalf("GetMessage dm: %v", err)
	}
	if dm.Text != "Hello from backup" || dm.SenderJID != "491234567890@s.whatsapp.net" {
		t.Fatalf("unexpected DM message: %+v", dm)
	}

	groupMsg, err := a.DB().GetMessage("120363000000000000@g.us", "ios-backup-101")
	if err != nil {
		t.Fatalf("GetMessage group: %v", err)
	}
	if groupMsg.MediaType != "image" || groupMsg.SenderJID != "491111111111@s.whatsapp.net" {
		t.Fatalf("unexpected group message: %+v", groupMsg)
	}

	info, err := a.DB().GetMediaDownloadInfo("120363000000000000@g.us", "ios-backup-101")
	if err != nil {
		t.Fatalf("GetMediaDownloadInfo: %v", err)
	}
	if info.LocalPath != mediaPath {
		t.Fatalf("expected resolved media path %q, got %q", mediaPath, info.LocalPath)
	}

	starred, err := a.DB().ListStarred("", time.Time{})
	if err != nil {
		t.Fatalf("ListStarred: %v", err)
	}
	if len(starred) != 1 || starred[0].MsgID != "m1" {
		t.Fatalf("unexpected starred messages: %+v", starred)
	}

	contact, err := a.DB().GetContact("491111111111@s.whatsapp.net")
	if err != nil {
		t.Fatalf("GetContact group member: %v", err)
	}
	if contact.Name == "" {
		t.Fatalf("expected imported group member contact, got %+v", contact)
	}

	countBefore, err := a.DB().CountMessages()
	if err != nil {
		t.Fatalf("CountMessages before second import: %v", err)
	}
	if _, err := a.ImportIPhoneBackup(ctx, backupDir, IPhoneBackupImportOptions{}); err != nil {
		t.Fatalf("ImportIPhoneBackup second run: %v", err)
	}
	countAfter, err := a.DB().CountMessages()
	if err != nil {
		t.Fatalf("CountMessages after second import: %v", err)
	}
	if countBefore != countAfter {
		t.Fatalf("expected idempotent import, before=%d after=%d", countBefore, countAfter)
	}
}

func TestImportIPhoneBackupMergesExistingAlternateChatIdentity(t *testing.T) {
	ctx := context.Background()
	backupDir := t.TempDir()
	chatDBPath := filepath.Join(backupDir, "ChatStorage.sqlite")
	contactsDBPath := filepath.Join(backupDir, "ContactsV2.sqlite")
	seedBackupDatabases(t, chatDBPath, contactsDBPath)

	appDir := t.TempDir()
	a, err := New(Options{StoreDir: appDir, AllowUnauthed: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer a.Close()

	seedLIDMap(t, filepath.Join(appDir, "session.db"), "lid-alice", "491234567890")

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

	if _, err := a.ImportIPhoneBackup(ctx, backupDir, IPhoneBackupImportOptions{}); err != nil {
		t.Fatalf("ImportIPhoneBackup: %v", err)
	}

	if _, err := a.DB().GetChat("lid-alice@lid"); !store.IsNotFound(err) {
		t.Fatalf("expected lid chat to be merged away, err=%v", err)
	}

	chat, err := a.DB().GetChat("491234567890@s.whatsapp.net")
	if err != nil {
		t.Fatalf("GetChat canonical: %v", err)
	}
	if chat.Name == "" {
		t.Fatalf("expected canonical chat to retain a name, got %+v", chat)
	}

	oldMsg, err := a.DB().GetMessage("491234567890@s.whatsapp.net", "old-lid-msg")
	if err != nil {
		t.Fatalf("GetMessage merged old lid msg: %v", err)
	}
	if oldMsg.ChatJID != "491234567890@s.whatsapp.net" {
		t.Fatalf("expected old message to move to canonical chat, got %+v", oldMsg)
	}

	if _, err := a.DB().GetMessage("491234567890@s.whatsapp.net", "m1"); err != nil {
		t.Fatalf("GetMessage imported canonical msg: %v", err)
	}

	count, err := a.DB().CountMessages()
	if err != nil {
		t.Fatalf("CountMessages: %v", err)
	}
	if count != 3 {
		t.Fatalf("expected 3 total messages after merge+import, got %d", count)
	}
}

func seedBackupDatabases(t *testing.T, chatDBPath, contactsDBPath string) {
	t.Helper()
	chatDB := openSQLite(t, chatDBPath)
	defer chatDB.Close()
	contactsDB := openSQLite(t, contactsDBPath)
	defer contactsDB.Close()

	mustExec(t, chatDB, `
		CREATE TABLE ZWACHATSESSION (
			Z_PK INTEGER PRIMARY KEY,
			ZGROUPINFO INTEGER,
			ZLASTMESSAGEDATE REAL,
			ZCONTACTJID TEXT,
			ZPARTNERNAME TEXT
		);
		CREATE TABLE ZWAGROUPINFO (
			Z_PK INTEGER PRIMARY KEY,
			ZCHATSESSION INTEGER,
			ZCREATIONDATE REAL,
			ZCREATORJID TEXT,
			ZOWNERJID TEXT
		);
		CREATE TABLE ZWAGROUPMEMBER (
			Z_PK INTEGER PRIMARY KEY,
			ZCHATSESSION INTEGER,
			ZMEMBERJID TEXT,
			ZCONTACTNAME TEXT,
			ZFIRSTNAME TEXT,
			ZISADMIN INTEGER
		);
		CREATE TABLE ZWAMEDIAITEM (
			Z_PK INTEGER PRIMARY KEY,
			ZMEDIALOCALPATH TEXT,
			ZMEDIAURL TEXT,
			ZTITLE TEXT,
			ZAUTHORNAME TEXT,
			ZVCARDNAME TEXT,
			ZVCARDSTRING TEXT,
			ZFILESIZE INTEGER,
			ZMOVIEDURATION INTEGER,
			ZLATITUDE REAL,
			ZLONGITUDE REAL,
			ZMEDIAKEY BLOB
		);
		CREATE TABLE ZWAMESSAGE (
			Z_PK INTEGER PRIMARY KEY,
			ZCHATSESSION INTEGER,
			ZGROUPMEMBER INTEGER,
			ZSTANZAID TEXT,
			ZFROMJID TEXT,
			ZTOJID TEXT,
			ZISFROMME INTEGER,
			ZMESSAGETYPE INTEGER,
			ZSTARRED INTEGER,
			ZMESSAGEDATE REAL,
			ZTEXT TEXT,
			ZMEDIAITEM INTEGER
		);
	`)

	mustExec(t, contactsDB, `
		CREATE TABLE ZWAADDRESSBOOKCONTACT (
			Z_PK INTEGER PRIMARY KEY,
			ZWHATSAPPID TEXT,
			ZFULLNAME TEXT,
			ZGIVENNAME TEXT,
			ZPHONENUMBER TEXT,
			ZBUSINESSNAME TEXT,
			ZLID TEXT
		);
	`)

	dmTS := appleSeconds(time.Date(2024, 3, 10, 9, 0, 0, 0, time.UTC))
	groupTS := appleSeconds(time.Date(2024, 3, 10, 9, 5, 0, 0, time.UTC))
	statusTS := appleSeconds(time.Date(2024, 3, 10, 9, 10, 0, 0, time.UTC))
	groupCreateTS := appleSeconds(time.Date(2024, 2, 1, 12, 0, 0, 0, time.UTC))

	mustExec(t, chatDB, `INSERT INTO ZWACHATSESSION (Z_PK, ZGROUPINFO, ZLASTMESSAGEDATE, ZCONTACTJID, ZPARTNERNAME) VALUES (1, 0, ?, '491234567890', 'Alice Example')`, dmTS)
	mustExec(t, chatDB, `INSERT INTO ZWACHATSESSION (Z_PK, ZGROUPINFO, ZLASTMESSAGEDATE, ZCONTACTJID, ZPARTNERNAME) VALUES (2, 10, ?, '120363000000000000@g.us', 'Project Team')`, groupTS)
	mustExec(t, chatDB, `INSERT INTO ZWACHATSESSION (Z_PK, ZGROUPINFO, ZLASTMESSAGEDATE, ZCONTACTJID, ZPARTNERNAME) VALUES (3, 0, ?, 'status@broadcast', 'Status')`, statusTS)

	mustExec(t, chatDB, `INSERT INTO ZWAGROUPINFO (Z_PK, ZCHATSESSION, ZCREATIONDATE, ZCREATORJID, ZOWNERJID) VALUES (10, 2, ?, '491111111111', '491111111111')`, groupCreateTS)
	mustExec(t, chatDB, `INSERT INTO ZWAGROUPMEMBER (Z_PK, ZCHATSESSION, ZMEMBERJID, ZCONTACTNAME, ZFIRSTNAME, ZISADMIN) VALUES (20, 2, '491111111111', 'Bob Builder', 'Bob', 1)`)
	mustExec(t, chatDB, `INSERT INTO ZWAGROUPMEMBER (Z_PK, ZCHATSESSION, ZMEMBERJID, ZCONTACTNAME, ZFIRSTNAME, ZISADMIN) VALUES (21, 2, '491222222222', 'Carol Creator', 'Carol', 0)`)
	mustExec(t, chatDB, `INSERT INTO ZWAMEDIAITEM (Z_PK, ZMEDIALOCALPATH, ZFILESIZE) VALUES (30, 'Media/photo.jpg', 1234)`)

	mustExec(t, chatDB, `INSERT INTO ZWAMESSAGE (Z_PK, ZCHATSESSION, ZGROUPMEMBER, ZSTANZAID, ZFROMJID, ZTOJID, ZISFROMME, ZMESSAGETYPE, ZSTARRED, ZMESSAGEDATE, ZTEXT, ZMEDIAITEM) VALUES (100, 1, 0, 'm1', '491234567890', '', 0, 0, 1, ?, 'Hello from backup', 0)`, dmTS)
	mustExec(t, chatDB, `INSERT INTO ZWAMESSAGE (Z_PK, ZCHATSESSION, ZGROUPMEMBER, ZSTANZAID, ZFROMJID, ZTOJID, ZISFROMME, ZMESSAGETYPE, ZSTARRED, ZMESSAGEDATE, ZTEXT, ZMEDIAITEM) VALUES (101, 2, 20, '', '120363000000000000@g.us', '', 0, 1, 0, ?, 'Look at this', 30)`, groupTS)
	mustExec(t, chatDB, `INSERT INTO ZWAMESSAGE (Z_PK, ZCHATSESSION, ZGROUPMEMBER, ZSTANZAID, ZFROMJID, ZTOJID, ZISFROMME, ZMESSAGETYPE, ZSTARRED, ZMESSAGEDATE, ZTEXT, ZMEDIAITEM) VALUES (102, 3, 0, 'status1', 'status@broadcast', '', 0, 0, 0, ?, 'status update', 0)`, statusTS)

	mustExec(t, contactsDB, `INSERT INTO ZWAADDRESSBOOKCONTACT (Z_PK, ZWHATSAPPID, ZFULLNAME, ZGIVENNAME, ZPHONENUMBER, ZBUSINESSNAME, ZLID) VALUES (1, '491234567890', 'Alice Example', 'Alice', '491234567890', '', '')`)
	mustExec(t, contactsDB, `INSERT INTO ZWAADDRESSBOOKCONTACT (Z_PK, ZWHATSAPPID, ZFULLNAME, ZGIVENNAME, ZPHONENUMBER, ZBUSINESSNAME, ZLID) VALUES (2, '491111111111', 'Bob Builder', 'Bob', '491111111111', '', '')`)
}

func openSQLite(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("sql.Open(%s): %v", path, err)
	}
	return db
}

func seedLIDMap(t *testing.T, sessionDBPath, lid, pn string) {
	t.Helper()
	db := openSQLite(t, sessionDBPath)
	defer db.Close()
	mustExec(t, db, `CREATE TABLE whatsmeow_lid_map (lid TEXT, pn TEXT)`)
	mustExec(t, db, `INSERT INTO whatsmeow_lid_map (lid, pn) VALUES (?, ?)`, lid, pn)
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("Exec failed for %q: %v", q, err)
	}
}

func appleSeconds(ts time.Time) float64 {
	base := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	return ts.Sub(base).Seconds()
}
