package supplier

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"time"

	"goapi-tk-register/server/internal/diagnostics"
)

const maxBillExportBytes = 16 << 20

// ExportClaudeBills downloads the supplier's read-only Claude workbook. It uses
// the same encrypted session cache, but a separate export cooldown; a bill 429
// must never invalidate an otherwise valid login.
func (c *Client) ExportClaudeBills(ctx context.Context, username, password, startDate, endDate string) ([]byte, error) {
	start, e1 := time.Parse("2006-01-02", startDate)
	end, e2 := time.Parse("2006-01-02", endDate)
	if e1 != nil || e2 != nil || end.Before(start) || end.Sub(start) > 59*24*time.Hour {
		return nil, safeError(CodeContractMismatch)
	}
	s := c.newSession()
	s.http.Timeout = 60 * time.Second
	if transport, ok := s.http.Transport.(*http.Transport); ok {
		copy := transport.Clone()
		copy.ResponseHeaderTimeout = 60 * time.Second
		s.http.Transport = copy
	}
	defer s.http.CloseIdleConnections()
	if s.cache != nil {
		if err := s.cache.cooldown(ctx, "bill_export"); err != nil {
			return nil, err
		}
	}
	if err := s.authenticate(ctx, username, password, false); err != nil {
		return nil, err
	}
	path := apiPrefix + "/bill/claude-export?" + url.Values{"start_ds": {start.Format("20060102")}, "end_ds": {end.Format("20060102")}}.Encode()
	for attempt := 0; attempt < 2; attempt++ {
		status, body, err := s.billExportRequest(ctx, path)
		if err != nil {
			return nil, err
		}
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			if attempt == 1 {
				return nil, safeError(CodeSessionExpired)
			}
			if err = s.reauthenticate(ctx, username, password); err != nil {
				return nil, err
			}
			continue
		}
		if err = readStatus(status); err != nil {
			return nil, err
		}
		if len(body) < 4 || string(body[:4]) != "PK\x03\x04" {
			return nil, safeError(CodeContractMismatch)
		}
		return body, nil
	}
	return nil, safeError(CodeSessionExpired)
}

func (s *session) billExportRequest(ctx context.Context, path string) (int, []byte, error) {
	if s.cache != nil {
		if err := s.cache.cooldown(ctx, "bill_export"); err != nil {
			return 0, nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, Origin+path, nil)
	if err != nil {
		return 0, nil, safeError(CodeContractMismatch)
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Accept", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	started := time.Now()
	event := diagnostics.RequestEvent{StartedAt: started.UTC(), Method: http.MethodGet, Path: apiPrefix + "/bill/claude-export", Outcome: "failed"}
	defer func() { event.DurationMS = time.Since(started).Milliseconds(); diagnostics.LogRequest(ctx, event) }()
	response, err := s.http.Do(req)
	if response != nil {
		event.HTTPStatus = response.StatusCode
		event.RetryAfterSeconds = diagnosticRetryAfter(response.Header.Get("Retry-After"), time.Now())
	}
	if err != nil {
		event.ErrorKind = diagnosticTransportKind(ctx, err)
		return 0, nil, collectionTransportError(ctx, err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusTooManyRequests {
		event.ErrorKind = "rate_limited"
		if s.cache != nil {
			return 0, nil, s.cache.recordCooldown(ctx, "bill_export", cooldownRetryAfter(response.Header.Get("Retry-After"), time.Now()))
		}
		return 0, nil, safeError(CodeRateLimited)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		event.ErrorKind = "http"
		return response.StatusCode, nil, nil
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBillExportBytes+1))
	if err != nil {
		event.ErrorKind = "body_read"
		return 0, nil, collectionTransportError(ctx, err)
	}
	if len(body) > maxBillExportBytes {
		event.ErrorKind = "response_too_large"
		return 0, nil, safeError(CodeContractMismatch)
	}
	event.Outcome = "succeeded"
	return response.StatusCode, body, nil
}
