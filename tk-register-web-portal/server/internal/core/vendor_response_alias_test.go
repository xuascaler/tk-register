package core

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestSubmissionResponseAliasLeavesOtherBodiesUnchanged(t *testing.T) {
	for _, body := range []string{
		" {\"id\":9007199254740993, \"message\":\"温州润晨\"}\n",
		`{"alias":"another-account","nested":{"alias":"温州润晨"}}`,
		`{"alias":null,"message":"温州润晨"}`,
		`{"alias":9007199254740993}`,
		`{"alias":true}`,
		`{"alias":{"name":"温州润晨"}}`,
		`{"alias":["温州润晨"]}`,
		`{"alias":"温州润晨",`,
		`{"alias":"温州润晨"} {}`,
		`[{"alias":"温州润晨"}]`,
		`"温州润晨"`,
		`null`,
		`vendor error: 温州润晨`,
		"",
	} {
		t.Run(body, func(t *testing.T) {
			if got := maskSubmissionResponseAlias([]byte(body)); string(got) != body {
				t.Fatalf("body changed: got %q, want %q", got, body)
			}
		})
	}
}

func TestCreateAPIMasksResponseAliasWithoutChangingSubmission(t *testing.T) {
	for _, aliasJSON := range []string{
		`"prefix-温州润晨_温州润晨-suffix"`,
		`"prefix-\u6e29\u5dde\u6da6\u6668_\u6e29\u5dde\u6da6\u6668-suffix"`,
		`"prefix-温州\u6da6\u6668_\u6e29州润晨-suffix"`,
	} {
		t.Run(aliasJSON, func(t *testing.T) {
			service, user, tokenID, params := apiExecutionFixture(t)
			params["remark"] = "keep 温州润晨 in the outbound request"
			expectedRequest, err := json.Marshal(params)
			if err != nil {
				t.Fatal(err)
			}
			upstreamBody := `{"id":9007199254740993,"alias":` + aliasJSON + `,"opaque":18446744073709551615,"decimal":1.2300e+45,"message":"温州润晨","extra":{"alias":"温州润晨","values":[true,null,9007199254740993]},"credential":"shared-vendor-token-fixture"}`
			var outboundRequestID string
			calls := 0
			service.Transport = storageTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != "POST" || r.URL.String() != VendorOrigin+"/supplier-account/api/openapi/claude/accounts" || r.Header.Get("Authorization") != "Bearer shared-vendor-token-fixture" {
					t.Error("outbound endpoint or authentication changed")
				}
				requestBody, err := io.ReadAll(r.Body)
				if err != nil || !bytes.Equal(requestBody, expectedRequest) {
					t.Error("outbound submission parameters changed", err)
				}
				outboundRequestID = r.Header.Get("X-Request-ID")
				return apiRemoteResponse(http.StatusCreated, upstreamBody), nil
			})

			result, err := service.CreateAPI(context.Background(), user.ID, tokenID, params)
			if err != nil {
				t.Fatal(err)
			}
			if result.HTTPStatus != http.StatusCreated || result.ContentType != "application/json; charset=utf-8" || result.ErrorCode != "" || calls != 1 {
				t.Fatalf("response metadata or call count changed: %+v; calls=%d", result, calls)
			}
			if result.SubmissionID == "" || result.RequestID == "" || result.RequestID != outboundRequestID {
				t.Fatal("missing correlated IDs")
			}
			var got, want map[string]json.RawMessage
			if err := json.Unmarshal(result.Body, &got); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(upstreamBody), &want); err != nil {
				t.Fatal(err)
			}
			want["alias"] = json.RawMessage(`"prefix-xxxxx_xxxxx-suffix"`)
			want["credential"] = json.RawMessage(`"[REDACTED]"`)
			if len(got) != len(want) {
				t.Fatal("response fields added or removed")
			}
			for key, value := range want {
				if !bytes.Equal(got[key], value) {
					t.Errorf("response field %q: got %s, want %s", key, got[key], value)
				}
			}
			var saved Submission
			if err := service.Store.DB.First(&saved, result.SubmissionID).Error; err != nil {
				t.Fatal(err)
			}
			const originalAlias = "prefix-温州润晨_温州润晨-suffix"
			if saved.Status != "accepted" || saved.RemoteID == nil || *saved.RemoteID != "9007199254740993" || saved.RemoteAlias == nil || *saved.RemoteAlias != originalAlias {
				t.Fatal("stored submission identity or original alias changed")
			}
			var execution ProtocolExecution
			if err := service.Store.DB.Where("submission_id = ?", saved.ID).First(&execution).Error; err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(execution.ResultJSON, originalAlias) || execution.RequestID != result.RequestID {
				t.Fatal("stored execution lost the original alias or request correlation")
			}
		})
	}
}
