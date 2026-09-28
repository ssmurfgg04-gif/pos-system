// Package cloudmeta holds the public, non-secret cloud identity used by
// every LedgerPOS client: the fleet Supabase project URL and its anon key.
//
// The anon key is designed to ship inside clients; it grants nothing
// beyond what RLS policies allow (read the store registry + public config
// rows, call device-authenticated RPCs). Secrets live behind the
// device-gated sync_device_config RPC, never in this file.
package cloudmeta

const (
	// ProjectURL is the fleet cloud project (stores registry, sync, config).
	ProjectURL = "https://ixxiqrobcwkvyjtxdkvh.supabase.co"

	// AnonKey is the project's public anon key (safe to embed; RLS-gated).
	AnonKey = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJpc3MiOiJzdXBhYmFzZSIsInJlZiI6Iml4eGlxcm9iY3drdnlqdHhka3ZoIiwicm9sZSI6ImFub24iLCJpYXQiOjE3ODk1MTU0MDUsImV4cCI6MjEwNTA5MTQwNX0.hBO1kg4hLSHLUl5m-MCSHGCVFa4a7SRXF2uSioCV9Yk"
)

// BaseURL is a var so tests can point clients at a fake server.
var BaseURL = ProjectURL
