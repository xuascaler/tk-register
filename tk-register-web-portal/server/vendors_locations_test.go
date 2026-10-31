package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"testing"

	"goapi-tk-register/server/common"
	"goapi-tk-register/server/internal/core"
)

func TestSubmissionRegistrationLocationsPersistAndForwardUnchanged(t *testing.T) {
	a, _, _ := fixture(t)
	admin, _, _ := vendorSession(t, a, "locations-admin@example.com", "admin")
	member, cookie, csrf := vendorSession(t, a, "locations-member@example.com", "member")
	vendor := configuredVendor(t, a, admin.ID)
	locations := []string{"US", "MY", "ID", "SG", "PH"}
	metadata := request(t, a, "GET", "vendors", nil, cookie, "")
	requireStatus(t, metadata, 200)
	var response struct {
		Data struct {
			Vendors []core.VendorDTO `json:"vendors"`
		} `json:"data"`
	}
	if err := common.Unmarshal(metadata.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, protocol := range response.Data.Vendors[0].Protocols {
		if protocol.Code != "submit_account" {
			continue
		}
		for _, parameter := range protocol.Parameters {
			if parameter.Name == "register_location" {
				found = true
				values := parameter.Format.(map[string]any)["enum"]
				if !reflect.DeepEqual(values, []any{"US", "MY", "ID", "SG", "PH"}) {
					t.Fatalf("wrong form location metadata: %#v", values)
				}
			}
		}
	}
	if !found {
		t.Fatal("missing registration location metadata")
	}
	var forwarded []string
	a.vendorTransport = vendorRoundTrip(func(r *http.Request) (*http.Response, error) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		var body map[string]any
		if err = common.Unmarshal(raw, &body); err != nil {
			t.Fatal(err)
		}
		forwarded = append(forwarded, body["register_location"].(string))
		return remoteResponse(200, fmt.Sprintf(`{"id":%d,"alias":"test account"}`, 1000+len(forwarded))), nil
	})
	for _, location := range locations {
		input := createInput(vendor, "location-request-"+location)
		input.Parameters["register_location"] = location
		input.Parameters["account"] = "supplier-" + location + "@example.com"
		w := request(t, a, "POST", "submissions", input, cookie, csrf)
		requireStatus(t, w, 200)
		detail := submissionDetail(t, w.Body.Bytes())
		if detail.Submission.Status != "accepted" || detail.Submission.Parameters["register_location"] != location {
			t.Fatalf("location not returned unchanged: %#v", detail.Submission)
		}
		id, _ := core.ParseBusinessID(detail.Submission.ID)
		record, err := a.store.OwnedSubmission(context.Background(), member.ID, id)
		if err != nil {
			t.Fatal(err)
		}
		var saved map[string]any
		if err = common.Unmarshal([]byte(record.ParametersJSON), &saved); err != nil || saved["register_location"] != location {
			t.Fatal("location not persisted unchanged", err)
		}
		var execution core.ProtocolExecution
		if err = a.store.DB.Where("submission_id = ?", id).First(&execution).Error; err != nil {
			t.Fatal(err)
		}
		if err = common.Unmarshal([]byte(execution.RequestJSON), &saved); err != nil || saved["register_location"] != location {
			t.Fatal("location missing from execution snapshot", err)
		}
	}
	if !reflect.DeepEqual(forwarded, locations) {
		t.Fatalf("vendor received different location codes: %#v", forwarded)
	}
	for _, location := range []any{"my", "sg", "Malaysia", " SG ", "CN", "", nil, float64(1)} {
		input := submissionParameters()
		input["register_location"] = location
		if _, _, err := core.ValidateSubmissionInput(input); err == nil {
			t.Fatalf("unsupported location accepted: %#v", location)
		}
	}
	input := submissionParameters()
	delete(input, "register_location")
	if _, _, err := core.ValidateSubmissionInput(input); err == nil {
		t.Fatal("missing registration location accepted")
	}
}
