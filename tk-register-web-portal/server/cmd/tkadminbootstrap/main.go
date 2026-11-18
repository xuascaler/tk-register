// Temporary, operator-only first-administrator provisioning utility.
// Build under server/cmd; never register this operation as an HTTP endpoint.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	mysql "github.com/go-sql-driver/mysql"
	"goapi-tk-register/server/internal/core"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type adminInput struct {
	Email    string
	Password string
}

func main() {
	if len(os.Args) != 1 {
		fmt.Fprintln(os.Stderr, "Use stdin JSON; command-line credentials are not accepted.")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := run(ctx); err != nil {
		// Deliberately do not print driver/validation errors, queries, or input.
		fmt.Fprintln(os.Stderr, "Administrator bootstrap refused or failed; no credentials were logged and existing users were not changed.")
		os.Exit(1)
	}
	fmt.Fprintln(os.Stdout, "Initial administrator created. Password plus email verification remains required for sign-in.")
}

func run(ctx context.Context) error {
	config, err := mysql.ParseDSN(os.Getenv("TK_REGISTER_SQL_DSN"))
	if err != nil || config.DBName != "tk_register" || config.Net != "tcp" {
		return errors.New("invalid target database")
	}
	host, _, err := net.SplitHostPort(config.Addr)
	if err != nil || os.Getenv("TK_ADMIN_BOOTSTRAP_CONFIRM_EMPTY") != "1" ||
		os.Getenv("TK_ADMIN_BOOTSTRAP_EXPECTED_HOST") == "" ||
		!strings.EqualFold(host, os.Getenv("TK_ADMIN_BOOTSTRAP_EXPECTED_HOST")) {
		return errors.New("explicit empty-target confirmation required")
	}
	if driver := os.Getenv("TK_REGISTER_SQL_DRIVER"); driver != "" && driver != "mysql" {
		return errors.New("mysql required")
	}
	key := os.Getenv("TK_REGISTER_ENCRYPTION_KEY")
	if len(key) < 32 {
		return errors.New("invalid encryption configuration")
	}
	input, err := decodeAdmin(os.Stdin, os.Getenv("TK_REGISTER_ADMIN_EMAIL"))
	if err != nil {
		return err
	}
	config.ParseTime = true
	config.Loc = time.UTC
	config.MultiStatements = false
	config.Timeout = 10 * time.Second
	config.ReadTimeout = 15 * time.Second
	config.WriteTimeout = 15 * time.Second
	dsn := config.FormatDSN()
	// The application must already have initialized its new schema. Refuse
	// before core.Open can run migrations if any user has already been created.
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(1)
	err = checkEmptyUsers(ctx, db)
	_ = db.Close()
	if err != nil {
		return err
	}
	store, err := core.Open("mysql", dsn, key)
	if err != nil {
		return err
	}
	defer store.Close()
	return createFirstAdministrator(ctx, store, input)
}

func checkEmptyUsers(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id uint64
	err = tx.QueryRowContext(ctx, "SELECT id FROM users ORDER BY id LIMIT 1").Scan(&id)
	if err == nil {
		return errors.New("target already has users")
	}
	if err != sql.ErrNoRows {
		return err
	}
	return tx.Commit()
}

func createFirstAdministrator(ctx context.Context, store *core.Store, input adminInput) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	return store.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing []core.User
		// An empty InnoDB users range is gap-locked at SERIALIZABLE isolation.
		// Concurrent first-user creators cannot both pass the empty check.
		if err := tx.Model(&core.User{}).Select("id").Clauses(clause.Locking{Strength: "UPDATE"}).Order("id").Limit(1).Find(&existing).Error; err != nil {
			return err
		}
		if len(existing) != 0 {
			return errors.New("target already has users")
		}
		txStore := *store
		txStore.DB = tx
		// Provisioning was explicitly approved by the deployment owner. This
		// records that administrative provisioning; it does not mint a login
		// session or bypass the normal administrator password + email challenge.
		user := &core.User{
			Email: input.Email, PasswordHash: string(hash), Role: "admin",
			Status: "active", EmailVerifiedAt: time.Now().UTC(),
		}
		if err := txStore.CreateUser(ctx, user); err != nil {
			return err
		}
		var users, selfLinks int64
		if err := tx.Model(&core.User{}).Count(&users).Error; err != nil {
			return err
		}
		if err := tx.Model(&core.UserHierarchy{}).Where("ancestor_user_id = ? AND descendant_user_id = ? AND depth = ?", user.ID, user.ID, 0).Count(&selfLinks).Error; err != nil {
			return err
		}
		if users != 1 || selfLinks != 1 {
			return errors.New("bootstrap consistency check failed")
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelSerializable})
}

func decodeAdmin(reader io.Reader, configuredEmail string) (adminInput, error) {
	var out adminInput
	const limit = 4096
	raw, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil || len(raw) > limit {
		return out, errors.New("invalid payload size")
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return out, errors.New("object required")
	}
	fields := map[string]string{}
	for decoder.More() {
		token, err = decoder.Token()
		name, good := token.(string)
		if err != nil || !good || (name != "email" && name != "password") {
			return out, errors.New("unexpected input field")
		}
		if _, exists := fields[name]; exists {
			return out, errors.New("duplicate input field")
		}
		token, err = decoder.Token()
		value, good := token.(string)
		if err != nil || !good {
			return out, errors.New("string field required")
		}
		fields[name] = value
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return out, errors.New("invalid object")
	}
	if _, err = decoder.Token(); err != io.EOF {
		return out, errors.New("trailing input")
	}
	out.Email, out.Password = core.NormalizeEmail(fields["email"]), fields["password"]
	adminEmail := core.NormalizeEmail(configuredEmail)
	if len(fields) != 2 || !core.ValidEmail(adminEmail) || out.Email != adminEmail ||
		!utf8.ValidString(out.Password) || utf8.RuneCountInString(out.Password) < 8 || len(out.Password) > 72 {
		return adminInput{}, errors.New("invalid bootstrap administrator input")
	}
	return out, nil
}
