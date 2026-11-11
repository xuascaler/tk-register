package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"

	mysqldriver "github.com/go-sql-driver/mysql"
)

// Log structural error metadata only. Database and transport error strings can
// contain SQL, account credentials, URLs, or submitted authentication JSON.
func logUnexpectedVendorFailure(w http.ResponseWriter, err error) {
	event := struct {
		RequestID   string `json:"request_id,omitempty"`
		Stage       string `json:"stage"`
		Category    string `json:"category"`
		ErrorType   string `json:"error_type"`
		MySQLNumber uint16 `json:"mysql_number,omitempty"`
		SQLState    string `json:"sql_state,omitempty"`
	}{
		RequestID: safeDiagnosticRequestID(w.Header().Get("X-Request-ID")),
		Stage:     "vendor_failure",
		Category:  "internal",
		ErrorType: fmt.Sprintf("%T", err),
	}
	var mysqlErr *mysqldriver.MySQLError
	var networkErr net.Error
	switch {
	case errors.Is(err, context.Canceled):
		event.Category = "context_canceled"
	case errors.Is(err, context.DeadlineExceeded):
		event.Category = "deadline_exceeded"
	case errors.As(err, &mysqlErr):
		event.Category = "database"
		event.MySQLNumber = mysqlErr.Number
		state := string(mysqlErr.SQLState[:])
		valid := true
		for _, c := range state {
			if !(c >= '0' && c <= '9') && !(c >= 'A' && c <= 'Z') {
				valid = false
			}
		}
		if valid {
			event.SQLState = state
		}
		if mysqlErr.Number == 1213 {
			event.Category = "database_deadlock"
		} else if mysqlErr.Number == 1205 {
			event.Category = "database_lock_timeout"
		}
	case errors.As(err, &networkErr):
		event.Category = "network"
		if networkErr.Timeout() {
			event.Category = "network_timeout"
		}
	}
	// These fields cannot fail JSON encoding; none is derived from err.Error().
	encoded, _ := json.Marshal(event)
	log.Printf("TK_INTERNAL_ERROR %s", encoded)
}

func safeDiagnosticRequestID(value string) string {
	if len(value) > 128 {
		return ""
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z') && !(c >= 'A' && c <= 'Z') && !(c >= '0' && c <= '9') && c != '-' && c != '_' {
			return ""
		}
	}
	return value
}
