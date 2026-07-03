package app

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/steipete/wacli/internal/store"
	"github.com/steipete/wacli/internal/wa"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

type SyncMode string

const (
	SyncModeBootstrap SyncMode = "bootstrap"
	SyncModeOnce      SyncMode = "once"
	SyncModeFollow    SyncMode = "follow"
)

type SyncOptions struct {
	Mode            SyncMode
	AllowQR         bool
	OnQRCode        func(string)
	AfterConnect    func(context.Context) error
	DownloadMedia   bool
	RefreshContacts bool
	RefreshGroups   bool
	IdleExit        time.Duration // only used for bootstrap/once
	StaleThreshold  time.Duration // force reconnect when keepalive failures last this long in follow mode (0 = disabled)
	Verbosity       int           // future
}

type SyncResult struct {
	MessagesStored int64
}

// MaxStaleThreshold returns the exclusive upper bound for keepalive-failure thresholds.
// It reserves one maximum keepalive probe interval plus response deadline before
// whatsmeow's own failed-keepalive auto-reconnect window.
func MaxStaleThreshold() time.Duration {
	return whatsmeow.KeepAliveMaxFailTime - whatsmeow.KeepAliveIntervalMax - whatsmeow.KeepAliveResponseDeadline
}

type staleReconnectRequest struct {
	threshold   time.Duration
	idle        time.Duration
	errorCount  int
	lastSuccess time.Time
}

func handleKeepAliveTimeout(opts SyncOptions, evt *events.KeepAliveTimeout, staleReconnect chan<- staleReconnectRequest) {
	if opts.Mode != SyncModeFollow || opts.StaleThreshold <= 0 || evt == nil || evt.LastSuccess.IsZero() {
		return
	}
	idle := time.Since(evt.LastSuccess)
	if idle < opts.StaleThreshold {
		return
	}
	req := staleReconnectRequest{
		threshold:   opts.StaleThreshold,
		idle:        idle,
		errorCount:  evt.ErrorCount,
		lastSuccess: evt.LastSuccess,
	}
	select {
	case staleReconnect <- req:
	default:
	}
}

// syncActivityEvent reports whether an event counts as sync activity for
// idle-exit purposes. Error and disconnect events must not count, otherwise
// a connection that only produces failures looks alive forever.
func syncActivityEvent(evt interface{}) bool {
	switch evt.(type) {
	case nil,
		*events.KeepAliveTimeout,
		*events.PairError,
		*events.LoggedOut,
		*events.StreamReplaced,
		*events.ManualLoginReconnect,
		*events.TemporaryBan,
		*events.ConnectFailure,
		*events.ClientOutdated,
		*events.CATRefreshError,
		*events.StreamError,
		*events.Disconnected,
		*events.AppStateSyncError,
		*events.MediaRetryError:
		return false
	default:
		return true
	}
}

func (a *App) Sync(ctx context.Context, opts SyncOptions) (SyncResult, error) {
	if opts.Mode == "" {
		opts.Mode = SyncModeFollow
	}
	if (opts.Mode == SyncModeBootstrap || opts.Mode == SyncModeOnce) && opts.IdleExit <= 0 {
		opts.IdleExit = 30 * time.Second
	}

	if err := a.OpenWA(); err != nil {
		return SyncResult{}, err
	}

	// The stale-threshold loop drives reconnects itself, so whatsmeow's
	// built-in auto-reconnect must be off to avoid competing reconnects.
	if opts.Mode == SyncModeFollow && opts.StaleThreshold > 0 {
		restoreAutoReconnect, ok := a.wa.SetAutoReconnect(false)
		if !ok {
			return SyncResult{}, fmt.Errorf("could not configure stale-threshold reconnect on an already-connected WhatsApp client")
		}
		defer func() {
			if !a.wa.IsConnected() {
				a.wa.SetAutoReconnect(restoreAutoReconnect)
			}
		}()
	}

	var messagesStored atomic.Int64
	lastEvent := atomic.Int64{}
	lastEvent.Store(time.Now().UTC().UnixNano())

	disconnected := make(chan struct{}, 1)
	staleReconnect := make(chan staleReconnectRequest, 1)
	fatal := make(chan error, 1)
	reportFatal := func(err error) {
		select {
		case fatal <- err:
		default:
		}
	}
	var connectionEpoch atomic.Int64

	var stopMedia func()
	var mediaJobs chan mediaJob
	enqueueMedia := func(chatJID, msgID string) {}
	if opts.DownloadMedia {
		mediaJobs = make(chan mediaJob, 512)
		enqueueMedia = func(chatJID, msgID string) {
			if strings.TrimSpace(chatJID) == "" || strings.TrimSpace(msgID) == "" {
				return
			}
			select {
			case mediaJobs <- mediaJob{chatJID: chatJID, msgID: msgID}:
			default:
				// Avoid blocking the event handler.
				go func() {
					select {
					case mediaJobs <- mediaJob{chatJID: chatJID, msgID: msgID}:
					case <-ctx.Done():
					}
				}()
			}
		}
	}

	handlerID := a.wa.AddEventHandler(func(evt interface{}) {
		if syncActivityEvent(evt) {
			lastEvent.Store(time.Now().UTC().UnixNano())
		}

		switch v := evt.(type) {
		case *events.Message:
			pm := wa.ParseLiveMessage(v)
			if pm.ReactionToID != "" && pm.ReactionEmoji == "" && v.Message != nil && v.Message.GetEncReactionMessage() != nil {
				if reaction, err := a.wa.DecryptReaction(ctx, v); err == nil && reaction != nil {
					pm.ReactionEmoji = reaction.GetText()
					if pm.ReactionToID == "" {
						if key := reaction.GetKey(); key != nil {
							pm.ReactionToID = key.GetID()
						}
					}
				}
			}
			if err := a.storeParsedMessage(ctx, pm); err == nil {
				messagesStored.Add(1)
			}
			if opts.DownloadMedia && pm.Media != nil && pm.ID != "" {
				enqueueMedia(pm.Chat.String(), pm.ID)
			}
			if messagesStored.Load()%25 == 0 {
				fmt.Fprintf(os.Stderr, "\rSynced %d messages...", messagesStored.Load())
			}
		case *events.HistorySync:
			fmt.Fprintf(os.Stderr, "\nProcessing history sync (%d conversations)...\n", len(v.Data.Conversations))
			for _, conv := range v.Data.Conversations {
				lastEvent.Store(time.Now().UTC().UnixNano())
				chatID := strings.TrimSpace(conv.GetID())
				if chatID == "" {
					continue
				}
				for _, m := range conv.Messages {
					lastEvent.Store(time.Now().UTC().UnixNano())
					if m.Message == nil {
						continue
					}
					pm := wa.ParseHistoryMessage(chatID, m.Message)
					if pm.ID == "" || pm.Chat.IsEmpty() {
						continue
					}
					if err := a.storeParsedMessage(ctx, pm); err == nil {
						messagesStored.Add(1)
					}
					if opts.DownloadMedia && pm.Media != nil && pm.ID != "" {
						enqueueMedia(pm.Chat.String(), pm.ID)
					}
				}
			}
			fmt.Fprintf(os.Stderr, "\rSynced %d messages...", messagesStored.Load())
		case *events.Star:
			if v == nil || v.Action == nil {
				return
			}
			_ = a.db.SetStarred(v.ChatJID.String(), v.SenderJID.String(), v.MessageID, v.Action.GetStarred(), v.Timestamp)
		case *events.Connected:
			fmt.Fprintln(os.Stderr, "\nConnected.")
		case *events.Disconnected:
			fmt.Fprintln(os.Stderr, "\nDisconnected.")
			select {
			case disconnected <- struct{}{}:
			default:
			}
		case *events.KeepAliveTimeout:
			handleKeepAliveTimeout(opts, v, staleReconnect)
		case *events.StreamReplaced:
			fmt.Fprintln(os.Stderr, "\nStream replaced (another client connected with this session).")
			// whatsmeow emits StreamReplaced before onDisconnect necessarily
			// clears the socket, so force-close before reconnecting.
			a.wa.Close()
			select {
			case disconnected <- struct{}{}:
			default:
			}
		case *events.LoggedOut:
			fmt.Fprintf(os.Stderr, "\nLogged out by server (%s); the session was removed. Run `wacli auth` to re-link.\n", v.Reason)
			reportFatal(fmt.Errorf("logged out by server: %s", v.Reason))
		case *events.TemporaryBan:
			fmt.Fprintf(os.Stderr, "\n%s\n", v.String())
			reportFatal(fmt.Errorf("temporarily banned: %s", v.Code))
		case *events.ClientOutdated:
			fmt.Fprintln(os.Stderr, "\nWhatsApp rejected the connection: client outdated. Update wacli (whatsmeow).")
			reportFatal(fmt.Errorf("client outdated; update wacli"))
		case *events.ConnectFailure:
			fmt.Fprintf(os.Stderr, "\nConnection failure: %s (%s)\n", v.Reason, v.Message)
			reportFatal(fmt.Errorf("connection failure: %s", v.Reason))
		case *events.StreamError:
			fmt.Fprintf(os.Stderr, "\nStream error (code %s).\n", v.Code)
		}
	})
	defer a.wa.RemoveEventHandler(handlerID)

	if err := a.Connect(ctx, opts.AllowQR, opts.OnQRCode); err != nil {
		return SyncResult{}, err
	}
	connectionEpoch.Store(time.Now().UTC().UnixNano())

	if opts.DownloadMedia {
		var err error
		stopMedia, err = a.runMediaWorkers(ctx, mediaJobs, 4)
		if err != nil {
			return SyncResult{}, err
		}
		defer stopMedia()
	}

	// Optional: bootstrap imports (helps contacts/groups management without waiting for events).
	if opts.RefreshContacts {
		_ = a.refreshContacts(ctx)
	}
	if opts.RefreshGroups {
		_ = a.refreshGroups(ctx)
	}
	if opts.AfterConnect != nil {
		if err := opts.AfterConnect(ctx); err != nil {
			return SyncResult{MessagesStored: messagesStored.Load()}, err
		}
	}

	if opts.Mode == SyncModeFollow {
		for {
			select {
			case <-ctx.Done():
				fmt.Fprintln(os.Stderr, "\nStopping sync.")
				return SyncResult{MessagesStored: messagesStored.Load()}, nil
			case err := <-fatal:
				return SyncResult{MessagesStored: messagesStored.Load()}, err
			case req := <-staleReconnect:
				// Ignore stale reports from a previous connection.
				if epoch := connectionEpoch.Load(); epoch > 0 && req.lastSuccess.Before(time.Unix(0, epoch)) {
					continue
				}
				fmt.Fprintf(os.Stderr, "\nKeepalive has been failing for %s (threshold %s, %d errors), reconnecting...\n", req.idle, req.threshold, req.errorCount)
				// Force-close so the reconnect starts from a clean socket.
				a.wa.Close()
				connectionEpoch.Store(time.Now().UTC().UnixNano())
				if err := a.wa.ReconnectWithBackoff(ctx, 2*time.Second, 30*time.Second); err != nil {
					return SyncResult{MessagesStored: messagesStored.Load()}, err
				}
			case <-disconnected:
				fmt.Fprintln(os.Stderr, "Reconnecting...")
				connectionEpoch.Store(time.Now().UTC().UnixNano())
				if err := a.wa.ReconnectWithBackoff(ctx, 2*time.Second, 30*time.Second); err != nil {
					return SyncResult{MessagesStored: messagesStored.Load()}, err
				}
			}
		}
	}

	// Bootstrap/once: exit after idle.
	poll := 250 * time.Millisecond
	if opts.IdleExit >= 2*time.Second {
		poll = 1 * time.Second
	}
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			fmt.Fprintln(os.Stderr, "\nStopping sync.")
			return SyncResult{MessagesStored: messagesStored.Load()}, nil
		case err := <-fatal:
			return SyncResult{MessagesStored: messagesStored.Load()}, err
		case <-disconnected:
			fmt.Fprintln(os.Stderr, "Reconnecting...")
			connectionEpoch.Store(time.Now().UTC().UnixNano())
			if err := a.wa.ReconnectWithBackoff(ctx, 2*time.Second, 30*time.Second); err != nil {
				return SyncResult{MessagesStored: messagesStored.Load()}, err
			}
		case <-ticker.C:
			last := time.Unix(0, lastEvent.Load())
			if time.Since(last) >= opts.IdleExit {
				fmt.Fprintf(os.Stderr, "\nIdle for %s, exiting.\n", opts.IdleExit)
				return SyncResult{MessagesStored: messagesStored.Load()}, nil
			}
		}
	}
}

func chatKind(chat types.JID) string {
	if chat.Server == types.GroupServer {
		return "group"
	}
	if chat.IsBroadcastList() {
		return "broadcast"
	}
	if chat.Server == types.DefaultUserServer || chat.Server == types.HiddenUserServer {
		return "dm"
	}
	return "unknown"
}

func (a *App) storeParsedMessage(ctx context.Context, pm wa.ParsedMessage) error {
	chatJID := pm.Chat.String()
	chatName := a.wa.ResolveChatName(ctx, pm.Chat, pm.PushName)
	if err := a.db.UpsertChat(chatJID, chatKind(pm.Chat), chatName, pm.Timestamp); err != nil {
		return err
	}

	// Best-effort: store contact info for DMs.
	if pm.Chat.Server == types.DefaultUserServer {
		if info, err := a.wa.GetContact(ctx, pm.Chat.ToNonAD()); err == nil {
			_ = a.db.UpsertContact(
				pm.Chat.String(),
				pm.Chat.User,
				info.PushName,
				info.FullName,
				info.FirstName,
				info.BusinessName,
			)
		}
	}

	senderName := ""
	if pm.FromMe {
		senderName = "me"
	} else if s := strings.TrimSpace(pm.PushName); s != "" && s != "-" {
		senderName = s
	}
	if pm.SenderJID != "" {
		if jid, err := types.ParseJID(pm.SenderJID); err == nil {
			if info, err := a.wa.GetContact(ctx, jid.ToNonAD()); err == nil {
				if name := wa.BestContactName(info); name != "" {
					senderName = name
				}
				_ = a.db.UpsertContact(
					jid.String(),
					jid.User,
					info.PushName,
					info.FullName,
					info.FirstName,
					info.BusinessName,
				)
			}
		}
	}

	// Best-effort: store group metadata (and participants) when available.
	if pm.Chat.Server == types.GroupServer {
		if gi, err := a.wa.GetGroupInfo(ctx, pm.Chat); err == nil && gi != nil {
			_ = a.db.UpsertGroup(gi.JID.String(), gi.GroupName.Name, gi.OwnerJID.String(), gi.GroupCreated)
			var ps []store.GroupParticipant
			for _, p := range gi.Participants {
				role := "member"
				if p.IsSuperAdmin {
					role = "superadmin"
				} else if p.IsAdmin {
					role = "admin"
				}
				ps = append(ps, store.GroupParticipant{
					GroupJID: pm.Chat.String(),
					UserJID:  p.JID.String(),
					Role:     role,
				})
			}
			_ = a.db.ReplaceGroupParticipants(pm.Chat.String(), ps)
		}
	}

	var mediaType, caption, filename, mimeType, directPath string
	var mediaKey, fileSha, fileEncSha []byte
	var fileLen uint64
	if pm.Media != nil {
		mediaType = pm.Media.Type
		caption = pm.Media.Caption
		filename = pm.Media.Filename
		mimeType = pm.Media.MimeType
		directPath = pm.Media.DirectPath
		mediaKey = pm.Media.MediaKey
		fileSha = pm.Media.FileSHA256
		fileEncSha = pm.Media.FileEncSHA256
		fileLen = pm.Media.FileLength
	}

	displayText := a.buildDisplayText(ctx, pm)

	return a.db.UpsertMessage(store.UpsertMessageParams{
		ChatJID:       chatJID,
		ChatName:      chatName,
		MsgID:         pm.ID,
		SenderJID:     pm.SenderJID,
		SenderName:    senderName,
		Timestamp:     pm.Timestamp,
		FromMe:        pm.FromMe,
		Text:          pm.Text,
		DisplayText:   displayText,
		MediaType:     mediaType,
		MediaCaption:  caption,
		Filename:      filename,
		MimeType:      mimeType,
		DirectPath:    directPath,
		MediaKey:      mediaKey,
		FileSHA256:    fileSha,
		FileEncSHA256: fileEncSha,
		FileLength:    fileLen,
	})
}

func (a *App) buildDisplayText(ctx context.Context, pm wa.ParsedMessage) string {
	base := baseDisplayText(pm)

	if pm.ReactionToID != "" || strings.TrimSpace(pm.ReactionEmoji) != "" {
		target := strings.TrimSpace(pm.ReactionToID)
		display := ""
		if target != "" {
			display = a.lookupMessageDisplayText(pm.Chat.String(), target)
		}
		if display == "" {
			display = "message"
		}
		emoji := strings.TrimSpace(pm.ReactionEmoji)
		if emoji != "" {
			return fmt.Sprintf("Reacted %s to %s", emoji, display)
		}
		return fmt.Sprintf("Reacted to %s", display)
	}

	if pm.ReplyToID != "" {
		quoted := strings.TrimSpace(pm.ReplyToDisplay)
		if quoted == "" {
			quoted = a.lookupMessageDisplayText(pm.Chat.String(), pm.ReplyToID)
		}
		if quoted == "" {
			quoted = "message"
		}
		if base == "" {
			base = "(message)"
		}
		return fmt.Sprintf("> %s\n%s", quoted, base)
	}

	if base == "" {
		base = "(message)"
	}
	return base
}

func baseDisplayText(pm wa.ParsedMessage) string {
	if pm.Media != nil {
		return "Sent " + mediaLabel(pm.Media.Type)
	}
	if text := strings.TrimSpace(pm.Text); text != "" {
		return text
	}
	return ""
}

func (a *App) lookupMessageDisplayText(chatJID, msgID string) string {
	if strings.TrimSpace(chatJID) == "" || strings.TrimSpace(msgID) == "" {
		return ""
	}
	msg, err := a.db.GetMessage(chatJID, msgID)
	if err != nil {
		return ""
	}
	if text := strings.TrimSpace(msg.DisplayText); text != "" {
		return text
	}
	if text := strings.TrimSpace(msg.Text); text != "" {
		return text
	}
	if strings.TrimSpace(msg.MediaType) != "" {
		return "Sent " + mediaLabel(msg.MediaType)
	}
	return ""
}

func mediaLabel(mediaType string) string {
	mt := strings.ToLower(strings.TrimSpace(mediaType))
	switch mt {
	case "gif":
		return "gif"
	case "image":
		return "image"
	case "video":
		return "video"
	case "audio":
		return "audio"
	case "sticker":
		return "sticker"
	case "document":
		return "document"
	case "location":
		return "location"
	case "contact":
		return "contact"
	case "contacts":
		return "contacts"
	case "":
		return "message"
	default:
		return mt
	}
}
