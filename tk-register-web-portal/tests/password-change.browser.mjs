import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { mkdir, readFile, writeFile } from 'node:fs/promises';
import { dirname, extname, resolve, sep } from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium, expect } from '@playwright/test';

// This test is entirely synthetic. The allowlist rejects every external URL,
// and every business endpoint is intercepted before reaching the local server.
const project = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const dist = resolve(project, 'dist');
const output = resolve(project, 'test-results/password-change');
await mkdir(output, { recursive: true });
const mime = { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.svg': 'image/svg+xml', '.woff2': 'font/woff2' };
const server = createServer(async (req, res) => {
  try {
    const pathname = decodeURIComponent(new URL(req.url, 'http://localhost').pathname);
    if (pathname.startsWith('/tk-register/api/')) throw new Error('Unmocked business endpoint');
    const file = resolve(dist, pathname === '/tk-register/' ? 'index.html' : pathname.replace(/^\/tk-register\//, ''));
    if (!pathname.startsWith('/tk-register/') || !file.startsWith(dist + sep)) throw new Error('Outside dist');
    res.writeHead(200, { 'Content-Type': mime[extname(file)] || 'application/octet-stream' });
    res.end(await readFile(file));
  } catch { res.writeHead(404).end(); }
});
await new Promise((done, reject) => { server.once('error', reject); server.listen(0, '127.0.0.1', done); });
const origin = `http://127.0.0.1:${server.address().port}`;
const results = [];
let browser;
let currentPage;
const delivery = { challenge_id: 'synthetic-challenge', retry_after: 60, expires_in: 600, code_length: 6 };
const status = { registration_enabled: true, password_login_enabled: true, email_code_login_enabled: true, email_send_cooldown_seconds: 60, registration_code_ttl_seconds: 600, login_code_ttl_seconds: 600, code_length: 6, email_max_length: 50, password_min_length: 8, password_max_length: 72 };

async function openPage(role = 'member', size = [1440, 1000]) {
  const page = await browser.newPage({ viewport: { width: size[0], height: size[1] } });
  currentPage = page;
  const state = { unexpected: [], sent: [], changes: [], mutations: [], errors: [], onCode: null, onChange: null, logins: [] };
  page.on('pageerror', error => state.errors.push(error.message));
  await page.clock.install();
  await page.route('**/*', async route => {
    const req = route.request(); const url = new URL(req.url());
    if (url.origin !== origin) { state.unexpected.push(`External ${url.origin}${url.pathname}`); return route.abort(); }
    if (!url.pathname.startsWith('/tk-register/api/')) return route.continue();
    const path = url.pathname.slice('/tk-register/api'.length);
    const method = req.method();
    const ok = data => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ success: true, data }) });
    const fail = (code, message, http = 400, retry_after = 0) => route.fulfill({ status: http, contentType: 'application/json', body: JSON.stringify({ success: false, code, message, retry_after }) });
    const user = { id: role === 'admin' ? 1 : 2, email: `${role}@example.test`, role, status: 'active', created_at: '2026-11-20T00:00:00Z' };
    if (method !== 'GET') state.mutations.push(path);
    if (path === '/auth/status' && method === 'GET') return ok(status);
    if (path === '/auth/me' && method === 'GET') return ok({ user, csrf_token: 'synthetic-csrf' });
    if (path === '/members/sources' && method === 'GET') return ok({ sources: [{ id: 'myself', label: 'myself' }] });
    if (path === '/account/api-token' && method === 'GET') return ok({ token: null });
    if (path === '/account/password/code' && method === 'POST') {
      state.sent.push({ body: req.postDataJSON(), csrf: req.headers()['x-csrf-token'] });
      return state.onCode ? state.onCode({ route, ok, fail }) : ok(delivery);
    }
    if (path === '/account/password' && method === 'POST') {
      state.changes.push({ body: req.postDataJSON(), csrf: req.headers()['x-csrf-token'] });
      return state.onChange ? state.onChange({ route, ok, fail }) : ok({ password_changed: true, sign_in_required: true });
    }
    if (path === '/auth/login' && method === 'POST') { state.logins.push(req.headers()); return fail('INVALID_CREDENTIALS', 'Synthetic sign-in failure.'); }
    state.unexpected.push(`${method} ${path}`); return fail('UNMOCKED', 'Unexpected business endpoint.', 500);
  });
  await page.goto(`${origin}/tk-register/#/account`);
  await expect(page.getByRole('heading', { name: 'Account settings' })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'API token', exact: true })).toBeVisible();
  return { page, state };
}
const modal = page => page.getByRole('dialog', { name: 'Change password' });
async function openDialog(page) {
  await page.getByRole('button', { name: 'Change password', exact: true }).click();
  await expect(modal(page)).toBeVisible();
  await expect(modal(page).getByLabel('Verification code', { exact: true })).toBeFocused();
}
async function populate(page, password = 'New-password-123') {
  const dialog = modal(page);
  await dialog.getByLabel('Verification code', { exact: true }).fill('abc123');
  await dialog.getByLabel('New password', { exact: true }).fill(password);
  await dialog.getByLabel('Confirm new password', { exact: true }).fill(password);
}
async function submit(page) { await modal(page).getByRole('button', { name: 'Change password', exact: true }).click(); }
async function clean(page, state) {
  assert.deepEqual(state.unexpected, []); assert.deepEqual(state.errors, []);
  assert.ok(!state.mutations.some(path => path.includes('api-token')), 'Password change must not mutate personal API tokens.');
  const persisted = await page.evaluate(() => ({ url: location.href, local: JSON.stringify(localStorage), session: JSON.stringify(sessionStorage) }));
  for (const text of Object.values(persisted)) assert.ok(!/New-password|abc123|synthetic-challenge/.test(text), 'Credentials must not appear in URLs or storage.');
  await page.close();
}
async function geometry(page) {
  const value = await page.evaluate(() => {
    const dialog = document.querySelector('dialog'); const box = dialog.getBoundingClientRect();
    const footer = dialog.querySelector('footer').getBoundingClientRect();
    const field = dialog.querySelector('.admin-create-fields');
    return { width: innerWidth, height: innerHeight, left: box.left, right: box.right, top: box.top, bottom: box.bottom, footerTop: footer.top, footerBottom: footer.bottom, dialogOverflow: dialog.scrollWidth - dialog.clientWidth, pageOverflow: document.documentElement.scrollWidth - innerWidth, fieldOverflow: field.scrollWidth - field.clientWidth, scrollable: field.scrollHeight > field.clientHeight, bodyOverflow: document.body.style.overflow };
  });
  assert.ok(value.left >= 0 && value.right <= value.width + 1 && value.top >= 0 && value.bottom <= value.height + 1, JSON.stringify(value));
  assert.ok(value.footerTop >= value.top && value.footerBottom <= value.height, 'Footer must remain reachable.');
  assert.ok(value.dialogOverflow <= 1 && value.pageOverflow <= 1 && value.fieldOverflow <= 1, JSON.stringify(value));
  assert.equal(value.bodyOverflow, 'hidden');
  if (value.height <= 568) assert.ok(value.scrollable, 'Short screens should scroll the fields inside the dialog.');
  return value;
}

try {
  browser = await chromium.launch({ executablePath: process.env.CHROME_PATH || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', headless: true });
  for (const role of ['member', 'admin']) {
    for (const size of [[1440, 1000], [393, 852], [320, 568], [844, 390]]) {
      const { page, state } = await openPage(role, size);
      await page.screenshot({ path: resolve(output, `${role}-${size.join('x')}-settings.png`), fullPage: true });
      await openDialog(page);
      await expect(modal(page).getByLabel('Email address', { exact: true })).toHaveValue(`${role}@example.test`);
      await expect(modal(page).getByLabel('Email address', { exact: true })).toHaveAttribute('readonly', '');
      await expect(modal(page).getByText('You’ll be signed out of all browser sessions after changing your password.')).toBeVisible();
      assert.equal(await modal(page).getByLabel('Current password', { exact: true }).count(), 0);
      const metrics = await geometry(page);
      await page.screenshot({ path: resolve(output, `${role}-${size.join('x')}-dialog.png`) });
      for (let i = 0; i < 12; i++) { await page.keyboard.press('Tab'); assert.ok(await modal(page).evaluate(element => element.contains(document.activeElement)), 'Keyboard focus must remain inside modal.'); }
      if (size[0] === 393) {
        await page.setViewportSize({ width: 844, height: 390 }); await geometry(page);
        await page.setViewportSize({ width: 393, height: 852 }); await geometry(page);
      }
      await page.keyboard.press('Escape');
      await expect(modal(page)).toHaveCount(0);
      await expect(page.getByRole('button', { name: 'Change password', exact: true })).toBeFocused();
      assert.equal(await page.evaluate(() => document.body.style.overflow), '');
      await clean(page, state); results.push({ role, size, metrics, passed: true });
    }
  }

  {
    const { page, state } = await openPage(); await openDialog(page);
    let deliver;
    state.onCode = ({ ok }) => new Promise(resolve => { deliver = async () => { await ok(delivery); resolve(); }; });
    await modal(page).getByRole('button', { name: 'Send code', exact: true }).click();
    await expect.poll(() => state.sent.length).toBe(1);
    await expect(modal(page).getByRole('button', { name: 'Cancel', exact: true })).toBeDisabled();
    await expect(modal(page).getByRole('button', { name: 'Close change password', exact: true })).toBeDisabled();
    await page.keyboard.press('Escape'); await expect(modal(page)).toBeVisible();
    await deliver();
    await expect(modal(page).getByRole('button', { name: 'Resend in 60s', exact: true })).toBeDisabled();
    await page.clock.fastForward(1000);
    await expect(modal(page).getByRole('button', { name: 'Resend in 59s', exact: true })).toBeDisabled();
    assert.deepEqual(state.sent[0], { body: {}, csrf: 'synthetic-csrf' });
    await expect(modal(page).getByText('Code sent to your email. Valid for 10 minutes.')).toBeVisible();
    await populate(page); await modal(page).getByLabel('Verification code', { exact: true }).fill('ab!');
    await submit(page); await expect(modal(page).getByText('Enter the 6-character verification code from your email.')).toBeVisible();
    assert.equal(state.changes.length, 0);
    await populate(page); await modal(page).getByLabel('Confirm new password', { exact: true }).fill('Does-not-match');
    await submit(page); await expect(modal(page).getByText('The passwords do not match.')).toBeVisible();
    assert.equal(state.changes.length, 0);
    await populate(page, '😀'.repeat(19)); await submit(page);
    await expect(modal(page).getByText('Password must be at most 72 UTF-8 bytes. Use a shorter password.')).toBeVisible();
    assert.equal(state.changes.length, 0);
    await populate(page);
    await modal(page).getByRole('button', { name: 'Show new password', exact: true }).click();
    await expect(modal(page).getByLabel('New password', { exact: true })).toHaveAttribute('type', 'text');
    await modal(page).getByRole('button', { name: 'Hide new password', exact: true }).click();
    state.onChange = ({ fail }) => fail('INVALID_CODE', 'The verification code is invalid or expired.');
    await submit(page); await expect(modal(page).getByText('The verification code is invalid or expired.')).toBeVisible();
    await expect(modal(page).getByLabel('Verification code', { exact: true })).toBeFocused();
    await modal(page).getByLabel('Verification code', { exact: true }).fill('def456');
    state.onChange = ({ fail }) => fail('PASSWORD_UNCHANGED', 'Choose a different password.');
    await submit(page); await expect(modal(page).getByText('Choose a different password.')).toBeVisible();
    await populate(page, 'New-password-456');
    state.onChange = ({ fail }) => fail('INTERNAL_ERROR', 'Unable to update your password. Please try again.', 503);
    await submit(page); await expect(modal(page).getByText('Unable to update your password. Please try again.')).toBeVisible();
    const first = state.changes.at(-1).body;
    state.onChange = ({ fail }) => fail('INTERNAL_ERROR', 'Unable to update your password. Please try again.', 503);
    await submit(page); await expect.poll(() => state.changes.length).toBe(4);
    await expect(modal(page).getByRole('button', { name: 'Change password', exact: true })).toBeEnabled();
    assert.deepEqual(state.changes.at(-1).body, first, 'SQL failure retry must preserve proof and request_id.');
    await modal(page).getByLabel('New password', { exact: true }).fill('New-password-789');
    await modal(page).getByLabel('Confirm new password', { exact: true }).fill('New-password-789');
    let finish;
    state.onChange = ({ ok }) => new Promise(resolve => { finish = async () => { await ok({ password_changed: true, sign_in_required: true }); resolve(); }; });
    await submit(page); await expect.poll(() => state.changes.length).toBe(5);
    assert.notEqual(state.changes.at(-1).body.request_id, first.request_id, 'Edited inputs must use a new operation ID.');
    await page.keyboard.press('Escape'); await expect(modal(page)).toBeVisible();
    await expect(modal(page).getByRole('button', { name: 'Cancel', exact: true })).toBeDisabled();
    await expect(modal(page).getByLabel('New password', { exact: true })).toBeDisabled();
    await finish();
    await expect(page.getByRole('heading', { name: 'Sign in', exact: true })).toBeVisible();
    await expect(page.getByRole('status').filter({ hasText: 'Password updated. Please sign in again.' })).toBeVisible();
    await expect(page.getByRole('heading', { name: 'Sign in', exact: true })).toBeFocused();
    assert.equal(await page.evaluate(() => document.body.style.overflow), '');
    assert.equal(await page.locator('dialog').count(), 0);
    await expect(page.getByLabel('Password', { exact: true })).toHaveValue('');
    await page.getByLabel('Email address', { exact: true }).fill('member@example.test');
    await page.getByLabel('Password', { exact: true }).fill('New-password-789');
    await page.getByRole('button', { name: 'Sign in', exact: true }).click();
    await expect.poll(() => state.logins.length).toBe(1);
    assert.equal(state.logins[0]['x-csrf-token'], undefined, 'Success must clear the old browser CSRF token.');
    for (const change of state.changes) {
      assert.equal(change.csrf, 'synthetic-csrf');
      assert.deepEqual(Object.keys(change.body).sort(), ['challenge_id', 'code', 'confirmation', 'password', 'request_id']);
    }
    await page.screenshot({ path: resolve(output, 'password-updated-sign-in.png') });
    await clean(page, state); results.push({ scenario: 'validation, busy guards, same-operation retries, edits, logout and CSRF clear', passed: true });
  }
  {
    const { page, state } = await openPage(); await openDialog(page);
    state.onCode = ({ fail }) => fail('RATE_LIMITED', 'Wait before requesting another code.', 429, 5);
    await modal(page).getByRole('button', { name: 'Send code', exact: true }).click();
    await expect(modal(page).getByText('Wait before requesting another code.')).toBeVisible();
    await expect(modal(page).getByRole('button', { name: 'Resend in 5s', exact: true })).toBeDisabled();
    await page.clock.fastForward(5000); state.onCode = null;
    await modal(page).getByRole('button', { name: 'Send code', exact: true }).click();
    await expect(modal(page).getByText('Code sent to your email. Valid for 10 minutes.')).toBeVisible();
    await populate(page); await page.clock.fastForward(601000); await submit(page);
    await expect(modal(page).locator('.field-error')).toHaveText('The code has expired. Send a new verification code.');
    assert.equal(state.changes.length, 0);
    await modal(page).getByRole('button', { name: 'Cancel', exact: true }).click();
    await openDialog(page);
    for (const label of ['Verification code', 'New password', 'Confirm new password']) await expect(modal(page).getByLabel(label, { exact: true })).toHaveValue('');
    await submit(page); await expect(modal(page).getByText('Send a verification code to your email first.')).toBeVisible();
    await clean(page, state); results.push({ scenario: 'rate limits, expiry and close clearing secrets', passed: true });
  }
  {
    const { page, state } = await openPage('admin', [393, 852]); await openDialog(page);
    await modal(page).getByRole('button', { name: 'Send code', exact: true }).click();
    await expect(modal(page).getByRole('button', { name: 'Resend in 60s', exact: true })).toBeDisabled();
    await populate(page);
    await modal(page).getByRole('button', { name: 'Cancel', exact: true }).click();
    await openDialog(page);
    for (const label of ['Verification code', 'New password', 'Confirm new password']) await expect(modal(page).getByLabel(label, { exact: true })).toHaveValue('');
    await expect(modal(page).getByRole('button', { name: 'Resend in 60s', exact: true })).toBeDisabled();
    await page.clock.fastForward(60000);
    await modal(page).getByRole('button', { name: 'Send code', exact: true }).click(); await populate(page);
    state.onChange = ({ fail }) => fail('RATE_LIMITED', 'Try changing your password again shortly.', 429, 3);
    await submit(page); await expect(modal(page).getByText('Try changing your password again shortly.')).toBeVisible();
    await expect(modal(page).getByRole('button', { name: 'Try again in 3s', exact: true })).toBeDisabled();
    await page.clock.fastForward(3000);
    state.onChange = ({ fail }) => fail('PASSWORD_CHANGE_CONFLICT', 'This operation has expired.', 409);
    await submit(page); await expect(modal(page).getByText('This password change is no longer available. Send a new verification code.')).toBeVisible();
    await expect(modal(page).getByLabel('Verification code', { exact: true })).toHaveValue('');
    assert.equal(state.sent.length, 2);
    await clean(page, state); results.push({ scenario: 'close preserves resend cooldown, submit throttling and conflict restart', passed: true });
  }
  {
    const { page, state } = await openPage(); await openDialog(page);
    let finish;
    state.onCode = ({ ok }) => new Promise(resolve => { finish = async () => { await ok(delivery).catch(() => {}); resolve(); }; });
    await modal(page).getByRole('button', { name: 'Send code', exact: true }).click();
    await expect.poll(() => state.sent.length).toBe(1);
    await page.evaluate(() => { location.hash = '#/docs'; });
    await expect(modal(page)).toHaveCount(0);
    await finish();
    assert.equal(await page.evaluate(() => document.body.style.overflow), '');
    await page.evaluate(() => { location.hash = '#/account'; });
    await openDialog(page); await expect(modal(page).getByLabel('Verification code', { exact: true })).toHaveValue('');
    assert.equal(state.changes.length, 0);
    await clean(page, state); results.push({ scenario: 'unmount aborts pending delivery and unlocks body', passed: true });
  }
  await writeFile(resolve(output, 'results.json'), JSON.stringify(results, null, 2));
  console.log(`Password change browser checks passed: ${results.length} scenarios. Screenshots: ${output}`);
} catch (error) {
  if (currentPage && !currentPage.isClosed()) await currentPage.screenshot({ path: resolve(output, 'failure.png'), fullPage: true }).catch(() => {});
  throw error;
} finally { await browser?.close(); await new Promise(done => server.close(done)); }
