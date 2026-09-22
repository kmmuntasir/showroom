// Package store — embedded migrations. Numbered, forward-only: applied in
// a transaction at boot and recorded in schema_migrations; an applied
// migration is never edited (backend rules §Database).
package migrations

import "embed"

//go:embed *.sql
var Files embed.FS
