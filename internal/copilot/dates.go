package copilot

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

var (
	usDateRe  = regexp.MustCompile(`^(\d{1,2})[/-](\d{1,2})[/-](\d{4})$`)
	usShortRe = regexp.MustCompile(`^(\d{1,2})/(\d{1,2})/(\d{2})$`)
	isoDateRe = regexp.MustCompile(`^(\d{4})-(\d{1,2})-(\d{1,2})$`)
)

// looseLayouts stand in for JS `new Date(s)`'s lenient parsing (e.g. "Jun 15, 1955"). They are
// tried in the local zone, like JS does for zoneless strings.
// ponytail: the common human/ISO forms only, not V8's full legacy parser.
var looseLayouts = []string{
	"Jan 2, 2006", "January 2, 2006", "2 Jan 2006", "2 January 2006",
	"Jan 2 2006", "January 2 2006", "Mon Jan 2 2006", "Mon, 02 Jan 2006",
	time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006/01/02",
}

// ToMDY normalizes a date string to MM/DD/YYYY. Empty input yields "" (no date); an unparseable
// one — including a non-empty but whitespace-only one — is an error, as in the TS.
func ToMDY(input string) (string, error) {
	if input == "" {
		return "", nil
	}
	s := strings.TrimSpace(input)
	if m := usDateRe.FindStringSubmatch(s); m != nil {
		return pad2(m[1]) + "/" + pad2(m[2]) + "/" + m[3], nil
	}
	if m := usShortRe.FindStringSubmatch(s); m != nil {
		// V8's two-digit-year rule: 00-49 -> 20xx, 50-99 -> 19xx.
		century := "20"
		if m[3] >= "50" {
			century = "19"
		}
		return pad2(m[1]) + "/" + pad2(m[2]) + "/" + century + m[3], nil
	}
	if m := isoDateRe.FindStringSubmatch(s); m != nil {
		return pad2(m[2]) + "/" + pad2(m[3]) + "/" + m[1], nil
	}
	for _, layout := range looseLayouts {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t.In(time.Local).Format("01/02/2006"), nil
		}
	}
	return "", fmt.Errorf("unrecognized date %s", s)
}

func pad2(s string) string { return strings.Repeat("0", max(0, 2-len(s))) + s }
