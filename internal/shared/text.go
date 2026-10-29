package shared

import (
	"net/url"
	"strings"
)

// SliceUTF16 is s.slice(0, n) measured in UTF-16 code units. A cut inside a surrogate pair drops
// the whole character (JS would leave a lone surrogate, which Go strings cannot hold).
func SliceUTF16(s string, n int) string {
	units := 0
	for i, r := range s {
		w := 1
		if r >= 0x10000 {
			w = 2
		}
		if units+w > n {
			return s[:i]
		}
		units += w
	}
	return s
}

// URIComponent is JS encodeURIComponent: query-escape, then undo the two differences (space is
// %20 not +, and !'()* stay literal).
func URIComponent(s string) string {
	return uriReplacer.Replace(url.QueryEscape(s))
}

var uriReplacer = strings.NewReplacer("+", "%20", "%21", "!", "%27", "'", "%28", "(", "%29", ")", "%2A", "*")
