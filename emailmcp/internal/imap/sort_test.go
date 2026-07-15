package imap

import (
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"

	"github.com/jpuckett/EmailMCP/emailmcp/internal/types"
)

func TestParseSearchSortKey(t *testing.T) {
	tests := []struct {
		in      string
		want    SearchSortKey
		wantErr bool
	}{
		{"", SortKeyArrival, false},
		{"arrival", SortKeyArrival, false},
		{"ARRIVAL", SortKeyArrival, false},
		{" date ", SortKeyDate, false},
		{"from", SortKeyFrom, false},
		{"to", SortKeyTo, false},
		{"cc", SortKeyCc, false},
		{"subject", SortKeySubject, false},
		{"size", SortKeySize, false},
		{"bogus", "", true},
	}
	for _, tt := range tests {
		got, err := ParseSearchSortKey(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Errorf("ParseSearchSortKey(%q) expected error", tt.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseSearchSortKey(%q) unexpected error: %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("ParseSearchSortKey(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestSearchSortResolveDefaults(t *testing.T) {
	key, rev := (SearchSort{}).Resolve()
	if key != SortKeyArrival || !rev {
		t.Fatalf("default Resolve() = (%q, %v), want (arrival, true)", key, rev)
	}

	key, rev = (SearchSort{Key: SortKeySubject}).Resolve()
	if key != SortKeySubject || rev {
		t.Fatalf("subject default reverse = (%q, %v), want (subject, false)", key, rev)
	}

	falseVal := false
	key, rev = (SearchSort{Key: SortKeyArrival, Reverse: &falseVal}).Resolve()
	if key != SortKeyArrival || rev {
		t.Fatalf("arrival with reverse=false = (%q, %v), want (arrival, false)", key, rev)
	}

	trueVal := true
	key, rev = (SearchSort{Key: SortKeyFrom, Reverse: &trueVal}).Resolve()
	if key != SortKeyFrom || !rev {
		t.Fatalf("from with reverse=true = (%q, %v), want (from, true)", key, rev)
	}
}

func TestOrderSummariesByUIDs(t *testing.T) {
	summaries := []types.EmailSummary{
		{UID: 3, Subject: "c"},
		{UID: 1, Subject: "a"},
		{UID: 2, Subject: "b"},
	}
	ordered := orderSummariesByUIDs(summaries, []imap.UID{2, 3, 1})
	if len(ordered) != 3 {
		t.Fatalf("len = %d, want 3", len(ordered))
	}
	if ordered[0].UID != 2 || ordered[1].UID != 3 || ordered[2].UID != 1 {
		t.Fatalf("order = %v, %v, %v", ordered[0].UID, ordered[1].UID, ordered[2].UID)
	}
}

func TestSortSummariesByDateReverse(t *testing.T) {
	older := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	summaries := []types.EmailSummary{
		{UID: 1, Date: older, Subject: "old"},
		{UID: 2, Date: newer, Subject: "new"},
	}
	sortSummaries(summaries, SortKeyDate, true)
	if summaries[0].UID != 2 {
		t.Fatalf("expected newest first, got UID %d", summaries[0].UID)
	}
}

func TestSortSummariesBySubject(t *testing.T) {
	summaries := []types.EmailSummary{
		{UID: 1, Subject: "Bravo"},
		{UID: 2, Subject: "alpha"},
	}
	sortSummaries(summaries, SortKeySubject, false)
	if summaries[0].Subject != "alpha" {
		t.Fatalf("expected alpha first, got %q", summaries[0].Subject)
	}
}

func TestReverseUIDs(t *testing.T) {
	uids := []imap.UID{1, 2, 3, 4}
	reverseUIDs(uids)
	if uids[0] != 4 || uids[3] != 1 {
		t.Fatalf("reverseUIDs = %v", uids)
	}
}

func TestToIMAPSortKeyCoversAll(t *testing.T) {
	for _, key := range SupportedSearchSortKeys {
		if got := toIMAPSortKey(key); got == "" {
			t.Errorf("toIMAPSortKey(%q) empty", key)
		}
	}
}
