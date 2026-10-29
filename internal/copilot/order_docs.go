package copilot

// Surface an order's CDN documents (auth-screenshot PDF + medical-authorization summary PDF) as
// clickable links on get_order.
//
// The CDN (cdn.<env>.ehrcopilotbe.com) is CloudFront signed-cookie protected, so these URLs
// return 403 (MissingKey) to any plain request — they open only in a context that holds the signed
// cookies (the Copilot web app in the user's browser). We therefore NEVER fetch or
// existence-probe the CDN here; "exists" is decided from the BE's own signals: the order's
// hasAuthScreenshot flag, and the authoritative /medicalAuthorizations?orderUid= listing.

import (
	"context"
	"net/url"
	"regexp"
	"strings"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

// OrderDocument is one CDN document link. Kind is "authScreenshot" or "authSummary".
type OrderDocument struct {
	Kind string `json:"kind"`
	URL  string `json:"url"`
}

// CDNContext is the CDN origin + account slug documents live under.
type CDNContext struct{ Origin, Account string }

var cdnURLRe = regexp.MustCompile(`^(https?://[^/]+)/([^/]+)/`)

// CDNFromOrder parses origin + account from a CDN url the BE already stamped on the order
// (authScreenshotFileUrl is present even when the file is absent):
//
//	https://cdn.prod.ehrcopilotbe.com/ossm/orders/<uid>/screenshots/authScreen.pdf
//	-> {https://cdn.prod.ehrcopilotbe.com, ossm}
func CDNFromOrder(order BeOrder) *CDNContext {
	for _, key := range []string{"authScreenshotFileUrl", "authorizationResultURL"} {
		if m := cdnURLRe.FindStringSubmatch(order.Str(key)); m != nil {
			return &CDNContext{Origin: m[1], Account: m[2]}
		}
	}
	return nil
}

// CDNOriginFromBase derives the CloudFront origin (https://cdn.<env>.ehrcopilotbe.com) from the BE
// base url (https://be.<env>.ehrcopilotbe.com): the CDN mirrors the BE host with the leading `be.`
// label swapped for `cdn.`. "" if base isn't a `be.` host.
func CDNOriginFromBase(base string) string {
	u, err := url.Parse(base)
	if err != nil {
		return ""
	}
	rest, ok := strings.CutPrefix(u.Hostname(), "be.")
	if !ok {
		return ""
	}
	return u.Scheme + "://cdn." + rest
}

// ResolveCDN resolves the CDN origin + account for an order. Prefer the DETERMINISTIC pairing —
// the env's CDN host (from the client base) + the account (the config profile) — so links resolve
// even for failed orders that carry no stamped CDN url. Fall back to parsing a url the BE stamped
// on the order (CDNFromOrder). base and account may be "".
func ResolveCDN(order BeOrder, base, account string) *CDNContext {
	if origin := CDNOriginFromBase(base); origin != "" && account != "" {
		return &CDNContext{Origin: origin, Account: account}
	}
	return CDNFromOrder(order)
}

// firstString is the first non-empty string among order-entity keys.
func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, _ := m[k].(string); s != "" {
			return s
		}
	}
	return ""
}

// SummaryURLFor is the Summary.pdf url for one medical-authorization entity: prefer a direct url
// the BE may already provide, else construct {origin}/{account}/medicalAuthorizations/{uid}/
// summary.pdf from the entity's uid + the order's CDN context. "" if neither is resolvable (so
// the caller just omits it rather than emitting a broken link).
func SummaryURLFor(auth map[string]any, cdn *CDNContext) string {
	if direct := firstString(auth, "summaryUrl", "summaryFileUrl", "summaryPdfUrl", "resultUrl"); direct != "" {
		return direct
	}
	uid := firstString(auth, "uid", "medicalAuthorizationUid", "medicalAuthorizationsUid", "id")
	if cdn != nil && cdn.Origin != "" && cdn.Account != "" && uid != "" {
		return cdn.Origin + "/" + cdn.Account + "/medicalAuthorizations/" + uid + "/summary.pdf"
	}
	return ""
}

// OrderDocuments builds the list of CDN document links that actually exist for an order. Never
// fails (a failed medicalAuthorizations lookup just yields no summary link). account may be "".
func OrderDocuments(ctx context.Context, c Client, order BeOrder, orderUID, account string) []OrderDocument {
	docs := []OrderDocument{}
	cdn := ResolveCDN(order, c.Base(), account)

	// 1) auth-screenshot PDF — hasAuthScreenshot is the BE's existence signal. Prefer the url the
	//    BE stamped, else construct the deterministic CDN path so the link still resolves when the
	//    order carries no stamped url.
	if has, _ := order["hasAuthScreenshot"].(bool); has {
		shot := order.Str("authScreenshotFileUrl")
		if shot == "" && cdn != nil {
			shot = cdn.Origin + "/" + cdn.Account + "/orders/" + orderUID + "/screenshots/authScreen.pdf"
		}
		if shot != "" {
			docs = append(docs, OrderDocument{Kind: "authScreenshot", URL: shot})
		}
	}

	// 2) medical-authorization summary PDFs — the listing is the authoritative existence check
	//    (only real auths come back). A failed lookup falls through to the order-level fallback.
	if r, err := c.Req(ctx, "GET", "/api/v1/medicalAuthorizations?orderUid="+url.PathEscape(orderUID), ReqBody{}); err == nil {
		for _, a := range shared.EnvelopeRows(r.Data) {
			m, ok := a.(map[string]any)
			if !ok {
				continue
			}
			if u := SummaryURLFor(m, cdn); u != "" {
				docs = append(docs, OrderDocument{Kind: "authSummary", URL: u})
			}
		}
	}

	// 3) fallback: order-level result url, if the listing yielded nothing.
	hasSummary := false
	for _, d := range docs {
		hasSummary = hasSummary || d.Kind == "authSummary"
	}
	if u := order.Str("authorizationResultURL"); !hasSummary && u != "" {
		docs = append(docs, OrderDocument{Kind: "authSummary", URL: u})
	}
	return docs
}
