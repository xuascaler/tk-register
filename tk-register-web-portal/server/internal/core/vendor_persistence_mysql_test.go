package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
)

// Opt-in integration test. Refuse any existing schema or a non-test database.
// The supplier is a fixture transport; no real account is submitted.
func TestMySQLSubmissionPersistence(t *testing.T) {
	dsn := os.Getenv("TK_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("set TK_TEST_MYSQL_DSN to a fresh tk_persistence_test_* database")
	}
	config, err := mysqldriver.ParseDSN(dsn)
	if err != nil || !strings.HasPrefix(config.DBName, "tk_persistence_test_") {
		t.Fatal("integration test requires a dedicated tk_persistence_test_* database")
	}
	probe, err := openDB("mysql", dsn)
	if err != nil {
		t.Fatal("could not connect to test database")
	}
	var count int64
	err = probe.Raw("SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE()").Scan(&count).Error
	connection, connectionErr := probe.DB()
	if connectionErr == nil {
		_ = connection.Close()
	}
	if err != nil || connectionErr != nil || count != 0 {
		t.Fatal("integration test requires an empty schema; existing tables will not be changed")
	}
	store, err := Open("mysql", dsn, testKey)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	db, err := store.DB.DB()
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(16)
	service, owner, tokenID, _ := apiExecutionFixtureWithStore(t, store)
	_, version, err := service.currentVersion(context.Background(), 1, "submit_account")
	if err != nil {
		t.Fatal(err)
	}
	const parallel = 10
	paramsFor := func(email string) map[string]any {
		auth := credentialTestAuth()
		auth["email"] = email
		return map[string]any{
			"plan": "claude/cc-Team Premium", "account": email, "password": "synthetic-password", "register_location": "MY",
			"register_time": "2026-10-01", "plan_start_time": "2026-10-01", "plan_end_time": "2026-11-01", "auth_json": auth,
		}
	}

	if !t.Run("parallel_auth_history_without_retry", func(t *testing.T) {
		var submissions []Submission
		base := time.Now().UTC().Add(-time.Hour)
		for i := range parallel {
			email := fmt.Sprintf("auth-history-%d@example.com", i)
			submissions = append(submissions, syncSubmission(t, store, owner.ID, 1, version.ID, email, "claude/cc-Team Premium", "accepted", fmt.Sprint(1000+i), base.Add(time.Duration(i)*time.Millisecond)))
		}
		if _, err := store.CollectionTargets(context.Background(), owner.ID, 0); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		// Release writers before taking account locks, as independent requests do.
		start := make(chan struct{})
		results := make(chan error, parallel)
		for _, sub := range submissions {
			go func() {
				<-start
				var account SyncedAccount
				if err := store.DB.WithContext(ctx).Where("current_submission_id = ?", sub.ID).First(&account).Error; err != nil {
					results <- err
					return
				}
				raw, _ := json.Marshal(paramsFor(sub.Account)["auth_json"])
				audit, err := store.BeginAuthJSONUpdateWithMethod(ctx, account, owner.ID, fmt.Sprintf("history-%d", sub.ID), "web", nil, nil, "manual", "", AuthJSONCredentialWrite{Raw: raw})
				if err == nil {
					_, err = store.FinishAuthJSONUpdate(ctx, audit, "updated", fmt.Sprint(1000+sub.ID), "", time.Now().UTC())
				}
				results <- err
			}()
		}
		close(start)
		for range parallel {
			if err := <-results; err != nil {
				t.Errorf("concurrent auth history write failed: %v (%+v)", err, classifyPersistenceError(err))
			}
		}
	}) {
		return
	}

	t.Run("same_account_history_serializes_without_retry", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		var account SyncedAccount
		if err := store.DB.First(&account).Error; err != nil {
			t.Fatal(err)
		}
		info, err := store.AccountProxyInfo(ctx, account)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := store.ReplaceAccountProxy(ctx, account, info.Revision, "http://new.example:8080")
		if err != nil {
			t.Fatal(err)
		}
		type outcome struct {
			audit AccountAuthJSONAudit
			err   error
		}
		results := make(chan outcome, parallel)
		start := make(chan struct{})
		for i := range parallel {
			go func() {
				<-start
				audit, err := store.BeginAuthJSONUpdateWithMethod(ctx, account, owner.ID, fmt.Sprintf("same-account-proxy-%d", i), "web", nil, nil, "proxy", "", AuthJSONCredentialWrite{Raw: raw, ExpectedRevision: &info.Revision})
				results <- outcome{audit, err}
			}()
		}
		close(start)
		var winner AccountAuthJSONAudit
		successes, busy := 0, 0
		for range parallel {
			result := <-results
			if result.err == nil {
				successes++
				winner = result.audit
			} else if errors.Is(result.err, ErrCurrentCredentialRunning) {
				busy++
			} else {
				t.Errorf("unexpected concurrent error: %v", result.err)
			}
		}
		if successes != 1 || busy != parallel-1 {
			t.Fatalf("successes=%d busy=%d", successes, busy)
		}
		if _, err := store.FinishAuthJSONUpdate(ctx, winner, "updated", "13152", "", time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
		after, err := store.AccountProxyInfo(ctx, account)
		if err != nil || after.Revision == info.Revision {
			t.Fatal("confirmed history did not advance", err)
		}
		_, err = store.BeginAuthJSONUpdateWithMethod(ctx, account, owner.ID, "stale-version-after-completion", "web", nil, nil, "proxy", "", AuthJSONCredentialWrite{Raw: raw, ExpectedRevision: &info.Revision})
		if !errors.Is(err, ErrCurrentCredentialChanged) {
			t.Fatal("stale dialog accepted", err)
		}
	})

	t.Run("parallel_api_submissions_send_once", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var calls atomic.Int32
		release := make(chan struct{})
		var logs bytes.Buffer
		service.ErrorLogger = log.New(&logs, "", 0)
		service.Transport = storageTransport(func(*http.Request) (*http.Response, error) {
			n := calls.Add(1)
			if n == parallel {
				close(release)
			}
			select {
			case <-release:
				return apiRemoteResponse(201, fmt.Sprintf(`{"id":%d}`, 2000+n)), nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		})
		results := make(chan error, parallel)
		for i := range parallel {
			go func() {
				_, err := service.CreateAPI(ctx, owner.ID, tokenID, paramsFor(fmt.Sprintf("parallel-api-%d@example.com", i)))
				results <- err
			}()
		}
		for range parallel {
			if err := <-results; err != nil {
				t.Errorf("parallel API submission failed: %v", err)
			}
		}
		if calls.Load() != parallel {
			t.Fatalf("%d upstream calls for %d submissions", calls.Load(), parallel)
		}
		var accepted, histories, jobs, incomplete int64
		checks := []struct {
			model any
			where string
			out   *int64
			want  int64
		}{
			{&Submission{}, "status = 'accepted'", &accepted, 2 * parallel},
			{&AccountAuthJSONAudit{}, "status = 'updated' AND auth_json_ciphertext IS NOT NULL", &histories, parallel + 1},
			{&PostSubmissionSyncJob{}, "status = 'pending'", &jobs, parallel},
			{&ProtocolExecution{}, "status <> 'succeeded'", &incomplete, 0},
		}
		for _, check := range checks {
			if err := store.DB.Model(check.model).Where(check.where).Count(check.out).Error; err != nil || *check.out != check.want {
				t.Errorf("%T count=%d want=%d: %v", check.model, *check.out, check.want, err)
			}
		}
		for _, event := range persistenceEvents(t, logs.String()) {
			if event["outcome"] == "failed" {
				t.Errorf("unrecovered persistence failure: %v", event)
			}
		}
	})
}
