// Package settings is the database-backed application configuration that
// replaced most of the GOBOOKSHELF_* environment: the external base URL,
// cookie and session behaviour, the background scan interval, OIDC, the
// reverse-proxy authentication mode, the online metadata provider and the
// /metrics allow list.
//
// The whole document lives as one JSON row in the settings table, so adding a
// field is a struct change and never a schema change. Fields marked secret are
// AES-256-GCM encrypted inside that JSON with the key from
// GOBOOKSHELF_SECRETS_KEY; they are decrypted once on load, kept in memory,
// and never returned to a client.
//
// A save is validated, then prepared, then persisted, then applied. Preparing
// is where anything that can fail against the outside world happens - OIDC
// discovery, most of all - so an unreachable issuer is reported as a rejected
// save rather than as a server that stored a configuration it cannot use.
package settings
