package receipts

import "embed"

// Migrations are this module's SQL files, applied by the migrate step in donation.
//
//go:embed migrations/*.sql
var Migrations embed.FS
