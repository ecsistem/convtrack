// Package migrations expõe os arquivos SQL embutidos no binário via go:embed.
// Isso elimina a necessidade de montar um volume de migrations em produção.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
