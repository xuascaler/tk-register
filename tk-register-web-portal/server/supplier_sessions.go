package main

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"goapi-tk-register/server/internal/supplier"
)

// Credential cache invalidation is done before saving a changed value so a
// Redis failure cannot silently leave a newly saved configuration using an old
// cache generation. A rejected settings edit may invalidate a cache harmlessly.
func (a *app) invalidateSupplierCredentials(ctx context.Context, changes map[string]string) error {
	if a.supplierClient == nil {
		return nil
	}
	username, hasUsername := changes["account_sync_website_username"]
	password, hasPassword := changes["account_sync_website_password"]
	if !hasUsername && (!hasPassword || password == "") {
		return nil
	}
	values, err := a.store.Settings(ctx)
	if err != nil {
		return err
	}
	changed := hasUsername && strings.TrimSpace(username) != values.String("account_sync_website_username")
	changed = changed || (hasPassword && password != "" && password != values.String("account_sync_website_password"))
	if !changed {
		return nil
	}
	return a.supplierClient.InvalidateSessions(ctx)
}

func supplierWaitMessage(err error) string {
	seconds, _, stage, _ := supplier.RetryDetails(err)
	if seconds < 1 {
		return "The supplier website is limiting requests. Please try again shortly. Previously collected data is retained."
	}
	switch stage {
	case "login":
		return fmt.Sprintf("Supplier sign-in is temporarily rate limited. Try again in %d seconds. Previously collected data is retained.", seconds)
	case "accounts":
		return fmt.Sprintf("Supplier account queries are temporarily rate limited. Try again in %d seconds. Previously collected data is retained.", seconds)
	case "login_lock":
		return fmt.Sprintf("A supplier sign-in is in progress. Try again in %d seconds. Previously collected data is retained.", seconds)
	default:
		return fmt.Sprintf("The supplier website is limiting requests. Try again in %d seconds. Previously collected data is retained.", seconds)
	}
}

func supplierWaitFailure(w http.ResponseWriter, err error) bool {
	seconds, _, stage, _ := supplier.RetryDetails(err)
	if seconds < 1 {
		return false
	}
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	respond(w, http.StatusTooManyRequests, envelope{Code: "SUPPLIER_RATE_LIMITED", Message: supplierWaitMessage(err), RetryAfter: seconds, RetryStage: stage})
	return true
}
