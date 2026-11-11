package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	mysqldriver "github.com/go-sql-driver/mysql"
	"goapi-tk-register/server/internal/core"
)

func captureInternalErrorLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var output bytes.Buffer
	writer := log.Writer()
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(writer) })
	return &output
}

func TestUnexpectedVendorFailureLogsDeadlockWithoutSecrets(t *testing.T) {
	output := captureInternalErrorLog(t)
	response := httptest.NewRecorder()
	response.Header().Set("X-Request-ID", "safe-request_123")
	secret := "user@example.com Bearer secret-token password auth_json SELECT"
	err := fmt.Errorf("query %s: %w", secret, &mysqldriver.MySQLError{
		Number: 1213, SQLState: [5]byte{'4', '0', '0', '0', '1'}, Message: secret,
	})
	vendorFailure(response, err)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), `"code":"UNAVAILABLE"`) {
		t.Fatalf("unexpected response: %d %s", response.Code, response.Body.String())
	}
	line := output.String()
	_, encoded, found := strings.Cut(line, "TK_INTERNAL_ERROR ")
	if !found {
		t.Fatalf("missing diagnostic event: %s", line)
	}
	var event map[string]any
	if err := json.Unmarshal([]byte(encoded), &event); err != nil {
		t.Fatal(err)
	}
	if event["request_id"] != "safe-request_123" || event["category"] != "database_deadlock" || event["mysql_number"] != float64(1213) || event["sql_state"] != "40001" {
		t.Fatalf("missing safe database metadata: %v", event)
	}
	for _, value := range strings.Fields(secret) {
		if strings.Contains(line, value) || strings.Contains(response.Body.String(), value) {
			t.Fatalf("sensitive diagnostic text leaked: %q", value)
		}
	}
}

func TestExpectedVendorFailuresDoNotLogInternalError(t *testing.T) {
	output := captureInternalErrorLog(t)
	for _, err := range []error{core.ErrNotFound, &core.VendorError{Status: 409, Code: "CONFLICT", Message: "Conflict"}} {
		response := httptest.NewRecorder()
		vendorFailure(response, err)
		if response.Code < 400 || response.Code >= 500 {
			t.Fatalf("expected unchanged client response, got %d", response.Code)
		}
	}
	if output.Len() != 0 {
		t.Fatalf("expected client failure logged as internal: %s", output.String())
	}
}

func TestUnexpectedVendorFailureClassifiesSafeMetadata(t *testing.T) {
	for _, test := range []struct {
		name, category string
		err            error
	}{
		{"canceled", "context_canceled", fmt.Errorf("private input: %w", context.Canceled)},
		{"deadline", "deadline_exceeded", context.DeadlineExceeded},
		{"lock_timeout", "database_lock_timeout", &mysqldriver.MySQLError{Number: 1205, Message: "private input"}},
		{"unknown", "internal", errors.New("private input")},
	} {
		t.Run(test.name, func(t *testing.T) {
			output := captureInternalErrorLog(t)
			response := httptest.NewRecorder()
			response.Header().Set("X-Request-ID", "secret@example.com\nprivate input")
			vendorFailure(response, test.err)
			if !strings.Contains(output.String(), `"category":"`+test.category+`"`) || strings.Contains(output.String(), "private input") || strings.Contains(output.String(), "secret@example.com") {
				t.Fatalf("unsafe or incorrect diagnostic: %s", output.String())
			}
		})
	}
}
