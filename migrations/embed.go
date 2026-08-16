// Package migrations 嵌入全部 SQL 迁移文件，供启动时的版本化迁移器执行。
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
