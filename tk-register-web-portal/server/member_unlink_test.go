package main

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"goapi-tk-register/server/internal/core"
	"gorm.io/gorm"
)

func TestMemberUnlinkHTTPSecurityAndFreshConsent(t *testing.T) {
	a, redis, mail := fixture(t)
	ctx := context.Background()
	actor, cookie, csrf := vendorSession(t, a, "parent@example.com", "member")
	target, targetCookie, _ := vendorSession(t, a, "child@example.com", "member")
	_, outsiderCookie, outsiderCSRF := vendorSession(t, a, "outsider@example.com", "member")
	grand := core.User{Email: "grand@example.com", PasswordHash: "hash", EmailVerifiedAt: time.Now()}
	if err := a.store.CreateMemberByUser(ctx, target.ID, target.AuthVersion, &grand); err != nil {
		t.Fatal(err)
	}
	vendor := configuredVendor(t, a, actor.ID)
	seedAdminSubmission(t, a, grand.ID, vendor, "grand-account@example.com", "accepted", time.Now())
	account := syncTarget(t, a, grand.ID)
	// An unused approval code must not survive a complete link/unlink cycle.
	link := map[string]string{"email": target.Email}
	requireStatus(t, request(t, a, "POST", "members/link-code", link, cookie, csrf), 200)
	link["code"] = mail.code(target.Email, "member_link:"+actor.Email)
	if _, err := a.store.LinkExistingMember(ctx, actor.ID, actor.AuthVersion, target.ID, target.AuthVersion, target.MembershipVersion); err != nil {
		t.Fatal(err)
	}
	page := request(t, a, "GET", "members", nil, cookie, "")
	requireStatus(t, page, 200)
	member := data(t, page)["items"].([]any)[0].(map[string]any)
	version, isString := member["membership_version"].(string)
	if !isString {
		t.Fatal("version lost string precision")
	}
	body := map[string]string{"member_id": member["id"].(string), "membership_version": version}
	requireStatus(t, request(t, a, "POST", "members/unlink", body, nil, ""), 401)
	requireStatus(t, request(t, a, "POST", "members/unlink", body, cookie, ""), 403)
	requireStatus(t, request(t, a, "POST", "members/unlink", body, outsiderCookie, outsiderCSRF), 409)
	for _, invalid := range []string{"", "-1", "01", "18446744073709551616"} {
		input := map[string]string{"member_id": body["member_id"], "membership_version": invalid}
		requireStatus(t, request(t, a, "POST", "members/unlink", input, cookie, csrf), 400)
	}
	response := request(t, a, "POST", "members/unlink", body, cookie, csrf)
	requireStatus(t, response, 200)
	if len(response.Result().Cookies()) != 0 || mail.count != 1 {
		t.Fatal("unlink replaced session or sent mail")
	}
	requireStatus(t, request(t, a, "POST", "members/unlink", body, cookie, csrf), 409)
	for _, route := range []string{fmt.Sprintf("accounts/%d", account.ID), fmt.Sprintf("accounts/%d/snapshots", account.ID), fmt.Sprintf("accounts/%d/daily-usage", account.ID)} {
		requireStatus(t, request(t, a, "GET", route, nil, cookie, ""), 404)
	}
	list := request(t, a, "GET", "accounts", nil, cookie, "")
	requireStatus(t, list, 200)
	if data(t, list)["total"].(float64) != 0 {
		t.Fatal("removed accounts listed")
	}
	requireStatus(t, request(t, a, "GET", "auth/me", nil, cookie, ""), 200)
	requireStatus(t, request(t, a, "GET", "auth/me", nil, targetCookie, ""), 200)
	requireStatus(t, request(t, a, "GET", fmt.Sprintf("accounts/%d", account.ID), nil, targetCookie, ""), 200)
	requireStatus(t, request(t, a, "POST", "members/link", link, cookie, csrf), 400)
	delete(link, "code")
	redis.FastForward(time.Minute + time.Second)
	requireStatus(t, request(t, a, "POST", "members/link-code", link, cookie, csrf), 200)
	link["code"] = mail.code(target.Email, "member_link:"+actor.Email)
	requireStatus(t, request(t, a, "POST", "members/link", link, cookie, csrf), 200)
	requireStatus(t, request(t, a, "GET", fmt.Sprintf("accounts/%d", account.ID), nil, cookie, ""), 200)
	requireStatus(t, request(t, a, "POST", "members/unlink", body, cookie, csrf), 409)
}

func TestMemberOverviewRefreshPersistsFailureAndRecovers(t *testing.T) {
	a, _, _ := fixture(t)
	ctx := context.Background()
	parent := addUser(t, a, "parent@example.com", "member")
	child := core.User{Email: "child@example.com", PasswordHash: "hash", EmailVerifiedAt: time.Now()}
	if err := a.store.CreateMemberByUser(ctx, parent.ID, parent.AuthVersion, &child); err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.UnlinkMember(ctx, parent.ID, parent.AuthVersion, child.ID, child.MembershipVersion); err != nil {
		t.Fatal(err)
	}
	if err := a.store.DB.Callback().Create().Before("gorm:create").Register("test:summary_write_failure", func(tx *gorm.DB) {
		if tx.Statement.Table == "overview_summaries_modified" {
			tx.AddError(errors.New("storage unavailable"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := a.processMemberOverviewRefresh(ctx); err == nil {
		t.Fatal("failure not surfaced")
	}
	a.store.DB.Callback().Create().Remove("test:summary_write_failure")
	var queued core.OverviewRefreshRequest
	if err := a.store.DB.First(&queued).Error; err != nil || !queued.RetryAfter.After(time.Now()) {
		t.Fatal("refresh request lost", queued, err)
	}
	if err := a.store.DB.Model(&queued).Update("retry_after", time.Now().UTC().Add(-time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	// No daily job is needed, and this path makes no supplier requests.
	if err := a.processOverviewSummaries(ctx); err != nil {
		t.Fatal(err)
	}
	var count int64
	a.store.DB.Model(&core.OverviewRefreshRequest{}).Count(&count)
	if count != 0 {
		t.Fatal("queue not completed")
	}
	view := core.WithModelUsageSource(ctx, "modified")
	out, err := a.store.ReadOverviewSummary(view, parent.ID, false, "all_time", time.Now())
	if err != nil || out.SummaryAvailable == nil || !*out.SummaryAvailable || out.SummaryRefreshing || len(out.Sources) != 1 {
		t.Fatal("personal summary not refreshed", out, err)
	}
}
