package store

import (
	_ "embed"
	"fmt"
)

//go:embed schema.sql
var coreSchemaSQL string

func migrateCoreSchema(d *DB) error {
	if _, err := d.sql.Exec(coreSchemaSQL); err != nil {
		return fmt.Errorf("create tables: %w", err)
	}
	if err := migrateStatusMessages(d); err != nil {
		return err
	}
	return nil
}

func migrateStatusMessageMediaColumns(d *DB) error {
	columns := []struct {
		name string
		sql  string
	}{
		{"sender_jid", `ALTER TABLE status_messages ADD COLUMN sender_jid TEXT`},
		{"sender_name", `ALTER TABLE status_messages ADD COLUMN sender_name TEXT`},
		{"filename", `ALTER TABLE status_messages ADD COLUMN filename TEXT`},
		{"mime_type", `ALTER TABLE status_messages ADD COLUMN mime_type TEXT`},
		{"direct_path", `ALTER TABLE status_messages ADD COLUMN direct_path TEXT`},
		{"media_key", `ALTER TABLE status_messages ADD COLUMN media_key BLOB`},
		{"file_sha256", `ALTER TABLE status_messages ADD COLUMN file_sha256 BLOB`},
		{"file_enc_sha256", `ALTER TABLE status_messages ADD COLUMN file_enc_sha256 BLOB`},
		{"file_length", `ALTER TABLE status_messages ADD COLUMN file_length INTEGER`},
	}
	for _, col := range columns {
		has, err := d.tableHasColumn("status_messages", col.name)
		if err != nil {
			return fmt.Errorf("inspect status_messages.%s: %w", col.name, err)
		}
		if has {
			continue
		}
		if _, err := d.sql.Exec(col.sql); err != nil {
			return fmt.Errorf("add status_messages.%s: %w", col.name, err)
		}
	}
	return nil
}
