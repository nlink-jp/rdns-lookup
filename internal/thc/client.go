package thc

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// maxBody caps a response read. A 50,000-row CSV of long domain names stays
// well under this; the limit exists so a misbehaving upstream cannot exhaust
// memory.
const maxBody = 64 << 20

// Doer is the injected HTTP seam, so tests never need a real network.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// Client talks to ip.thc.org. No credentials: the service has no
// authentication mechanism at all.
type Client struct {
	HTTP      Doer
	BaseURL   string // "" ⇒ DefaultBaseURL
	UserAgent string
}

// Fetch performs one upstream request and returns the normalized page.
func (c *Client) Fetch(req Request) (*Page, error) {
	if req.Route == RouteCSV {
		return c.fetchCSV(req)
	}
	return c.fetchJSON(req)
}

func (c *Client) base() string {
	if c.BaseURL != "" {
		return strings.TrimRight(c.BaseURL, "/")
	}
	return DefaultBaseURL
}

// jsonPath and csvPath map a lookup kind onto its two upstream endpoints.
func jsonPath(k Kind) (string, error) {
	switch k {
	case KindRDNS:
		return "/lookup", nil
	case KindSubdomains:
		return "/lookup/subdomains", nil
	case KindCNAMEs:
		return "/lookup/cnames", nil
	}
	return "", fmt.Errorf("unknown lookup kind %q", k)
}

func csvPath(k Kind) (string, error) {
	switch k {
	case KindRDNS:
		return "/download", nil
	case KindSubdomains:
		return "/subdomains/download", nil
	case KindCNAMEs:
		return "/cnames/download", nil
	}
	return "", fmt.Errorf("unknown lookup kind %q", k)
}

// queryField is the request-body key naming the target for each kind.
func queryField(k Kind) string {
	switch k {
	case KindSubdomains:
		return "domain"
	case KindCNAMEs:
		return "target_domain"
	default:
		return "ip_address"
	}
}

// wireJSON mirrors the upstream JSON response.
//
// Note the subdomains key: upstream's documentation calls it "subdomains", but
// every face actually returns "domains". The observed name wins.
type wireJSON struct {
	Comment         json.RawMessage `json:"comment"`
	MatchingRecords int             `json:"matching_records"`
	NextPageState   string          `json:"next_page_state"`
	Domains         json.RawMessage `json:"domains"`
	Status          string          `json:"status"`
	Error           string          `json:"error"`
}

// wireRDNSRow is one object from the rdns JSON face.
type wireRDNSRow struct {
	Domain     string `json:"domain"`
	ApexDomain string `json:"apex_domain"`
	TLD        string `json:"tld"`
	IPAddress  string `json:"ip_address"`
	ASN        string `json:"asn"`
	Org        string `json:"organization"`
	Country    string `json:"country"`
	City       string `json:"city"`
}

// wireSubdomainRow is one object from the subdomains JSON face.
type wireSubdomainRow struct {
	Domain   string `json:"domain"`
	LastSeen string `json:"last_seen_on"`
}

func (c *Client) fetchJSON(req Request) (*Page, error) {
	path, err := jsonPath(req.Kind)
	if err != nil {
		return nil, err
	}
	limit := req.Limit
	if limit <= 0 || limit > JSONMaxLimit {
		limit = JSONMaxLimit
	}
	body := map[string]any{
		queryField(req.Kind): req.Query,
		"limit":              limit,
	}
	if req.PageState != "" {
		body["page_state"] = req.PageState
	}
	if req.Kind == KindRDNS {
		if len(req.TLDs) > 0 {
			body["tld"] = req.TLDs
		}
		if req.ApexDomain != "" {
			body["apex_domain"] = req.ApexDomain
		}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequest(http.MethodPost, c.base()+path, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	raw, rl, err := c.do(httpReq)
	if err != nil {
		return nil, err
	}

	var w wireJSON
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, fmt.Errorf("decode ip.thc.org response: %w", err)
	}
	// A 200 body can still carry an error envelope.
	if w.Status == "error" || w.Error != "" {
		return nil, upstreamError(http.StatusOK, w.Error)
	}

	records, err := decodeJSONRows(req.Kind, w.Domains)
	if err != nil {
		return nil, err
	}
	page := &Page{
		Records:         records,
		MatchingRecords: w.MatchingRecords,
		NextPageState:   w.NextPageState,
		Route:           RouteJSON,
		RateLimit:       rl,
	}
	if req.KeepRaw {
		page.Raw = raw
	}
	return page, nil
}

// decodeJSONRows handles the three row shapes: rdns returns objects with the
// full enrichment, subdomains returns objects with a last-seen date, and
// cnames returns bare strings.
func decodeJSONRows(kind Kind, rowsRaw json.RawMessage) ([]Record, error) {
	if len(rowsRaw) == 0 || string(rowsRaw) == "null" {
		return nil, nil
	}
	switch kind {
	case KindRDNS:
		var rows []wireRDNSRow
		if err := json.Unmarshal(rowsRaw, &rows); err != nil {
			return nil, fmt.Errorf("decode rdns rows: %w", err)
		}
		out := make([]Record, 0, len(rows))
		for _, r := range rows {
			out = append(out, Record{
				Domain: r.Domain, ApexDomain: r.ApexDomain, TLD: r.TLD,
				IPAddress: r.IPAddress, ASN: r.ASN, Org: r.Org,
				Country: r.Country, City: r.City,
			})
		}
		return out, nil
	case KindSubdomains:
		var rows []wireSubdomainRow
		if err := json.Unmarshal(rowsRaw, &rows); err != nil {
			return nil, fmt.Errorf("decode subdomain rows: %w", err)
		}
		out := make([]Record, 0, len(rows))
		for _, r := range rows {
			out = append(out, Record{Domain: r.Domain, LastSeen: r.LastSeen})
		}
		return out, nil
	default: // KindCNAMEs
		var rows []string
		if err := json.Unmarshal(rowsRaw, &rows); err != nil {
			return nil, fmt.Errorf("decode cname rows: %w", err)
		}
		out := make([]Record, 0, len(rows))
		for _, d := range rows {
			out = append(out, Record{Domain: d})
		}
		return out, nil
	}
}

func (c *Client) fetchCSV(req Request) (*Page, error) {
	path, err := csvPath(req.Kind)
	if err != nil {
		return nil, err
	}
	limit := req.Limit
	if limit <= 0 || limit > CSVMaxLimit {
		limit = CSVMaxLimit
	}
	q := url.Values{}
	q.Set(queryField(req.Kind), req.Query)
	q.Set("limit", strconv.Itoa(limit))
	// Always suppress upstream's header row: it is rotated by one column
	// relative to the data (see csvColumns), so reading it would mislabel
	// every field. Rows are mapped positionally instead.
	q.Set("hide_header", "true")
	if req.Kind == KindRDNS && req.ApexDomain != "" {
		q.Set("apex_domain", req.ApexDomain)
	}

	httpReq, err := http.NewRequest(http.MethodGet, c.base()+path+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Accept", "text/csv")

	raw, rl, err := c.do(httpReq)
	if err != nil {
		return nil, err
	}
	// The CSV face reports an invalid target as a JSON error body despite the
	// text/csv Accept, so check before parsing rows.
	if looksLikeJSONError(raw) {
		var w wireJSON
		if json.Unmarshal(raw, &w) == nil && (w.Status == "error" || w.Error != "") {
			return nil, upstreamError(http.StatusOK, w.Error)
		}
	}

	records, err := parseCSV(req.Kind, raw)
	if err != nil {
		return nil, err
	}
	page := &Page{
		Records: records,
		// The CSV face reports no total, so the count we hold is all we know.
		MatchingRecords: len(records),
		Route:           RouteCSV,
		RateLimit:       rl,
	}
	if req.KeepRaw {
		page.Raw = raw
	}
	return page, nil
}

// parseCSV maps headerless rows positionally. rdns rows follow csvColumns;
// subdomains and cnames are a single domain column.
func parseCSV(kind Kind, raw []byte) ([]Record, error) {
	r := csv.NewReader(bytes.NewReader(raw))
	r.FieldsPerRecord = -1 // tolerate short rows rather than aborting the page
	rows, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("parse ip.thc.org CSV: %w", err)
	}
	out := make([]Record, 0, len(rows))
	for _, row := range rows {
		if len(row) == 0 || strings.TrimSpace(strings.Join(row, "")) == "" {
			continue
		}
		if isCSVHeaderRow(row[0]) {
			continue
		}
		if kind != KindRDNS {
			d := strings.TrimSpace(row[0])
			if d == "" {
				continue
			}
			out = append(out, Record{Domain: d})
			continue
		}
		rec := Record{}
		for i, col := range csvColumns {
			if i >= len(row) {
				break
			}
			v := strings.TrimSpace(row[i])
			switch col {
			case "apex_domain":
				rec.ApexDomain = v
			case "domain":
				rec.Domain = v
			case "tld":
				rec.TLD = v
			case "country":
				rec.Country = v
			case "city":
				rec.City = v
			case "asn":
				rec.ASN = v
			case "organization":
				rec.Org = v
			case "ip_address":
				rec.IPAddress = v
			}
		}
		if rec.Domain == "" {
			continue
		}
		out = append(out, rec)
	}
	return out, nil
}

// isCSVHeaderRow reports whether a row is upstream's own header line. Requests
// pin hide_header=true, but if upstream ever ignored it the header would arrive
// as data and yield a record named "ipAddress". Every header name is a bare
// label with no dot, so no real domain can collide with one.
func isCSVHeaderRow(first string) bool {
	switch strings.TrimSpace(first) {
	case "ipAddress", "apexDomain", "subdomain", "domain":
		return true
	}
	return false
}

func looksLikeJSONError(raw []byte) bool {
	t := bytes.TrimSpace(raw)
	return len(t) > 0 && t[0] == '{'
}

// do executes the request, reads the body, and parses the rate-limit budget.
// Upstream reports a bad target as HTTP 406 with a JSON body, so the body is
// read for every status.
func (c *Client) do(req *http.Request) ([]byte, RateLimit, error) {
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, RateLimit{Limit: -1, Remaining: -1}, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, parseRateLimit(resp.Header), fmt.Errorf("read ip.thc.org response: %w", err)
	}
	rl := parseRateLimit(resp.Header)

	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, rl, ErrRateLimited
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, rl, upstreamError(resp.StatusCode, extractError(body))
	}
	return body, rl, nil
}

// upstreamError maps upstream's error text onto a sentinel where the text is
// actionable, falling back to a typed APIError.
func upstreamError(status int, detail string) error {
	if strings.Contains(strings.ToLower(detail), "invalid ip") {
		return fmt.Errorf("%w: %s", ErrInvalidTarget, detail)
	}
	return &APIError{StatusCode: status, Detail: detail}
}

// extractError pulls the message out of upstream's {"status","error"} body,
// falling back to a trimmed snippet of whatever arrived.
func extractError(body []byte) string {
	var w wireJSON
	if json.Unmarshal(body, &w) == nil && w.Error != "" {
		return w.Error
	}
	s := strings.TrimSpace(string(body))
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return s
}

func parseRateLimit(h http.Header) RateLimit {
	rl := RateLimit{Limit: -1, Remaining: -1}
	if v := h.Get("X-Ratelimit-Limit"); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			rl.Limit = n
		}
	}
	if v := h.Get("X-Ratelimit-Remaining"); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			rl.Remaining = n
		}
	}
	if v := h.Get("X-Ratelimit-Rate"); v != "" {
		if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
			rl.Rate = f
		}
	}
	return rl
}

// IsInvalidTarget reports whether err is upstream's rejection of the target.
func IsInvalidTarget(err error) bool { return errors.Is(err, ErrInvalidTarget) }
