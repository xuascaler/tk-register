import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { mkdir } from "node:fs/promises";
import { chromium } from "@playwright/test";

const port = Number(process.env.TK_TEST_PORT || 4179);
const server = spawn(
  process.execPath,
  [
    "node_modules/vite/bin/vite.js",
    "--host",
    "127.0.0.1",
    "--port",
    String(port),
    "--strictPort",
  ],
  { stdio: "pipe" },
);
server.stderr.on("data", (data) => process.stderr.write(data));
let browser;
try {
  await new Promise((resolve, reject) => {
    server.stdout.on("data", (data) => {
      if (String(data).includes("Local:")) resolve();
    });
    server.on("exit", (code) => reject(new Error(`Vite exited: ${code}`)));
    setTimeout(
      () => reject(new Error("Vite startup timed out")),
      15000,
    ).unref();
  });
  browser = await chromium.launch({
    headless: true,
    ...(process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH
      ? { executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH }
      : {}),
  });
  const page = await browser.newPage({
    viewport: { width: 1280, height: 1000 },
  });
  await mkdir("test-results", { recursive: true });
  const capture = async (name) => {
    for (const width of [1280, 768, 390, 320]) {
      await page.setViewportSize({ width, height: 1000 });
      await page.evaluate(() => document.fonts.ready);
      await page.waitForTimeout(120);
      assert.equal(await page.locator(".header").count(), 1);
      assert.equal(
        await page.evaluate(
          () => document.documentElement.scrollWidth <= window.innerWidth,
        ),
        true,
        `No horizontal overflow in ${name} at ${width}`,
      );
      await page.screenshot({
        path: `test-results/${name}-${width}.png`,
        fullPage: true,
      });
    }
    await page.setViewportSize({ width: 1280, height: 1000 });
  };
  const pageErrors = [];
  page.on("pageerror", (error) => pageErrors.push(error.message));
  const status = {
    registration_enabled: true,
    password_login_enabled: true,
    email_code_login_enabled: true,
    email_send_cooldown_seconds: 60,
    registration_code_ttl_seconds: 600,
    login_code_ttl_seconds: 600,
  };
  let user = null;
  let saves = [];
  let codes = 0;
  let sessionExpired = false;
  let personalToken = null;
  let tokenRevision = 0;
  const tokenSecrets = [];
  const userSession = (role) => ({
    user: {
      id: role === "admin" ? 1 : 2,
      email: role === "admin" ? "admin@example.com" : "member@example.com",
      username: role === "admin" ? "admin@example.com" : "member@example.com",
      role,
      email_verified: true,
    },
    csrf_token: "test-csrf",
  });
  let settings = [
    {
      key: "smtp_host",
      type: "string",
      category: "email",
      value: "smtp.example.com",
      description: "Hostname of your SMTP server.",
    },
    {
      key: "smtp_port",
      type: "integer",
      category: "email",
      value: "587",
      min: 1,
      max: 65535,
      description: "SMTP connection port.",
    },
    {
      key: "smtp_password",
      type: "secret",
      category: "email",
      value: "",
      configured: true,
      secret: true,
      description: "Leave blank to keep the stored password.",
    },
    {
      key: "smtp_from_name",
      type: "string",
      category: "email",
      value: "GoAPI",
      description: "Sender name shown in the inbox.",
    },
    {
      key: "registration_enabled",
      type: "boolean",
      category: "access",
      value: "true",
      description: "Allow new accounts with verified email.",
    },
    {
      key: "email_send_cooldown_seconds",
      type: "integer",
      category: "verification",
      value: "60",
      min: 1,
      description: "Minimum interval between code emails.",
    },
    {
      key: "admin_session_absolute_ttl_seconds",
      type: "integer",
      category: "sessions",
      value: "28800",
      min: 1,
      description: "Administrator session lifetime.",
    },
  ];
  await page.route("**/tk-register/api/**", async (route) => {
    const req = route.request();
    const path = new URL(req.url()).pathname.replace("/tk-register/api", "");
    const body = req.postDataJSON();
    const ok = (data) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ success: true, data }),
      });
    const fail = (code, message) =>
      route.fulfill({
        status: code,
        contentType: "application/json",
        body: JSON.stringify({ success: false, message }),
      });
    if (path === "/vendors") return ok({ vendors: [] });
    if (path === "/auth/status") return ok(status);
    if (path === "/auth/me")
      return user ? ok(userSession(user)) : fail(401, "Please sign in.");
    if (path === "/account/api-token" || path === "/account/api-token/rotate") {
      assert.ok(user);
      if (req.method() === "GET") return ok({ token: personalToken });
      assert.equal(req.headers()["x-csrf-token"], "test-csrf");
      if (req.method() === "DELETE" || path.endsWith("/rotate")) {
        assert.deepEqual(body, { token_id: personalToken.id, revision: personalToken.revision });
      }
      if (req.method() === "DELETE") {
        personalToken = { ...personalToken, revoked_at: new Date().toISOString(), revision: ++tokenRevision };
        return ok({ token: personalToken });
      }
      const plaintext = `tkr_fixture_secret_${++tokenRevision}_abcdefghijklmnopqrstuvwxyz`;
      tokenSecrets.push(plaintext);
      personalToken = { id: "1", revision: tokenRevision, prefix: "tkr_fixture_", last_four: plaintext.slice(-4), created_at: new Date().toISOString(), last_used_at: null, expires_at: null, revoked_at: null };
      return ok({ token: personalToken, plaintext_token: plaintext });
    }
    if (path === "/auth/code") {
      codes++;
      assert.ok(["register", "login"].includes(body.purpose));
      return ok({ retry_after: 60, expires_in: 600 });
    }
    if (path === "/auth/register") {
      assert.equal(body.username, body.email);
      assert.equal(body.code, "A1b2C3");
      user = "member";
      return ok(userSession(user));
    }
    if (path === "/auth/login") {
      if (body.email === "admin@example.com" && body.method === "code")
        return fail(403, "Administrators must sign in with a password first.");
      if (body.email === "admin@example.com")
        return ok({
          challenge_required: true,
          challenge_id: "challenge-1",
          expires_in: 600,
          code_expires_in: 600,
          retry_after: 60,
        });
      user = "member";
      return ok(userSession(user));
    }
    if (path === "/auth/admin/verify") {
      assert.equal(body.challenge_id, "challenge-1");
      assert.equal(body.code, "B2c3D4");
      user = "admin";
      return ok(userSession(user));
    }
    if (path === "/auth/logout") {
      assert.equal(req.headers()["x-csrf-token"], "test-csrf");
      user = null;
      return ok({});
    }
    if (path === "/admin/settings") {
      if (sessionExpired) return fail(401, "Your session has expired.");
      assert.equal(user, "admin");
      if (req.method() === "PUT") {
        assert.equal(req.headers()["x-csrf-token"], "test-csrf");
        saves.push(body.settings);
        settings = settings.map((s) => ({
          ...s,
          value: body.settings[s.key] ?? s.value,
        }));
      }
      return ok({ settings });
    }
    throw new Error(`Unexpected request ${path}`);
  });
  await page.goto(`http://127.0.0.1:${port}/tk-register/`);
  await page.getByRole("heading", { name: "Sign in", exact: true }).waitFor();
  await capture("login");
  await page
    .getByRole("button", { name: "Create account", exact: true })
    .click();
  await capture("register");
  await page
    .getByLabel("Email address", { exact: true })
    .fill("member@example.com");
  await page.getByRole("button", { name: "Send code", exact: true }).click();
  assert.equal(
    await page.getByRole("button", { name: /Resend in/ }).isDisabled(),
    true,
  );
  await page.getByLabel("Verification code", { exact: true }).fill("A1b2C3");
  await page.getByLabel("Password", { exact: true }).fill("test-password");
  await page
    .getByLabel("Confirm password", { exact: true })
    .fill("other-password");
  await page
    .getByRole("button", { name: "Create account", exact: true })
    .click();
  await page
    .getByRole("alert")
    .filter({ hasText: "passwords do not match" })
    .waitFor();
  await page
    .getByLabel("Confirm password", { exact: true })
    .fill("test-password");
  await page
    .getByRole("button", { name: "Create account", exact: true })
    .click();
  await page
    .getByRole("heading", { name: "Submit account", exact: true })
    .waitFor();
  await page
    .getByRole("button", { name: "Account settings", exact: true })
    .click();
  await capture("account");
  await page.getByRole("button", { name: "Generate token", exact: true }).click();
  const freshToken = page.getByLabel("New API token", { exact: true });
  await freshToken.waitFor();
  assert.equal(await freshToken.inputValue(), tokenSecrets[0]);
  assert.equal(await page.getByRole("button", { name: "Copy API token", exact: true }).count(), 1);
  await capture("account-token");
  for (const secret of tokenSecrets) {
    assert.equal(await page.evaluate(value => JSON.stringify(localStorage).includes(value) || JSON.stringify(sessionStorage).includes(value), secret), false);
  }
  await page.reload();
  await page.getByLabel("Masked API token", { exact: true }).waitFor();
  assert.equal(await page.getByRole("button", { name: "Copy API token", exact: true }).count(), 0);
  assert.equal((await page.locator("body").textContent()).includes(tokenSecrets[0]), false);
  await page.getByRole("button", { name: "Regenerate", exact: true }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Cancel", exact: true }).click();
  assert.equal(tokenSecrets.length, 1, "Cancelling must not rotate the token");
  await page.getByRole("button", { name: "Regenerate", exact: true }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Regenerate", exact: true }).click();
  await freshToken.waitFor();
  assert.equal(await freshToken.inputValue(), tokenSecrets[1]);
  assert.notEqual(tokenSecrets[0], tokenSecrets[1]);
  await page.getByRole("button", { name: "Revoke token", exact: true }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Revoke token", exact: true }).click();
  await page.getByRole("button", { name: "Generate token", exact: true }).waitFor();
  assert.equal(await freshToken.count(), 0);
  assert.equal(await page.getByRole("button", { name: "Copy API token", exact: true }).count(), 0);
  for (const secret of tokenSecrets) {
    assert.equal(await page.evaluate(value => JSON.stringify(localStorage).includes(value) || JSON.stringify(sessionStorage).includes(value), secret), false);
  }
  assert.equal(
    await page.getByRole("button", { name: "Config", exact: true }).count(),
    0,
  );
  assert.equal(codes, 1);
  await page.getByRole("button", { name: "Sign out", exact: true }).click();
  await page
    .getByLabel("Email address", { exact: true })
    .fill("admin@example.com");
  await page.getByLabel("Password", { exact: true }).fill("test-password");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await page.getByRole("heading", { name: "Check your email" }).waitFor();
  await capture("admin-verify");
  assert.equal(
    await page.getByRole("button", { name: "Config", exact: true }).count(),
    0,
  );
  await page.getByLabel("Verification code", { exact: true }).fill("B2c3D4");
  await page.getByRole("button", { name: "Verify and sign in" }).click();
  await page.getByRole("button", { name: "Config", exact: true }).click();
  await page.getByLabel("smtp_host", { exact: true }).waitFor();
  await page.getByLabel("smtp_from_name", { exact: true }).fill("TK Register");
  await page.getByRole("tab", { name: "Access" }).click();
  await page.getByRole("switch", { name: "registration_enabled" }).uncheck();
  await page.getByRole("button", { name: "Save changes" }).click();
  await page
    .getByRole("status")
    .filter({ hasText: "Changes saved." })
    .waitFor();
  assert.deepEqual(saves[0], {
    smtp_from_name: "TK Register",
    registration_enabled: "false",
  });
  await page.getByRole("tab", { name: "Email delivery" }).click();
  assert.equal(
    await page.getByLabel("smtp_password", { exact: true }).inputValue(),
    "",
  );
  await page.getByLabel("smtp_port", { exact: true }).fill("65536");
  await page.getByRole("button", { name: "Save changes" }).click();
  await page.getByText("Enter 65535 or less.", { exact: true }).waitFor();
  assert.equal(saves.length, 1);
  await page.getByRole("button", { name: "Discard", exact: true }).click();
  await capture("config");
  await page.setViewportSize({ width: 390, height: 1000 });
  await page
    .getByLabel("Configuration section", { exact: true })
    .selectOption("sessions");
  await page.getByText("8 hours", { exact: true }).waitFor();
  await page.setViewportSize({ width: 1280, height: 1000 });
  sessionExpired = true;
  await page
    .getByLabel("admin_session_absolute_ttl_seconds", { exact: true })
    .fill("3600");
  await page.getByRole("button", { name: "Save changes" }).click();
  await page.getByRole("heading", { name: "Sign in", exact: true }).waitFor();
  await page
    .getByText("Your session has expired. Please sign in again.", {
      exact: true,
    })
    .waitFor();
  user = null;
  status.password_login_enabled = false;
  await page.reload();
  await page.getByLabel("Verification code", { exact: true }).waitFor();
  assert.equal(await page.getByLabel("Password", { exact: true }).count(), 0);
  await page
    .getByRole("button", { name: "Admin password", exact: true })
    .click();
  await page.getByLabel("Password", { exact: true }).waitFor();
  await page
    .getByText(
      "Password sign-in is available to administrators. Members can use an email code.",
      { exact: true },
    )
    .waitFor();
  assert.deepEqual(await page.evaluate(() => Object.keys(localStorage)), []);
  assert.deepEqual(pageErrors, []);
  console.log(
    "Passed registration, member access, administrator two-step sign-in, settings saves, secret handling, validation, responsive layouts, and session expiry. No real APIs or emails were used.",
  );
} finally {
  if (browser) await browser.close();
  server.kill("SIGTERM");
}
