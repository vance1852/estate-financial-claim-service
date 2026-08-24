package migrations

import "embed"

// Files contains the ordered SQL migration assets used by every deployment.
//
//go:embed *.sql
var Files embed.FS
