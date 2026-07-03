package store

import (
	"testing"
	"time"
)

func TestListQueriesEscapeLIKEWildcards(t *testing.T) {
	db := openTestDB(t)
	when := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)

	if err := db.UpsertChat("literal_percent@s.whatsapp.net", "dm", "100% literal", when); err != nil {
		t.Fatalf("UpsertChat literal: %v", err)
	}
	if err := db.UpsertChat("wildcard@s.whatsapp.net", "dm", "100X wildcard", when); err != nil {
		t.Fatalf("UpsertChat wildcard: %v", err)
	}
	chats, err := db.ListChats("100%", 10)
	if err != nil {
		t.Fatalf("ListChats: %v", err)
	}
	if len(chats) != 1 || chats[0].JID != "literal_percent@s.whatsapp.net" {
		t.Fatalf("ListChats wildcard leak: %+v", chats)
	}

	if err := db.UpsertContact("literal_under@s.whatsapp.net", "", "Ann_", "", "", ""); err != nil {
		t.Fatalf("UpsertContact literal: %v", err)
	}
	if err := db.UpsertContact("wildcard_under@s.whatsapp.net", "", "AnnX", "", "", ""); err != nil {
		t.Fatalf("UpsertContact wildcard: %v", err)
	}
	contacts, err := db.SearchContacts("Ann_", 10)
	if err != nil {
		t.Fatalf("SearchContacts: %v", err)
	}
	if len(contacts) != 1 || contacts[0].JID != "literal_under@s.whatsapp.net" {
		t.Fatalf("SearchContacts wildcard leak: %+v", contacts)
	}

	if err := db.UpsertGroup("literal_group@g.us", "team_1", "", when); err != nil {
		t.Fatalf("UpsertGroup literal: %v", err)
	}
	if err := db.UpsertGroup("wildcard_group@g.us", "teamA1", "", when); err != nil {
		t.Fatalf("UpsertGroup wildcard: %v", err)
	}
	groups, err := db.ListGroups("team_1", 10)
	if err != nil {
		t.Fatalf("ListGroups: %v", err)
	}
	if len(groups) != 1 || groups[0].JID != "literal_group@g.us" {
		t.Fatalf("ListGroups wildcard leak: %+v", groups)
	}
}
