// Package thc is the ip.thc.org API client. The service exposes the same data
// twice — a JSON face that paginates with an opaque token but silently caps
// limit at 100, and a CSV face that returns up to 50,000 rows in one request
// but cannot paginate at all. Both are implemented here and the engine picks
// between them; callers never see which face answered except as provenance.
//
// Two upstream quirks are corrected here rather than leaked upward. The CSV
// header row is rotated by one column relative to the data (it declares
// ipAddress first while the value arrives last), so every CSV request pins
// hide_header=true and the columns are mapped positionally from a fixed
// schema. And upstream errors arrive as HTTP 406 with a JSON body rather than
// as a status code alone, so they are decoded into a typed APIError.
//
// The service is free, unauthenticated, and asks not to be abused; every
// response carries its rate-limit budget, which the client parses and returns
// so the engine can pace itself.
package thc
