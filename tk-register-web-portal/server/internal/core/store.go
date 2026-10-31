package core

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var ErrNotFound = gorm.ErrRecordNotFound

type User struct {
	ID              uint64    `gorm:"primaryKey" json:"id"`
	Email           string    `gorm:"size:50;not null;uniqueIndex" json:"email"`
	Role            string    `gorm:"size:16;not null" json:"role"`
	Status          string    `gorm:"size:16;not null" json:"status"`
	AuthVersion     uint64    `gorm:"not null;default:0" json:"-"`
	PasswordHash    string    `gorm:"size:255;not null" json:"-"`
	EmailVerifiedAt time.Time `json:"email_verified_at"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type Setting struct {
	Key       string `gorm:"size:80;primaryKey"`
	Value     string `gorm:"type:text;not null"`
	Category  string `gorm:"size:24;not null"`
	Type      string `gorm:"size:16;not null"`
	IsSecret  bool   `gorm:"not null"`
	UpdatedBy uint64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// EmailVerification stores delivery and consumption metadata, never a code.
type EmailVerification struct {
	ID         uint64 `gorm:"primaryKey"`
	RequestID  string `gorm:"size:64;not null;uniqueIndex"`
	Email      string `gorm:"size:50;not null;index"`
	Purpose    string `gorm:"size:24;not null"`
	Status     string `gorm:"size:24;not null"`
	CreatedAt  time.Time
	ExpiresAt  time.Time
	SentAt     *time.Time
	ConsumedAt *time.Time
}

type Store struct {
	DB     *gorm.DB
	aead   cipher.AEAD
	macKey []byte
}

func openDB(driver, dsn string) (*gorm.DB, error) {
	var dialector gorm.Dialector
	var suppliedDB *sql.DB
	switch strings.ToLower(driver) {
	case "mysql":
		configuredDSN, err := mysqlDSN(dsn)
		if err != nil {
			return nil, errors.New("invalid MySQL database configuration")
		}
		dialector = mysql.Open(configuredDSN)
	case "postgres", "postgresql":
		config, err := pgx.ParseConfig(dsn)
		if err != nil {
			return nil, errors.New("invalid PostgreSQL database configuration")
		}
		if config.ConnectTimeout == 0 {
			config.ConnectTimeout = 10 * time.Second
		}
		suppliedDB = stdlib.OpenDB(*config)
		dialector = postgres.New(postgres.Config{Conn: suppliedDB})
	case "sqlite":
		dialector = sqlite.Open(dsn)
	default:
		return nil, errors.New("unsupported database driver")
	}
	// SQL statements can contain emails and encrypted secrets; do not log them.
	db, err := gorm.Open(dialector, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent), NowFunc: func() time.Time { return time.Now().UTC() }})
	if err != nil {
		if suppliedDB != nil {
			suppliedDB.Close()
		}
		return nil, errors.New("could not open TK Register database")
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxIdleConns(2)
	sqlDB.SetMaxOpenConns(10)
	sqlDB.SetConnMaxLifetime(5 * time.Minute)
	if strings.ToLower(driver) == "sqlite" {
		sqlDB.SetMaxOpenConns(1)
	}
	return db, nil
}

func mysqlDSN(dsn string) (string, error) {
	config, err := mysqldriver.ParseDSN(dsn)
	if err != nil {
		return "", err
	}
	config.ParseTime = true
	config.Loc = time.UTC
	if config.Timeout == 0 {
		config.Timeout = 10 * time.Second
	}
	if config.ReadTimeout == 0 {
		config.ReadTimeout = 15 * time.Second
	}
	if config.WriteTimeout == 0 {
		config.WriteTimeout = 15 * time.Second
	}
	return config.FormatDSN(), nil
}

// Open migrates only the explicitly supplied TK database. The encryption key
// belongs in the runtime environment and must stay unchanged across restarts.
func Open(driver, dsn, encryptionKey string) (*Store, error) {
	if len(encryptionKey) < 32 {
		return nil, errors.New("TK encryption key must contain at least 32 characters")
	}
	key := sha256.Sum256([]byte(encryptionKey))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	db, err := openDB(driver, dsn)
	if err != nil {
		return nil, err
	}
	s := &Store{DB: db, aead: aead, macKey: key[:]}
	if err = db.AutoMigrate(&User{}, &Setting{}, &EmailVerification{}, &UserAPIToken{}); err != nil {
		s.Close()
		return nil, errors.New("could not migrate TK Register database")
	}
	if err = s.migrateAdmin(); err != nil {
		s.Close()
		return nil, err
	}
	if err = s.seedDefaults(); err != nil {
		s.Close()
		return nil, err
	}
	if err = s.migrateVendors(); err != nil {
		s.Close()
		return nil, err
	}
	if err = s.migrateAccountSync(); err != nil {
		s.Close()
		return nil, err
	}
	if err = s.validateVendorSecrets(); err != nil {
		s.Close()
		return nil, err
	}
	// Detect a changed encryption key before accepting traffic.
	if _, err = s.Settings(context.Background()); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	db, err := s.DB.DB()
	if err != nil {
		return err
	}
	return db.Close()
}
func (s *Store) Ping(ctx context.Context) error {
	db, err := s.DB.DB()
	if err != nil {
		return err
	}
	return db.PingContext(ctx)
}

func NormalizeEmail(email string) string { return strings.ToLower(strings.TrimSpace(email)) }
func ValidEmail(email string) bool {
	if len(email) == 0 || len(email) > 50 || strings.ContainsAny(email, "\r\n\x00") {
		return false
	}
	for _, r := range email {
		if r > 127 {
			return false
		}
	}
	parsed, err := mail.ParseAddress(email)
	return err == nil && parsed.Address == email && strings.Contains(email, "@") && !strings.ContainsAny(email, " \t")
}

func (s *Store) CreateUser(ctx context.Context, user *User) error {
	user.Email = NormalizeEmail(user.Email)
	if !ValidEmail(user.Email) || user.PasswordHash == "" || user.EmailVerifiedAt.IsZero() {
		return errors.New("a verified email and password are required")
	}
	if user.Role != "member" && user.Role != "admin" {
		return errors.New("invalid user role")
	}
	if user.Status == "" {
		user.Status = "active"
	}
	if user.Status != "active" {
		return errors.New("invalid new user status")
	}
	return s.DB.WithContext(ctx).Create(user).Error
}
func (s *Store) GetUserByEmail(ctx context.Context, email string) (*User, error) {
	var user User
	err := s.DB.WithContext(ctx).Where("email = ?", NormalizeEmail(email)).First(&user).Error
	return &user, err
}
func (s *Store) GetUserByID(ctx context.Context, id uint64) (*User, error) {
	var user User
	err := s.DB.WithContext(ctx).First(&user, id).Error
	return &user, err
}
func (s *Store) RecordVerification(ctx context.Context, record *EmailVerification) error {
	if record.RequestID == "" || len(record.RequestID) > 64 || !ValidEmail(record.Email) {
		return errors.New("invalid verification audit record")
	}
	return s.DB.WithContext(ctx).Create(record).Error
}
func (s *Store) UpdateVerification(ctx context.Context, requestID, status string) error {
	updates := map[string]interface{}{"status": status}
	now := time.Now().UTC()
	if status == "sent" {
		updates["sent_at"] = now
	}
	if status == "consumed" {
		updates["consumed_at"] = now
	}
	return s.DB.WithContext(ctx).Model(&EmailVerification{}).Where("request_id = ?", requestID).Updates(updates).Error
}

func (s *Store) encrypt(key, value string) (string, error) {
	if value == "" {
		return "", nil
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := s.aead.Seal(nonce, nonce, []byte(value), []byte("tk-register:setting:"+key))
	return "enc:v1:" + base64.RawStdEncoding.EncodeToString(sealed), nil
}
func (s *Store) decrypt(key, value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if !strings.HasPrefix(value, "enc:v1:") {
		return "", errors.New("invalid encrypted setting")
	}
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(value, "enc:v1:"))
	if err != nil || len(raw) < s.aead.NonceSize()+s.aead.Overhead() {
		return "", errors.New("invalid encrypted setting")
	}
	plain, err := s.aead.Open(nil, raw[:s.aead.NonceSize()], raw[s.aead.NonceSize():], []byte("tk-register:setting:"+key))
	if err != nil {
		return "", fmt.Errorf("could not decrypt setting %s; verify the TK encryption key", key)
	}
	return string(plain), nil
}
