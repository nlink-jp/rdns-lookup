// Package e2e holds live end-to-end tests that query the real ip.thc.org API.
// They are guarded by the `e2e` build tag so `go test ./...` (offline) never
// runs them; run them deliberately with:
//
//	make e2e          # or: go test -tags e2e -count=1 ./e2e/...
//
// The suite is written to be frugal with upstream's shared rate-limit budget
// (250 burst, refilling at 0.5/sec): it makes roughly a dozen requests in
// total and asks for small record counts except where a large one is the point
// of the test. ip.thc.org is a free service that asks not to be abused, so do
// not add checks here casually.
//
// This file has no build tag so the package always compiles (and reports "no
// test files" without the tag).
package e2e
