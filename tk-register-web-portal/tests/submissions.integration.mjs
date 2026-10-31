import assert from "node:assert/strict";
import { mkdir } from "node:fs/promises";
import { chromium } from "@playwright/test";

// TestBrowserFixture uses isolated SQLite/miniredis, fake mail and an in-memory
// vendor transport. The loopback check prevents production account creation.
const origin = new URL(process.env.TK_E2E_URL || "http://127.0.0.1:39124");
if (
  origin.protocol !== "http:" ||
  !["127.0.0.1", "localhost", "[::1]"].includes(origin.hostname)
)
  throw new Error("Use an HTTP loopback fixture.");
const base = origin.origin;
const browser = await chromium.launch({
  headless: true,
  ...(process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH
    ? { executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH }
    : {}),
});
const errors = [];
let page;
try {
  const context = await browser.newContext({
    viewport: { width: 1440, height: 1060 },
  });
  page = await context.newPage();
  page.on("pageerror", (error) => errors.push(error.message));
  const data = async (response) => {
    assert.equal(response.status(), 200, await response.text());
    return (await response.json()).data;
  };
  const api = async (client, path, method = "GET", body, csrf = "") =>
    client.fetch(`${base}/tk-register/api${path}`, {
      method,
      headers: { Origin: base, ...(csrf ? { "X-CSRF-Token": csrf } : {}) },
      ...(body ? { data: body } : {}),
    });
  const register = async (client, email) => {
    await data(
      await api(client, "/auth/code", "POST", { email, purpose: "register" }),
    );
    const { code } = await data(
      await client.get(
        `${base}/__test/code?${new URLSearchParams({ email, purpose: "register" })}`,
      ),
    );
    return data(
      await api(client, "/auth/register", "POST", {
        email,
        username: email,
        password: "Fixture-password-2026!",
        code,
      }),
    );
  };
  const audit = () =>
    page.request.get(`${base}/__test/vendor-audit`).then(data);
  const mode = (value) =>
    page.request.get(`${base}/__test/vendor-mode?mode=${value}`).then(data);
  const go = async (route) => {
    await page.goto(`${base}/tk-register/#/${route}`);
    await page.locator(".workspace").waitFor();
    await page.locator("#main-content h1").waitFor();
  };
  const screenshot = async (name, width) => {
    await page.setViewportSize({ width, height: 1000 });
    await page.waitForTimeout(120);
    assert.equal(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
      true,
      `Horizontal overflow on ${name} at ${width}px`,
    );
    await page.screenshot({
      path: `test-results/submissions-${name}-${width}.png`,
      fullPage: true,
    });
  };
  await mkdir("test-results", { recursive: true });

  await register(page.request, "admin@example.com");
  await go("submit");
  await page
    .getByText("This platform is not ready for submissions.", { exact: true })
    .waitFor();
  assert.equal(
    await page
      .locator("form")
      .getByRole("button", { name: "Submit account", exact: true })
      .isDisabled(),
    true,
  );
  assert.equal((await audit()).pushes, 0);
  await screenshot("missing-credential", 1440);
  await page.getByRole("button", { name: /Open vendor configuration/ }).click();
  await page
    .getByLabel("Vendor access token", { exact: true })
    .fill("fixture-vendor-token-not-a-real-secret");
  if (
    (await page
      .getByRole("switch", { name: "Enable vendor submissions" })
      .getAttribute("aria-checked")) !== "true"
  )
    await page
      .getByRole("switch", { name: "Enable vendor submissions" })
      .click();
  await page.getByRole("button", { name: "Save changes", exact: true }).click();
  await page.getByText("Credential configured", { exact: true }).waitFor();
  assert.equal(
    await page.getByLabel("Vendor access token", { exact: true }).inputValue(),
    "",
  );
  await page.reload();
  await page.getByText("Credential configured", { exact: true }).waitFor();
  let adminVendors = await data(await api(page.request, "/vendors"));
  assert.equal(adminVendors.vendors[0].configured, true);
  assert.equal(
    JSON.stringify(adminVendors).includes("fixture-vendor-token"),
    false,
  );
  assert.equal((await audit()).credential_encrypted, true);
  await screenshot("config", 1440);
  await screenshot("config", 390);
  await page.setViewportSize({ width: 1440, height: 1060 });
  await page.getByRole("button", { name: "Sign out", exact: true }).click();
  await page.getByRole("heading", { name: "Sign in", exact: true }).waitFor();
  await page.waitForLoadState("networkidle");

  const member = await register(page.request, "supplier@example.com");
  await page.reload();
  await go("submit");
  assert.equal(
    await page.getByRole("button", { name: "Config", exact: true }).count(),
    0,
  );
  await screenshot("form", 1440);
  await screenshot("form", 768);
  await screenshot("form", 390);
  await screenshot("form", 320);
  await page.setViewportSize({ width: 1440, height: 1060 });
  const auth = {
    id_token: "",
    access_token: "fixture-access-token",
    refresh_token: "fixture-refresh-token",
    last_refresh: "2026-10-24T00:00:00Z",
    email: "account@example.com",
    account_uuid: "fixture-account-uuid",
    organization_uuid: "fixture-org-uuid",
    organization_name: "Fixture organization",
    claude_device_ids: ["fixture-device"],
    type: "claude",
    expired: "2027-10-24T00:00:00Z",
    proxy_url: "https://proxy.example.com:443",
    verification_status: "verified",
  };
  const fillAccount = async (name) => {
    await page.getByLabel("Account", { exact: true }).fill(name);
    await page
      .getByLabel("Account password", { exact: true })
      .fill("fixture-account-password");
    await page
      .getByLabel("Upload authentication JSON file")
      .setInputFiles({
        name: "fixture-auth.json",
        mimeType: "application/json",
        buffer: Buffer.from(JSON.stringify(auth)),
      });
    await page.getByText("fixture-auth.json", { exact: true }).waitFor();
    await page
      .getByLabel("Registration date", { exact: true })
      .fill("2026-10-23");
    await page
      .getByLabel("Plan start date", { exact: true })
      .fill("2026-10-23");
    await page.getByLabel("Plan end date", { exact: true }).fill("2026-11-23");
    await page
      .locator("summary")
      .filter({ hasText: "Additional details" })
      .click();
    await page
      .getByLabel("Payment method", { exact: true })
      .selectOption("third_party_payment");
    await page
      .getByLabel("Registration device", { exact: true })
      .selectOption("Windows");
    await page.getByLabel("Remaining balance (%)", { exact: true }).fill("0");
    await page.getByLabel("Warranty period", { exact: true }).selectOption("7");
    await page
      .getByLabel("Remark", { exact: true })
      .fill("Browser integration fixture");
  };
  await fillAccount("account@example.com");
  let captured;
  page.on("request", (request) => {
    if (
      request.method() === "POST" &&
      request.url() === `${base}/tk-register/api/submissions`
    )
      captured = request.postDataJSON();
  });
  let loseFirstResponse = true;
  let firstAcceptedSubmissionID;
  await page.route(`${base}/tk-register/api/submissions`, async (route) => {
    if (route.request().method() !== "POST" || !loseFirstResponse)
      return route.continue();
    loseFirstResponse = false;
    const response = await route.fetch();
    assert.equal(response.status(), 200);
    firstAcceptedSubmissionID = (await response.json()).data.submission.id;
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ success: true, data: {} }),
    });
  });
  await page
    .locator("form")
    .getByRole("button", { name: "Submit account", exact: true })
    .click();
  await page
    .getByRole("button", { name: "Submit again", exact: true })
    .waitFor();
  const firstRequestKey = captured.client_request_key;
  const firstPayload = captured;
  assert.equal(
    await page.getByLabel("Account", { exact: true }).isDisabled(),
    false,
  );
  const retryResponse = page.waitForResponse(response => response.request().method() === "POST" && response.url() === `${base}/tk-register/api/submissions`);
  await page
    .getByRole("button", { name: "Submit again", exact: true })
    .click();
  assert.equal((await retryResponse).status(), 409, "Accepted accounts must still be protected against duplicates");
  assert.notEqual(captured.client_request_key, firstRequestKey, "Each deliberate retry uses a new request key");
  captured = firstPayload;
  await go(`submissions/${firstAcceptedSubmissionID}`);
  await page
    .getByRole("heading", { name: "account@example.com", exact: true })
    .waitFor();
  const id = page.url().split("/").at(-1);
  assert.match(id, /^\d+$/);
  let detail = await data(await api(page.request, `/submissions/${id}`));
  assert.equal(detail.submission.status, "accepted");
  assert.equal(detail.submission.remote_id, "9007199254740993");
  assert.equal(detail.submission.parameters.balance, 0);
  assert.equal(
    detail.submission.parameters.payment_method,
    "third_party_payment",
  );
  assert.equal(
    JSON.stringify(detail).includes("fixture-account-password"),
    false,
  );
  assert.equal(JSON.stringify(detail).includes("fixture-access-token"), false);
  // Identical client retries return the stored result and do not push again.
  let repeat = await data(
    await api(
      page.request,
      "/submissions",
      "POST",
      captured,
      member.csrf_token,
    ),
  );
  assert.equal(repeat.submission.id, id);
  assert.equal((await audit()).pushes, 1);
  await page
    .getByRole("button", { name: "Refresh status", exact: true })
    .click();
  await page.getByText("Online", { exact: true }).waitFor();
  detail = await data(await api(page.request, `/submissions/${id}`));
  assert.equal(detail.submission.platform_account_id, "10725");
  assert.equal(detail.metrics.account.prompt_tokens, 1120273);
  await screenshot("overview", 1440);
  await screenshot("overview", 390);
  await page.getByRole("tab", { name: "Bills", exact: true }).click();
  await page.getByRole("button", { name: "View bills", exact: true }).click();
  await page.getByText(/CNY.*4,482\.04/).waitFor();
  detail = await data(await api(page.request, `/submissions/${id}`));
  assert.equal(detail.bills.currency, "CNY");
  assert.equal(detail.bills.account.cost, 4482.04);
  await screenshot("bills", 390);
  await screenshot("bills", 1440);
  await page.reload();
  await page
    .getByRole("heading", { name: "claude-fixture-account", exact: true })
    .waitFor();
  await page
    .getByRole("tab", { name: "Submitted parameters", exact: true })
    .click();
  await page.getByRole("button", { name: "View JSON", exact: true }).click();
  assert.equal(
    (await page.locator(".parameter-json").innerText()).includes(
      "fixture-access-token",
    ),
    false,
  );
  await screenshot("parameters", 1440);
  // A fresh empty vendor result clears the cached result, rather than keeping
  // stale usage or pretending missing values are zero.
  await mode("empty");
  await page.getByRole("tab", { name: "Bills", exact: true }).click();
  await page.getByRole("button", { name: "View bills", exact: true }).click();
  await page
    .getByRole("heading", { name: "No billing data for this period" })
    .waitFor();
  await mode("success");
  await go("submissions");
  await page
    .getByRole("button", { name: "account@example.com", exact: true })
    .waitFor();
  await screenshot("history", 1440);
  await screenshot("history", 768);
  await screenshot("history", 390);
  await screenshot("history", 320);
  await page
    .getByLabel("Search accounts", { exact: true })
    .fill("does-not-exist");
  await page
    .getByRole("button", { name: "account@example.com", exact: true })
    .waitFor({ state: "hidden" });
  await page
    .getByLabel("Search accounts", { exact: true })
    .fill("account@example.com");
  await page
    .getByRole("button", { name: "account@example.com", exact: true })
    .waitFor();

  // A different user has no access to another user's record, query or admin API.
  const outsider = await browser.newContext();
  const outsiderSession = await register(
    outsider.request,
    "outsider@example.com",
  );
  assert.equal(
    (await api(outsider.request, `/submissions/${id}`)).status(),
    404,
  );
  assert.equal(
    (
      await api(
        outsider.request,
        `/submissions/${id}/bills`,
        "POST",
        { start_ds: "2026-10-01", end_ds: "2026-10-24" },
        outsiderSession.csrf_token,
      )
    ).status(),
    404,
  );
  assert.equal(
    (
      await api(
        outsider.request,
        `/admin/vendors/${adminVendors.vendors[0].id}`,
      )
    ).status(),
    403,
  );
  assert.equal(
    (await data(await api(outsider.request, "/submissions"))).total,
    0,
  );
  await outsider.close();

  await mode("timeout");
  await go("submit");
  await fillAccount("uncertain@example.com");
  await page
    .locator("form")
    .getByRole("button", { name: "Submit account", exact: true })
    .click();
  await page
    .getByRole("heading", { name: "uncertain@example.com", exact: true })
    .waitFor();
  const uncertainID = page.url().split("/").at(-1);
  const uncertainPayload = captured;
  detail = await data(await api(page.request, `/submissions/${uncertainID}`));
  assert.equal(detail.submission.status, "unknown");
  repeat = await data(
    await api(
      page.request,
      "/submissions",
      "POST",
      uncertainPayload,
      member.csrf_token,
    ),
  );
  assert.equal(repeat.submission.id, uncertainID);
  assert.equal((await audit()).pushes, 2);
  await screenshot("unknown", 390);

  const result = await audit();
  for (const key of [
    "credential_encrypted",
    "secrets_protected",
    "remote_id_snapshot_saved",
    "owner_remote_id_used",
    "aggregate_false",
    "zero_preserved",
  ])
    assert.equal(result[key], true, key);
  assert.equal(result.submissions, 2);
  assert.equal(result.states.accepted, 1);
  assert.equal(result.states.unknown, 1);
  assert.equal(result.pushes, 2);
  assert.deepEqual(await page.evaluate(() => Object.keys(localStorage)), []);
  assert.deepEqual(errors, []);
  console.log(
    JSON.stringify(
      {
        result: "passed",
        scope: "mock vendor only; isolated SQLite/miniredis/fake SMTP",
        audit: result,
        viewport_widths: [1440, 768, 390, 320],
      },
      null,
      2,
    ),
  );
} catch (error) {
  if (page)
    await page
      .screenshot({
        path: "test-results/submissions-failure.png",
        fullPage: true,
      })
      .catch(() => {});
  throw error;
} finally {
  await browser.close();
}
