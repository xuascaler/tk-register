package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

type executionPersistenceError struct {
	stage string
	cause error
}

// Driver error text may contain SQL and credentials. Preserve the typed cause,
// but never expose its text through this wrapper or the diagnostic log.
func (e *executionPersistenceError) Error() string {
	return "submission persistence failed at " + e.stage
}
func (e *executionPersistenceError) Unwrap() error { return e.cause }
func persistenceFailure(stage string, err error) error {
	if err == nil {
		return nil
	}
	var staged *executionPersistenceError
	if errors.As(err, &staged) {
		return err
	} // Keep the most specific failing step.
	return &executionPersistenceError{stage: stage, cause: err}
}

type persistenceDiagnostic struct {
	record    Submission
	execution ProtocolExecution
	result    executionResult
	secrets   []string
	started   time.Time
}

// Execute exactly one local transaction. The supplier has already responded;
// neither a database failure nor an uncertain COMMIT may cause resubmission.
func (v *VendorService) persistExecutionResult(ctx context.Context, diagnostic persistenceDiagnostic, apply func(*gorm.DB) error) error {
	tx := v.Store.DB.WithContext(ctx).Begin()
	if tx.Error != nil {
		v.logPersistence(diagnostic, "begin_transaction", "not_started", "not_needed", tx.Error, nil)
		return persistenceFailure("begin_transaction", tx.Error)
	}
	completed := false
	defer func() {
		if !completed {
			// Keep panic rollback semantics without putting a potentially secret panic
			// value in logs; the caller's recovery policy still handles the panic.
			rollback, rollbackErr := rollbackExecutionTx(tx)
			v.logPersistence(diagnostic, "transaction_panic", "failed", rollback, nil, rollbackErr)
		}
	}()
	if err := apply(tx); err != nil {
		rollback, rollbackErr := rollbackExecutionTx(tx)
		completed = true
		v.logPersistence(diagnostic, "transaction", "failed", rollback, err, rollbackErr)
		return persistenceFailure("transaction", err)
	}
	err := tx.Commit().Error
	completed = true
	if err != nil {
		rollback, rollbackErr := rollbackExecutionTx(tx)
		// Even a later Rollback cannot prove whether a lost COMMIT reply was
		// committed remotely. Conservatively retain an explicit uncertain outcome.
		v.logPersistence(diagnostic, "commit_transaction", "commit_unknown", rollback, err, rollbackErr)
		return persistenceFailure("commit_transaction", err)
	}
	return nil
}

func rollbackExecutionTx(tx *gorm.DB) (string, error) {
	// GORM Rollback adds the error to tx.Error, which can already hold a COMMIT
	// error. Read the rollback result directly so the two remain distinguishable.
	committer, ok := tx.Statement.ConnPool.(gorm.TxCommitter)
	if !ok {
		return "failed", gorm.ErrInvalidTransaction
	}
	err := committer.Rollback()
	if err == nil {
		return "succeeded", nil
	}
	if errors.Is(err, sql.ErrTxDone) {
		return "already_finished", nil
	}
	return "failed", err
}

type persistenceErrorInfo struct {
	Category    string `json:"category,omitempty"`
	ErrorType   string `json:"error_type,omitempty"`
	MySQLNumber uint16 `json:"mysql_number,omitempty"`
	SQLState    string `json:"sql_state,omitempty"`
}

func classifyPersistenceError(err error) persistenceErrorInfo {
	if err == nil {
		return persistenceErrorInfo{}
	}
	info := persistenceErrorInfo{Category: "internal", ErrorType: fmt.Sprintf("%T", err)}
	var mysqlErr *mysqldriver.MySQLError
	switch {
	case errors.Is(err, context.Canceled):
		info.Category = "context_canceled"
	case errors.Is(err, context.DeadlineExceeded):
		info.Category = "deadline_exceeded"
	case errors.As(err, &mysqlErr):
		info.Category, info.MySQLNumber = "database", mysqlErr.Number
		if mysqlErr.Number == 1213 {
			info.Category = "database_deadlock"
		} else if mysqlErr.Number == 1205 {
			info.Category = "database_lock_timeout"
		}
		valid := true
		for _, c := range mysqlErr.SQLState {
			if !(c >= '0' && c <= '9') && !(c >= 'A' && c <= 'Z') {
				valid = false
			}
		}
		if valid {
			info.SQLState = string(mysqlErr.SQLState[:])
		}
	}
	return info
}

var persistenceRequestIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func (v *VendorService) logPersistence(d persistenceDiagnostic, stage, outcome, rollback string, err, rollbackErr error) {
	var staged *executionPersistenceError
	if errors.As(err, &staged) {
		stage, err = staged.stage, staged.cause
	}
	event := struct {
		SubmissionID       uint64                `json:"submission_id"`
		ExecutionID        uint64                `json:"execution_id"`
		RequestID          string                `json:"request_id,omitempty"`
		UpstreamStatus     string                `json:"upstream_status"`
		UpstreamHTTPStatus *int                  `json:"upstream_http_status,omitempty"`
		UpstreamAccountID  string                `json:"upstream_account_id,omitempty"`
		UpstreamAlias      string                `json:"upstream_alias,omitempty"`
		Stage              string                `json:"stage"`
		Outcome            string                `json:"outcome"`
		Rollback           string                `json:"rollback"`
		RollbackError      *persistenceErrorInfo `json:"rollback_error,omitempty"`
		DurationMS         int64                 `json:"duration_ms"`
		persistenceErrorInfo
	}{SubmissionID: d.record.ID, ExecutionID: d.execution.ID, UpstreamStatus: d.result.status,
		UpstreamHTTPStatus: d.result.httpStatus, Stage: stage, Outcome: outcome, Rollback: rollback,
		DurationMS: time.Since(d.started).Milliseconds(), persistenceErrorInfo: classifyPersistenceError(err)}
	if persistenceRequestIDPattern.MatchString(d.execution.RequestID) {
		event.RequestID = d.execution.RequestID
	}
	replace := logSecretReplacer(d.secrets)
	safeIdentifier := func(value *string) string {
		if value == nil {
			return ""
		}
		safe := redactLogText(replace.Replace(*value))
		// Omit credential-bearing documents masquerading as an identifier. Ordinary
		// supplier aliases (including non-English text) remain exactly as returned.
		if logCredentialMarker.MatchString(logRedactedCredential.ReplaceAllString(safe, "")) {
			return "[REDACTED]"
		}
		if len(safe) > 512 || strings.ContainsAny(safe, "\r\n\x00") {
			return "[OMITTED invalid identifier]"
		}
		return safe
	}
	event.UpstreamAccountID, event.UpstreamAlias = safeIdentifier(d.result.remoteID), safeIdentifier(d.result.alias)
	if rollbackErr != nil {
		info := classifyPersistenceError(rollbackErr)
		event.RollbackError = &info
	}
	// Never include err.Error(), SQL, credentials, or the full upstream response.
	encoded, _ := json.Marshal(event)
	logger := v.ErrorLogger
	if logger == nil {
		logger = log.Default()
	}
	logger.Printf("TK_REGISTER_PERSISTENCE %s", encoded)
}
