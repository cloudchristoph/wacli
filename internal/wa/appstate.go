package wa

import (
	"context"
	"fmt"
	"time"

	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/types"
)

func (c *Client) SendAppState(ctx context.Context, patch appstate.PatchInfo) error {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || !cli.IsConnected() {
		return fmt.Errorf("not connected")
	}
	return cli.SendAppState(ctx, patch)
}

func (c *Client) ArchiveChat(ctx context.Context, target types.JID, archive bool, lastMsgTS time.Time, lastMsgKey *waCommon.MessageKey) error {
	return c.SendAppState(ctx, appstate.BuildArchive(target, archive, lastMsgTS, lastMsgKey))
}

func (c *Client) PinChat(ctx context.Context, target types.JID, pin bool) error {
	return c.SendAppState(ctx, appstate.BuildPin(target, pin))
}

func (c *Client) MuteChat(ctx context.Context, target types.JID, mute bool, duration time.Duration) error {
	return c.SendAppState(ctx, appstate.BuildMute(target, mute, duration))
}

func (c *Client) MarkChatAsRead(ctx context.Context, target types.JID, read bool, lastMsgTS time.Time, lastMsgKey *waCommon.MessageKey) error {
	return c.SendAppState(ctx, appstate.BuildMarkChatAsRead(target, read, lastMsgTS, lastMsgKey))
}
