package supplier

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func targetClient(t *testing.T, read func(*http.Request, int) *http.Response) (*Client, *supplierFixture) {
	t.Helper()
	f := &supplierFixture{t: t, read: func(r *http.Request, n int) *http.Response {
		q := r.URL.Query()
		if q.Get("keyword") == "" || q.Has("model") {
			t.Fatal("batch attempted unfiltered or model-filtered inventory read")
		}
		if r.Header.Get("Accept") != "application/json" {
			t.Fatal("missing JSON response header")
		}
		return read(r, n)
	}}
	return f.client(), f
}

func TestCollectTargetsOneSessionOrderedExactMatchesAndReadOnlyMetadata(t *testing.T) {
	targets := []Target{{Account: "First@example.com"}, {Account: "second@example.com"}, {Account: " FIRST@EXAMPLE.COM "}}
	var keywords []string
	client, f := targetClient(t, func(r *http.Request, n int) *http.Response {
		keywords = append(keywords, r.URL.Query().Get("keyword"))
		row := editableFixture(n, r.URL.Query().Get("keyword"))
		row["a1_status"], row["is_locked"] = "using", map[string]any{"future_lock": "unsupported_for_edits"}
		row["remark"], row["model"] = []any{"not edit metadata"}, map[string]any{"future": true}
		// Valid account collection must not need password, plan dates, or other
		// form fields used only when preserving an edit payload.
		row["auth_config"] = map[string]any{"account": strings.ToUpper(r.URL.Query().Get("keyword")), "plan": "claude/cc-pro"}
		return jsonResponse(200, map[string]any{"total": 2, "list": []any{editableFixture(100+n, "prefix-"+r.URL.Query().Get("keyword")), row}})
	})
	got, err := client.CollectTargets(context.Background(), "supplier-user", "private-password-中文", targets)
	if err != nil || len(got) != 3 || f.logins != 1 || f.keys != 1 || f.reads != 2 {
		t.Fatalf("incorrect shared batch: result=%+v err=%v logins=%d reads=%d", got, err, f.logins, f.reads)
	}
	if !reflect.DeepEqual(keywords, []string{"First@example.com", "second@example.com"}) {
		t.Fatal(keywords)
	}
	for i, result := range got {
		if result.Target != targets[i] || result.Err != nil || result.Account == nil || normalizeAccount(result.Account.Account) != normalizeAccount(targets[i].Account) || result.Account.Status == nil || *result.Account.Status != "active" || result.Account.Plan != "claude/cc-pro" {
			t.Fatalf("incorrect result[%d]: %+v", i, result)
		}
	}
	if got[0].Account.ID != got[2].Account.ID {
		t.Fatal("duplicate target was reread")
	}
}

func TestCollectTargetsPartialErrorsDoNotDiscardSuccessfulResults(t *testing.T) {
	targets := []Target{{Account: "good@example.com"}, {Account: "missing@example.com"}, {Account: "broken@example.com"}, {Account: "later@example.com"}}
	client, f := targetClient(t, func(r *http.Request, n int) *http.Response {
		switch r.URL.Query().Get("keyword") {
		case "missing@example.com":
			return jsonResponse(200, map[string]any{"total": 1, "list": []any{editableFixture(n, "prefix-missing@example.com")}})
		case "broken@example.com":
			return jsonResponse(503, map[string]any{"error": "TOKEN_SECRET"})
		default:
			return jsonResponse(200, map[string]any{"total": 1, "list": []any{editableFixture(n, r.URL.Query().Get("keyword"))}})
		}
	})
	got, err := client.CollectTargets(context.Background(), "supplier-user", "private-password-中文", targets)
	if err != nil || len(got) != 4 || f.reads != 4 || f.logins != 1 {
		t.Fatalf("batch failed: %+v %v", got, err)
	}
	if got[0].Account == nil || got[0].Err != nil || got[3].Account == nil || got[3].Err != nil {
		t.Fatal("lost successful results")
	}
	requireSupplierCode(t, got[1].Err, CodeAccountNotFound)
	requireSupplierCode(t, got[2].Err, CodeUnavailable)
	if got[1].Account != nil || got[2].Account != nil {
		t.Fatal("failed result included account")
	}
}

func TestCollectTargetsAmbiguityRequiresConfirmedWebsiteID(t *testing.T) {
	targets := []Target{{Account: "account@example.com"}, {Account: "account@example.com", RecordID: "13152"}, {Account: "account@example.com", RecordID: "42"}}
	client, f := targetClient(t, func(*http.Request, int) *http.Response {
		return jsonResponse(200, map[string]any{"total": 2, "list": []any{editableFixture(41, "account@example.com"), editableFixture(42, "account@example.com")}})
	})
	got, err := client.CollectTargets(context.Background(), "supplier-user", "private-password-中文", targets)
	if err != nil || f.logins != 1 || f.reads != 3 {
		t.Fatal(err)
	}
	requireSupplierCode(t, got[0].Err, CodeAccountAmbiguous)
	requireSupplierCode(t, got[1].Err, CodeAccountAmbiguous)
	if got[2].Err != nil || got[2].Account == nil || got[2].Account.ID != "42" {
		t.Fatal("did not select confirmed website row")
	}
}

func TestCollectTargetsFilteredPaginationPerAccount(t *testing.T) {
	targets := []Target{{Account: "one@example.com"}, {Account: "two@example.com"}}
	seen := make(map[string][]string)
	client, f := targetClient(t, func(r *http.Request, n int) *http.Response {
		q := r.URL.Query()
		email, page := q.Get("keyword"), q.Get("page")
		seen[email] = append(seen[email], page)
		account := "prefix-" + email
		if page == "2" {
			account = email
		}
		return jsonResponse(200, map[string]any{"total": 2, "list": []any{editableFixture(n, account)}})
	})
	got, err := client.CollectTargets(context.Background(), "supplier-user", "private-password-中文", targets)
	if err != nil || f.logins != 1 || f.reads != 4 {
		t.Fatal(err)
	}
	for i, target := range targets {
		if got[i].Err != nil || got[i].Account == nil || !reflect.DeepEqual(seen[target.Account], []string{"1", "2"}) {
			t.Fatal("incorrect filtered pagination", seen, got[i])
		}
	}
}

func TestCollectTargetsOneReauthenticationAcrossWholeBatch(t *testing.T) {
	targets := []Target{{Account: "one@example.com"}, {Account: "two@example.com"}, {Account: "three@example.com"}, {Account: "four@example.com"}}
	client, f := targetClient(t, func(r *http.Request, n int) *http.Response {
		if n == 1 || r.URL.Query().Get("keyword") == "three@example.com" {
			return jsonResponse(401, map[string]any{"error": "TOKEN_SECRET"})
		}
		return jsonResponse(200, map[string]any{"total": 1, "list": []any{editableFixture(n, r.URL.Query().Get("keyword"))}})
	})
	got, err := client.CollectTargets(context.Background(), "supplier-user", "private-password-中文", targets)
	if err != nil || f.logins != 2 || f.keys != 2 || f.reads != 4 {
		t.Fatalf("unexpected retries: err=%v logins=%d reads=%d", err, f.logins, f.reads)
	}
	if got[0].Err != nil || got[1].Err != nil || got[0].Account == nil || got[1].Account == nil {
		t.Fatal("first reauthentication did not recover")
	}
	requireSupplierCode(t, got[2].Err, CodeSessionExpired)
	requireSupplierCode(t, got[3].Err, CodeSessionExpired)
}

func TestCollectTargetsInitialLoginFailureIsTopLevel(t *testing.T) {
	calls := 0
	client := &Client{transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Path != apiPrefix+"/pubkey" {
			t.Fatal("attempted read after login failure")
		}
		return jsonResponse(401, map[string]any{"error": "TOKEN_SECRET"}), nil
	})}
	results, err := client.CollectTargets(context.Background(), "supplier-user", "private-password-中文", []Target{{Account: "one@example.com"}})
	requireSupplierCode(t, err, CodeCredentialsInvalid)
	if results != nil || calls != 1 {
		t.Fatal("login failure produced results or retried")
	}
}

func TestCollectTargetsEmptyAndOversizedPageDoNotLogin(t *testing.T) {
	client := &Client{transport: roundTripFunc(func(*http.Request) (*http.Response, error) { t.Fatal("unexpected network request"); return nil, nil })}
	results, err := client.CollectTargets(context.Background(), "supplier-user", "private-password-中文", nil)
	if err != nil || len(results) != 0 {
		t.Fatal("empty page failed")
	}
	_, err = client.CollectTargets(context.Background(), "supplier-user", "private-password-中文", make([]Target, 21))
	requireSupplierCode(t, err, CodeContractMismatch)
}

func TestCollectTargetsRequestAndPerAccountPageBudgets(t *testing.T) {
	targets := make([]Target, 20)
	for i := range targets {
		targets[i] = Target{Account: fmt.Sprintf("account%d@example.com", i)}
	}
	counts := make(map[string]int)
	client, f := targetClient(t, func(r *http.Request, n int) *http.Response {
		q := r.URL.Query()
		email := q.Get("keyword")
		counts[email]++
		if q.Get("page") != strconv.Itoa(counts[email]) || counts[email] > maxTargetSearchPages {
			t.Fatal("pagination escaped per-target bound")
		}
		return jsonResponse(200, map[string]any{"total": 11, "list": []any{editableFixture(n, "prefix-"+email)}})
	})
	got, err := client.CollectTargets(context.Background(), "supplier-user", "private-password-中文", targets)
	if err != nil || len(got) != 20 || f.logins != 1 || f.reads != maxBatchSearchRequests {
		t.Fatalf("unbounded batch: err=%v reads=%d logins=%d", err, f.reads, f.logins)
	}
	for _, result := range got {
		if result.Err == nil || result.Account != nil {
			t.Fatal("incomplete lookup was accepted")
		}
	}
	requireSupplierCode(t, got[len(got)-1].Err, CodeUnavailable)
}

func TestCollectTargetsStopsNetworkAfterCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client, f := targetClient(t, func(r *http.Request, n int) *http.Response {
		deadline, ok := r.Context().Deadline()
		if !ok || time.Until(deadline) > maxBatchDuration {
			t.Fatal("batch has no bounded deadline")
		}
		cancel()
		return jsonResponse(200, map[string]any{"total": 1, "list": []any{editableFixture(n, r.URL.Query().Get("keyword"))}})
	})
	got, err := client.CollectTargets(ctx, "supplier-user", "private-password-中文", []Target{{Account: "first@example.com"}, {Account: "second@example.com"}})
	if err != nil || len(got) != 2 || f.reads != 1 || got[0].Account == nil {
		t.Fatalf("cancellation lost completed result: %+v %v", got, err)
	}
	requireSupplierCode(t, got[1].Err, CodeUnavailable)
}

func TestCollectTargetsReadTimeoutDoesNotStarveRemainingAccounts(t *testing.T) {
	client, f := targetClient(t, func(r *http.Request, n int) *http.Response {
		return jsonResponse(200, map[string]any{"total": 1, "list": []any{editableFixture(n, r.URL.Query().Get("keyword"))}})
	})
	base := client.transport
	client.transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == apiPrefix+"/accounts" {
			deadline, ok := r.Context().Deadline()
			if !ok || time.Until(deadline) > maxTargetDuration {
				t.Fatal("target has no individual deadline")
			}
			if r.URL.Query().Get("keyword") == "slow@example.com" {
				return nil, context.DeadlineExceeded
			}
		}
		return base.RoundTrip(r)
	})
	got, err := client.CollectTargets(context.Background(), "supplier-user", "private-password-中文", []Target{{Account: "slow@example.com"}, {Account: "healthy@example.com"}})
	if err != nil || len(got) != 2 || f.logins != 1 || f.reads != 1 {
		t.Fatalf("timeout damaged batch: %+v %v", got, err)
	}
	requireSupplierCode(t, got[0].Err, CodeUnavailable)
	if got[1].Err != nil || got[1].Account == nil {
		t.Fatal("healthy account was skipped after timeout")
	}
}
