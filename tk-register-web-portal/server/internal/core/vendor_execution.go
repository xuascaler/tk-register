package core

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"goapi-tk-register/server/common"
	"gorm.io/gorm"
)

type CreateSubmissionInput struct {
	VendorID          string         `json:"vendor_id"`
	ProtocolVersionID string         `json:"protocol_version_id"`
	ClientRequestKey  string         `json:"client_request_key"`
	Parameters        map[string]any `json:"parameters"`
}

var clientKeyPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{16,80}$`)

// VendorService permits transport injection for tests. Production requests use
// the pinned HTTPS origin, with a certificate exception for account submission,
// account-status queries, and billing queries.
type VendorService struct {
	Store     *Store
	Transport http.RoundTripper
	// ErrorLogger defaults to the process logger, collected by Docker.
	ErrorLogger *log.Logger
}

var verifiedVendorTransport = func() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	return transport
}()

var approvedVendorTransport = func() *http.Transport {
	transport := verifiedVendorTransport.Clone()
	// The deployment owner explicitly permits this vendor's expired/mismatched
	// certificate for account submissions, status queries, and billing queries.
	// Keep this transport scoped to those protocols and the exact vendor origin.
	transport.TLSClientConfig.InsecureSkipVerify = true
	return transport
}()

func (v *VendorService) client(vendor Vendor, protocolCode string) *http.Client {
	transport := v.Transport
	if transport == nil {
		transport = verifiedVendorTransport
		if vendor.Code == "tk-register-0" && vendor.BaseURL == VendorOrigin && (protocolCode == "submit_account" || protocolCode == "metrics" || protocolCode == "bills") {
			transport = approvedVendorTransport
		}
	}
	return &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
}
func (v *VendorService) currentVersion(ctx context.Context, vendorID uint64, code string) (Vendor, ProtocolVersion, error) {
	var vendor Vendor
	if e := v.Store.DB.WithContext(ctx).First(&vendor, vendorID).Error; e != nil {
		return vendor, ProtocolVersion{}, e
	}
	var p VendorProtocol
	if e := v.Store.DB.WithContext(ctx).Where("vendor_id = ? AND code = ?", vendorID, code).First(&p).Error; e != nil {
		return vendor, ProtocolVersion{}, e
	}
	var version ProtocolVersion
	e := v.Store.DB.WithContext(ctx).Where("protocol_id = ?", p.ID).Order("version DESC").First(&version).Error
	version.Protocol = p
	return vendor, version, e
}
func (v *VendorService) credential(ctx context.Context, vendor Vendor, version ProtocolVersion) (string, error) {
	if vendor.Code != "tk-register-0" || vendor.BaseURL != VendorOrigin {
		return "", vendorError(409, "UNSUPPORTED_VENDOR", "This vendor is not supported.")
	}
	if !vendor.Enabled {
		return "", vendorError(409, "VENDOR_DISABLED", "This vendor is currently disabled.")
	}
	if version.CredentialID == nil {
		return "", vendorError(409, "VENDOR_NOT_CONFIGURED", "An administrator must configure this vendor before accounts can be submitted.")
	}
	var c VendorCredential
	if e := v.Store.DB.WithContext(ctx).Where("id = ? AND vendor_id = ?", *version.CredentialID, vendor.ID).First(&c).Error; e != nil {
		return "", e
	}
	token, e := v.Store.decrypt(credentialKey(vendor.ID), c.SecretCiphertext)
	if e != nil {
		return "", e
	}
	if token == "" {
		return "", vendorError(409, "VENDOR_NOT_CONFIGURED", "The vendor access token has not been configured.")
	}
	return token, nil
}

// APIExecutionResult keeps the complete upstream response in memory only. The
// durable submission history contains bounded, redacted fields instead.
type APIExecutionResult struct {
	SubmissionID string
	RequestID    string
	HTTPStatus   int
	ContentType  string
	Body         []byte
	ErrorCode    string
	Message      string
}

func (v *VendorService) Create(ctx context.Context, userID uint64, input CreateSubmissionInput) (SubmissionDetail, error) {
	return v.create(ctx, userID, input, "web", nil, nil)
}

// CreateAPI gives each explicit API request its own submission record. It uses
// the same validation, account lock and outbound protocol as the web form.
func (v *VendorService) CreateAPI(ctx context.Context, userID, tokenID uint64, parameters map[string]any) (APIExecutionResult, error) {
	var vendor Vendor
	if err := v.Store.DB.WithContext(ctx).Where("code = ?", "tk-register-0").First(&vendor).Error; err != nil {
		return APIExecutionResult{}, err
	}
	_, version, err := v.currentVersion(ctx, vendor.ID, "submit_account")
	if err != nil {
		return APIExecutionResult{}, err
	}
	input := CreateSubmissionInput{VendorID: idString(vendor.ID), ProtocolVersionID: idString(version.ID), ClientRequestKey: uuid.NewString(), Parameters: parameters}
	var response APIExecutionResult
	_, err = v.create(ctx, userID, input, "api", &tokenID, &response)
	return response, err
}

func (v *VendorService) create(ctx context.Context, userID uint64, input CreateSubmissionInput, source string, apiTokenID *uint64, apiResponse *APIExecutionResult) (SubmissionDetail, error) {
	if !clientKeyPattern.MatchString(input.ClientRequestKey) {
		return SubmissionDetail{}, vendorError(400, "INVALID_REQUEST_KEY", "Provide a unique request key of 16 to 80 letters, digits, underscores or hyphens.")
	}
	vendorID, e := ParseBusinessID(input.VendorID)
	if e != nil {
		return SubmissionDetail{}, e
	}
	versionID, e := ParseBusinessID(input.ProtocolVersionID)
	if e != nil {
		return SubmissionDetail{}, e
	}
	normalized, safe, e := ValidateSubmissionInput(input.Parameters)
	if e != nil {
		return SubmissionDetail{}, e
	}
	canonical, e := jsonText(map[string]any{"vendor_id": input.VendorID, "protocol_version_id": input.ProtocolVersionID, "parameters": normalized})
	if e != nil {
		return SubmissionDetail{}, e
	}
	fingerprint := v.Store.fingerprint(canonical)
	replay := func() (SubmissionDetail, bool, error) {
		var existing Submission
		e := v.Store.DB.WithContext(ctx).Where("user_id = ? AND client_request_key = ?", userID, input.ClientRequestKey).First(&existing).Error
		if errors.Is(e, gorm.ErrRecordNotFound) {
			return SubmissionDetail{}, false, nil
		}
		if e != nil {
			return SubmissionDetail{}, false, e
		}
		if existing.RequestHash != fingerprint {
			return SubmissionDetail{}, true, vendorError(409, "IDEMPOTENCY_CONFLICT", "This request key was already used with different parameters.")
		}
		detail, e := v.Store.SubmissionDetail(ctx, userID, existing.ID)
		return detail, true, e
	}
	if detail, found, e := replay(); found || e != nil {
		return detail, e
	}
	vendor, version, e := v.currentVersion(ctx, vendorID, "submit_account")
	if e != nil {
		return SubmissionDetail{}, e
	}
	if version.ID != versionID {
		return SubmissionDetail{}, vendorError(409, "PROTOCOL_VERSION_CHANGED", "The vendor configuration changed. Reload the form before submitting.")
	}
	token, e := v.credential(ctx, vendor, version)
	if e != nil {
		return SubmissionDetail{}, e
	}
	safeJSON, e := jsonText(safe)
	if e != nil {
		return SubmissionDetail{}, e
	}
	secretJSON, e := jsonText(map[string]any{"password": normalized["password"], "auth_json": normalized["auth_json"]})
	if e != nil {
		return SubmissionDetail{}, e
	}
	encrypted, e := v.Store.encrypt("submission:"+idString(userID)+":"+input.ClientRequestKey, secretJSON)
	if e != nil {
		return SubmissionDetail{}, e
	}
	accountKey := v.Store.fingerprint(idString(userID) + ":" + idString(vendorID) + ":" + normalized["account"].(string))
	record := Submission{UserID: userID, Source: source, APITokenID: apiTokenID, VendorID: vendorID, ProtocolVersionID: versionID, ClientRequestKey: input.ClientRequestKey, RequestHash: fingerprint, ActiveAccountKey: &accountKey, Account: normalized["account"].(string), Plan: normalized["plan"].(string), ParametersJSON: safeJSON, SecretParametersCiphertext: encrypted, Status: "running"}
	execution := ProtocolExecution{ProtocolVersionID: versionID, RequestID: uuid.NewString(), Operation: "push", Status: "running", RequestJSON: safeJSON}
	e = v.Store.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := tx.Create(&record).Error; e != nil {
			return e
		}
		execution.SubmissionID = record.ID
		return tx.Create(&execution).Error
	})
	if e != nil {
		if detail, found, lookupErr := replay(); found || lookupErr != nil {
			return detail, lookupErr
		}
		var previous Submission
		if q := v.Store.DB.WithContext(ctx).Where("active_account_key = ?", accountKey).First(&previous).Error; q == nil {
			return SubmissionDetail{}, vendorError(409, "ACCOUNT_ALREADY_SUBMITTED", "This account already has an accepted or currently running submission. Check Submission history before submitting again.")
		}
		return SubmissionDetail{}, e
	}
	// Once the durable record exists, finish independently of browser disconnection.
	result := v.execute(vendor, version, execution.RequestID, normalized, nil, token)
	if apiResponse != nil {
		apiResponse.SubmissionID = idString(record.ID)
		apiResponse.RequestID = execution.RequestID
		if result.gatewayStatus == 0 && result.httpStatus != nil && !result.responseIncomplete && !result.responseTruncated {
			apiResponse.HTTPStatus = *result.httpStatus
			apiResponse.ContentType = result.contentType
			apiResponse.Body = redactVendorCredential(maskSubmissionResponseAlias(result.completeResponseBody), token)
		} else {
			apiResponse.HTTPStatus = result.gatewayStatus
			if apiResponse.HTTPStatus == 0 {
				apiResponse.HTTPStatus = http.StatusInternalServerError
			}
			apiResponse.ErrorCode = result.code
			apiResponse.Message = result.message
		}
	}
	return v.finish(userID, record, execution, result, normalized, token)
}

// Mask only the public submission response's top-level alias. RawMessage keeps
// other values intact, including numbers too large for a float64.
func maskSubmissionResponseAlias(body []byte) []byte {
	var response map[string]json.RawMessage
	if json.Unmarshal(body, &response) != nil {
		return body
	}
	var alias string
	if json.Unmarshal(response["alias"], &alias) != nil || !strings.Contains(alias, "温州润晨") {
		return body
	}
	maskedAlias, err := json.Marshal(strings.ReplaceAll(alias, "温州润晨", "xxxxx"))
	if err != nil {
		return body
	}
	response["alias"] = maskedAlias
	maskedBody, err := json.Marshal(response)
	if err != nil {
		return body
	}
	return maskedBody
}

// Keep normal upstream responses byte-for-byte intact, but never return the
// platform's shared credential if a remote error happens to echo it.
func redactVendorCredential(body []byte, token string) []byte {
	if token == "" {
		return body
	}
	encoded, err := common.Marshal(token)
	if err == nil && len(encoded) >= 2 {
		body = bytes.ReplaceAll(body, encoded[1:len(encoded)-1], []byte("[REDACTED]"))
	}
	return bytes.ReplaceAll(body, []byte(token), []byte("[REDACTED]"))
}

type QueryInput struct {
	Metrics MetricsInput
	Bills   BillsInput
}

// RefreshStatus queries only an already accepted account. Opening its history
// and details in quick succession can reuse a recent successful status check.
// It never submits or retries account creation.
func (v *VendorService) RefreshStatus(ctx context.Context, userID, id uint64, force bool) (SubmissionDetail, error) {
	record, e := v.Store.OwnedSubmission(ctx, userID, id)
	if e != nil {
		return SubmissionDetail{}, e
	}
	if record.Status != "accepted" || record.RemoteID == nil {
		return SubmissionDetail{}, vendorError(409, "ACCOUNT_NOT_ACCEPTED", "Remote queries are available only after the vendor accepts this account.")
	}
	now := time.Now().UTC()
	if !force && record.MetricsJSON != "" {
		var metrics MetricResult
		if e := common.Unmarshal([]byte(record.MetricsJSON), &metrics); e != nil {
			return SubmissionDetail{}, e
		}
		if !metrics.QueriedAt.After(now) && now.Sub(metrics.QueriedAt) < 30*time.Second {
			return v.Store.SubmissionDetail(ctx, userID, id)
		}
	}
	var vendor Vendor
	if e := v.Store.DB.WithContext(ctx).First(&vendor, record.VendorID).Error; e != nil {
		return SubmissionDetail{}, e
	}
	loc, e := time.LoadLocation(vendor.Timezone)
	if e != nil {
		return SubmissionDetail{}, e
	}
	end := now.In(loc).Truncate(time.Second)
	return v.Query(ctx, userID, id, "metrics", QueryInput{Metrics: MetricsInput{
		BeginTime: end.Add(-24 * time.Hour).Format("2006-01-02 15:04:05"),
		EndTime:   end.Format("2006-01-02 15:04:05"),
	}})
}

func (v *VendorService) Query(ctx context.Context, userID, id uint64, operation string, input QueryInput) (SubmissionDetail, error) {
	if operation != "metrics" && operation != "bills" {
		return SubmissionDetail{}, vendorError(400, "INVALID_OPERATION", "Unsupported query operation.")
	}
	record, e := v.Store.OwnedSubmission(ctx, userID, id)
	if e != nil {
		return SubmissionDetail{}, e
	}
	if record.Status != "accepted" || record.RemoteID == nil {
		return SubmissionDetail{}, vendorError(409, "ACCOUNT_NOT_ACCEPTED", "Remote queries are available only after the vendor accepts this account.")
	}
	vendor, version, e := v.currentVersion(ctx, record.VendorID, operation)
	if e != nil {
		return SubmissionDetail{}, e
	}
	token, e := v.credential(ctx, vendor, version)
	if e != nil {
		return SubmissionDetail{}, e
	}
	params := map[string]any{}
	if operation == "metrics" {
		if e = ValidateMetrics(input.Metrics, vendor.Timezone, time.Now()); e != nil {
			return SubmissionDetail{}, e
		}
		params["begin_time"] = input.Metrics.BeginTime
		params["end_time"] = input.Metrics.EndTime
	} else {
		if e = ValidateBills(input.Bills, vendor.Timezone, time.Now()); e != nil {
			return SubmissionDetail{}, e
		}
		params["start_ds"] = input.Bills.StartDS
		params["end_ds"] = input.Bills.EndDS
	}
	safe, e := jsonText(params)
	if e != nil {
		return SubmissionDetail{}, e
	}
	execution := ProtocolExecution{SubmissionID: id, ProtocolVersionID: version.ID, RequestID: uuid.NewString(), Operation: operation, Status: "running", RemoteIDSnapshot: record.RemoteID, RequestJSON: safe}
	if e = v.Store.DB.WithContext(ctx).Create(&execution).Error; e != nil {
		return SubmissionDetail{}, e
	}
	result := v.execute(vendor, version, execution.RequestID, params, record.RemoteID, token)
	// Secret snapshots are decrypted only for response redaction; never returned to clients.
	secrets := map[string]any{}
	if raw, e := v.Store.decrypt("submission:"+idString(userID)+":"+record.ClientRequestKey, record.SecretParametersCiphertext); e == nil {
		_ = common.Unmarshal([]byte(raw), &secrets)
	} else {
		return v.finish(userID, record, execution, executionResult{status: "failed", code: "SECRET_UNAVAILABLE", message: "The account configuration could not be read safely."}, nil, token)
	}
	return v.finish(userID, record, execution, result, secrets, token)
}

type executionResult struct {
	status, code, message string
	httpStatus            *int
	data                  any
	remoteID, alias       *string
	metrics               *MetricResult
	bills                 *BillResult
	// Complete responses are returned only to the API caller, never diagnostic logs.
	completeResponseBody []byte
	gatewayStatus        int
	contentType          string
	responseBody         []byte
	responseTruncated    bool
	responseIncomplete   bool
}

func requestBody(version ProtocolVersion, input map[string]any, remote *string) (string, map[string]any, error) {
	var definition struct {
		Method string         `json:"method"`
		Path   string         `json:"path"`
		Body   map[string]any `json:"body"`
	}
	if e := common.Unmarshal([]byte(version.RequestDefinition), &definition); e != nil {
		return "", nil, e
	}
	expected := ""
	for _, seed := range protocolSeeds() {
		if seed.code == version.Protocol.Code {
			expected = seed.path
		}
	}
	if definition.Method != "POST" || expected == "" || definition.Path != expected {
		return "", nil, errors.New("unapproved protocol definition")
	}
	output := map[string]any{}
	for key, value := range definition.Body {
		if placeholder, ok := value.(string); ok && strings.HasPrefix(placeholder, "{{input.") && strings.HasSuffix(placeholder, "}}") {
			name := strings.TrimSuffix(strings.TrimPrefix(placeholder, "{{input."), "}}")
			if val, exists := input[name]; exists {
				output[key] = val
			}
			continue
		}
		if key == "account_ids" {
			if remote == nil {
				return "", nil, errors.New("missing remote account")
			}
			n, e := strconv.ParseUint(*remote, 10, 64)
			if e != nil || n == 0 {
				return "", nil, errors.New("invalid remote account")
			}
			output[key] = []uint64{n}
			continue
		}
		if key == "aggregate" && value == false {
			output[key] = false
			continue
		}
		return "", nil, errors.New("unsupported protocol binding")
	}
	return definition.Path, output, nil
}
func (v *VendorService) execute(vendor Vendor, version ProtocolVersion, requestID string, input map[string]any, remote *string, token string) (result executionResult) {
	path, body, e := requestBody(version, input, remote)
	if e != nil {
		return executionResult{status: "failed", code: "PROTOCOL_INVALID", message: "The stored protocol definition is not supported."}
	}
	raw, e := common.Marshal(body)
	if e != nil {
		return executionResult{status: "failed", code: "INVALID_PARAMETERS", message: "The request could not be encoded."}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 32*time.Second)
	defer cancel()
	req, e := http.NewRequestWithContext(ctx, "POST", vendor.BaseURL+path, bytes.NewReader(raw))
	if e != nil {
		return executionResult{status: "failed", code: "PROTOCOL_INVALID", message: "The remote endpoint is invalid."}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Request-ID", requestID)
	queryName := ""
	if version.Protocol.Code == "metrics" {
		queryName = "status"
	} else if version.Protocol.Code == "bills" {
		queryName = "billing"
	}
	response, e := v.client(vendor, version.Protocol.Code).Do(req)
	uncertain := func(code, msg string, status *int) executionResult {
		s := "failed"
		if version.Protocol.Code == "submit_account" {
			s = "unknown"
		}
		return executionResult{status: s, code: code, message: msg, httpStatus: status}
	}
	if e != nil {
		var timeout interface{ Timeout() bool }
		timedOut := errors.Is(e, context.DeadlineExceeded) || (errors.As(e, &timeout) && timeout.Timeout())
		message := "Could not connect to the vendor. The remote outcome could not be confirmed; you may retry manually."
		code, gatewayStatus := "UPSTREAM_CONNECTION_ERROR", http.StatusBadGateway
		if timedOut {
			message = "The vendor request timed out. The remote outcome could not be confirmed; you may retry manually."
			code, gatewayStatus = "UPSTREAM_TIMEOUT", http.StatusGatewayTimeout
		}
		if queryName != "" {
			message = "The vendor " + queryName + " request could not be completed. Please try again."
			code = "UPSTREAM_UNCONFIRMED"
			if timedOut {
				message = "The vendor " + queryName + " request timed out. Please try again."
			}
		}
		result = uncertain(code, message, nil)
		result.gatewayStatus = gatewayStatus
		return result
	}
	defer response.Body.Close()
	status := response.StatusCode
	raw, e = io.ReadAll(io.LimitReader(response.Body, 256<<10+1))
	responseIncomplete := e != nil
	responseTruncated := len(raw) > 256<<10
	defer func() {
		if result.status != "succeeded" {
			result.responseBody = raw
		}
		if !responseTruncated && !responseIncomplete {
			result.completeResponseBody = raw
		}
		result.contentType = response.Header.Get("Content-Type")
		result.responseTruncated = responseTruncated
		result.responseIncomplete = responseIncomplete
		if responseTruncated || responseIncomplete {
			result.gatewayStatus = http.StatusBadGateway
		}
	}()
	if e != nil || len(raw) > 256<<10 {
		if queryName != "" {
			return uncertain("UPSTREAM_RESPONSE_INVALID", "The vendor "+queryName+" response was incomplete or too large (HTTP "+strconv.Itoa(status)+"). Please try again.", &status)
		}
		return uncertain("UPSTREAM_RESPONSE_INVALID", "The remote response was incomplete or too large.", &status)
	}
	if status < 200 || status > 299 {
		if queryName != "" {
			code := "UPSTREAM_UNCONFIRMED"
			if status == 400 || status == 401 || status == 403 || status == 422 {
				code = "UPSTREAM_REJECTED"
			}
			message := "The vendor " + queryName + " endpoint returned HTTP " + strconv.Itoa(status) + ". "
			if status == http.StatusNotFound {
				message = "The vendor returned HTTP 404 for this account's " + queryName + ". The account may not exist or be accessible with the configured platform credential."
			} else if status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500 {
				message += "Please try again."
			} else {
				message += "Contact the platform administrator."
			}
			return uncertain(code, message, &status)
		}
		if status == 400 || status == 401 || status == 403 || status == 422 {
			return executionResult{status: "failed", code: "UPSTREAM_REJECTED", message: "The vendor rejected the request. Check its account requirements and administrator configuration.", httpStatus: &status}
		}
		return uncertain("UPSTREAM_UNCONFIRMED", "The vendor did not confirm the request. Do not resubmit automatically.", &status)
	}
	result = executionResult{status: "succeeded", httpStatus: &status}
	switch version.Protocol.Code {
	case "submit_account":
		var output struct {
			ID    json.Number `json:"id"`
			Alias string      `json:"alias"`
		}
		if common.Unmarshal(raw, &output) != nil {
			return uncertain("UPSTREAM_RESPONSE_INVALID", "The remote acceptance response could not be verified.", &status)
		}
		id, e := remoteID(output.ID)
		if e != nil {
			return uncertain("UPSTREAM_RESPONSE_INVALID", "The vendor response did not include a valid account ID.", &status)
		}
		result.remoteID = &id
		result.alias = cleanText(&output.Alias, 160)
		result.message = "The vendor accepted the account. Query its status to check availability."
		result.data = map[string]any{"id": id, "alias": result.alias}
	case "metrics":
		parsed, e := parseMetrics(raw, *remote, MetricsInput{input["begin_time"].(string), input["end_time"].(string)})
		if e != nil {
			return uncertain("UPSTREAM_RESPONSE_INVALID", "The vendor status response could not be parsed (HTTP "+strconv.Itoa(status)+"). Please try again.", &status)
		}
		result.metrics = parsed
		result.data = parsed
		result.message = "Account status and metrics updated."
	case "bills":
		parsed, e := parseBills(raw, *remote, BillsInput{input["start_ds"].(string), input["end_ds"].(string)})
		if e != nil {
			return uncertain("UPSTREAM_RESPONSE_INVALID", "The vendor billing response could not be parsed (HTTP "+strconv.Itoa(status)+"). Please try again.", &status)
		}
		result.bills = parsed
		result.data = parsed
		result.message = "Account billing updated. Amounts are in CNY."
	}
	return result
}
func collectSecretStrings(value any, out *[]string) {
	switch item := value.(type) {
	case string:
		if len(item) > 0 {
			*out = append(*out, item)
		}
	case map[string]any:
		for _, v := range item {
			collectSecretStrings(v, out)
		}
	case []any:
		for _, v := range item {
			collectSecretStrings(v, out)
		}
	}
}
func redactStrings(value any, secrets []string) any {
	switch item := value.(type) {
	case string:
		for _, secret := range secrets {
			item = strings.ReplaceAll(item, secret, "[REDACTED]")
		}
		return item
	case map[string]any:
		for k, v := range item {
			item[k] = redactStrings(v, secrets)
		}
	case []any:
		for i, v := range item {
			item[i] = redactStrings(v, secrets)
		}
	}
	return value
}
func redactedResult(result any, secrets []string) (string, error) {
	if result == nil {
		return "", nil
	}
	raw, e := common.Marshal(result)
	if e != nil {
		return "", e
	}
	var value any
	if e = common.Unmarshal(raw, &value); e != nil {
		return "", e
	}
	return jsonText(redactStrings(value, secrets))
}
func (v *VendorService) finish(userID uint64, record Submission, execution ProtocolExecution, result executionResult, input map[string]any, token string) (SubmissionDetail, error) {
	v.logExecutionFailure(record, execution, result, input, token)
	secrets := []string{token}
	collectSecretStrings(input["password"], &secrets)
	collectAuthSecrets(input["auth_json"], &secrets)
	// Only bounded, allow-listed response fields are stored. No raw upstream error body.
	resultJSON, e := redactedResult(result.data, secrets)
	if e != nil {
		return SubmissionDetail{}, e
	}
	now := time.Now().UTC()
	updates := map[string]any{"status": result.status, "http_status": result.httpStatus, "result_json": resultJSON, "finished_at": now}
	if result.code != "" {
		updates["error_code"] = result.code
	}
	if result.message != "" {
		updates["message"] = result.message
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	e = v.Store.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		finished := tx.Model(&ProtocolExecution{}).Where("id = ? AND status = ?", execution.ID, "running").Updates(updates)
		if finished.Error != nil {
			return finished.Error
		}
		if finished.RowsAffected == 0 {
			return nil
		}
		changes := map[string]any{}
		if execution.Operation == "push" {
			state := result.status
			if state == "succeeded" {
				state = "accepted"
			}
			changes["status"] = state
			changes["remote_id"] = result.remoteID
			if result.alias != nil {
				redacted := redactStrings(*result.alias, secrets).(string)
				changes["remote_alias"] = redacted
			}
			if state == "failed" || state == "unknown" {
				changes["active_account_key"] = nil
			}
		} else if result.status == "succeeded" {
			changes["last_checked_at"] = now
			if result.metrics != nil {
				changes["metrics_json"] = resultJSON
				var safe MetricResult
				if e := common.Unmarshal([]byte(resultJSON), &safe); e != nil {
					return e
				}
				// A successful check without an account/status must not leave the
				// previous platform status looking current.
				changes["remote_status"] = nil
				if safe.Account != nil {
					changes["remote_status"] = safe.Account.Status
					changes["platform_account_id"] = safe.Account.AID
					if safe.Account.AccountAlias != nil {
						changes["remote_alias"] = safe.Account.AccountAlias
					}
				}
			}
			if result.bills != nil {
				changes["bills_json"] = resultJSON
			}
		}
		if len(changes) > 0 {
			if err := tx.Model(&Submission{}).Where("id = ? AND user_id = ?", record.ID, userID).Updates(changes).Error; err != nil {
				return err
			}
		}
		if execution.Operation == "push" && result.status == "succeeded" {
			var accepted Submission
			if err := tx.Select("id, user_id, vendor_id, account, plan, status, source, remote_id, platform_account_id, created_at").Where("id = ? AND user_id = ?", record.ID, userID).First(&accepted).Error; err != nil {
				return err
			}
			return enqueuePostSubmissionSyncTx(tx, accepted, now)
		}
		return nil
	})
	if e != nil {
		return SubmissionDetail{}, e
	}
	return v.Store.SubmissionDetail(ctx, userID, record.ID)
}

// Protocol metadata such as type and timestamps is not an authentication secret.
// Every credential value (including short passwords/tokens) is always redacted.
func collectAuthSecrets(value any, out *[]string) {
	object, ok := value.(map[string]any)
	if !ok {
		return
	}
	for key, item := range object {
		switch key {
		case "type", "email", "last_refresh", "expired", "verification_status", "organization_name":
			continue
		}
		collectSecretStrings(item, out)
	}
}
