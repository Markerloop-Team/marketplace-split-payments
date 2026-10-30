// Package migrations embeds the SQL schema, applied in lexical file order.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
