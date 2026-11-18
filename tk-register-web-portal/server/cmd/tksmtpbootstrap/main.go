// Operator-only SMTP initialization utility. Never expose this utility through
// the production server's HTTP routes.
package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"regexp"
	"strings"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	"goapi-tk-register/server/internal/core"
)

var smtpKeys = []string{
	"smtp_host", "smtp_port", "smtp_tls_mode", "smtp_username", "smtp_password",
	"smtp_from_name", "smtp_from_email", "smtp_force_auth_login", "smtp_timeout_seconds",
}

func allowedSMTPKey(key string) bool {
	for _, allowed := range smtpKeys {
		if key == allowed {
			return true
		}
	}
	return false
}

func main() {
	if len(os.Args) != 2 || (os.Args[1] != "export-smtp" && os.Args[1] != "init-smtp") {
		fmt.Fprintln(os.Stderr, "usage: tksmtpbootstrap export-smtp|init-smtp")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := run(ctx, os.Args[1]); err != nil {
		// Driver and validation errors can contain DSNs, SQL values, or secrets.
		// All returned errors are intentionally discarded at this boundary.
		fmt.Fprintln(os.Stderr, "SMTP bootstrap failed; no credential values were logged.")
		os.Exit(1)
	}
}

func run(ctx context.Context, mode string) error {
	dsn := os.Getenv("TK_REGISTER_SQL_DSN")
	key := os.Getenv("TK_REGISTER_ENCRYPTION_KEY")
	config, err := mysql.ParseDSN(dsn)
	if err != nil || config.DBName != "tk_register" || config.Net != "tcp" || len(key) < 32 {
		return errors.New("invalid database or encryption configuration")
	}
	if driver := os.Getenv("TK_REGISTER_SQL_DRIVER"); driver != "" && driver != "mysql" {
		return errors.New("mysql required")
	}
	config.ParseTime = true
	config.Loc = time.UTC
	config.MultiStatements = false
	config.Timeout = 10 * time.Second
	config.ReadTimeout = 15 * time.Second
	config.WriteTimeout = 15 * time.Second
	if mode == "export-smtp" {
		stat, e := os.Stdout.Stat()
		if e != nil || stat.Mode()&os.ModeCharDevice != 0 {
			return errors.New("secret export requires a pipe or protected file")
		}
		db, e := sql.Open("mysql", config.FormatDSN())
		if e != nil {
			return e
		}
		defer db.Close()
		db.SetMaxOpenConns(1)
		values, e := exportSMTP(ctx, db, key)
		if e != nil {
			return e
		}
		return json.NewEncoder(os.Stdout).Encode(values)
	}
	// Deliberately make source/target confusion fail before opening either DB.
	host, _, err := net.SplitHostPort(config.Addr)
	if err != nil || os.Getenv("TK_SMTP_BOOTSTRAP_CONFIRM_FRESH") != "1" ||
		os.Getenv("TK_SMTP_BOOTSTRAP_EXPECTED_HOST") == "" ||
		!strings.EqualFold(host, os.Getenv("TK_SMTP_BOOTSTRAP_EXPECTED_HOST")) {
		return errors.New("fresh target confirmation required")
	}
	values, err := decodeSMTP(os.Stdin)
	if err != nil {
		return err
	}
	db, err := sql.Open("mysql", config.FormatDSN())
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(1)
	err = checkFresh(ctx, db)
	_ = db.Close()
	if err != nil {
		return err
	}
	store, err := core.Open("mysql", config.FormatDSN(), key)
	if err != nil {
		return err
	}
	defer store.Close()
	// The complete application validation and encryption remain authoritative.
	if err = store.PatchSettings(ctx, values, 0); err != nil {
		return err
	}
	saved, err := store.Settings(ctx)
	if err != nil {
		return err
	}
	for _, name := range smtpKeys {
		if saved.String(name) != values[name] {
			return errors.New("SMTP verification failed")
		}
	}
	fmt.Fprintln(os.Stdout, "SMTP settings initialized and verified; no user, account, or history data was imported.")
	return nil
}

func exportSMTP(ctx context.Context, db *sql.DB, key string) (map[string]string, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	args := make([]any, len(smtpKeys))
	marks := make([]string, len(smtpKeys))
	for i, name := range smtpKeys {
		args[i], marks[i] = name, "?"
	}
	rows, err := tx.QueryContext(ctx, "SELECT `key`, `value`, is_secret FROM settings WHERE `key` IN ("+strings.Join(marks, ",")+")", args...)
	if err != nil {
		return nil, err
	}
	values := make(map[string]string, len(smtpKeys))
	for rows.Next() {
		var name, value string
		var secret bool
		if err = rows.Scan(&name, &value, &secret); err != nil {
			rows.Close()
			return nil, err
		}
		if !allowedSMTPKey(name) || secret != (name == "smtp_password") {
			rows.Close()
			return nil, errors.New("unexpected SMTP setting shape")
		}
		if secret {
			value, err = decryptSetting(key, name, value)
			if err != nil {
				rows.Close()
				return nil, err
			}
		}
		values[name] = value
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if err = validateSMTPShape(values); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return values, nil
}

func decryptSetting(key, name, value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if len(key) < 32 || !strings.HasPrefix(value, "enc:v1:") {
		return "", errors.New("invalid encrypted setting")
	}
	digest := sha256.Sum256([]byte(key))
	block, err := aes.NewCipher(digest[:])
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(value, "enc:v1:"))
	if err != nil || len(raw) < aead.NonceSize()+aead.Overhead() {
		return "", errors.New("invalid encrypted setting")
	}
	plain, err := aead.Open(nil, raw[:aead.NonceSize()], raw[aead.NonceSize():], []byte("tk-register:setting:"+name))
	if err != nil {
		return "", errors.New("SMTP decryption failed")
	}
	return string(plain), nil
}

func decodeSMTP(input io.Reader) (map[string]string, error) {
	// Bound both stream size and the entire decoded object. Reject duplicate
	// keys rather than letting the decoder silently choose the last value.
	const limit = 64 * 1024
	data, err := io.ReadAll(io.LimitReader(input, limit+1))
	if err != nil || len(data) > limit {
		return nil, errors.New("invalid SMTP payload size")
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("SMTP object required")
	}
	values := make(map[string]string, len(smtpKeys))
	for decoder.More() {
		token, err = decoder.Token()
		if err != nil {
			return nil, err
		}
		name, good := token.(string)
		if !good || !allowedSMTPKey(name) {
			return nil, errors.New("unsupported SMTP key")
		}
		if _, duplicate := values[name]; duplicate {
			return nil, errors.New("duplicate SMTP key")
		}
		token, err = decoder.Token()
		value, good := token.(string)
		if err != nil || !good {
			return nil, errors.New("SMTP values must be strings")
		}
		values[name] = value
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, errors.New("invalid SMTP object")
	}
	if _, err = decoder.Token(); err != io.EOF {
		return nil, errors.New("trailing SMTP payload")
	}
	if err = validateSMTPShape(values); err != nil {
		return nil, err
	}
	return values, nil
}

func validateSMTPShape(values map[string]string) error {
	if len(values) != len(smtpKeys) {
		return errors.New("complete SMTP configuration required")
	}
	for _, name := range smtpKeys {
		if _, exists := values[name]; !exists {
			return errors.New("missing SMTP setting")
		}
	}
	if values["smtp_host"] == "" || values["smtp_from_email"] == "" ||
		(values["smtp_username"] != "" && values["smtp_password"] == "") {
		return errors.New("SMTP is not configured")
	}
	return nil
}

var tableIdentifier = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

func checkFresh(ctx context.Context, db *sql.DB) error {
	// Run before exposing the new service. This check is a misuse guard, not a
	// cross-process lock against concurrent registration or configuration.
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT TABLE_NAME FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE()")
	if err != nil {
		return err
	}
	var tables []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		if !tableIdentifier.MatchString(name) {
			rows.Close()
			return errors.New("unexpected target table")
		}
		tables = append(tables, name)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	seedTables := map[string]bool{
		"settings": true, "data_migrations": true, "vendors": true,
		"vendor_protocols": true, "protocol_versions": true, "protocol_parameters": true,
		"account_sync_states": true, "admin_role_guards": true,
	}
	for _, table := range tables {
		if table == "settings" {
			var existing int
			err = tx.QueryRowContext(ctx, "SELECT 1 FROM settings WHERE (`key` IN ('smtp_host','smtp_username','smtp_password','smtp_from_email') AND `value` <> '') OR `key` = '_goapi_smtp_imported' LIMIT 1").Scan(&existing)
			if err == nil {
				return errors.New("SMTP already configured")
			}
			if err != sql.ErrNoRows {
				return err
			}
		}
		if seedTables[table] {
			continue
		}
		var existing int
		err = tx.QueryRowContext(ctx, "SELECT 1 FROM `"+table+"` LIMIT 1").Scan(&existing)
		if err == nil {
			return errors.New("target contains business data")
		}
		if err != sql.ErrNoRows {
			return err
		}
	}
	return tx.Commit()
}
