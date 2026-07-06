// SPDX-Licence-Identifier: EUPL-1.2

package ws

import (
	"testing"
	"time"
)

func TestWs_stringCompare_Good(t *testing.T) {
	if got := stringCompare("a", "b"); got != -1 {
		t.Errorf("expected -1, got %d", got)
	}
	if got := stringCompare("b", "a"); got != 1 {
		t.Errorf("expected 1, got %d", got)
	}
	if got := stringCompare("a", "a"); got != 0 {
		t.Errorf("expected 0, got %d", got)
	}
}

func TestWs_stringCompare_Bad(t *testing.T) {
	// Empty strings sort before any non-empty string.
	if got := stringCompare("", "a"); got != -1 {
		t.Errorf("expected -1, got %d", got)
	}
	if got := stringCompare("a", ""); got != 1 {
		t.Errorf("expected 1, got %d", got)
	}
}

func TestWs_stringCompare_Ugly(t *testing.T) {
	// Two empty strings compare equal.
	if got := stringCompare("", ""); got != 0 {
		t.Errorf("expected 0, got %d", got)
	}
}

func TestWs_stampServerMessageIfNeeded_Good(t *testing.T) {
	// A message with no timestamp gets one stamped.
	msg := stampServerMessageIfNeeded(Message{Type: TypeEvent})
	if msg.Timestamp.IsZero() {
		t.Errorf("expected a stamped timestamp")
	}
}

func TestWs_stampServerMessageIfNeeded_Bad(t *testing.T) {
	// A message that already carries a timestamp is returned unchanged.
	fixed := time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC)
	msg := stampServerMessageIfNeeded(Message{Type: TypeEvent, Timestamp: fixed})
	if !msg.Timestamp.Equal(fixed) {
		t.Errorf("expected timestamp preserved, got %v", msg.Timestamp)
	}
}

func TestWs_stampServerMessageIfNeeded_Ugly(t *testing.T) {
	// stampServerMessage always overwrites, even when a stamp is present.
	fixed := time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC)
	msg := stampServerMessage(Message{Type: TypeEvent, Timestamp: fixed})
	if msg.Timestamp.Equal(fixed) {
		t.Errorf("expected timestamp overwritten")
	}
}

func TestWs_sortedHubClients_Good_ByUserID(t *testing.T) {
	hub := NewHub()
	hub.clients[&Client{UserID: "bravo"}] = true
	hub.clients[&Client{UserID: "alpha"}] = true
	sorted := sortedHubClients(hub)
	if len(sorted) != 2 {
		t.Fatalf("expected 2 clients, got %d", len(sorted))
	}
	if sorted[0].UserID != "alpha" || sorted[1].UserID != "bravo" {
		t.Errorf("expected alpha,bravo order, got %s,%s", sorted[0].UserID, sorted[1].UserID)
	}
}

func TestWs_sortedHubClients_Ugly_NilEntry(t *testing.T) {
	// nil client entries sort ahead of populated ones without panicking.
	hub := NewHub()
	hub.clients[&Client{UserID: "zulu"}] = true
	hub.clients[nil] = true
	sorted := sortedHubClients(hub)
	if len(sorted) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(sorted))
	}
	if sorted[0] != nil {
		t.Errorf("expected nil client first, got %v", sorted[0])
	}
}

func TestWs_stringEqualFold_Good(t *testing.T) {
	if !stringEqualFold("Origin", "origin") {
		t.Errorf("expected case-insensitive match")
	}
	if stringEqualFold("a", "b") {
		t.Errorf("expected mismatch")
	}
}
