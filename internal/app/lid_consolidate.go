package app

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"

	_ "github.com/mattn/go-sqlite3"
	"go.mau.fi/whatsmeow/types"
)

type ConsolidateLIDOptions struct {
	DryRun bool
	Limit  int
}

type ConsolidateIdentityPair struct {
	FromJID       string `json:"from_jid"`
	ToJID         string `json:"to_jid"`
	MessagesMoved int64  `json:"messages_moved"`
	Merged        bool   `json:"merged"`
	SkippedReason string `json:"skipped_reason,omitempty"`
}

type ConsolidateLIDResult struct {
	MappingsFound   int
	MappingsTried   int
	ChatsMerged     int
	MessagesMoved   int64
	Pairs           []string
	Details         []ConsolidateIdentityPair
	SkippedInvalid  int
	SkippedUnmapped int
}

// ResolveChatJIDCandidates returns preferred + fallback JIDs for the same user,
// based on whatsmeow_lid_map. The first candidate is preferred.
func (a *App) ResolveChatJIDCandidates(ctx context.Context, input string) ([]string, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, nil
	}

	parsed, err := types.ParseJID(input)
	if err != nil {
		return []string{input}, nil
	}

	sessionPath := filepath.Join(a.opts.StoreDir, "session.db")
	sdb, err := sql.Open("sqlite3", sessionPath)
	if err != nil {
		return []string{parsed.String()}, nil
	}
	defer sdb.Close()

	seen := map[string]bool{}
	add := func(arr []string, jid string) []string {
		jid = strings.TrimSpace(jid)
		if jid == "" || seen[jid] {
			return arr
		}
		seen[jid] = true
		return append(arr, jid)
	}

	var out []string
	out = add(out, parsed.String())

	switch parsed.Server {
	case types.HiddenUserServer: // @lid -> prefer mapped phone JID
		var pn string
		err := sdb.QueryRowContext(ctx, `SELECT pn FROM whatsmeow_lid_map WHERE lid = ?`, parsed.User).Scan(&pn)
		if err == nil && strings.TrimSpace(pn) != "" {
			pnJID := types.NewJID(strings.TrimSpace(pn), types.DefaultUserServer).ToNonAD().String()
			out = add(nil, pnJID)
			out = add(out, parsed.String())
		}
	case types.DefaultUserServer: // @s.whatsapp.net -> fallback to mapped @lid if needed
		var lid string
		err := sdb.QueryRowContext(ctx, `SELECT lid FROM whatsmeow_lid_map WHERE pn = ?`, parsed.User).Scan(&lid)
		if err == nil && strings.TrimSpace(lid) != "" {
			lidJID := types.NewJID(strings.TrimSpace(lid), types.HiddenUserServer).String()
			out = add(out, lidJID)
		}
	}

	if len(out) == 0 {
		out = []string{parsed.String()}
	}
	return out, nil
}

// ConsolidateLIDChats merges historical @lid chat identities into their mapped
// phone-number JIDs from session.db/whatsmeow_lid_map without dropping messages.
func (a *App) ConsolidateLIDChats(ctx context.Context, opts ConsolidateLIDOptions) (ConsolidateLIDResult, error) {
	sessionPath := filepath.Join(a.opts.StoreDir, "session.db")
	sdb, err := sql.Open("sqlite3", sessionPath)
	if err != nil {
		return ConsolidateLIDResult{}, fmt.Errorf("open session db: %w", err)
	}
	defer sdb.Close()

	rows, err := sdb.QueryContext(ctx, `SELECT lid, pn FROM whatsmeow_lid_map`)
	if err != nil {
		return ConsolidateLIDResult{}, fmt.Errorf("query whatsmeow_lid_map: %w", err)
	}
	defer rows.Close()

	result := ConsolidateLIDResult{}
	seen := map[string]bool{}

	for rows.Next() {
		var lidUser, pnUser string
		if err := rows.Scan(&lidUser, &pnUser); err != nil {
			return result, err
		}
		result.MappingsFound++

		lidUser = strings.TrimSpace(lidUser)
		pnUser = strings.TrimSpace(pnUser)
		if lidUser == "" || pnUser == "" {
			result.SkippedInvalid++
			continue
		}

		from := types.NewJID(lidUser, types.HiddenUserServer).String()
		to := types.NewJID(pnUser, types.DefaultUserServer).ToNonAD().String()
		key := from + "=>" + to
		if seen[key] {
			continue
		}
		seen[key] = true

		result.MappingsTried++
		if opts.Limit > 0 && result.MappingsTried > opts.Limit {
			break
		}

		pairResult := ConsolidateIdentityPair{
			FromJID: from,
			ToJID:   to,
		}

		if opts.DryRun {
			pairResult.SkippedReason = "dry-run"
			result.Details = append(result.Details, pairResult)
			result.Pairs = append(result.Pairs, key)
			continue
		}

		moved, err := a.db.MergeChatIdentity(from, to)
		if err != nil {
			return result, fmt.Errorf("merge %s -> %s: %w", from, to, err)
		}
		pairResult.MessagesMoved = moved
		if moved <= 0 {
			pairResult.SkippedReason = "no-source-chat"
			result.Details = append(result.Details, pairResult)
			result.SkippedUnmapped++
			continue
		}
		pairResult.Merged = true
		result.MessagesMoved += moved
		result.ChatsMerged++
		result.Details = append(result.Details, pairResult)
		result.Pairs = append(result.Pairs, key)
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	return result, nil
}
