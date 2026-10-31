package core

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func TestVendorQueryFailuresDescribeQueryWithoutExposingRemoteData(t *testing.T) {
	for _, operation := range []struct{ code, name string }{{"metrics", "status"}, {"bills", "billing"}} {
		for _, scenario := range []struct {
			name   string
			status int
			err    error
			body   string
		}{
			{name: "account inaccessible", status: 404, body: `{"detail":"账号不存在或无权访问","secret":"test-only-secret"}`},
			{name: "invalid credentials", status: 401, body: "test-only-secret"},
			{name: "rate limited", status: 429, body: "test-only-secret"},
			{name: "server failure", status: 503, body: "test-only-secret"},
			{name: "network failure", err: errors.New("test-only-secret")},
			{name: "timeout", err: context.DeadlineExceeded},
			{name: "invalid response", status: 200, body: "test-only-secret"},
			{name: "oversized response", status: 200, body: strings.Repeat("x", 256<<10+1)},
		} {
			t.Run(operation.code+"/"+scenario.name, func(t *testing.T) {
				version := queryErrorTestVersion(t, operation.code)
				calls := 0
				service := VendorService{Transport: storageTransport(func(*http.Request) (*http.Response, error) {
					calls++
					if scenario.err != nil {
						return nil, scenario.err
					}
					return &http.Response{StatusCode: scenario.status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(scenario.body))}, nil
				})}
				remote := "12345"
				result := service.execute(Vendor{BaseURL: "https://vendor.example"}, version, "request-test", map[string]any{
					"begin_time": "2026-10-27 00:00:00", "end_time": "2026-10-28 00:00:00",
					"start_ds": "2026-10-27", "end_ds": "2026-10-28",
				}, &remote, "test-only-secret")
				if result.status != "failed" || calls != 1 {
					t.Fatalf("query should fail without retry: %#v, calls=%d", result, calls)
				}
				if !strings.Contains(result.message, "vendor") || !strings.Contains(result.message, operation.name) || strings.Contains(result.message, "resubmit") || strings.Contains(result.message, "submission") || strings.Contains(result.message, "test-only-secret") {
					t.Fatalf("unsafe or misleading query message: %s", result.message)
				}
				if scenario.err == nil {
					if result.httpStatus == nil || *result.httpStatus != scenario.status || !strings.Contains(result.message, "HTTP "+strconv.Itoa(scenario.status)) {
						t.Fatalf("query response did not preserve HTTP status: %#v", result)
					}
				} else if result.httpStatus != nil {
					t.Fatal("network failure invented an HTTP status")
				}
				if scenario.status == 404 {
					want := "The vendor returned HTTP 404 for this account's " + operation.name + ". The account may not exist or be accessible with the configured platform credential."
					if result.message != want {
						t.Fatalf("got %q, want %q", result.message, want)
					}
				}
				if scenario.name == "timeout" && !strings.Contains(result.message, "timed out") {
					t.Fatal("timeout was not explained")
				}
			})
		}
	}
}

func TestVendorSubmissionFailureStillWarnsAgainstDuplicatePush(t *testing.T) {
	service := VendorService{Transport: storageTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 404, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("test-only-secret"))}, nil
	})}
	result := service.execute(Vendor{BaseURL: "https://vendor.example"}, queryErrorTestVersion(t, "submit_account"), "request-test", map[string]any{}, nil, "test-only-secret")
	if result.status != "unknown" || result.code != "UPSTREAM_UNCONFIRMED" || result.message != "The vendor did not confirm the request. Do not resubmit automatically." {
		t.Fatalf("submission uncertainty behavior changed: %#v", result)
	}
}

func queryErrorTestVersion(t *testing.T, code string) ProtocolVersion {
	t.Helper()
	for _, seed := range protocolSeeds() {
		if seed.code != code {
			continue
		}
		definition, err := jsonText(map[string]any{"method": "POST", "path": seed.path, "body": seed.body})
		if err != nil {
			t.Fatal(err)
		}
		return ProtocolVersion{Protocol: VendorProtocol{Code: code}, RequestDefinition: definition}
	}
	t.Fatalf("unknown test protocol: %s", code)
	return ProtocolVersion{}
}
