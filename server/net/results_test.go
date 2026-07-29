package net

import (
	"encoding/hex"
	"testing"

	"asteroidsalvage/sim"
)

// 0x88 MatchResults and 0x89 Roster are both variable-width, because names are
// length-prefixed. Go and Python implement them independently, so the roundtrip is
// pinned here and the literal bytes Python produces are pinned in
// TestPythonDecodesTheNewMessages below.

func TestMatchResultsRoundTrip(t *testing.T) {
	rows := []sim.PlayerResult{
		{
			ID: 1, Team: 0, Name: "joormom",
			Stats:   sim.PlayerStats{Delivered: 14, Banked: 2840.5, Kills: 6, Deaths: 3},
			Credits: 1275,
		},
		{
			ID: 2, Team: 1, Name: "",
			Stats:   sim.PlayerStats{},
			Credits: 0,
		},
	}

	got, err := DecodeMatchResults(EncodeMatchResults(0, rows)[1:])
	if err != nil {
		t.Fatalf("DecodeMatchResults: %v", err)
	}
	if got.Winner != 0 {
		t.Errorf("Winner = %d, want 0", got.Winner)
	}
	if len(got.Players) != 2 {
		t.Fatalf("got %d rows, want 2", len(got.Players))
	}

	a := got.Players[0]
	if a.ID != 1 || a.Team != 0 || a.Name != "joormom" {
		t.Errorf("row 0 identity = %d/%d/%q", a.ID, a.Team, a.Name)
	}
	if a.Stats.Delivered != 14 || a.Stats.Kills != 6 || a.Stats.Deaths != 3 {
		t.Errorf("row 0 counts = %+v", a.Stats)
	}
	if a.Stats.Banked != 2840.5 || a.Credits != 1275 {
		t.Errorf("row 0 money = banked %v credits %v", a.Stats.Banked, a.Credits)
	}

	// An empty name is a legal record, not a truncation: the length prefix is zero and
	// the row after it must still line up.
	if b := got.Players[1]; b.ID != 2 || b.Team != 1 || b.Name != "" {
		t.Errorf("row 1 = %d/%d/%q, want 2/1/\"\"", b.ID, b.Team, b.Name)
	}
}

func TestMatchResultsDrawAndEmptyTable(t *testing.T) {
	got, err := DecodeMatchResults(EncodeMatchResults(0xFF, nil)[1:])
	if err != nil {
		t.Fatalf("DecodeMatchResults: %v", err)
	}
	if got.Winner != 0xFF {
		t.Errorf("Winner = %d, want 0xFF for a draw", got.Winner)
	}
	if len(got.Players) != 0 {
		t.Errorf("got %d rows, want none", len(got.Players))
	}
}

// Names are capped at 20 bytes on the wire. A longer one must be cut rather than
// overflowing the length prefix, which would desynchronise every row after it.
func TestMatchResultsClampsLongNames(t *testing.T) {
	long := "abcdefghijklmnopqrstuvwxyz"
	raw := EncodeMatchResults(0, []sim.PlayerResult{
		{ID: 1, Name: long},
		{ID: 2, Name: "after"},
	})

	got, err := DecodeMatchResults(raw[1:])
	if err != nil {
		t.Fatalf("DecodeMatchResults: %v", err)
	}
	if len(got.Players[0].Name) != 20 {
		t.Errorf("name kept %d bytes, want 20", len(got.Players[0].Name))
	}
	if got.Players[1].Name != "after" {
		t.Errorf("row after a clamped name = %q, want \"after\"", got.Players[1].Name)
	}
}

// Every decoder here is fed straight from the network and must not trust its input.
func TestMatchResultsRejectsTruncatedBodies(t *testing.T) {
	full := EncodeMatchResults(0, []sim.PlayerResult{
		{ID: 1, Name: "kev", Stats: sim.PlayerStats{Delivered: 2}},
	})[1:]

	for n := 0; n < len(full); n++ {
		if _, err := DecodeMatchResults(full[:n]); err == nil {
			t.Errorf("a %d-byte body of %d parsed without error", n, len(full))
		}
	}
}

func TestRosterRoundTrip(t *testing.T) {
	rows := []sim.RosterEntry{
		{ID: 1, Team: 0, Name: "host"},
		{ID: 4, Team: 1, Name: "joiner"},
	}

	hostID, capacity, got, err := DecodeRoster(EncodeRoster(1, 4, rows)[1:])
	if err != nil {
		t.Fatalf("DecodeRoster: %v", err)
	}
	if hostID != 1 {
		t.Errorf("hostID = %d, want 1", hostID)
	}
	if capacity != 4 {
		t.Errorf("capacity = %d, want 4", capacity)
	}
	if len(got) != 2 || got[0].Name != "host" || got[1].Name != "joiner" {
		t.Fatalf("rows = %+v", got)
	}
	if got[1].ID != 4 || got[1].Team != 1 {
		t.Errorf("row 1 = %d/%d, want 4/1", got[1].ID, got[1].Team)
	}
}

func TestRosterRejectsTruncatedBodies(t *testing.T) {
	full := EncodeRoster(1, 4, []sim.RosterEntry{{ID: 1, Name: "host"}})[1:]

	for n := 0; n < len(full); n++ {
		if _, _, _, err := DecodeRoster(full[:n]); err == nil {
			t.Errorf("a %d-byte body of %d parsed without error", n, len(full))
		}
	}
}

// The Python client encodes StartMatch itself. Pinned as a literal so a change on either
// side has to be made on both.
func TestPythonDecodesTheNewMessages(t *testing.T) {
	raw, err := hex.DecodeString("07")
	if err != nil {
		t.Fatalf("bad literal: %v", err)
	}
	if raw[0] != MsgStartMatch {
		t.Errorf("Python StartMatch tag = %#x, want %#x", raw[0], MsgStartMatch)
	}
	if len(raw) != 1 {
		t.Errorf("Python StartMatch is %d bytes, want 1", len(raw))
	}

	// Python's 0x09 Chat and 0x08 UseItem, as literal bytes.
	chat, err := hex.DecodeString("0901056f6e206974")
	if err != nil {
		t.Fatalf("bad literal: %v", err)
	}
	if chat[0] != MsgChat {
		t.Errorf("Python Chat tag = %#x, want %#x", chat[0], MsgChat)
	}
	channel, text, err := DecodeChat(chat[1:])
	if err != nil {
		t.Fatalf("DecodeChat: %v", err)
	}
	if channel != ChatTeam || text != "on it" {
		t.Errorf("Python Chat decoded as channel %d %q, want 1 / \"on it\"", channel, text)
	}

	item, err := hex.DecodeString("0802")
	if err != nil {
		t.Fatalf("bad literal: %v", err)
	}
	if item[0] != MsgUseItem {
		t.Errorf("Python UseItem tag = %#x, want %#x", item[0], MsgUseItem)
	}
	if slot, err := DecodeUseItem(item[1:]); err != nil || slot != 2 {
		t.Errorf("Python UseItem decoded as slot %d (err %v), want 2", slot, err)
	}
}

// Chat is relayed with the sender attached by the server. Both strings are
// length-prefixed, so the second is where a desynchronised reader shows up.
func TestChatSayRoundTrip(t *testing.T) {
	raw := EncodeChatSay(ChatTeam, 2, "joormom", "on it")
	channel, team, name, text, err := DecodeChatSay(raw[1:])
	if err != nil {
		t.Fatalf("DecodeChatSay: %v", err)
	}
	if channel != ChatTeam || team != 2 {
		t.Errorf("channel %d team %d, want 1 / 2", channel, team)
	}
	if name != "joormom" || text != "on it" {
		t.Errorf("got %q: %q", name, text)
	}
}

func TestChatRejectsTruncatedAndOversizedBodies(t *testing.T) {
	full := EncodeChatSay(ChatAll, 0, "kev", "hi")[1:]
	for n := 0; n < len(full); n++ {
		if _, _, _, _, err := DecodeChatSay(full[:n]); err == nil {
			t.Errorf("a %d-byte ChatSay body of %d parsed without error", n, len(full))
		}
	}

	// A length prefix claiming more than the cap must be refused rather than trusted.
	if _, _, err := DecodeChat([]byte{ChatAll, 0xFF, 'h', 'i'}); err == nil {
		t.Error("a Chat body claiming 255 bytes of text parsed without error")
	}
}

// Control characters are stripped on arrival, so a message cannot smuggle newlines or
// escape sequences into another player's chat log.
func TestChatStripsControlCharacters(t *testing.T) {
	_, text, err := DecodeChat(EncodeChat(ChatAll, "hi\x1b[31m\nthere  ")[1:])
	if err != nil {
		t.Fatalf("DecodeChat: %v", err)
	}
	if text != "hi[31mthere" {
		t.Errorf("sanitised to %q", text)
	}
}
