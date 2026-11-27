import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { readFile, writeFile, mkdir } from 'node:fs/promises';
import { dirname, extname, resolve, sep } from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium, expect } from '@playwright/test';

// Real built UI, synthetic data only. Every business API and external request is
// intercepted. Failure details must not trigger a submission, token grant or sync.
const project = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const dist = resolve(project, 'dist'), output = resolve(project, 'test-results/authorization-diagnostics');
await mkdir(output, { recursive: true });
const mime = { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.svg': 'image/svg+xml' };
const server = createServer(async (req, res) => {
  try {
    const path = new URL(req.url, 'http://localhost').pathname;
    const file = resolve(dist, path === '/tk-register/' ? 'index.html' : path.replace(/^\/tk-register\//, ''));
    if (!path.startsWith('/tk-register/') || !file.startsWith(dist + sep) || path.includes('/api/')) return res.writeHead(404).end();
    res.writeHead(200, { 'Content-Type': mime[extname(file)] || 'application/octet-stream', 'Cache-Control': 'no-store' }); res.end(await readFile(file));
  } catch { res.writeHead(404).end(); }
});
await new Promise(ok => server.listen(0, '127.0.0.1', ok));
const origin = `http://127.0.0.1:${server.address().port}`;
const proxy = 'socks5://example-user:example-proxy-secret@proxy.example.com:1080';
const proxyCheck = { ok: true, ip: '203.0.113.24', checked_at: new Date().toISOString(), elapsed_ms: 8 };
const snapshot = { id: '1', account_id: '13152', snapshot_date: '2026-11-20', collected_at: '2026-11-20T01:00:00Z', account_status: 'offline', upstream_status_raw: 'offline', upstream_error_message: '', cost_total_usd: 0, input_tokens_total: 0, output_tokens_total: 0, currency: 'USD', usage_5h_percent: 0, usage_7d_percent: 0, data_source: 'website' };
const account = { id: '13152', account_source: { id: 'myself', label: 'myself' }, user_id: '2', user_email: 'owner@example.test', can_manage: true, account: 'user@example.com', plan: 'claude/cc-max-20x', vendor_id: '1', vendor_name: 'TK Register 0', current_submission_id: '10', latest_submission_id: '10', submission_status: 'accepted', remote_id: '592781', platform_account_id: '25935', supplier_record_id: '592781', auth_json_updated_at: null, last_success_at: '2026-11-20T01:00:00Z', last_attempt_at: '2026-11-20T01:00:00Z', last_error: '', snapshot };
const results = [];
let browser, currentPage;
try {
  browser = await chromium.launch({ headless: true, executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome' });
  for (const entry of ['submit', 'update', 'deactivate']) for (const role of ['member', 'admin']) for (const width of [1024, 393]) for (const scenario of ['cookie-403', 'cookie-stale-session', 'cookie-no-upstream', 'cookie-unknown', 'proxy-auth']) {
    const context = await browser.newContext({ viewport: { width, height: 1000 } });
    const page = currentPage = await context.newPage();
    const calls = [], errors = [], unexpected = [];
    const requestID = `server-${entry}-${role}-${width}-${scenario}`;
    const scope = role === 'admin' ? '/admin/synced-accounts/13152' : '/accounts/13152';
    const operation = entry === 'deactivate' ? 'deactivate' : 'auth-json';
    const base = entry === 'submit' ? '/credential-flows' : `${scope}/${operation}/flows`;
    const failure = scenario === 'cookie-403'
      ? { code: 'AUTHORIZATION_REJECTED', message: 'The authorization provider rejected the request. Please try again or authorize this account again.', diagnostics: { stage: 'cookie_organizations', upstream_status: 403, upstream_code: 'permission_error', response_kind: 'json' } }
      : scenario === 'cookie-stale-session'
      ? { code: 'AUTHORIZATION_SESSION_NOT_FRESH', message: 'Session is not fresh enough to grant elevated access. Sign in again to continue.', diagnostics: { stage: 'cookie_authorize', upstream_status: 403, upstream_code: 'permission_error', response_kind: 'json' } }
      : scenario === 'cookie-no-upstream'
      ? { code: 'AUTHORIZATION_CONNECTION_FAILED', message: 'The authorization provider could not be reached. Please try again.', diagnostics: { stage: 'cookie_authorize', helper_status: 502 } }
      : scenario === 'proxy-auth'
      ? { code: 'PROXY_AUTHENTICATION_FAILED', message: 'The proxy rejected its username or password. Check the proxy credentials.', diagnostics: { stage: 'proxy_check' } }
      : { code: 'AUTHORIZATION_FAILED', message: 'Authorization could not be completed. Check your credentials or start again.', diagnostics: { stage: 'sk-ant-sid-private-server-secret', upstream_status: '403', upstream_code: 'sk-ant-sid-private-server-secret', response_kind: '<html>private</html>' } };
    page.on('pageerror', error => errors.push(error.message));
    page.on('dialog', dialog => dialog.accept());
    await page.addInitScript(() => Object.defineProperty(navigator, 'clipboard', { value: { writeText: async value => { window.syntheticCopiedRequestID = value; } }, configurable: true }));
    await page.route('**/*', async route => {
      const req = route.request(), url = new URL(req.url());
      if (url.origin !== origin) { unexpected.push(`external ${url.origin}`); return route.abort(); }
      if (!url.pathname.startsWith('/tk-register/api/')) return route.continue();
      const path = url.pathname.slice('/tk-register/api'.length), method = req.method();
      calls.push({ method, path, body: method === 'POST' ? req.postDataJSON() : undefined });
      const ok = data => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ success: true, data }) });
      const rejected = () => route.fulfill({ status: 502, contentType: 'application/json', headers: { 'X-Request-ID': requestID }, body: JSON.stringify({ success: false, code: failure.code, message: failure.message, data: { diagnostics: { ...failure.diagnostics, request_id: 'body-request-must-not-win', token: 'sk-ant-sid-private-server-secret', url: proxy, raw: 'private-response-body' } } }) });
      if (path === '/auth/status') return ok({ registration_enabled: true, password_login_enabled: true, email_code_login_enabled: true, code_length: 6 });
      if (path === '/auth/me') return ok({ user: { id: role === 'admin' ? 1 : 2, email: `${role}@example.test`, role, status: 'active', created_at: '2026-11-20T00:00:00Z' }, csrf_token: 'synthetic-diagnostics-test' });
      if (path === '/members/sources') return ok({ sources: [{ id: 'myself', label: 'myself' }] });
      if (path === '/vendors') return ok({ vendors: [{ id: '1', name: 'TK Register 0', enabled: true, configured: true, timezone: 'Asia/Shanghai', protocols: [{ id: '2', version_id: '3', name: 'Push', operation: 'push', version: 1 }] }] });
      if (method === 'GET' && path === scope) return ok({ account, snapshots: [snapshot], snapshot_count: 1, snapshot_page: 1, snapshot_page_size: 20, trend: { cost_total_usd: [], input_tokens_total: [], output_tokens_total: [] }, trend_reduced: false, interval_seconds: 300, timezone: 'Asia/Shanghai' });
      if (method === 'GET' && path === `${scope}/daily-usage`) return ok({ start_date: '2026-11-13', end_date: '2026-11-19', timezone: 'Asia/Shanghai', model_costs_enabled: false, days: [], daily_totals: [], total_cost_usd: 0, collection_time: '03:00', exchange_rate_cny_per_usd: 7 });
      if (method === 'GET' && path === `${scope}/auth-json`) return ok({ account: account.account, plan: account.plan, editable: true, reason: '', supplier_record_id: '592781' });
      if (method === 'GET' && path === `${scope}/deactivate`) return ok({ available: true, account: account.account, plan: account.plan });
      if (method === 'POST' && path === `${base}/proxy-test`) return scenario === 'proxy-auth' ? rejected() : ok({ proxy_check: proxyCheck });
      if (method === 'POST' && path === `${base}/import`) { assert.equal(req.postDataJSON().kind, 'cookie'); return rejected(); }
      unexpected.push(`${method} ${path}`);
      return route.fulfill({ status: 500, contentType: 'application/json', body: JSON.stringify({ success: false, message: 'Unexpected synthetic test request.' }) });
    });
    await page.goto(`${origin}/tk-register/#/${entry === 'submit' ? 'submit' : role === 'admin' ? 'account-pool/accounts/13152/usage' : 'accounts/13152'}`);
    if (entry === 'submit') await page.getByRole('tab', { name: 'Authorize & submit', exact: true }).click();
    else await page.getByRole('button', { name: entry === 'deactivate' ? 'Deactivate' : 'Update Auth JSON', exact: true }).click();
    const panel = entry === 'submit' ? page.locator('#authorize-submit-panel') : page.locator('.account-auth-dialog');
    await panel.getByLabel('Account proxy', { exact: true }).fill(proxy);
    await panel.getByRole('button', { name: 'Test connection', exact: true }).click();
    if (scenario !== 'proxy-auth') {
      await expect(panel.getByText(/Connected · Exit IP/)).toBeVisible();
      await panel.locator('.authorize-other-methods summary').click();
      await panel.getByRole('button', { name: 'Cookie', exact: true }).click();
      await panel.getByLabel('sessionKey', { exact: true }).fill('sk-ant-sid-synthetic-session-secret');
      await panel.getByRole('button', { name: entry === 'submit' ? 'Authorize & submit' : entry === 'update' ? 'Authorize & update' : 'Generate Auth JSON', exact: true }).click();
    }
    await expect(panel.getByText(scenario === 'proxy-auth' ? 'Proxy authentication failed. Check the username and password.' : failure.message, { exact: true })).toBeVisible();
    const details = panel.locator('.authorization-error-details');
    await expect(details).toHaveCount(1);
    await details.locator('summary').click();
    await expect(details.getByText(requestID, { exact: true })).toBeVisible();
    await expect(details.getByText(failure.code, { exact: true })).toBeVisible();
    if (scenario === 'cookie-403' || scenario === 'cookie-stale-session') {
      await expect(details.getByText(scenario === 'cookie-403' ? 'Read Claude organizations' : 'Authorize using sessionKey', { exact: true })).toBeVisible();
      await expect(details.getByText('403', { exact: true })).toBeVisible();
      await expect(details.getByText('permission_error', { exact: true })).toBeVisible();
      await expect(details.getByText('JSON', { exact: true })).toBeVisible();
      assert(!(await panel.innerText()).toLowerCase().includes('sessionkey expired'));
    } else {
      await expect(details.getByText('Upstream HTTP', { exact: true })).toHaveCount(0);
      await expect(details.getByText('502', { exact: true })).toHaveCount(0);
      if (scenario === 'cookie-no-upstream') await expect(details.getByText('Authorize using sessionKey', { exact: true })).toBeVisible();
      if (scenario === 'proxy-auth') await expect(details.getByText('Test proxy connection', { exact: true })).toBeVisible();
      if (scenario === 'cookie-unknown') await expect(details.getByText('Step', { exact: true })).toHaveCount(0);
    }
    const rendered = await details.innerText();
    for (const secret of ['sk-ant-sid-private-server-secret', 'private-response-body', 'body-request-must-not-win', proxy]) assert(!rendered.includes(secret));
    await details.getByRole('button', { name: 'Copy Request ID', exact: true }).click();
    assert.equal(await page.evaluate(() => window.syntheticCopiedRequestID), requestID);
    if (scenario === 'cookie-stale-session') {
      // Simulate the shared language preference changing in another tab; open
      // dialogs must update their existing error without another request.
      await page.evaluate(() => { localStorage.setItem('tk-register.language', 'zh-CN'); window.dispatchEvent(new StorageEvent('storage', { key: 'tk-register.language', newValue: 'zh-CN' })); });
      await expect(panel.getByText(failure.message, { exact: true })).toBeVisible();
      if (role === 'member') await page.screenshot({ path: resolve(output, `${entry}-${width}-stale-zh.png`), fullPage: true });
      await page.evaluate(() => { localStorage.setItem('tk-register.language', 'en-US'); window.dispatchEvent(new StorageEvent('storage', { key: 'tk-register.language', newValue: 'en-US' })); });
    }
    if (role === 'member' && scenario === 'cookie-403') await page.screenshot({ path: resolve(output, `${entry}-${width}.png`), fullPage: true });
    const before = calls.length;
    await page.waitForTimeout(75);
    assert.equal(calls.length, before, 'No automatic retry or follow-up request');
    assert(!calls.some(call => call.path.endsWith('/start') || call.path.endsWith('/exchange') || call.path.endsWith('/export') || call.path.endsWith('/submit') || call.path.endsWith('/sync') || call.method === 'POST' && call.path === `${scope}/auth-json`));
    assert.equal(calls.filter(call => call.path === `${base}/import`).length, scenario === 'proxy-auth' ? 0 : 1);
    if (scenario === 'proxy-auth') await panel.getByLabel('Account proxy', { exact: true }).fill('socks5://changed.example.com:1080');
    else await panel.getByRole('tab', { name: 'OAuth', exact: true }).click();
    await expect(panel.locator('.authorization-error-details')).toHaveCount(0);
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth + 1), false);
    assert.deepEqual(errors, []); assert.deepEqual(unexpected, []);
    results.push({ entry, role, width, scenario, requestID });
    await context.close();
  }
  await writeFile(resolve(output, 'results.json'), JSON.stringify({ passed: results.length, results }, null, 2));
  console.log(JSON.stringify({ passed: results.length, output }));
} catch (error) { if (currentPage && !currentPage.isClosed()) await currentPage.screenshot({ path: resolve(output, 'failure.png'), fullPage: true }); throw error; }
finally { await browser?.close(); await new Promise(ok => server.close(ok)); }
