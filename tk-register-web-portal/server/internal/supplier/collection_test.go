package supplier

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"
)

func collectionRows(first, count int) []any {
	rows := make([]any, count)
	for i := range rows {
		rows[i] = fixtureAccount(first + i)
	}
	return rows
}

func TestCollectionUses500RowsAndPacesPages(t *testing.T) {
	var firstReturned time.Time
	f := &supplierFixture{t: t, read: func(r *http.Request, n int) *http.Response {
		if r.URL.Query().Get("page_size") != "500" || r.URL.Query().Get("page") != strconv.Itoa(n) {
			t.Fatal("incorrect collection pagination")
		}
		if n == 1 {
			response := jsonResponse(200, map[string]any{"total": 581, "list": collectionRows(1, 500)})
			firstReturned = time.Now()
			return response
		}
		if time.Since(firstReturned) < collectionPageInterval {
			t.Fatal("pages were not paced")
		}
		return jsonResponse(200, map[string]any{"total": 581, "list": collectionRows(501, 81)})
	}}
	rows, err := f.client().Collect(context.Background(), "supplier-user", "private-password-中文")
	if err != nil || len(rows) != 581 || f.reads != 2 {
		t.Fatalf("rows=%d reads=%d err=%v", len(rows), f.reads, err)
	}
	if rows[0].CollectedAt.IsZero() || rows[0].CollectedAt != rows[499].CollectedAt || !rows[500].CollectedAt.After(rows[0].CollectedAt) {
		t.Fatal("page observation times were not preserved")
	}
}

func TestCollection429RetriesSamePageAfterWait(t *testing.T) {
	var limitedAt time.Time
	f := &supplierFixture{t: t, read: func(r *http.Request, n int) *http.Response {
		wantPage := "2"
		if n == 1 {
			wantPage = "1"
		}
		if r.URL.Query().Get("page") != wantPage {
			t.Fatal("rate limited page was skipped or collection restarted")
		}
		if n == 1 {
			return jsonResponse(200, map[string]any{"total": 501, "list": collectionRows(1, 500)})
		}
		if n == 2 {
			limitedAt = time.Now()
			r := jsonResponse(429, nil)
			r.Header.Set("Retry-After", "1")
			r.Body = unreadableBody{}
			return r
		}
		if time.Since(limitedAt) < time.Second {
			t.Fatal("retried before Retry-After")
		}
		return jsonResponse(200, map[string]any{"total": 501, "list": collectionRows(501, 1)})
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rows, err := f.client().Collect(ctx, "supplier-user", "private-password-中文")
	if err != nil || len(rows) != 501 || f.reads != 3 || f.logins != 1 {
		t.Fatalf("rows=%d reads=%d err=%v", len(rows), f.reads, err)
	}
	if !rows[0].CollectedAt.Before(limitedAt) || rows[500].CollectedAt.Sub(rows[0].CollectedAt) < time.Second {
		t.Fatal("earlier page acquired a false later observation time")
	}
}

func TestCollection429StopsAfterTwoRetriesAndKeepsValidatedPage(t *testing.T) {
	f := &supplierFixture{t: t, read: func(r *http.Request, n int) *http.Response {
		if n == 1 {
			return jsonResponse(200, map[string]any{"total": 501, "list": collectionRows(1, 500)})
		}
		if r.URL.Query().Get("page") != "2" {
			t.Fatal("wrong retry page")
		}
		response := jsonResponse(429, nil)
		response.Header.Set("Retry-After", "1")
		return response
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rows, err := f.client().Collect(ctx, "supplier-user", "private-password-中文")
	assertCode(t, err, CodeRateLimited)
	progress, ok := CollectionDetails(err)
	if !ok || !progress.PartialSafe || progress.CompletedPages != 1 || progress.FailedPage != 2 || progress.RetryCount != 2 || progress.TotalRecords != 501 || progress.PageSize != 500 || len(rows) != 500 || f.reads != 4 {
		t.Fatalf("invalid partial collection: rows=%d reads=%d progress=%+v", len(rows), f.reads, progress)
	}
	if progress.LastCollectedAt != rows[0].CollectedAt {
		t.Fatal("lost partial page timestamp")
	}
}

func TestCollection429InsufficientDeadlineRetainsFallbackAndDoesNotRetry(t *testing.T) {
	for _, header := range []string{"", "invalid", "60"} {
		t.Run(header, func(t *testing.T) {
			f := &supplierFixture{t: t, read: func(_ *http.Request, n int) *http.Response {
				if n == 1 {
					return jsonResponse(200, map[string]any{"total": 2, "list": collectionRows(1, 1)})
				}
				response := jsonResponse(429, nil)
				if header != "" {
					response.Header.Set("Retry-After", header)
				}
				return response
			}}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			started := time.Now()
			rows, err := f.client().Collect(ctx, "supplier-user", "private-password-中文")
			assertCode(t, err, CodeRateLimited)
			seconds, _, stage, _ := RetryDetails(err)
			progress, ok := CollectionDetails(err)
			if len(rows) != 1 || f.reads != 2 || seconds != 60 || stage != "accounts" || !ok || !progress.PartialSafe || progress.RetryCount != 0 {
				t.Fatalf("incorrect bounded wait: rows=%d reads=%d seconds=%d progress=%+v", len(rows), f.reads, seconds, progress)
			}
			if time.Since(started) > time.Second {
				t.Fatal("waited despite insufficient deadline")
			}
		})
	}
}

func TestCollectionOperationalFailurePreservesOnlyCompletedPages(t *testing.T) {
	for _, kind := range []string{"transport", "timeout", "unauthorized", "http500", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f := &supplierFixture{t: t, read: func(_ *http.Request, n int) *http.Response {
				if n == 1 {
					return jsonResponse(200, map[string]any{"total": 2, "list": collectionRows(1, 1)})
				}
				if kind == "unauthorized" {
					return jsonResponse(401, nil)
				}
				return jsonResponse(500, nil)
			}}
			client := f.client()
			original := client.transport
			client.transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == apiPrefix+"/accounts" && r.URL.Query().Get("page") == "2" {
					switch kind {
					case "transport":
						return nil, errors.New("SECRET_NETWORK_DETAIL")
					case "timeout":
						return nil, context.DeadlineExceeded
					case "cancel":
						cancel()
						return nil, context.Canceled
					}
				}
				return original.RoundTrip(r)
			})
			rows, err := client.Collect(ctx, "supplier-user", "private-password-中文")
			progress, ok := CollectionDetails(err)
			if err == nil || len(rows) != 1 || !ok || !progress.PartialSafe || progress.CompletedPages != 1 || progress.FailedPage != 2 {
				t.Fatalf("partial page lost: rows=%d err=%v progress=%+v", len(rows), err, progress)
			}
			want := CodeUnavailable
			if kind == "timeout" {
				want = CodeTimeout
			}
			if kind == "unauthorized" {
				want = CodeSessionExpired
			}
			assertCode(t, err, want)
		})
	}
}

func TestCollectionContractFailureDiscardsPartialInventory(t *testing.T) {
	for _, kind := range []string{"duplicate", "bad-row", "changed-total", "empty", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			f := &supplierFixture{t: t, read: func(_ *http.Request, n int) *http.Response {
				if n == 1 {
					return jsonResponse(200, map[string]any{"total": 3, "list": collectionRows(1, 1)})
				}
				switch kind {
				case "duplicate":
					return jsonResponse(200, map[string]any{"total": 3, "list": collectionRows(1, 1)})
				case "bad-row":
					return jsonResponse(200, map[string]any{"total": 3, "list": []any{fixtureAccount(2), map[string]any{"id": 3}}})
				case "changed-total":
					return jsonResponse(200, map[string]any{"total": 4, "list": collectionRows(2, 2)})
				case "empty":
					return jsonResponse(200, map[string]any{"total": 3, "list": []any{}})
				default:
					return jsonResponse(200, map[string]any{"total": 3, "list": collectionRows(2, 501)})
				}
			}}
			rows, err := f.client().Collect(context.Background(), "supplier-user", "private-password-中文")
			assertCode(t, err, CodeContractMismatch)
			progress, ok := CollectionDetails(err)
			if rows != nil || !ok || progress.PartialSafe || progress.CompletedPages != 1 {
				t.Fatalf("unsafe rows escaped: rows=%d progress=%+v", len(rows), progress)
			}
		})
	}
}

func TestCollectionFirstPageFailureHasNoTrustedPartialRows(t *testing.T) {
	f := &supplierFixture{t: t, read: func(_ *http.Request, _ int) *http.Response { return jsonResponse(500, nil) }}
	rows, err := f.client().Collect(context.Background(), "supplier-user", "private-password-中文")
	progress, ok := CollectionDetails(err)
	if err == nil || len(rows) != 0 || !ok || progress.PartialSafe || progress.CompletedPages != 0 || progress.FailedPage != 1 {
		t.Fatal("first-page failure marked partial success")
	}
}

func TestCollectionCached429WaitsWithoutNewLogin(t *testing.T) {
	client, fixture, _, _ := newCacheFixture(t)
	original := client.transport
	reads := 0
	client.transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != apiPrefix+"/accounts" {
			return original.RoundTrip(r)
		}
		reads++
		if reads == 1 {
			return jsonResponse(200, map[string]any{"total": 2, "list": collectionRows(1, 1)}), nil
		}
		if r.URL.Query().Get("page") != "2" {
			t.Fatal("cached retry did not stay on page 2")
		}
		if reads == 2 {
			response := jsonResponse(429, nil)
			response.Header.Set("Retry-After", "1")
			return response, nil
		}
		return jsonResponse(200, map[string]any{"total": 2, "list": collectionRows(2, 1)}), nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rows, err := client.Collect(ctx, "private-supplier@example.com", "private-password")
	if err != nil || len(rows) != 2 || reads != 3 || fixture.logins.Load() != 1 {
		t.Fatalf("cached retry failed: rows=%d reads=%d logins=%d err=%v", len(rows), reads, fixture.logins.Load(), err)
	}
	client.transport = original
	if err := collectCached(t, client); err != nil || fixture.logins.Load() != 1 {
		t.Fatal("429 invalidated a healthy login", err)
	}
}

func TestCollectionCancellationInterrupts429Wait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := &supplierFixture{t: t, read: func(_ *http.Request, n int) *http.Response {
		if n == 1 {
			return jsonResponse(200, map[string]any{"total": 2, "list": collectionRows(1, 1)})
		}
		time.AfterFunc(20*time.Millisecond, cancel)
		return jsonResponse(429, nil)
	}}
	started := time.Now()
	rows, err := f.client().Collect(ctx, "supplier-user", "private-password-中文")
	progress, ok := CollectionDetails(err)
	if err == nil || len(rows) != 1 || !ok || !progress.PartialSafe || progress.RetryCount != 0 || f.reads != 2 || time.Since(started) > time.Second {
		t.Fatalf("canceled wait did not retain partial data: rows=%d err=%v progress=%+v", len(rows), err, progress)
	}
}
