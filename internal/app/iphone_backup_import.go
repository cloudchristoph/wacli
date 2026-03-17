package app

import (
	"context"
	"database/sql"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/steipete/wacli/internal/store"
)

type IPhoneBackupImportOptions struct {
	IncludeStatus bool
}

type IPhoneBackupImportResult struct {
	BackupPath                string `json:"backup_path"`
	ContactsImported          int    `json:"contacts_imported"`
	ChatsImported             int    `json:"chats_imported"`
	GroupsImported            int    `json:"groups_imported"`
	GroupParticipantsImported int    `json:"group_participants_imported"`
	MessagesImported          int    `json:"messages_imported"`
	StarredImported           int    `json:"starred_imported"`
	MediaMessagesImported     int    `json:"media_messages_imported"`
	SkippedStatusChats        int    `json:"skipped_status_chats"`
	SkippedStatusMessages     int    `json:"skipped_status_messages"`
}

type backupChatSession struct {
	PK            int64
	JID           string
	Name          string
	Kind          string
	LastMessageTS time.Time
	GroupInfoPK   int64
	IsStatus      bool
}

type backupGroupInfo struct {
	SessionPK  int64
	CreatedAt  time.Time
	OwnerJID   string
	CreatorJID string
}

type backupGroupMember struct {
	SessionPK   int64
	MemberJID   string
	ContactName string
	FirstName   string
	IsAdmin     bool
}

type backupMediaItem struct {
	LocalPath     string
	MediaURL      string
	Title         string
	AuthorName    string
	VCardName     string
	VCardString   string
	FileSize      int64
	MovieDuration int64
	Latitude      float64
	Longitude     float64
	MediaKey      []byte
}

type backupImportCanonicalizer struct {
	app       *App
	canonical map[string]string
	merged    map[string]bool
}

func (a *App) ImportIPhoneBackup(ctx context.Context, backupDir string, opts IPhoneBackupImportOptions) (IPhoneBackupImportResult, error) {
	backupDir = strings.TrimSpace(backupDir)
	if backupDir == "" {
		return IPhoneBackupImportResult{}, fmt.Errorf("backup directory is required")
	}
	absBackupDir, err := filepath.Abs(backupDir)
	if err != nil {
		return IPhoneBackupImportResult{}, fmt.Errorf("resolve backup directory: %w", err)
	}
	chatPath := filepath.Join(absBackupDir, "ChatStorage.sqlite")
	if _, err := os.Stat(chatPath); err != nil {
		return IPhoneBackupImportResult{}, fmt.Errorf("ChatStorage.sqlite not found in %s", absBackupDir)
	}

	res := IPhoneBackupImportResult{BackupPath: absBackupDir}
	canon := &backupImportCanonicalizer{
		app:       a,
		canonical: make(map[string]string),
		merged:    make(map[string]bool),
	}

	chatDB, err := sql.Open("sqlite3", fmt.Sprintf("file:%s?mode=ro&_busy_timeout=5000", chatPath))
	if err != nil {
		return res, fmt.Errorf("open ChatStorage.sqlite: %w", err)
	}
	defer chatDB.Close()

	contactNames := map[string]string{}
	contactsPath := filepath.Join(absBackupDir, "ContactsV2.sqlite")
	if _, err := os.Stat(contactsPath); err == nil {
		contactsDB, err := sql.Open("sqlite3", fmt.Sprintf("file:%s?mode=ro&_busy_timeout=5000", contactsPath))
		if err != nil {
			return res, fmt.Errorf("open ContactsV2.sqlite: %w", err)
		}
		defer contactsDB.Close()
		imported, err := a.importBackupContacts(ctx, contactsDB, canon, contactNames)
		if err != nil {
			return res, err
		}
		res.ContactsImported += imported
	}

	groupInfos, err := loadBackupGroupInfos(ctx, chatDB)
	if err != nil {
		return res, err
	}
	groupMembers, participantsBySession, importedContacts, err := a.loadBackupGroupMembers(ctx, chatDB, canon, contactNames)
	if err != nil {
		return res, err
	}
	res.ContactsImported += importedContacts

	mediaItems, err := loadBackupMediaItems(ctx, chatDB)
	if err != nil {
		return res, err
	}

	sessions, importedChats, importedGroups, skippedStatusChats, err := a.importBackupChatSessions(ctx, chatDB, canon, contactNames, groupInfos, opts)
	if err != nil {
		return res, err
	}
	res.ChatsImported += importedChats
	res.GroupsImported += importedGroups
	res.SkippedStatusChats += skippedStatusChats

	participantsImported, err := a.importBackupGroupParticipants(ctx, sessions, participantsBySession)
	if err != nil {
		return res, err
	}
	res.GroupParticipantsImported += participantsImported

	messagesImported, mediaImported, starredImported, skippedStatusMessages, err := a.importBackupMessages(ctx, chatDB, absBackupDir, canon, sessions, groupMembers, mediaItems, contactNames, opts)
	if err != nil {
		return res, err
	}
	res.MessagesImported += messagesImported
	res.MediaMessagesImported += mediaImported
	res.StarredImported += starredImported
	res.SkippedStatusMessages += skippedStatusMessages

	return res, nil
}

func (a *App) importBackupContacts(ctx context.Context, contactsDB *sql.DB, canon *backupImportCanonicalizer, contactNames map[string]string) (int, error) {
	rows, err := contactsDB.QueryContext(ctx, `
		SELECT COALESCE(ZWHATSAPPID,''),
		       COALESCE(ZFULLNAME,''),
		       COALESCE(ZGIVENNAME,''),
		       COALESCE(ZPHONENUMBER,''),
		       COALESCE(ZBUSINESSNAME,''),
		       COALESCE(ZLID,'')
		FROM ZWAADDRESSBOOKCONTACT
	`)
	if err != nil {
		return 0, fmt.Errorf("query backup contacts: %w", err)
	}
	defer rows.Close()

	var imported int
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return imported, err
		}
		var whatsappID, fullName, givenName, phone, businessName, lid string
		if err := rows.Scan(&whatsappID, &fullName, &givenName, &phone, &businessName, &lid); err != nil {
			return imported, fmt.Errorf("scan backup contact: %w", err)
		}
		jid := normalizeBackupContactJID(whatsappID, lid)
		if jid == "" {
			continue
		}
		jid, err = canon.CanonicalizeUserJID(ctx, jid)
		if err != nil {
			return imported, fmt.Errorf("canonicalize contact %s: %w", jid, err)
		}
		name := bestNonEmpty(fullName, businessName, givenName)
		contactNames[jid] = bestNonEmpty(name, contactNames[jid])
		if err := a.db.UpsertContact(jid, firstNonEmpty(phone, jidUser(jid)), "", fullName, givenName, businessName); err != nil {
			return imported, fmt.Errorf("upsert contact %s: %w", jid, err)
		}
		imported++
	}
	if err := rows.Err(); err != nil {
		return imported, fmt.Errorf("iterate backup contacts: %w", err)
	}
	return imported, nil
}

func loadBackupGroupInfos(ctx context.Context, chatDB *sql.DB) (map[int64]backupGroupInfo, error) {
	rows, err := chatDB.QueryContext(ctx, `
		SELECT COALESCE(ZCHATSESSION,0), COALESCE(ZCREATIONDATE,0), COALESCE(ZOWNERJID,''), COALESCE(ZCREATORJID,'')
		FROM ZWAGROUPINFO
	`)
	if err != nil {
		return nil, fmt.Errorf("query backup group info: %w", err)
	}
	defer rows.Close()

	out := make(map[int64]backupGroupInfo)
	for rows.Next() {
		var sessionPK int64
		var createdRaw float64
		var ownerJID, creatorJID string
		if err := rows.Scan(&sessionPK, &createdRaw, &ownerJID, &creatorJID); err != nil {
			return nil, fmt.Errorf("scan backup group info: %w", err)
		}
		out[sessionPK] = backupGroupInfo{
			SessionPK:  sessionPK,
			CreatedAt:  backupAppleTime(createdRaw),
			OwnerJID:   normalizeBackupJID(ownerJID),
			CreatorJID: normalizeBackupJID(creatorJID),
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate backup group info: %w", err)
	}
	return out, nil
}

func (a *App) loadBackupGroupMembers(ctx context.Context, chatDB *sql.DB, canon *backupImportCanonicalizer, contactNames map[string]string) (map[int64]backupGroupMember, map[int64][]store.GroupParticipant, int, error) {
	rows, err := chatDB.QueryContext(ctx, `
		SELECT Z_PK, COALESCE(ZCHATSESSION,0), COALESCE(ZMEMBERJID,''), COALESCE(ZCONTACTNAME,''), COALESCE(ZFIRSTNAME,''), COALESCE(ZISADMIN,0)
		FROM ZWAGROUPMEMBER
	`)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("query backup group members: %w", err)
	}
	defer rows.Close()

	byPK := make(map[int64]backupGroupMember)
	participants := make(map[int64][]store.GroupParticipant)
	var importedContacts int
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return nil, nil, importedContacts, err
		}
		var pk, sessionPK int64
		var memberJID, contactName, firstName string
		var isAdmin int
		if err := rows.Scan(&pk, &sessionPK, &memberJID, &contactName, &firstName, &isAdmin); err != nil {
			return nil, nil, importedContacts, fmt.Errorf("scan backup group member: %w", err)
		}
		jid := normalizeBackupJID(memberJID)
		if jid == "" {
			continue
		}
		jid, err = canon.CanonicalizeUserJID(ctx, jid)
		if err != nil {
			return nil, nil, importedContacts, fmt.Errorf("canonicalize group member %s: %w", memberJID, err)
		}
		member := backupGroupMember{
			SessionPK:   sessionPK,
			MemberJID:   jid,
			ContactName: contactName,
			FirstName:   firstName,
			IsAdmin:     isAdmin != 0,
		}
		byPK[pk] = member
		contactNames[jid] = bestNonEmpty(contactName, firstName, contactNames[jid])
		_ = a.db.UpsertContact(jid, jidUser(jid), "", contactName, firstName, "")
		importedContacts++
		participants[sessionPK] = append(participants[sessionPK], store.GroupParticipant{
			UserJID: jid,
			Role:    roleFromAdmin(member.IsAdmin),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, nil, importedContacts, fmt.Errorf("iterate backup group members: %w", err)
	}
	return byPK, participants, importedContacts, nil
}

func loadBackupMediaItems(ctx context.Context, chatDB *sql.DB) (map[int64]backupMediaItem, error) {
	rows, err := chatDB.QueryContext(ctx, `
		SELECT Z_PK,
		       COALESCE(ZMEDIALOCALPATH,''),
		       COALESCE(ZMEDIAURL,''),
		       COALESCE(ZTITLE,''),
		       COALESCE(ZAUTHORNAME,''),
		       COALESCE(ZVCARDNAME,''),
		       COALESCE(ZVCARDSTRING,''),
		       COALESCE(ZFILESIZE,0),
		       COALESCE(ZMOVIEDURATION,0),
		       COALESCE(ZLATITUDE,0),
		       COALESCE(ZLONGITUDE,0),
		       ZMEDIAKEY
		FROM ZWAMEDIAITEM
	`)
	if err != nil {
		return nil, fmt.Errorf("query backup media items: %w", err)
	}
	defer rows.Close()

	out := make(map[int64]backupMediaItem)
	for rows.Next() {
		var pk int64
		var item backupMediaItem
		if err := rows.Scan(&pk, &item.LocalPath, &item.MediaURL, &item.Title, &item.AuthorName, &item.VCardName, &item.VCardString, &item.FileSize, &item.MovieDuration, &item.Latitude, &item.Longitude, &item.MediaKey); err != nil {
			return nil, fmt.Errorf("scan backup media item: %w", err)
		}
		out[pk] = item
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate backup media items: %w", err)
	}
	return out, nil
}

func (a *App) importBackupChatSessions(ctx context.Context, chatDB *sql.DB, canon *backupImportCanonicalizer, contactNames map[string]string, groupInfos map[int64]backupGroupInfo, opts IPhoneBackupImportOptions) (map[int64]backupChatSession, int, int, int, error) {
	rows, err := chatDB.QueryContext(ctx, `
		SELECT Z_PK, COALESCE(ZCONTACTJID,''), COALESCE(ZPARTNERNAME,''), COALESCE(ZLASTMESSAGEDATE,0), COALESCE(ZGROUPINFO,0)
		FROM ZWACHATSESSION
	`)
	if err != nil {
		return nil, 0, 0, 0, fmt.Errorf("query backup chat sessions: %w", err)
	}
	defer rows.Close()

	sessions := make(map[int64]backupChatSession)
	seenChats := make(map[string]bool)
	seenGroups := make(map[string]bool)
	var importedChats, importedGroups, skippedStatus int
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return nil, importedChats, importedGroups, skippedStatus, err
		}
		var pk, groupInfoPK int64
		var rawJID, partnerName string
		var lastMessageRaw float64
		if err := rows.Scan(&pk, &rawJID, &partnerName, &lastMessageRaw, &groupInfoPK); err != nil {
			return nil, importedChats, importedGroups, skippedStatus, fmt.Errorf("scan backup chat session: %w", err)
		}
		jid := normalizeBackupJID(rawJID)
		if jid == "" {
			continue
		}
		if !isNonUserChatJID(jid) {
			jid, err = canon.CanonicalizeUserJID(ctx, jid)
			if err != nil {
				return nil, importedChats, importedGroups, skippedStatus, fmt.Errorf("canonicalize chat %s: %w", rawJID, err)
			}
		}
		isStatus := isStatusLikeJID(jid)
		if isStatus && !opts.IncludeStatus {
			skippedStatus++
			continue
		}
		name := bestNonEmpty(partnerName, contactNames[jid])
		kind := detectBackupChatKind(jid, groupInfoPK != 0)
		lastTS := backupAppleTime(lastMessageRaw)
		if err := a.db.UpsertChat(jid, kind, name, lastTS); err != nil {
			return nil, importedChats, importedGroups, skippedStatus, fmt.Errorf("upsert chat %s: %w", jid, err)
		}
		sessions[pk] = backupChatSession{
			PK:            pk,
			JID:           jid,
			Name:          name,
			Kind:          kind,
			LastMessageTS: lastTS,
			GroupInfoPK:   groupInfoPK,
			IsStatus:      isStatus,
		}
		if !seenChats[jid] {
			seenChats[jid] = true
			importedChats++
		}
		if kind == "group" {
			g := groupInfos[pk]
			ownerJID := bestNonEmpty(g.OwnerJID, g.CreatorJID)
			if ownerJID != "" {
				ownerJID, err = canon.CanonicalizeUserJID(ctx, ownerJID)
				if err != nil {
					return nil, importedChats, importedGroups, skippedStatus, fmt.Errorf("canonicalize group owner %s: %w", ownerJID, err)
				}
			}
			if err := a.db.UpsertGroup(jid, name, ownerJID, g.CreatedAt); err != nil {
				return nil, importedChats, importedGroups, skippedStatus, fmt.Errorf("upsert group %s: %w", jid, err)
			}
			if !seenGroups[jid] {
				seenGroups[jid] = true
				importedGroups++
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, importedChats, importedGroups, skippedStatus, fmt.Errorf("iterate backup chat sessions: %w", err)
	}
	return sessions, importedChats, importedGroups, skippedStatus, nil
}

func (a *App) importBackupGroupParticipants(ctx context.Context, sessions map[int64]backupChatSession, participantsBySession map[int64][]store.GroupParticipant) (int, error) {
	var imported int
	for sessionPK, participants := range participantsBySession {
		if err := ctx.Err(); err != nil {
			return imported, err
		}
		session, ok := sessions[sessionPK]
		if !ok || session.Kind != "group" || len(participants) == 0 {
			continue
		}
		for i := range participants {
			participants[i].GroupJID = session.JID
		}
		if err := a.db.ReplaceGroupParticipants(session.JID, participants); err != nil {
			return imported, fmt.Errorf("replace participants for %s: %w", session.JID, err)
		}
		imported += len(participants)
	}
	return imported, nil
}

func (a *App) importBackupMessages(ctx context.Context, chatDB *sql.DB, backupDir string, canon *backupImportCanonicalizer, sessions map[int64]backupChatSession, groupMembers map[int64]backupGroupMember, mediaItems map[int64]backupMediaItem, contactNames map[string]string, opts IPhoneBackupImportOptions) (int, int, int, int, error) {
	rows, err := chatDB.QueryContext(ctx, `
		SELECT Z_PK,
		       COALESCE(ZCHATSESSION,0),
		       COALESCE(ZGROUPMEMBER,0),
		       COALESCE(ZSTANZAID,''),
		       COALESCE(ZFROMJID,''),
		       COALESCE(ZTOJID,''),
		       COALESCE(ZISFROMME,0),
		       COALESCE(ZMESSAGETYPE,0),
		       COALESCE(ZSTARRED,0),
		       COALESCE(ZMESSAGEDATE,0),
		       COALESCE(ZTEXT,''),
		       COALESCE(ZMEDIAITEM,0)
		FROM ZWAMESSAGE
		ORDER BY ZMESSAGEDATE ASC, Z_PK ASC
	`)
	if err != nil {
		return 0, 0, 0, 0, fmt.Errorf("query backup messages: %w", err)
	}
	defer rows.Close()

	var imported, mediaImported, starredImported, skippedStatus int
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return imported, mediaImported, starredImported, skippedStatus, err
		}
		var pk, chatSessionPK, groupMemberPK, mediaItemPK int64
		var stanzaID, fromJID, toJID, text string
		var isFromMe, messageType, starred int
		var messageDateRaw float64
		if err := rows.Scan(&pk, &chatSessionPK, &groupMemberPK, &stanzaID, &fromJID, &toJID, &isFromMe, &messageType, &starred, &messageDateRaw, &text, &mediaItemPK); err != nil {
			return imported, mediaImported, starredImported, skippedStatus, fmt.Errorf("scan backup message: %w", err)
		}
		session, ok := sessions[chatSessionPK]
		if !ok {
			continue
		}
		if session.IsStatus && !opts.IncludeStatus {
			skippedStatus++
			continue
		}
		messageTS := backupAppleTime(messageDateRaw)
		member := groupMembers[groupMemberPK]
		media := mediaItems[mediaItemPK]
		senderJID := importedMessageSenderJID(session, member, normalizeBackupJID(fromJID), normalizeBackupJID(toJID), isFromMe != 0)
		if senderJID == "" && session.Kind == "dm" && !isFromMeBool(isFromMe) {
			senderJID = session.JID
		}
		if senderJID != "" && !isNonUserChatJID(senderJID) {
			senderJID, err = canon.CanonicalizeUserJID(ctx, senderJID)
			if err != nil {
				return imported, mediaImported, starredImported, skippedStatus, fmt.Errorf("canonicalize sender %s: %w", senderJID, err)
			}
		}
		senderName := bestNonEmpty(member.ContactName, member.FirstName, contactNames[senderJID], session.Name)
		resolvedLocalPath := resolveBackupMediaPath(backupDir, media.LocalPath)
		mediaType := detectBackupMediaType(messageType, media, resolvedLocalPath)
		filename := detectBackupFilename(media, resolvedLocalPath)
		mimeType := detectBackupMimeType(filename)
		displayText := buildImportedDisplayText(strings.TrimSpace(text), mediaType, filename, media)
		msgID := strings.TrimSpace(stanzaID)
		if msgID == "" {
			msgID = fmt.Sprintf("ios-backup-%d", pk)
		}
		params := store.UpsertMessageParams{
			ChatJID:      session.JID,
			ChatName:     session.Name,
			MsgID:        msgID,
			SenderJID:    senderJID,
			SenderName:   senderName,
			Timestamp:    messageTS,
			FromMe:       isFromMe != 0,
			Text:         strings.TrimSpace(text),
			DisplayText:  displayText,
			MediaType:    mediaType,
			MediaCaption: mediaCaption(strings.TrimSpace(text), mediaType),
			Filename:     filename,
			MimeType:     mimeType,
			LocalPath:    resolvedLocalPath,
		}
		if resolvedLocalPath != "" {
			params.DownloadedAt = messageTS
		}
		if err := a.db.UpsertMessage(params); err != nil {
			return imported, mediaImported, starredImported, skippedStatus, fmt.Errorf("upsert message %s/%s: %w", session.JID, msgID, err)
		}
		imported++
		if mediaType != "" {
			mediaImported++
		}
		if starred != 0 {
			starSender := senderJID
			if starSender == "" {
				starSender = session.JID
			}
			if err := a.db.SetStarred(session.JID, starSender, msgID, true, messageTS); err != nil {
				return imported, mediaImported, starredImported, skippedStatus, fmt.Errorf("set starred %s/%s: %w", session.JID, msgID, err)
			}
			starredImported++
		}
	}
	if err := rows.Err(); err != nil {
		return imported, mediaImported, starredImported, skippedStatus, fmt.Errorf("iterate backup messages: %w", err)
	}
	return imported, mediaImported, starredImported, skippedStatus, nil
}

func backupAppleTime(v float64) time.Time {
	if v <= 0 {
		return time.Time{}
	}
	base := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	secs := time.Duration(v * float64(time.Second))
	return base.Add(secs).UTC()
}

func normalizeBackupContactJID(whatsAppID, lid string) string {
	if jid := normalizeBackupJID(whatsAppID); jid != "" {
		return jid
	}
	if s := strings.TrimSpace(lid); s != "" {
		if strings.Contains(s, "@") {
			return s
		}
		return s + "@lid"
	}
	return ""
}

func normalizeBackupJID(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	lower := strings.ToLower(s)
	for _, suffix := range []string{"@s.whatsapp.net", "@g.us", "@broadcast", "@status", "@lid", "@newsletter"} {
		if strings.HasSuffix(lower, suffix) {
			return s
		}
	}
	if strings.Contains(s, "@") {
		return s
	}
	return s + "@s.whatsapp.net"
}

func isStatusLikeJID(jid string) bool {
	jid = strings.ToLower(strings.TrimSpace(jid))
	return jid == "status@broadcast" || strings.HasSuffix(jid, "@status")
}

func isNonUserChatJID(jid string) bool {
	jid = strings.ToLower(strings.TrimSpace(jid))
	return strings.HasSuffix(jid, "@g.us") || strings.HasSuffix(jid, "@broadcast") || strings.HasSuffix(jid, "@status") || strings.HasSuffix(jid, "@newsletter")
}

func detectBackupChatKind(jid string, hasGroupInfo bool) string {
	if hasGroupInfo || strings.HasSuffix(strings.ToLower(jid), "@g.us") {
		return "group"
	}
	if strings.HasSuffix(strings.ToLower(jid), "@broadcast") || isStatusLikeJID(jid) {
		return "broadcast"
	}
	return "dm"
}

func importedMessageSenderJID(session backupChatSession, member backupGroupMember, fromJID, toJID string, fromMe bool) string {
	if member.MemberJID != "" {
		return member.MemberJID
	}
	if !fromMe {
		if session.Kind == "dm" {
			return session.JID
		}
		if fromJID != "" && fromJID != session.JID {
			return fromJID
		}
	}
	if fromMe && session.Kind == "dm" && toJID != "" {
		return toJID
	}
	return ""
}

func detectBackupMediaType(messageType int, media backupMediaItem, resolvedLocalPath string) string {
	if media.VCardString != "" || media.VCardName != "" {
		return "contact"
	}
	if media.Latitude != 0 || media.Longitude != 0 {
		return "location"
	}
	ext := strings.ToLower(filepath.Ext(firstNonEmpty(resolvedLocalPath, media.LocalPath)))
	switch ext {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".heic":
		return "image"
	case ".mp4", ".mov", ".m4v", ".avi", ".mkv":
		return "video"
	case ".mp3", ".m4a", ".aac", ".wav", ".ogg", ".opus":
		return "audio"
	case ".pdf", ".doc", ".docx", ".xls", ".xlsx", ".ppt", ".pptx", ".txt", ".zip":
		return "document"
	}
	if resolvedLocalPath != "" || media.LocalPath != "" || media.MediaURL != "" {
		if media.MovieDuration > 0 {
			return "video"
		}
		return "media"
	}
	if messageType != 0 {
		return ""
	}
	return ""
}

func detectBackupFilename(media backupMediaItem, resolvedLocalPath string) string {
	path := firstNonEmpty(resolvedLocalPath, media.LocalPath)
	if path != "" {
		return filepath.Base(path)
	}
	if media.VCardName != "" {
		return media.VCardName + ".vcf"
	}
	return firstNonEmpty(media.Title, media.AuthorName)
}

func detectBackupMimeType(filename string) string {
	if filename == "" {
		return ""
	}
	return mime.TypeByExtension(strings.ToLower(filepath.Ext(filename)))
}

func buildImportedDisplayText(text, mediaType, filename string, media backupMediaItem) string {
	if text != "" {
		return text
	}
	switch mediaType {
	case "contact":
		return bestNonEmpty(media.VCardName, "Shared contact")
	case "location":
		return "Shared location"
	case "image", "video", "audio", "document", "media":
		if filename != "" {
			return fmt.Sprintf("Sent %s: %s", mediaType, filename)
		}
		return "Sent " + mediaType
	default:
		return ""
	}
}

func mediaCaption(text, mediaType string) string {
	if text != "" && mediaType != "" {
		return text
	}
	return ""
}

func resolveBackupMediaPath(backupDir, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	cleaned := filepath.Clean(raw)
	candidates := []string{}
	if filepath.IsAbs(cleaned) {
		candidates = append(candidates, cleaned)
		cleaned = strings.TrimPrefix(cleaned, string(filepath.Separator))
	}
	for _, prefix := range []string{"", "Media", "Message"} {
		candidate := filepath.Join(backupDir, prefix, cleaned)
		candidates = append(candidates, candidate)
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if fi, err := os.Stat(candidate); err == nil && !fi.IsDir() {
			return candidate
		}
	}
	return ""
}

func jidUser(jid string) string {
	if i := strings.Index(jid, "@"); i > 0 {
		return jid[:i]
	}
	return jid
}

func roleFromAdmin(isAdmin bool) string {
	if isAdmin {
		return "admin"
	}
	return "member"
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func bestNonEmpty(values ...string) string { return firstNonEmpty(values...) }

func isFromMeBool(v int) bool { return v != 0 }

func (r *backupImportCanonicalizer) CanonicalizeUserJID(ctx context.Context, jid string) (string, error) {
	jid = normalizeBackupJID(jid)
	if jid == "" || isNonUserChatJID(jid) {
		return jid, nil
	}
	if canonical, ok := r.canonical[jid]; ok {
		return canonical, nil
	}
	candidates, err := r.app.ResolveChatJIDCandidates(ctx, jid)
	if err != nil || len(candidates) == 0 {
		candidates = []string{jid}
	}
	canonical := normalizeBackupJID(candidates[0])
	if canonical == "" {
		canonical = jid
	}
	for _, candidate := range candidates {
		candidate = normalizeBackupJID(candidate)
		if candidate == "" {
			continue
		}
		r.canonical[candidate] = canonical
	}
	for _, alt := range candidates[1:] {
		alt = normalizeBackupJID(alt)
		if alt == "" || alt == canonical {
			continue
		}
		key := alt + "=>" + canonical
		if r.merged[key] {
			continue
		}
		if _, err := r.app.db.MergeChatIdentity(alt, canonical); err != nil {
			return "", fmt.Errorf("merge duplicate identity %s -> %s: %w", alt, canonical, err)
		}
		r.merged[key] = true
	}
	return canonical, nil
}
