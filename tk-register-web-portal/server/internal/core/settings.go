package core

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Values map[string]string

type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }
func IsValidationError(err error) bool {
	var validation *ValidationError
	return errors.As(err, &validation)
}
func validationErrorf(format string, args ...interface{}) error {
	return &ValidationError{Message: fmt.Sprintf(format, args...)}
}

func (v Values) String(key string) string { return v[key] }
func (v Values) Int(key string) int       { n, _ := strconv.Atoi(v[key]); return n }
func (v Values) Bool(key string) bool     { return v[key] == "true" }

type SettingDescriptor struct {
	Key         string   `json:"key"`
	Category    string   `json:"category"`
	Type        string   `json:"type"`
	Value       string   `json:"value"`
	Description string   `json:"description"`
	Secret      bool     `json:"secret"`
	Configured  bool     `json:"configured"`
	Options     []string `json:"options,omitempty"`
	Min         *int     `json:"min,omitempty"`
	Max         *int     `json:"max,omitempty"`
}
type settingSpec struct {
	Key, Category, Type, Default, Description string
	Min, Max                                  int
	Options                                   []string
}

var specs = []settingSpec{
	{"smtp_host", "email", "string", "", "SMTP server hostname.", 0, 253, nil},
	{"smtp_port", "email", "integer", "587", "SMTP server port.", 1, 65535, nil},
	{"smtp_tls_mode", "email", "enum", "starttls", "Use STARTTLS or TLS from the beginning of the connection.", 0, 0, []string{"starttls", "tls"}},
	{"smtp_username", "email", "string", "", "SMTP authentication username.", 0, 254, nil},
	{"smtp_password", "email", "secret", "", "Encrypted at rest. Leave blank to keep the saved password.", 0, 4096, nil},
	{"smtp_from_name", "email", "string", "GoAPI", "Display name shown to recipients.", 1, 100, nil},
	{"smtp_from_email", "email", "email", "", "Verified sender email address.", 0, 254, nil},
	{"smtp_force_auth_login", "email", "boolean", "false", "Use AUTH LOGIN instead of AUTH PLAIN.", 0, 0, nil},
	{"smtp_timeout_seconds", "email", "integer", "15", "Maximum time allowed to deliver a message to SMTP, in seconds.", 3, 60, nil},
	{"registration_enabled", "access", "boolean", "true", "Allow registration using a verified email and a password.", 0, 0, nil},
	{"password_login_enabled", "access", "boolean", "true", "Allow members to sign in with a password. Administrators still require a password and an email code.", 0, 0, nil},
	{"email_code_login_enabled", "access", "boolean", "true", "Allow members to sign in with an email code. Administrators always require both factors.", 0, 0, nil},
	{"registration_code_ttl_seconds", "verification", "integer", "600", "Registration code lifetime, in seconds.", 60, 3600, nil},
	{"login_code_ttl_seconds", "verification", "integer", "600", "Member sign-in code lifetime, in seconds.", 60, 3600, nil},
	{"admin_login_code_ttl_seconds", "verification", "integer", "600", "Administrator sign-in code lifetime, in seconds.", 60, 1800, nil},
	{"admin_login_challenge_ttl_seconds", "verification", "integer", "600", "Time to finish administrator email verification after password verification, in seconds.", 60, 1800, nil},
	{"email_send_cooldown_seconds", "verification", "integer", "60", "Minimum time between code requests for the same email, in seconds.", 60, 3600, nil},
	{"verification_max_attempts", "verification", "integer", "5", "Incorrect code attempts allowed before the code expires.", 1, 10, nil},
	{"email_send_limit_per_hour", "verification", "integer", "10", "Maximum code deliveries to one email per hour.", 1, 100, nil},
	{"ip_send_limit_per_hour", "verification", "integer", "30", "Maximum code requests from one IP per hour.", 1, 500, nil},
	{"login_failure_max_attempts", "verification", "integer", "5", "Failed password attempts allowed in the configured window.", 1, 20, nil},
	{"login_failure_window_seconds", "verification", "integer", "900", "Password attempt limit window, in seconds.", 60, 86400, nil},
	{"user_session_absolute_ttl_seconds", "sessions", "integer", "604800", "Maximum member session lifetime, in seconds.", 300, 2592000, nil},
	{"admin_session_absolute_ttl_seconds", "sessions", "integer", "28800", "Maximum administrator session lifetime, in seconds.", 300, 86400, nil},
	{"admin_session_idle_ttl_seconds", "sessions", "integer", "1800", "Administrator session expires after this much inactivity, in seconds.", 60, 86400, nil},
}

func (s *Store) seedDefaults() error {
	return s.DB.Transaction(func(tx *gorm.DB) error {
		for _, spec := range specs {
			row := Setting{Key: spec.Key, Value: spec.Default, Category: spec.Category, Type: spec.Type, IsSecret: spec.Type == "secret"}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) values(db *gorm.DB) (Values, error) {
	var rows []Setting
	if err := db.Find(&rows).Error; err != nil {
		return nil, err
	}
	values := Values{}
	for _, row := range rows {
		if strings.HasPrefix(row.Key, "_") {
			continue
		}
		value := row.Value
		if row.IsSecret {
			var err error
			value, err = s.decrypt(row.Key, value)
			if err != nil {
				return nil, err
			}
		}
		values[row.Key] = value
	}
	return values, nil
}
func (s *Store) Settings(ctx context.Context) (Values, error) { return s.values(s.DB.WithContext(ctx)) }

func (s *Store) Descriptors(ctx context.Context) ([]SettingDescriptor, error) {
	values, err := s.Settings(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]SettingDescriptor, 0, len(specs))
	for _, spec := range specs {
		value := values[spec.Key]
		d := SettingDescriptor{Key: spec.Key, Category: spec.Category, Type: spec.Type, Value: value, Description: spec.Description, Secret: spec.Type == "secret", Configured: value != "", Options: spec.Options}
		if d.Secret {
			d.Value = ""
		}
		if spec.Type == "integer" {
			min, max := spec.Min, spec.Max
			d.Min = &min
			d.Max = &max
		}
		result = append(result, d)
	}
	return result, nil
}

func validate(spec settingSpec, value string) error {
	if strings.ContainsAny(value, "\r\n\x00") {
		return errors.New("line breaks and null bytes are not allowed")
	}
	switch spec.Type {
	case "boolean":
		if value != "true" && value != "false" {
			return errors.New("must be true or false")
		}
	case "integer":
		n, err := strconv.Atoi(value)
		if err != nil || n < spec.Min || n > spec.Max || strconv.Itoa(n) != value {
			return fmt.Errorf("must be an integer from %d to %d", spec.Min, spec.Max)
		}
	case "enum":
		for _, option := range spec.Options {
			if value == option {
				return nil
			}
		}
		return errors.New("invalid option")
	default:
		if len(value) < spec.Min || len(value) > spec.Max {
			return fmt.Errorf("must contain between %d and %d characters", spec.Min, spec.Max)
		}
	}
	if spec.Key == "smtp_host" && value != "" {
		if strings.ContainsAny(value, " /\\@?#:\t") && net.ParseIP(value) == nil {
			return errors.New("must be a hostname or IP address, without a URL scheme or port")
		}
	}
	if spec.Key == "smtp_from_email" && value != "" && !validSenderEmail(value) {
		return errors.New("must be a valid sender email address")
	}
	return nil
}

func (s *Store) PatchSettings(ctx context.Context, changes map[string]string, actorID uint64) error {
	if len(changes) == 0 {
		return validationErrorf("no settings supplied")
	}
	known := map[string]settingSpec{}
	for _, spec := range specs {
		known[spec.Key] = spec
	}
	for key, value := range changes {
		spec, ok := known[key]
		if !ok {
			return validationErrorf("unknown setting: %s", key)
		}
		if spec.Type == "secret" && value == "" {
			continue
		}
		if err := validate(spec, value); err != nil {
			return validationErrorf("%s: %s", key, err.Error())
		}
	}
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		values, err := s.values(tx.Clauses(clause.Locking{Strength: "UPDATE"}))
		if err != nil {
			return err
		}
		for key, value := range changes {
			if known[key].Type == "secret" && value == "" {
				continue
			}
			values[key] = value
		}
		if !values.Bool("password_login_enabled") && !values.Bool("email_code_login_enabled") {
			return validationErrorf("at least one member sign-in method must remain enabled")
		}
		if values.Int("admin_session_idle_ttl_seconds") > values.Int("admin_session_absolute_ttl_seconds") {
			return validationErrorf("administrator idle lifetime cannot exceed absolute session lifetime")
		}
		for key, value := range changes {
			if known[key].Type == "secret" {
				if value == "" {
					continue
				}
				value, err = s.encrypt(key, value)
				if err != nil {
					return err
				}
			}
			if err = tx.Model(&Setting{}).Where(clause.Eq{Column: "key", Value: key}).Updates(map[string]interface{}{"value": value, "updated_by": actorID}).Error; err != nil {
				return err
			}
		}
		// Explicit TK email configuration takes precedence over any later import.
		for key := range changes {
			if known[key].Category == "email" {
				marker := Setting{Key: "_goapi_smtp_imported", Value: "true", Category: "internal", Type: "boolean", UpdatedBy: actorID}
				if err = tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&marker).Error; err != nil {
					return err
				}
				break
			}
		}
		return nil
	})
}

// ImportGoAPISMTP reads existing GoAPI options once. The marker and values are
// committed together, so restarts never overwrite TK administrators' changes.
func (s *Store) ImportGoAPISMTP(ctx context.Context, driver, dsn string) error {
	if dsn == "" {
		return nil
	}
	var count int64
	if err := s.DB.WithContext(ctx).Model(&Setting{}).Where(clause.Eq{Column: "key", Value: "_goapi_smtp_imported"}).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	source, err := openDB(driver, dsn)
	if err != nil {
		return errors.New("could not open GoAPI SMTP configuration source")
	}
	sqlDB, err := source.DB()
	if err != nil {
		return err
	}
	defer sqlDB.Close()
	var options []struct {
		Key   string
		Value string
	}
	keys := []string{"SMTPServer", "SMTPPort", "SMTPAccount", "SMTPToken", "SMTPFrom", "SMTPSSLEnabled", "SMTPForceAuthLogin"}
	if err = source.WithContext(ctx).Table("options").Where(clause.IN{Column: "key", Values: stringInterfaces(keys)}).Find(&options).Error; err != nil {
		return errors.New("could not read GoAPI SMTP configuration")
	}
	old := map[string]string{}
	for _, row := range options {
		old[row.Key] = row.Value
	}
	// Leave import retryable when GoAPI has not configured SMTP yet.
	if old["SMTPServer"] == "" {
		return nil
	}
	values := map[string]string{"smtp_host": old["SMTPServer"], "smtp_username": old["SMTPAccount"], "smtp_password": old["SMTPToken"], "smtp_from_email": old["SMTPFrom"], "smtp_from_name": "GoAPI", "smtp_tls_mode": "starttls", "smtp_force_auth_login": "false"}
	if values["smtp_from_email"] == "" {
		values["smtp_from_email"] = old["SMTPAccount"]
	}
	if old["SMTPPort"] != "" {
		values["smtp_port"] = old["SMTPPort"]
	}
	if old["SMTPSSLEnabled"] == "true" || old["SMTPPort"] == "465" {
		values["smtp_tls_mode"] = "tls"
	}
	if old["SMTPForceAuthLogin"] == "true" || strings.Contains(strings.ToLower(old["SMTPAccount"]), "outlook") {
		values["smtp_force_auth_login"] = "true"
	}
	for _, spec := range specs {
		if value, ok := values[spec.Key]; ok {
			if err = validate(spec, value); err != nil {
				return fmt.Errorf("GoAPI SMTP setting %s is invalid", spec.Key)
			}
		}
	}
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		marker := Setting{Key: "_goapi_smtp_imported", Value: "true", Category: "internal", Type: "boolean"}
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&marker)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}
		for key, value := range values {
			if key == "smtp_password" {
				value, err = s.encrypt(key, value)
				if err != nil {
					return err
				}
			}
			if err = tx.Model(&Setting{}).Where(clause.Eq{Column: "key", Value: key}).Updates(map[string]interface{}{"value": value}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
func stringInterfaces(values []string) []interface{} {
	result := make([]interface{}, len(values))
	for i, value := range values {
		result[i] = value
	}
	return result
}
