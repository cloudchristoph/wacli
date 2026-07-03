package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// This file holds store helpers that exist only in the local wacli feature set
// (iPhone backup import, LID consolidation, bulk media download, backfill-all).
// They are written against the upstream schema and are kept separate from the
// sqlc-generated and upstream-authored store files.

// ListMediaDownloadInfos returns downloadable media entries for a chat, ordered
// oldest first. Pass includeDownloaded=false to skip already-downloaded media,
// and limit<=0 for no limit. Used for bulk media download.
func (d *DB) ListMediaDownloadInfos(chatJID string, limit int, includeDownloaded bool) ([]MediaDownloadInfo, error) {
	chatJID = strings.TrimSpace(chatJID)
	if chatJID == "" {
		return nil, fmt.Errorf("chat JID is required")
	}

	query := `
		SELECT m.chat_jid,
		       COALESCE(c.name,''),
		       m.msg_id,
		       COALESCE(m.media_type,''),
		       COALESCE(m.filename,''),
		       COALESCE(m.mime_type,''),
		       COALESCE(m.direct_path,''),
		       m.media_key,
		       m.file_sha256,
		       m.file_enc_sha256,
		       COALESCE(m.file_length,0),
		       COALESCE(m.local_path,''),
		       COALESCE(m.downloaded_at,0)
		FROM messages m
		LEFT JOIN chats c ON c.jid = m.chat_jid
		WHERE m.chat_jid = ?
		  AND COALESCE(m.media_type,'') != ''
		  AND COALESCE(m.direct_path,'') != ''
		  AND m.media_key IS NOT NULL
		  AND length(m.media_key) > 0`
	args := []interface{}{chatJID}

	if !includeDownloaded {
		query += ` AND COALESCE(m.local_path,'') = ''`
	}

	query += ` ORDER BY m.ts ASC`
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}

	rows, err := d.sql.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]MediaDownloadInfo, 0)
	for rows.Next() {
		var info MediaDownloadInfo
		var fileLen sql.NullInt64
		var downloadedAt int64
		if err := rows.Scan(
			&info.ChatJID,
			&info.ChatName,
			&info.MsgID,
			&info.MediaType,
			&info.Filename,
			&info.MimeType,
			&info.DirectPath,
			&info.MediaKey,
			&info.FileSHA256,
			&info.FileEncSHA256,
			&fileLen,
			&info.LocalPath,
			&downloadedAt,
		); err != nil {
			return nil, err
		}
		if fileLen.Valid && fileLen.Int64 > 0 {
			info.FileLength = uint64(fileLen.Int64)
		}
		info.DownloadedAt = fromUnix(downloadedAt)
		out = append(out, info)
	}
	return out, rows.Err()
}

// StoredMediaPathInfo describes a message whose media has already been
// downloaded to a local path. Used by the iPhone backup import to migrate
// media file paths.
type StoredMediaPathInfo struct {
	ChatJID      string
	MsgID        string
	MediaType    string
	Filename     string
	MimeType     string
	LocalPath    string
	DownloadedAt time.Time
}

// ListStoredMediaPathInfos returns every message that has a non-empty local
// media path, ordered oldest first. Used for media path migration.
func (d *DB) ListStoredMediaPathInfos() ([]StoredMediaPathInfo, error) {
	rows, err := d.sql.Query(`
		SELECT chat_jid,
		       msg_id,
		       COALESCE(media_type,''),
		       COALESCE(filename,''),
		       COALESCE(mime_type,''),
		       COALESCE(local_path,''),
		       COALESCE(downloaded_at,0)
		FROM messages
		WHERE COALESCE(media_type,'') != ''
		  AND COALESCE(local_path,'') != ''
		ORDER BY ts ASC, rowid ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]StoredMediaPathInfo, 0)
	for rows.Next() {
		var info StoredMediaPathInfo
		var downloadedAt int64
		if err := rows.Scan(
			&info.ChatJID,
			&info.MsgID,
			&info.MediaType,
			&info.Filename,
			&info.MimeType,
			&info.LocalPath,
			&downloadedAt,
		); err != nil {
			return nil, err
		}
		info.DownloadedAt = fromUnix(downloadedAt)
		out = append(out, info)
	}
	return out, rows.Err()
}

// ListChatsWithMessages returns all chats that have at least one message stored
// locally, ordered by most recent message first. Pass limit=0 for all chats.
// Used by backfill-all.
func (d *DB) ListChatsWithMessages(limit int) ([]Chat, error) {
	q := `
		SELECT c.jid, c.kind, COALESCE(c.name,''), COALESCE(c.last_message_ts,0)
		FROM chats c
		WHERE EXISTS (SELECT 1 FROM messages m WHERE m.chat_jid = c.jid)
		ORDER BY c.last_message_ts DESC`
	var args []interface{}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := d.sql.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Chat
	for rows.Next() {
		var c Chat
		var ts int64
		if err := rows.Scan(&c.JID, &c.Kind, &c.Name, &ts); err != nil {
			return nil, err
		}
		c.LastMessageTS = fromUnix(ts)
		out = append(out, c)
	}
	return out, rows.Err()
}

// MergeChatIdentity moves every row belonging to fromJID into toJID across all
// chat-scoped tables (chats, messages, starred, polls, poll_votes, call_events,
// status references, contacts, contact aliases/tags), then removes the source
// chat. It returns the number of messages moved out of the source chat.
//
// Unlike MigrateLIDToPN (which is LID→PN specific and poll/vote aware for
// hidden-user rewrites), this is a generic from→to chat merge used by the
// iPhone backup import and LID consolidation features.
func (d *DB) MergeChatIdentity(fromJID, toJID string) (int64, error) {
	fromJID = strings.TrimSpace(fromJID)
	toJID = strings.TrimSpace(toJID)
	if fromJID == "" || toJID == "" || fromJID == toJID {
		return 0, nil
	}

	tx, err := d.sql.Begin()
	if err != nil {
		return 0, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	// Ensure destination chat exists and keep best metadata, folding in the
	// upstream chat-state columns.
	if _, err = tx.Exec(`
		INSERT INTO chats(jid, kind, name, last_message_ts, archived, pinned, muted_until, unread, unread_count)
		SELECT ?,
		       CASE WHEN kind = 'unknown' THEN 'dm' ELSE kind END,
		       name,
		       last_message_ts,
		       archived, pinned, muted_until, unread, unread_count
		FROM chats WHERE jid = ?
		ON CONFLICT(jid) DO UPDATE SET
			kind=CASE WHEN chats.kind='unknown' AND excluded.kind!='unknown' THEN excluded.kind ELSE chats.kind END,
			name=CASE WHEN (COALESCE(chats.name,'')='') AND COALESCE(excluded.name,'')!='' THEN excluded.name ELSE chats.name END,
			last_message_ts=CASE WHEN COALESCE(excluded.last_message_ts,0) > COALESCE(chats.last_message_ts,0) THEN excluded.last_message_ts ELSE chats.last_message_ts END,
			archived=CASE WHEN chats.archived!=0 OR excluded.archived!=0 THEN 1 ELSE 0 END,
			pinned=CASE WHEN chats.pinned!=0 OR excluded.pinned!=0 THEN 1 ELSE 0 END,
			muted_until=max(COALESCE(chats.muted_until,0), COALESCE(excluded.muted_until,0)),
			unread=CASE WHEN chats.unread!=0 OR excluded.unread!=0 THEN 1 ELSE 0 END,
			unread_count=COALESCE(chats.unread_count,0) + COALESCE(excluded.unread_count,0)
	`, toJID, fromJID); err != nil {
		return 0, err
	}

	// Move messages with conflict-safe upsert into destination chat, carrying
	// the upstream reply/reaction/forward/edit/revoke columns.
	if _, err = tx.Exec(`
		INSERT INTO messages(
			chat_jid, chat_name, msg_id, sender_jid, sender_name, ts, from_me, text, display_text,
			quoted_msg_id, quoted_sender_jid, is_forwarded, forwarding_score, reaction_to_id, reaction_emoji,
			media_type, media_caption, filename, mime_type, direct_path,
			media_key, file_sha256, file_enc_sha256, file_length, local_path, downloaded_at,
			revoked, deleted_for_me, edited, edited_ts, buttons
		)
		SELECT ?, chat_name, msg_id, sender_jid, sender_name, ts, from_me, text, display_text,
		       quoted_msg_id, quoted_sender_jid, is_forwarded, forwarding_score, reaction_to_id, reaction_emoji,
		       media_type, media_caption, filename, mime_type, direct_path,
		       media_key, file_sha256, file_enc_sha256, file_length, local_path, downloaded_at,
		       revoked, deleted_for_me, edited, edited_ts, buttons
		FROM messages
		WHERE chat_jid = ?
		ON CONFLICT(chat_jid, msg_id) DO UPDATE SET
			chat_name=COALESCE(NULLIF(messages.chat_name,''), NULLIF(excluded.chat_name,''), messages.chat_name),
			sender_jid=COALESCE(NULLIF(messages.sender_jid,''), excluded.sender_jid),
			sender_name=COALESCE(NULLIF(messages.sender_name,''), NULLIF(excluded.sender_name,''), messages.sender_name),
			ts=CASE WHEN excluded.ts < messages.ts THEN excluded.ts ELSE messages.ts END,
			from_me=CASE WHEN messages.from_me=1 OR excluded.from_me=1 THEN 1 ELSE 0 END,
			text=CASE WHEN COALESCE(length(messages.text),0) >= COALESCE(length(excluded.text),0) THEN messages.text ELSE excluded.text END,
			display_text=CASE WHEN COALESCE(length(messages.display_text),0) >= COALESCE(length(excluded.display_text),0) THEN messages.display_text ELSE excluded.display_text END,
			quoted_msg_id=COALESCE(NULLIF(messages.quoted_msg_id,''), excluded.quoted_msg_id),
			quoted_sender_jid=COALESCE(NULLIF(messages.quoted_sender_jid,''), excluded.quoted_sender_jid),
			is_forwarded=CASE WHEN messages.is_forwarded!=0 THEN messages.is_forwarded ELSE excluded.is_forwarded END,
			forwarding_score=max(messages.forwarding_score, excluded.forwarding_score),
			reaction_to_id=COALESCE(NULLIF(messages.reaction_to_id,''), excluded.reaction_to_id),
			reaction_emoji=COALESCE(NULLIF(messages.reaction_emoji,''), excluded.reaction_emoji),
			media_type=COALESCE(NULLIF(messages.media_type,''), excluded.media_type),
			media_caption=COALESCE(NULLIF(messages.media_caption,''), excluded.media_caption),
			filename=COALESCE(NULLIF(messages.filename,''), excluded.filename),
			mime_type=COALESCE(NULLIF(messages.mime_type,''), excluded.mime_type),
			direct_path=COALESCE(NULLIF(messages.direct_path,''), excluded.direct_path),
			media_key=CASE WHEN messages.media_key IS NOT NULL AND length(messages.media_key)>0 THEN messages.media_key ELSE excluded.media_key END,
			file_sha256=CASE WHEN messages.file_sha256 IS NOT NULL AND length(messages.file_sha256)>0 THEN messages.file_sha256 ELSE excluded.file_sha256 END,
			file_enc_sha256=CASE WHEN messages.file_enc_sha256 IS NOT NULL AND length(messages.file_enc_sha256)>0 THEN messages.file_enc_sha256 ELSE excluded.file_enc_sha256 END,
			file_length=CASE WHEN COALESCE(messages.file_length,0) > 0 THEN messages.file_length ELSE excluded.file_length END,
			local_path=COALESCE(NULLIF(messages.local_path,''), excluded.local_path),
			downloaded_at=CASE WHEN COALESCE(messages.downloaded_at,0) > 0 THEN messages.downloaded_at ELSE excluded.downloaded_at END,
			revoked=CASE WHEN messages.revoked!=0 OR excluded.revoked!=0 THEN 1 ELSE 0 END,
			deleted_for_me=CASE WHEN messages.deleted_for_me!=0 OR excluded.deleted_for_me!=0 THEN 1 ELSE 0 END,
			edited=CASE WHEN messages.edited!=0 OR excluded.edited!=0 THEN 1 ELSE 0 END,
			edited_ts=max(COALESCE(messages.edited_ts,0), COALESCE(excluded.edited_ts,0)),
			buttons=COALESCE(messages.buttons, excluded.buttons)
	`, toJID, fromJID); err != nil {
		return 0, err
	}

	res, err := tx.Exec(`DELETE FROM messages WHERE chat_jid = ?`, fromJID)
	if err != nil {
		return 0, err
	}
	moved, _ := res.RowsAffected()

	// Starred (upstream schema: chat_jid, msg_id, sender_jid, from_me, starred_at).
	if _, err = tx.Exec(`
		INSERT INTO starred(chat_jid, msg_id, sender_jid, from_me, starred_at)
		SELECT ?, msg_id, sender_jid, from_me, starred_at
		FROM starred
		WHERE chat_jid = ?
		ON CONFLICT(chat_jid, msg_id) DO UPDATE SET
			sender_jid=COALESCE(NULLIF(starred.sender_jid,''), excluded.sender_jid),
			from_me=CASE WHEN starred.from_me!=0 OR excluded.from_me!=0 THEN 1 ELSE 0 END,
			starred_at=CASE WHEN excluded.starred_at < starred.starred_at THEN excluded.starred_at ELSE starred.starred_at END
	`, toJID, fromJID); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(`DELETE FROM starred WHERE chat_jid = ?`, fromJID); err != nil {
		return 0, err
	}

	// Polls.
	if _, err = tx.Exec(`
		INSERT INTO polls(chat_jid, msg_id, sender_jid, question, options_json, selectable_count, created_ts)
		SELECT ?, msg_id, sender_jid, question, options_json, selectable_count, created_ts
		FROM polls
		WHERE chat_jid = ?
		ON CONFLICT(chat_jid, msg_id) DO UPDATE SET
			sender_jid=COALESCE(NULLIF(polls.sender_jid,''), excluded.sender_jid),
			question=CASE WHEN COALESCE(polls.question,'')='' THEN excluded.question ELSE polls.question END,
			options_json=CASE WHEN COALESCE(polls.options_json,'')='' THEN excluded.options_json ELSE polls.options_json END,
			selectable_count=CASE WHEN polls.selectable_count>0 THEN polls.selectable_count ELSE excluded.selectable_count END,
			created_ts=max(COALESCE(polls.created_ts,0), COALESCE(excluded.created_ts,0))
	`, toJID, fromJID); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(`DELETE FROM polls WHERE chat_jid = ?`, fromJID); err != nil {
		return 0, err
	}

	// Poll votes.
	if _, err = tx.Exec(`
		INSERT INTO poll_votes(chat_jid, poll_msg_id, voter_jid, vote_msg_id, selected_options_json, ts)
		SELECT ?, poll_msg_id, voter_jid, vote_msg_id, selected_options_json, ts
		FROM poll_votes
		WHERE chat_jid = ?
		ON CONFLICT(chat_jid, poll_msg_id, voter_jid) DO UPDATE SET
			vote_msg_id=excluded.vote_msg_id,
			selected_options_json=excluded.selected_options_json,
			ts=CASE WHEN excluded.ts >= poll_votes.ts THEN excluded.ts ELSE poll_votes.ts END
	`, toJID, fromJID); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(`DELETE FROM poll_votes WHERE chat_jid = ?`, fromJID); err != nil {
		return 0, err
	}

	// Call events (upstream table).
	if _, err = tx.Exec(`
		INSERT INTO call_events(chat_jid, chat_name, sender_jid, sender_name, call_id, msg_id, event_type, direction, media, outcome, reason, call_type, duration_secs, ts, participants)
		SELECT ?, chat_name, sender_jid, sender_name, call_id, msg_id, event_type, direction, media, outcome, reason, call_type, duration_secs, ts, participants
		FROM call_events
		WHERE chat_jid = ?
		ON CONFLICT(chat_jid, call_id, event_type, ts) DO NOTHING
	`, toJID, fromJID); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(`DELETE FROM call_events WHERE chat_jid = ?`, fromJID); err != nil {
		return 0, err
	}

	// Contacts (upstream schema: jid, phone, push_name, full_name, first_name, business_name, system_name, updated_at).
	if _, err = tx.Exec(`
		INSERT INTO contacts(jid, phone, push_name, full_name, first_name, business_name, system_name, updated_at)
		SELECT ?, phone, push_name, full_name, first_name, business_name, system_name, updated_at
		FROM contacts
		WHERE jid = ?
		ON CONFLICT(jid) DO UPDATE SET
			phone=COALESCE(NULLIF(contacts.phone,''), excluded.phone),
			push_name=COALESCE(NULLIF(contacts.push_name,''), excluded.push_name),
			full_name=COALESCE(NULLIF(contacts.full_name,''), excluded.full_name),
			first_name=COALESCE(NULLIF(contacts.first_name,''), excluded.first_name),
			business_name=COALESCE(NULLIF(contacts.business_name,''), excluded.business_name),
			system_name=COALESCE(NULLIF(contacts.system_name,''), excluded.system_name),
			updated_at=CASE WHEN excluded.updated_at > contacts.updated_at THEN excluded.updated_at ELSE contacts.updated_at END
	`, toJID, fromJID); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(`DELETE FROM contacts WHERE jid = ?`, fromJID); err != nil {
		return 0, err
	}

	// Contact aliases.
	if _, err = tx.Exec(`
		INSERT INTO contact_aliases(jid, alias, notes, updated_at)
		SELECT ?, alias, notes, updated_at
		FROM contact_aliases
		WHERE jid = ?
		ON CONFLICT(jid) DO UPDATE SET
			alias=CASE WHEN COALESCE(contact_aliases.alias,'')='' AND COALESCE(excluded.alias,'')!='' THEN excluded.alias ELSE contact_aliases.alias END,
			notes=CASE WHEN COALESCE(contact_aliases.notes,'')='' AND COALESCE(excluded.notes,'')!='' THEN excluded.notes ELSE contact_aliases.notes END,
			updated_at=CASE WHEN excluded.updated_at > contact_aliases.updated_at THEN excluded.updated_at ELSE contact_aliases.updated_at END
	`, toJID, fromJID); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(`DELETE FROM contact_aliases WHERE jid = ?`, fromJID); err != nil {
		return 0, err
	}

	// Contact tags.
	if _, err = tx.Exec(`INSERT OR IGNORE INTO contact_tags(jid, tag, updated_at) SELECT ?, tag, updated_at FROM contact_tags WHERE jid = ?`, toJID, fromJID); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(`DELETE FROM contact_tags WHERE jid = ?`, fromJID); err != nil {
		return 0, err
	}

	if _, err = tx.Exec(`DELETE FROM chats WHERE jid = ?`, fromJID); err != nil {
		return 0, err
	}

	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return moved, nil
}
