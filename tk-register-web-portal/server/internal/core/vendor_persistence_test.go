package core

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"testing"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

func persistenceEvents(t *testing.T, output string) []map[string]any {
	t.Helper()
	var events []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		_, encoded, ok := strings.Cut(line, "TK_REGISTER_PERSISTENCE ")
		if !ok {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(encoded), &event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	return events
}

func TestSubmissionPersistenceDoesNotRetry(t *testing.T) {
	secret := "private-email@example.com Bearer private-token password=private-password auth_json=private-auth SELECT private-sql"
	deadlock := &mysqldriver.MySQLError{Number: 1213, SQLState: [5]byte{'4', '0', '0', '0', '1'}, Message: secret}
	for _, tc := range []struct {
		name     string
		err      error
		failures int
		attempts int
		accepted bool
	}{
		{"normal", nil, 0, 1, true},
		{"wrapped deadlock", fmt.Errorf("%s: %w", secret, deadlock), 1, 1, false},
		{"deadlock", deadlock, 1, 1, false},
		{"lock wait timeout", &mysqldriver.MySQLError{Number: 1205, Message: secret}, 10, 1, false},
		{"duplicate key", &mysqldriver.MySQLError{Number: 1062, Message: secret}, 10, 1, false},
		{"connection lost", driver.ErrBadConn, 10, 1, false},
		{"deadline", context.DeadlineExceeded, 10, 1, false},
		{"untyped deadlock text", errors.New("1213 deadlock " + secret), 10, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, owner, tokenID, params := apiExecutionFixture(t)
			var logs bytes.Buffer
			service.ErrorLogger = log.New(&logs, "", 0)
			calls, attempts := 0, 0
			service.Transport = storageTransport(func(*http.Request) (*http.Response, error) {
				calls++
				return apiRemoteResponse(201, `{"id":13152,"alias":"claude-test-test"}`), nil
			})
			// Fail after the INSERT, when acceptance, the account link and the job
			// have also been written. Each failed transaction must undo all of them.
			if err := service.Store.DB.Callback().Create().After("gorm:create").Register("fixture:job_failure", func(tx *gorm.DB) {
				if tx.Statement.Table == "account_post_submit_sync_jobs" && tx.Error == nil {
					attempts++
					if attempts <= tc.failures {
						tx.AddError(tc.err)
					}
				}
			}); err != nil {
				t.Fatal(err)
			}
			result, err := service.CreateAPI(context.Background(), owner.ID, tokenID, params)
			if (err == nil) != tc.accepted || attempts != tc.attempts || calls != 1 || result.HTTPStatus != 201 {
				t.Fatalf("accepted=%v attempts=%d supplier calls=%d status=%d", err == nil, attempts, calls, result.HTTPStatus)
			}
			var sub Submission
			var account SyncedAccount
			var execution ProtocolExecution
			if err := service.Store.DB.First(&sub, result.SubmissionID).Error; err != nil {
				t.Fatal(err)
			}
			if err := service.Store.DB.Where("latest_submission_id = ?", sub.ID).First(&account).Error; err != nil {
				t.Fatal(err)
			}
			if err := service.Store.DB.Where("submission_id = ?", sub.ID).First(&execution).Error; err != nil {
				t.Fatal(err)
			}
			for _, table := range []string{"account_post_submit_sync_jobs"} {
				var count int64
				if err := service.Store.DB.Table(table).Count(&count).Error; err != nil {
					t.Fatal(err)
				}
				want := int64(0)
				if tc.accepted {
					want = 1
				}
				if count != want {
					t.Fatalf("%s has %d rows, want %d", table, count, want)
				}
			}
			if tc.accepted {
				if sub.Status != "accepted" || sub.RemoteID == nil || *sub.RemoteID != "13152" || account.CurrentSubmissionID == nil || *account.CurrentSubmissionID != sub.ID || execution.Status != "succeeded" {
					t.Fatal("result did not commit atomically")
				}
				if _, err := service.Store.AccountProxyInfo(context.Background(), account); err != nil {
					t.Fatal("accepted submission history unavailable", err)
				}

			} else if sub.Status != "running" || sub.RemoteID != nil || account.CurrentSubmissionID != nil || account.SubmissionStatus != "running" || execution.Status != "running" || execution.FinishedAt != nil {
				t.Fatal("failed transaction left partially accepted data")
			}
			events := persistenceEvents(t, logs.String())
			if tc.failures == 0 {
				if len(events) != 0 {
					t.Fatal("ordinary success unexpectedly logged as a persistence problem")
				}
			} else {
				if len(events) != tc.attempts {
					t.Fatalf("got %d diagnostic events for %d attempts", len(events), tc.attempts)
				}
				for _, event := range events {
					if event["outcome"] != "failed" || event["stage"] != "enqueue_post_submission_sync" || event["rollback"] != "succeeded" || event["submission_id"] != float64(sub.ID) || event["execution_id"] != float64(execution.ID) || event["request_id"] != result.RequestID {
						t.Fatalf("incorrect persistence metadata: %v", event)
					}
					if event["upstream_account_id"] != "13152" || event["upstream_alias"] != "claude-test-test" || event["upstream_status"] != "succeeded" || event["upstream_http_status"] != float64(201) || event["duration_ms"] == nil {
						t.Fatal("missing supplier response or elapsed time", event)
					}
					if strings.Contains(tc.name, "deadlock") && tc.name != "untyped deadlock text" && (event["mysql_number"] != float64(1213) || event["sql_state"] != "40001") {
						t.Fatal("deadlock code missing from safe diagnostic")
					}
				}
			}
			for _, value := range append(strings.Fields(secret), "fixture-password", "fixture-access", "fixture-refresh", "shared-vendor-token-fixture", "test-account@example.com") {
				if strings.Contains(logs.String(), value) {
					t.Fatalf("sensitive value leaked in diagnostic: %q", value)
				}
			}
		})
	}
}

// Inject commit/rollback transport failures independently of SQL. This ensures
// a successful rollback after a COMMIT error is not reported as proof of failure.
type persistenceFaultPool struct {
	gorm.ConnPool
	transaction *persistenceFaultTx
	begins      int
}

func (p *persistenceFaultPool) BeginTx(context.Context, *sql.TxOptions) (gorm.ConnPool, error) {
	p.begins++
	return p.transaction, nil
}

type persistenceFaultTx struct {
	gorm.ConnPool
	commitErr, rollbackErr error
	commits, rollbacks     int
}

func (t *persistenceFaultTx) Commit() error   { t.commits++; return t.commitErr }
func (t *persistenceFaultTx) Rollback() error { t.rollbacks++; return t.rollbackErr }

func TestPersistenceTransactionOutcomeDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name                             string
		applyErr, commitErr, rollbackErr error
		outcome, rollback                string
		commits                          int
	}{
		{"commit lost", nil, driver.ErrBadConn, nil, "commit_unknown", "succeeded", 1},
		{"commit already finished", nil, driver.ErrBadConn, sql.ErrTxDone, "commit_unknown", "already_finished", 1},
		{"rollback lost", errors.New("private SQL"), nil, driver.ErrBadConn, "failed", "failed", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, _, _, _ := apiExecutionFixture(t)
			var logs bytes.Buffer
			service.ErrorLogger = log.New(&logs, "", 0)
			transaction := &persistenceFaultTx{commitErr: tc.commitErr, rollbackErr: tc.rollbackErr}
			pool := &persistenceFaultPool{transaction: transaction}
			isolated := service.Store.DB.Session(&gorm.Session{NewDB: true})
			isolated.Statement = &gorm.Statement{DB: isolated, ConnPool: pool, Context: context.Background()}
			original := service.Store.DB
			service.Store.DB = isolated
			defer func() { service.Store.DB = original }()
			calls := 0
			err := service.persistExecutionResult(context.Background(), persistenceDiagnostic{started: time.Now()}, func(*gorm.DB) error { calls++; return tc.applyErr })
			if err == nil || calls != 1 || pool.begins != 1 || transaction.commits != tc.commits || transaction.rollbacks != 1 {
				t.Fatal("unexpected transaction attempts")
			}
			events := persistenceEvents(t, logs.String())
			if len(events) != 1 || events[0]["outcome"] != tc.outcome || events[0]["rollback"] != tc.rollback {
				t.Fatal("incorrect commit/rollback outcome", events)
			}
			if tc.rollback == "failed" && events[0]["rollback_error"] == nil {
				t.Fatal("rollback error omitted")
			}
			if strings.Contains(logs.String(), "private SQL") {
				t.Fatal("error text leaked")
			}
		})
	}
}

func TestPersistenceAliasRedaction(t *testing.T) {
	service, _, _, _ := apiExecutionFixture(t)
	for _, alias := range []string{"claude-test-test", "claude-温州润晨-test", "claude-secret-refresh-test", "https://user:secret-proxy@proxy.example", "auth_json={private-data}"} {
		var logs bytes.Buffer
		service.ErrorLogger = log.New(&logs, "", 0)
		service.logPersistence(persistenceDiagnostic{result: executionResult{alias: &alias}, secrets: []string{"secret-refresh", "secret-proxy"}, started: time.Now()}, "save_submission", "failed", "succeeded", errors.New("private SQL"), nil)
		events := persistenceEvents(t, logs.String())
		if len(events) != 1 {
			t.Fatal("missing event")
		}
		if alias == "claude-test-test" || alias == "claude-温州润晨-test" {
			if events[0]["upstream_alias"] != alias {
				t.Fatal("original alias changed")
			}
		}
		for _, secret := range []string{"secret-refresh", "secret-proxy", "private-data", "private SQL"} {
			if strings.Contains(logs.String(), secret) {
				t.Fatal("secret leaked")
			}
		}
	}
}
