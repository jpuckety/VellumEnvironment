package imap

import (
	"fmt"
	"sort"
	"strings"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/jpuckett/EmailMCP/emailmcp/internal/types"
)

// SearchSortKey is a client-facing sort field for search_emails.
// Values match IMAP SORT keys (RFC 5256), lowercased.
type SearchSortKey string

const (
	SortKeyArrival SearchSortKey = "arrival" // internal date
	SortKeyDate    SearchSortKey = "date"    // Date header
	SortKeyFrom    SearchSortKey = "from"
	SortKeyTo      SearchSortKey = "to"
	SortKeyCc      SearchSortKey = "cc"
	SortKeySubject SearchSortKey = "subject"
	SortKeySize    SearchSortKey = "size"
)

// SupportedSearchSortKeys lists all sort keys accepted by search_emails.
var SupportedSearchSortKeys = []SearchSortKey{
	SortKeyArrival,
	SortKeyDate,
	SortKeyFrom,
	SortKeyTo,
	SortKeyCc,
	SortKeySubject,
	SortKeySize,
}

// SearchSort controls ordering of search results.
type SearchSort struct {
	// Key is one of SupportedSearchSortKeys. Empty defaults to arrival.
	Key SearchSortKey
	// Reverse overrides the default reverse behavior when non-nil.
	// Defaults: true for arrival/date/size (newest/largest first); false for string keys.
	Reverse *bool
}

// ParseSearchSortKey validates and normalizes a sort key string.
// Empty string means the default key (arrival).
func ParseSearchSortKey(s string) (SearchSortKey, error) {
	switch SearchSortKey(strings.ToLower(strings.TrimSpace(s))) {
	case "", SortKeyArrival:
		return SortKeyArrival, nil
	case SortKeyDate:
		return SortKeyDate, nil
	case SortKeyFrom:
		return SortKeyFrom, nil
	case SortKeyTo:
		return SortKeyTo, nil
	case SortKeyCc:
		return SortKeyCc, nil
	case SortKeySubject:
		return SortKeySubject, nil
	case SortKeySize:
		return SortKeySize, nil
	default:
		return "", fmt.Errorf("unsupported sort_by %q; supported: arrival, date, from, to, cc, subject, size", s)
	}
}

// Resolve returns the effective sort key and reverse flag.
func (s SearchSort) Resolve() (SearchSortKey, bool) {
	key := s.Key
	if key == "" {
		key = SortKeyArrival
	}
	reverse := defaultSortReverse(key)
	if s.Reverse != nil {
		reverse = *s.Reverse
	}
	return key, reverse
}

func defaultSortReverse(key SearchSortKey) bool {
	switch key {
	case SortKeyArrival, SortKeyDate, SortKeySize:
		return true
	default:
		return false
	}
}

func toIMAPSortKey(key SearchSortKey) imapclient.SortKey {
	switch key {
	case SortKeyDate:
		return imapclient.SortKeyDate
	case SortKeyFrom:
		return imapclient.SortKeyFrom
	case SortKeyTo:
		return imapclient.SortKeyTo
	case SortKeyCc:
		return imapclient.SortKeyCc
	case SortKeySubject:
		return imapclient.SortKeySubject
	case SortKeySize:
		return imapclient.SortKeySize
	default:
		return imapclient.SortKeyArrival
	}
}

func reverseUIDs(uids []imap.UID) {
	for i, j := 0, len(uids)-1; i < j; i, j = i+1, j-1 {
		uids[i], uids[j] = uids[j], uids[i]
	}
}

// orderSummariesByUIDs reorders summaries to match the given UID order.
// UIDs with no matching summary are skipped; unmatched summaries are dropped.
func orderSummariesByUIDs(summaries []types.EmailSummary, uids []imap.UID) []types.EmailSummary {
	byUID := make(map[uint32]types.EmailSummary, len(summaries))
	for _, s := range summaries {
		byUID[s.UID] = s
	}
	out := make([]types.EmailSummary, 0, len(uids))
	for _, u := range uids {
		if s, ok := byUID[uint32(u)]; ok {
			out = append(out, s)
		}
	}
	return out
}

func firstAddress(addrs []types.Address) string {
	if len(addrs) == 0 {
		return ""
	}
	if addrs[0].Address != "" {
		return strings.ToLower(addrs[0].Address)
	}
	return strings.ToLower(addrs[0].Name)
}

func compareSummaries(a, b types.EmailSummary, key SearchSortKey) int {
	switch key {
	case SortKeyDate:
		if a.Date.Before(b.Date) {
			return -1
		}
		if a.Date.After(b.Date) {
			return 1
		}
		return 0
	case SortKeyFrom:
		return strings.Compare(firstAddress(a.From), firstAddress(b.From))
	case SortKeyTo:
		return strings.Compare(firstAddress(a.To), firstAddress(b.To))
	case SortKeyCc:
		return strings.Compare(firstAddress(a.Cc), firstAddress(b.Cc))
	case SortKeySubject:
		return strings.Compare(strings.ToLower(a.Subject), strings.ToLower(b.Subject))
	case SortKeySize:
		switch {
		case a.Size < b.Size:
			return -1
		case a.Size > b.Size:
			return 1
		default:
			return 0
		}
	default: // arrival — approximate with UID when server SORT is unavailable
		switch {
		case a.UID < b.UID:
			return -1
		case a.UID > b.UID:
			return 1
		default:
			return 0
		}
	}
}

func sortSummaries(summaries []types.EmailSummary, key SearchSortKey, reverse bool) {
	sort.SliceStable(summaries, func(i, j int) bool {
		cmp := compareSummaries(summaries[i], summaries[j], key)
		if reverse {
			return cmp > 0
		}
		return cmp < 0
	})
}
