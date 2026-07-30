// Package config resolves runtime settings from an optional sectioned-TOML
// file and RDNS_LOOKUP_* environment variables. Precedence is flag > env >
// file > built-in default (the flag layer is applied by the app package).
// There are no credentials: ip.thc.org has no authentication mechanism, so
// there is nothing secret to hold. The TOML reader is a deliberately tiny
// subset (headers + key = value) to keep the module dependency-free.
package config
