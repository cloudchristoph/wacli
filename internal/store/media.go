package store

import (
	"time"

	"github.com/openclaw/wacli/internal/store/storedb"
)

func (d *DB) GetMediaDownloadInfo(chatJID, msgID string) (MediaDownloadInfo, error) {
	row, err := d.q.GetMediaDownloadInfo(storeCtx(), storedb.GetMediaDownloadInfoParams{ChatJid: chatJID, MsgID: msgID})
	if err != nil {
		return MediaDownloadInfo{}, err
	}
	info := MediaDownloadInfo{
		ChatJID:       row.ChatJid,
		ChatName:      row.Name,
		MsgID:         row.MsgID,
		MediaType:     row.MediaType,
		Filename:      row.Filename,
		MimeType:      row.MimeType,
		DirectPath:    row.DirectPath,
		MediaKey:      row.MediaKey,
		FileSHA256:    row.FileSha256,
		FileEncSHA256: row.FileEncSha256,
		LocalPath:     row.LocalPath,
		DownloadedAt:  fromUnix(row.DownloadedAt),
	}
	if row.FileLength > 0 {
		info.FileLength = uint64(row.FileLength)
	}
	return info, nil
}

func (d *DB) MarkMediaDownloaded(chatJID, msgID, localPath string, downloadedAt time.Time) error {
	return d.q.MarkMediaDownloaded(storeCtx(), storedb.MarkMediaDownloadedParams{
		LocalPath:    nullStringIfEmpty(localPath),
		DownloadedAt: sqlNullInt64(unix(downloadedAt)),
		ChatJid:      chatJID,
		MsgID:        msgID,
	})
}
