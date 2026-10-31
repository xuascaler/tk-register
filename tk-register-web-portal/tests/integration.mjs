import assert from "node:assert/strict";
import { mkdir } from "node:fs/promises";
import { chromium } from "@playwright/test";

// Run against TestBrowserFixture only: isolated SQLite, miniredis, and fake SMTP.
// The loopback guard prevents accidentally running account creation on production.
const origin = new URL(process.env.TK_E2E_URL || "http://127.0.0.1:39123");
if (
  origin.protocol !== "http:" ||
  !["127.0.0.1", "localhost", "[::1]"].includes(origin.hostname)
) {
  throw new Error("TK_E2E_URL must be an HTTP loopback fixture.");
}
const base = origin.origin;
const browser = await chromium.launch({
  headless: true,
  ...(process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH
    ? { executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH }
    : {}),
});
try {
  const context = await browser.newContext({
    viewport: { width: 1280, height: 1000 },
  });
  const page = await context.newPage();
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  const code = async (email, purpose) => {
    const response = await page.request.get(
      `${base}/__test/code?${new URLSearchParams({ email, purpose })}`,
    );
    assert.equal(response.status(), 200);
    const body = await response.json();
    assert.match(body.data.code, /^[A-Z0-9]{6}$/);
    return body.data.code;
  };
  const advance = async (seconds) => {
    const response = await page.request.get(
      `${base}/__test/advance?seconds=${seconds}`,
    );
    assert.equal(response.status(), 200);
  };
  const register = async (email) => {
    await page
      .getByRole("button", { name: "Create account", exact: true })
      .click();
    await page.getByLabel("Email address", { exact: true }).fill(email);
    await page.getByRole("button", { name: "Send code", exact: true }).click();
    await page.getByText(/Code requested\. Check your inbox/).waitFor();
    await page
      .getByLabel("Verification code", { exact: true })
      .fill(await code(email, "register"));
    await page
      .getByLabel("Password", { exact: true })
      .fill("Fixture-password-2026!");
    await page
      .getByLabel("Confirm password", { exact: true })
      .fill("Fixture-password-2026!");
    await page
      .getByRole("button", { name: "Create account", exact: true })
      .click();
    await page
      .getByRole("heading", { name: "Submit account", exact: true })
      .waitFor();
  };
  const signout = async () => {
    await page.getByRole("button", { name: "Sign out", exact: true }).click();
    await page.getByRole("heading", { name: "Sign in", exact: true }).waitFor();
  };
  await mkdir("test-results", { recursive: true });
  await page.goto(`${base}/tk-register/`);
  await page.getByRole("heading", { name: "Sign in", exact: true }).waitFor();
  await register("admin@example.com");
  await page
    .getByRole("button", { name: "Account settings", exact: true })
    .click();
  await page.getByText("Administrator", { exact: true }).waitFor();
  const cookies = await context.cookies();
  const session = cookies.find(
    (cookie) => cookie.name === "tk_register_session",
  );
  assert.ok(session?.httpOnly);
  assert.equal(session.path, "/tk-register");
  assert.equal(session.sameSite, "Lax");
  await signout();
  await advance(61);
  await page
    .getByLabel("Email address", { exact: true })
    .fill("admin@example.com");
  await page
    .getByLabel("Password", { exact: true })
    .fill("Fixture-password-2026!");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await page
    .getByRole("heading", { name: "Check your email", exact: true })
    .waitFor();
  assert.equal(
    (await context.cookies()).some(
      (cookie) => cookie.name === "tk_register_session",
    ),
    false,
  );
  await page
    .getByLabel("Verification code", { exact: true })
    .fill(await code("admin@example.com", "admin_login"));
  await page
    .getByRole("button", { name: "Verify and sign in", exact: true })
    .click();
  await page
    .getByRole("heading", { name: "Submit account", exact: true })
    .waitFor();
  await page.getByRole("button", { name: "Config", exact: true }).click();
  await page.getByLabel("smtp_host", { exact: true }).waitFor();
  let response = await page.request.get(
    `${base}/tk-register/api/admin/settings`,
  );
  assert.equal(response.status(), 200);
  let saved = (await response.json()).data.settings;
  assert.equal(saved.length, 25);
  assert.equal(
    saved.find((setting) => setting.key === "smtp_password").value,
    "",
  );
  await page.getByRole("tab", { name: "Verification" }).click();
  await page
    .getByLabel("email_send_cooldown_seconds", { exact: true })
    .fill("90");
  await page.getByRole("button", { name: "Save changes", exact: true }).click();
  await page
    .getByRole("status")
    .filter({ hasText: "Changes saved." })
    .waitFor();
  response = await page.request.get(`${base}/tk-register/api/admin/settings`);
  saved = (await response.json()).data.settings;
  assert.equal(
    saved.find((setting) => setting.key === "email_send_cooldown_seconds")
      .value,
    "90",
  );
  await page.reload();
  await page.getByRole("button", { name: "Config", exact: true }).click();
  await page.getByRole("tab", { name: "Verification" }).click();
  assert.equal(
    await page
      .getByLabel("email_send_cooldown_seconds", { exact: true })
      .inputValue(),
    "90",
  );
  await page.screenshot({
    path: "test-results/integration-config-1280.png",
    fullPage: true,
  });
  await page.setViewportSize({ width: 390, height: 900 });
  await page.waitForTimeout(100);
  assert.equal(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
    true,
  );
  await page.screenshot({
    path: "test-results/integration-config-390.png",
    fullPage: true,
  });
  await page.setViewportSize({ width: 1280, height: 1000 });
  await signout();
  await register("member@example.com");
  assert.equal(
    await page.getByRole("button", { name: "Config", exact: true }).count(),
    0,
  );
  response = await page.request.get(`${base}/tk-register/api/admin/settings`);
  assert.equal(response.status(), 403);
  await page.reload();
  await page
    .getByRole("heading", { name: "Submit account", exact: true })
    .waitFor();
  await page
    .getByRole("button", { name: "Account settings", exact: true })
    .click();
  await page.getByText("member@example.com", { exact: true }).waitFor();
  await signout();
  await advance(91);
  await page.getByRole("button", { name: "Email code", exact: true }).click();
  await page
    .getByLabel("Email address", { exact: true })
    .fill("member@example.com");
  await page.getByRole("button", { name: "Send code", exact: true }).click();
  await page.getByText(/Code requested\. Check your inbox/).waitFor();
  await page
    .getByLabel("Verification code", { exact: true })
    .fill(await code("member@example.com", "login"));
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await page
    .getByRole("heading", { name: "Submit account", exact: true })
    .waitFor();
  await signout();
  await page
    .getByLabel("Email address", { exact: true })
    .fill("member@example.com");
  await page
    .getByLabel("Password", { exact: true })
    .fill("Fixture-password-2026!");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await page
    .getByRole("heading", { name: "Submit account", exact: true })
    .waitFor();
  await signout();
  assert.deepEqual(await page.evaluate(() => Object.keys(localStorage)), []);
  assert.deepEqual(errors, []);
  console.log(
    "Real backend integration passed: admin and member registration; admin two-step sign-in; member password/code sign-in; HttpOnly session restoration/logout; 25 setting descriptors; Config save persisted after reload; member access rejected. Isolated SQLite/miniredis/fake SMTP only.",
  );
} finally {
  await browser.close();
}
