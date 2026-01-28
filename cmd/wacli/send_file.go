package main

import (
	"bytes"
	"context"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/steipete/wacli/internal/app"
	"github.com/steipete/wacli/internal/store"
	"github.com/steipete/wacli/internal/wa"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// getAudioDuration returns the duration of an audio file in seconds using ffprobe
func getAudioDuration(filePath string) (uint32, error) {
	cmd := exec.Command("ffprobe", "-v", "quiet", "-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", filePath)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return 0, err
	}
	duration, err := strconv.ParseFloat(strings.TrimSpace(out.String()), 64)
	if err != nil {
		return 0, err
	}
	return uint32(duration), nil
}

// generateWaveform generates a realistic-looking 64-byte waveform for voice messages
func generateWaveform(filePath string) []byte {
	waveform := make([]byte, 64)
	
	// Seed with file path for consistent but varied results per file
	seed := int64(0)
	for _, c := range filePath {
		seed += int64(c)
	}
	seed += time.Now().UnixNano()
	
	// Simulate realistic speech pattern with words and pauses
	inWord := true
	wordEnergy := byte(40)
	
	for i := 0; i < 64; i++ {
		// Random chance to switch between word/pause
		randVal := (seed + int64(i*17)) % 100
		
		if randVal < 8 { // 8% chance to toggle
			inWord = !inWord
			if inWord {
				// New word - random energy level
				wordEnergy = byte(25 + (seed+int64(i*31))%45) // 25-70
			}
		}
		
		if inWord {
			// Speaking - vary around word energy
			variation := int64((seed+int64(i*23))%30) - 15 // -15 to +15
			val := int64(wordEnergy) + variation
			// Add micro-variations for naturalness
			microVar := (seed + int64(i*13)) % 10 - 5
			val += microVar
			
			if val < 15 {
				val = 15
			} else if val > 80 {
				val = 80
			}
			waveform[i] = byte(val)
		} else {
			// Pause - low but not zero (breath, ambient)
			pause := byte(5 + (seed+int64(i*7))%12) // 5-17
			waveform[i] = pause
		}
	}
	return waveform
}

func sendFile(ctx context.Context, a interface {
	WA() app.WAClient
	DB() *store.DB
}, to types.JID, filePath, filename, caption, mimeOverride string, ptt bool) (string, map[string]string, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", nil, err
	}

	name := strings.TrimSpace(filename)
	if name == "" {
		name = filepath.Base(filePath)
	}
	mimeType := strings.TrimSpace(mimeOverride)
	if mimeType == "" {
		// Use filePath for MIME detection, not the display name override
		mimeType = mime.TypeByExtension(strings.ToLower(filepath.Ext(filePath)))
	}
	if mimeType == "" {
		sniff := data
		if len(sniff) > 512 {
			sniff = sniff[:512]
		}
		mimeType = http.DetectContentType(sniff)
	}

	mediaType := "document"
	uploadType, _ := wa.MediaTypeFromString("document")
	switch {
	case strings.HasPrefix(mimeType, "image/"):
		mediaType = "image"
		uploadType, _ = wa.MediaTypeFromString("image")
	case strings.HasPrefix(mimeType, "video/"):
		mediaType = "video"
		uploadType, _ = wa.MediaTypeFromString("video")
	case strings.HasPrefix(mimeType, "audio/"):
		mediaType = "audio"
		uploadType, _ = wa.MediaTypeFromString("audio")
	}

	up, err := a.WA().Upload(ctx, data, uploadType)
	if err != nil {
		return "", nil, err
	}

	now := time.Now().UTC()
	msg := &waProto.Message{}

	switch mediaType {
	case "image":
		msg.ImageMessage = &waProto.ImageMessage{
			URL:           proto.String(up.URL),
			DirectPath:    proto.String(up.DirectPath),
			MediaKey:      up.MediaKey,
			FileEncSHA256: up.FileEncSHA256,
			FileSHA256:    up.FileSHA256,
			FileLength:    proto.Uint64(up.FileLength),
			Mimetype:      proto.String(mimeType),
			Caption:       proto.String(caption),
		}
	case "video":
		msg.VideoMessage = &waProto.VideoMessage{
			URL:           proto.String(up.URL),
			DirectPath:    proto.String(up.DirectPath),
			MediaKey:      up.MediaKey,
			FileEncSHA256: up.FileEncSHA256,
			FileSHA256:    up.FileSHA256,
			FileLength:    proto.Uint64(up.FileLength),
			Mimetype:      proto.String(mimeType),
			Caption:       proto.String(caption),
		}
	case "audio":
		audioMsg := &waProto.AudioMessage{
			URL:           proto.String(up.URL),
			DirectPath:    proto.String(up.DirectPath),
			MediaKey:      up.MediaKey,
			FileEncSHA256: up.FileEncSHA256,
			FileSHA256:    up.FileSHA256,
			FileLength:    proto.Uint64(up.FileLength),
			Mimetype:      proto.String(mimeType),
			PTT:           proto.Bool(ptt),
		}
		if ptt {
			// For PTT voice messages, force correct mimetype and add required fields
			audioMsg.Mimetype = proto.String("audio/ogg; codecs=opus")
			audioMsg.MediaKeyTimestamp = proto.Int64(now.Unix())
			// Add duration
			if duration, err := getAudioDuration(filePath); err == nil {
				audioMsg.Seconds = proto.Uint32(duration)
			}
			// Add waveform (64 bytes)
			audioMsg.Waveform = generateWaveform(filePath)
		}
		msg.AudioMessage = audioMsg
	default:
		msg.DocumentMessage = &waProto.DocumentMessage{
			URL:           proto.String(up.URL),
			DirectPath:    proto.String(up.DirectPath),
			MediaKey:      up.MediaKey,
			FileEncSHA256: up.FileEncSHA256,
			FileSHA256:    up.FileSHA256,
			FileLength:    proto.Uint64(up.FileLength),
			Mimetype:      proto.String(mimeType),
			FileName:      proto.String(name),
			Caption:       proto.String(caption),
			Title:         proto.String(name),
		}
	}

	id, err := a.WA().SendProtoMessage(ctx, to, msg)
	if err != nil {
		return "", nil, err
	}

	chatName := a.WA().ResolveChatName(ctx, to, "")
	kind := chatKindFromJID(to)
	_ = a.DB().UpsertChat(to.String(), kind, chatName, now)
	_ = a.DB().UpsertMessage(store.UpsertMessageParams{
		ChatJID:       to.String(),
		ChatName:      chatName,
		MsgID:         id,
		SenderJID:     "",
		SenderName:    "me",
		Timestamp:     now,
		FromMe:        true,
		Text:          caption,
		MediaType:     mediaType,
		MediaCaption:  caption,
		Filename:      name,
		MimeType:      mimeType,
		DirectPath:    up.DirectPath,
		MediaKey:      up.MediaKey,
		FileSHA256:    up.FileSHA256,
		FileEncSHA256: up.FileEncSHA256,
		FileLength:    up.FileLength,
	})

	return id, map[string]string{
		"name":      name,
		"mime_type": mimeType,
		"media":     mediaType,
	}, nil
}

func chatKindFromJID(j types.JID) string {
	if j.Server == types.GroupServer {
		return "group"
	}
	if j.IsBroadcastList() {
		return "broadcast"
	}
	if j.Server == types.DefaultUserServer {
		return "dm"
	}
	return "unknown"
}
