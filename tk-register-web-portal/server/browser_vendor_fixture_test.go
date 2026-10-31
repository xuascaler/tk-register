package main

// Test-only transport: no network connection is made, even for a production
// vendor URL. These endpoints are never compiled into the TK Register binary.
import (
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"goapi-tk-register/server/common"
	"goapi-tk-register/server/internal/core"
)

const browserVendorToken = "fixture-vendor-token-not-a-real-secret"

type browserVendorFixture struct {
	t                                             *testing.T
	a                                             *app
	mu                                            sync.Mutex
	pushes, metrics, bills                        int
	mode                                          string
	matchedOwnerID, aggregateFalse, zeroPreserved bool
}

func newBrowserVendorFixture(t *testing.T, a *app) *browserVendorFixture {
	f := &browserVendorFixture{t: t, a: a, mode: "success"}
	a.vendorTransport = f
	return f
}

func (f *browserVendorFixture) RoundTrip(r *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Scheme+"://"+r.URL.Host != core.VendorOrigin || r.Method != http.MethodPost {
		f.t.Error("unexpected vendor origin or method")
		return nil, errors.New("fixture rejected request")
	}
	if r.Header.Get("Authorization") != "Bearer "+browserVendorToken {
		f.t.Error("incorrect mock vendor credential")
		return nil, errors.New("fixture rejected credential")
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	var input map[string]any
	if err := common.Unmarshal(raw, &input); err != nil {
		return nil, err
	}
	status := 200
	var body any
	switch r.URL.Path {
	case "/supplier-account/api/openapi/claude/accounts":
		f.pushes++
		f.zeroPreserved = input["balance"] == float64(0)
		if f.mode == "timeout" {
			return nil, errors.New("simulated response lost after remote acceptance")
		}
		if f.mode == "reject" {
			status = 400
			body = map[string]any{"error": "invalid account", "echo": input["password"]}
		} else {
			body = map[string]any{"id": uint64(9007199254740993), "alias": "claude-fixture-account", "msg": "accepted"}
		}
	case "/supplier-account/api/openapi/metrics/query":
		f.metrics++
		// Inspect the original bytes so IDs above JavaScript's safe integer limit
		// cannot pass after an accidental float round-trip.
		f.matchedOwnerID = strings.Contains(string(raw), `"account_ids":[9007199254740993]`)
		f.aggregateFalse = input["aggregate"] == false
		body = map[string]any{"accounts": []any{map[string]any{"id": uint64(9007199254740993), "aid": 10725, "account_alias": "claude-fixture-account", "status": "online", "requests": 42, "cost": 0, "success_rate": 99.5, "prompt_tokens": 1120273, "completion_tokens": 15723659}}}
		if f.mode == "empty" {
			body = map[string]any{"accounts": []any{}}
		}
	case "/supplier-account/api/openapi/bill/query":
		f.bills++
		f.matchedOwnerID = strings.Contains(string(raw), `"account_ids":[9007199254740993]`)
		body = map[string]any{"list": []any{map[string]any{"id": uint64(9007199254740993), "request_count": 42, "prompt_tokens": 1120273, "completion_tokens": 15723659, "cache_read_input_tokens": 10000, "cache_write_input_tokens": 2000, "cost": 4482.04}}, "range": map[string]any{"start_ds": strings.ReplaceAll(input["start_ds"].(string), "-", ""), "end_ds": strings.ReplaceAll(input["end_ds"].(string), "-", "")}}
		if f.mode == "empty" {
			body = map[string]any{"list": []any{}, "range": map[string]any{}}
		}
	default:
		f.t.Error("unexpected vendor path")
		return nil, errors.New("fixture rejected path")
	}
	encoded, err := common.Marshal(body)
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(encoded))), Request: r}, nil
}

func (f *browserVendorFixture) handle(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path == "/__test/vendor-mode" {
		mode := r.URL.Query().Get("mode")
		if mode != "success" && mode != "timeout" && mode != "reject" && mode != "empty" {
			w.WriteHeader(400)
			return true
		}
		f.mu.Lock()
		f.mode = mode
		f.mu.Unlock()
		ok(w, map[string]bool{"updated": true})
		return true
	}
	if r.URL.Path != "/__test/vendor-audit" {
		return false
	}
	var rows []core.Submission
	var credentials []core.VendorCredential
	var executions []core.ProtocolExecution
	if f.a.store.DB.Find(&rows).Error != nil || f.a.store.DB.Find(&credentials).Error != nil || f.a.store.DB.Find(&executions).Error != nil {
		w.WriteHeader(500)
		return true
	}
	encrypted := len(credentials) > 0
	for _, c := range credentials {
		encrypted = encrypted && c.SecretCiphertext != "" && !strings.Contains(c.SecretCiphertext, browserVendorToken)
	}
	safe := true
	snapshot := false
	states := map[string]int{}
	for _, row := range rows {
		states[row.Status]++
		safe = safe && row.SecretParametersCiphertext != "" && !strings.Contains(row.SecretParametersCiphertext, "fixture-account-password") && !strings.Contains(row.ParametersJSON, "fixture-account-password") && !strings.Contains(row.ParametersJSON, "fixture-access-token")
	}
	for _, x := range executions {
		safe = safe && !strings.Contains(x.RequestJSON+x.ResultJSON, "fixture-account-password") && !strings.Contains(x.RequestJSON+x.ResultJSON, "fixture-access-token") && !strings.Contains(x.RequestJSON+x.ResultJSON, browserVendorToken)
		if x.Operation == "metrics" && x.RemoteIDSnapshot != nil && *x.RemoteIDSnapshot == "9007199254740993" {
			snapshot = true
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	ok(w, map[string]any{"pushes": f.pushes, "metrics": f.metrics, "bills": f.bills, "submissions": len(rows), "executions": len(executions), "states": states, "credential_encrypted": encrypted, "secrets_protected": safe, "remote_id_snapshot_saved": snapshot, "owner_remote_id_used": f.matchedOwnerID, "aggregate_false": f.aggregateFalse, "zero_preserved": f.zeroPreserved})
	return true
}
