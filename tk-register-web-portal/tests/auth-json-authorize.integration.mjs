import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { readFile, writeFile, mkdir } from 'node:fs/promises';
import { dirname, extname, resolve, sep } from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium, expect } from '@playwright/test';

// Loads the real production bundle with synthetic data. Every API request is
// intercepted; missed mocks and external network requests fail the test.
const project = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const dist = resolve(project, 'dist');
const output = resolve(project, 'test-results/auth-json-authorize');
await mkdir(output, { recursive: true });
const mime = { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.svg': 'image/svg+xml' };
const server = createServer(async (req, res) => {
  try {
    const pathname = new URL(req.url, 'http://localhost').pathname;
    const path = resolve(dist, pathname === '/tk-register/' ? 'index.html' : pathname.replace(/^\/tk-register\//, ''));
    if (!pathname.startsWith('/tk-register/') || !path.startsWith(dist + sep) || pathname.includes('/api/')) return res.writeHead(404).end();
    res.writeHead(200, { 'Content-Type': mime[extname(path)] || 'application/octet-stream', 'Cache-Control': 'no-store' });
    res.end(await readFile(path));
  } catch { res.writeHead(404).end(); }
});
await new Promise(ok => server.listen(0, '127.0.0.1', ok));
const origin = `http://127.0.0.1:${server.address().port}`;
const snapshot = { id: '1', account_id: '13152', snapshot_date: '2026-11-19', collected_at: '2026-11-19T10:00:00Z', source_updated_at: null, account_status: 'offline', upstream_status_raw: 'offline', upstream_error_message: '', input_tokens_total: 0, output_tokens_total: 0, cost_total: 0, currency: 'CNY', cost_total_usd: 0, exchange_rate_cny_per_usd: 7, usage_5h_percent: 0, reset_5h_at: null, usage_7d_percent: 0, reset_7d_at: null, data_source: 'website' };
const account = { id: '13152', account_source: { id: 'myself', label: 'myself' }, user_id: '2', user_email: 'owner@example.test', can_manage: true, account: 'user@example.com', plan: 'claude/cc-pro', vendor_id: '1', vendor_name: 'TK Register 0', current_submission_id: '10', latest_submission_id: '10', submission_status: 'accepted', remote_id: '592781', platform_account_id: '25935', supplier_record_id: '592781', auth_json_updated_at: null, last_success_at: '2026-11-19T10:00:00Z', last_attempt_at: '2026-11-19T10:00:00Z', last_error: '', snapshot };
const validAuth = { id_token: '', access_token: 'example-access-token', refresh_token: 'example-refresh-token', last_refresh: '2026-11-19T10:00:00Z', email: 'user@example.com', account_uuid: 'example-account', organization_uuid: 'example-org', organization_name: 'Example', claude_device_ids: ['example-device'], type: 'claude', expired: '2026-12-19T10:00:00Z', proxy_url: 'http://proxy.example.com:8080', verification_status: 'verified' };
const result = { updated: true, audit_id: '12', refresh_status: 'not_requested', refresh_message: 'Auth JSON updated. Account status is updated by scheduled background checks.', account: { ...account, auth_json_updated_at: '2026-11-19T11:00:00Z' }, updated_at: '2026-11-19T11:00:00Z' };
const proxyCheck = { ok: true, ip: '203.0.113.24', checked_at: new Date().toISOString(), elapsed_ms: 8 };
const flowID = 'example-auth-update-flow-1234';
const expires = new Date(Date.now() + 30 * 60000).toISOString();
const results = [];
let browser, activePage;
try {
  browser = await chromium.launch({ headless: true, executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome' });
  for (const role of ['member', 'admin']) for (const width of [1024, 393]) for (const scenario of ['oauth', 'mismatch', 'manual', 'unknown', 'setup', 'import-json', 'cookie', 'token', 'missing', 'restore-finished', 'restore-invalid-success', 'manual-clears-flow', 'import-file', 'patch-expired']) {
    const context = await browser.newContext({ viewport: { width, height: 1000 } });
    const page = activePage = await context.newPage();
    const requests = [], unexpected = [], errors = [];
    let updateCount = 0;
    const scope = role === 'admin' ? '/admin/synced-accounts/13152' : '/accounts/13152';
    const base = `${scope}/auth-json/flows`;
    const flow = { flow_id: flowID, auth_url: 'https://claude.ai/oauth/authorize?state=example', expires_at: expires, status: 'awaiting_code', proxy_check: proxyCheck, notes: [] };
    page.on('pageerror', error => errors.push(error.message));
    page.on('dialog', dialog => dialog.accept());
    await page.route('**/*', async route => {
      const req = route.request(), url = new URL(req.url());
      if (url.origin !== origin) { unexpected.push(`external ${url.origin}`); return route.abort(); }
      if (!url.pathname.startsWith('/tk-register/api/')) return route.continue();
      const path = url.pathname.slice('/tk-register/api'.length), method = req.method();
      const body = method === 'POST' ? req.postDataJSON() : undefined;
      requests.push({ method, path, body });
      const ok = data => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ success: true, data }) });
      if (method === 'GET' && path === '/auth/status') return ok({ registration_enabled: true, password_login_enabled: true, email_code_login_enabled: true, email_send_cooldown_seconds: 60, registration_code_ttl_seconds: 600, login_code_ttl_seconds: 600, code_length: 6 });
      if (method === 'GET' && path === '/auth/me') return ok({ user: { id: role === 'admin' ? 1 : 2, email: `${role}@example.test`, role, status: 'active', created_at: '2026-11-19T00:00:00Z' }, csrf_token: 'synthetic-auth-update-test' });
      if (method === 'GET' && path === '/members/sources') return ok({ sources: [{ id: 'myself', label: 'myself' }] });
      if (method === 'GET' && path === scope) return ok({ account, snapshots: [snapshot], snapshot_count: 1, snapshot_page: 1, snapshot_page_size: 20, trend: { cost_total_usd: [], input_tokens_total: [], output_tokens_total: [] }, trend_reduced: false, interval_seconds: 300, timezone: 'Asia/Shanghai' });
      if (method === 'GET' && path === `${scope}/daily-usage`) return ok({ start_date: '2026-11-12', end_date: '2026-11-18', timezone: 'Asia/Shanghai', model_costs_enabled: false, days: [], daily_totals: [], total_cost_usd: 0, collection_time: '03:00', exchange_rate_cny_per_usd: 7 });
      if (method === 'GET' && path === `${scope}/auth-json`) return ok({ account: account.account, plan: account.plan, editable: true, reason: '', supplier_record_id: '592781' });
      if (method === 'POST' && path === `${base}/proxy-test`) return ok({ proxy_check: proxyCheck });
      if (method === 'POST' && path === `${base}/start`) return ok(flow);
      if (method === 'POST' && path === `${base}/exchange`) {
        const auth = { ...validAuth, email: scenario === 'mismatch' ? 'another@example.com' : validAuth.email };
        if (scenario === 'missing' || scenario === 'patch-expired') delete auth.organization_name;
        return ok({ ...flow, status: 'ready', auth });
      }
      if (method === 'POST' && path === `${base}/import`) return ok({ ...flow, status: 'ready', auth: validAuth });
      if (method === 'POST' && path === `${base}/${flowID}/draft`) return ok({ ...flow, expires_at: scenario === 'patch-expired' ? new Date(Date.now() - 1000).toISOString() : expires, status: 'ready', auth: validAuth });
      if (method === 'GET' && path === `${base}/${flowID}`) return ok({ ...flow, status: scenario === 'restore-finished' ? 'failed' : scenario === 'restore-invalid-success' ? 'updated' : 'unknown', auth: validAuth });
      if (method === 'POST' && path === `${scope}/auth-json`) {
        updateCount++;
        if (scenario === 'unknown') return route.fulfill({ status: 504, contentType: 'application/json', body: JSON.stringify({ success: false, code: 'AUTH_JSON_UPDATE_UNCONFIRMED', message: 'We could not confirm the update.' }) });
        return ok(result);
      }
      unexpected.push(`${method} ${path}`);
      return route.fulfill({ status: 500, contentType: 'application/json', body: JSON.stringify({ success: false, message: 'Unexpected test request' }) });
    });
    if (scenario.startsWith('restore-')) await page.addInitScript(({ scope, flowID, expires }) => {
      sessionStorage.setItem(`tk-register:auth-json-flow:${scope}/auth-json`, JSON.stringify({ flow_id: flowID, expires_at: expires, method: 'oauth', setup_kind: 'full' }));
    }, { scope, flowID, expires });
    if (scenario === 'import-file') await page.addInitScript(() => {
      const text = File.prototype.text;
      File.prototype.text = async function () { await new Promise(ok => setTimeout(ok, 500)); return text.call(this); };
    });
    await page.goto(`${origin}/tk-register/#/${role === 'admin' ? 'account-pool/accounts/13152/usage' : 'accounts/13152'}`);
    await page.getByRole('button', { name: 'Update Auth JSON', exact: true }).click();
    const dialog = page.locator('.account-auth-dialog');
    await expect(dialog).toBeVisible();
    await expect(dialog.getByText('Account ID 25935', { exact: true })).toBeVisible();
    assert(!(await dialog.getByText('Account ID 592781', { exact: true }).count()));
    if (scenario.startsWith('restore-')) {
      await expect(dialog.getByText(scenario === 'restore-finished' ? /already finished an update attempt/ : /saved update result could not be confirmed/)).toBeVisible();
      await expect(dialog.getByRole('button', { name: 'Authorize & update', exact: true })).toHaveCount(0);
      assert.equal(updateCount, 0);
    } else if (scenario === 'manual' || scenario === 'manual-clears-flow') {
      if (scenario === 'manual-clears-flow') {
        await dialog.getByLabel('Account proxy', { exact: true }).fill('http://proxy.example.com:8080');
        await dialog.getByRole('button', { name: 'Test connection', exact: true }).click();
        await expect(dialog.getByText(/Connected · Exit IP/)).toBeVisible();
        await dialog.getByRole('button', { name: 'Generate authorization link', exact: true }).click();
        await expect(dialog.getByRole('textbox', { name: 'Authorization code or callback URL', exact: true })).toBeVisible();
      }
      await dialog.getByRole('tab', { name: 'Upload Auth JSON', exact: true }).click();
      await dialog.getByRole('textbox', { name: 'New Auth JSON' }).fill(JSON.stringify(validAuth));
      await dialog.getByRole('button', { name: 'Update Auth JSON', exact: true }).click();
    } else {
      const proxy = dialog.getByLabel('Account proxy', { exact: true });
      const generate = dialog.getByRole('button', { name: 'Generate authorization link', exact: true });
      await proxy.fill('http://proxy.example.com:8080');
      await generate.click();
      await expect(dialog.getByText('Test connection required', { exact: true })).toBeVisible();
      assert.equal(requests.filter(item => item.path === `${base}/start` || item.path === `${base}/proxy-test`).length, 0);
      await expect(dialog.getByRole('button', { name: 'Test connection', exact: true })).toBeFocused();
      await dialog.getByRole('button', { name: 'Test connection', exact: true }).click();
      await expect(dialog.getByText(/Connected · Exit IP/)).toBeVisible();
      if (scenario === 'setup') await dialog.getByRole('tab', { name: 'Setup Token', exact: true }).click();
      if (['import-json', 'import-file', 'cookie', 'token'].includes(scenario)) {
        if (scenario === 'import-json' || scenario === 'import-file') {
          await dialog.getByRole('tab', { name: 'Import JSON', exact: true }).click();
          if (scenario === 'import-file') {
            await dialog.getByLabel('Upload source JSON file', { exact: true }).setInputFiles({ name: 'synthetic-auth.json', mimeType: 'application/json', buffer: Buffer.from(JSON.stringify(validAuth)) });
            await expect(dialog.getByRole('button', { name: 'Authorize & update', exact: true })).toBeDisabled();
            await expect(dialog.getByRole('textbox', { name: 'Source JSON', exact: true })).toHaveValue(JSON.stringify(validAuth));
          } else await dialog.getByRole('textbox', { name: 'Source JSON', exact: true }).fill(JSON.stringify(validAuth));
        } else {
          await dialog.locator('.authorize-other-methods > summary').click();
          await dialog.getByRole('button', { name: scenario === 'cookie' ? 'Cookie' : 'Existing token', exact: true }).click();
          await dialog.getByLabel(scenario === 'cookie' ? 'sessionKey' : 'Access Token', { exact: true }).fill('synthetic-credential');
        }
        await dialog.getByRole('button', { name: 'Authorize & update', exact: true }).click();
      } else {
      await generate.click();
      const code = dialog.getByRole('textbox', { name: 'Authorization code or callback URL', exact: true });
      await expect(code).toBeVisible();
      await code.fill('example-code#example-state');
      if (scenario === 'oauth') {
        await proxy.fill('http://different-proxy.example.com:8080');
        await expect(code).toHaveCount(0);
        await expect(dialog.locator('.authorize-link-display')).toHaveCount(0);
        await generate.click();
        await expect(dialog.getByText('Test connection required', { exact: true })).toBeVisible();
        assert.equal(requests.filter(item => item.path === `${base}/start`).length, 1);
        await dialog.getByRole('button', { name: 'Test connection', exact: true }).click();
        await expect(dialog.getByText(/Connected · Exit IP/)).toBeVisible();
        await generate.click();
        await code.fill('replacement-code#replacement-state');
      }
      await dialog.getByRole('button', { name: 'Authorize & update', exact: true }).click();
      if (scenario === 'missing' || scenario === 'patch-expired') {
        await expect(dialog.getByRole('textbox', { name: 'Standard Auth JSON', exact: true })).toBeVisible();
        assert.equal(updateCount, 0);
        await dialog.getByRole('textbox', { name: 'Standard Auth JSON', exact: true }).fill(JSON.stringify(validAuth));
        await dialog.getByRole('button', { name: 'Authorize & update', exact: true }).click();
        assert(requests.some(item => item.path === `${base}/${flowID}/draft` && item.body.auth_patch.organization_name === 'Example'));
      }
      }
    }
    if (scenario.startsWith('restore-')) {
      assert.equal(updateCount, 0);
    } else if (scenario === 'patch-expired') {
      await expect(dialog.getByText('This authorization has expired. Generate a new authorization.', { exact: true })).toBeVisible();
      assert.equal(updateCount, 0);
    } else if (scenario === 'mismatch') {
      await expect(dialog.getByText('Account email does not match', { exact: true })).toBeVisible();
      assert.equal(updateCount, 0);
      await expect(dialog.getByRole('button', { name: 'Authorize & update', exact: true })).toHaveCount(0);
    } else if (scenario === 'unknown') {
      await expect(dialog.getByText(/could not confirm whether the update completed/)).toBeVisible();
      await expect(dialog.getByRole('button', { name: 'Authorize & update', exact: true })).toHaveCount(0);
      assert.equal(updateCount, 1);
      await dialog.getByRole('button', { name: 'Check saved update result', exact: true }).click();
      await expect(dialog.getByText(/still unconfirmed/)).toBeVisible();
      assert.equal(updateCount, 1);
    } else {
      await expect(dialog.getByRole('heading', { name: 'Auth JSON updated', exact: true })).toBeVisible();
      await expect(dialog.getByText('Offline', { exact: true }).first()).toBeVisible();
      await dialog.getByRole('button', { name: 'Back to account', exact: true }).click();
      await expect(dialog).toHaveCount(0);
      await expect(page.getByText('Auth JSON updated. Account status is updated by scheduled background checks.', { exact: true })).toBeVisible();
      assert.equal(updateCount, 1);
      const mutation = requests.find(item => item.method === 'POST' && item.path === `${scope}/auth-json`);
      assert.deepEqual(Object.keys(mutation.body), [scenario.startsWith('manual') ? 'auth_json' : 'flow_id']);
      if (scenario === 'manual-clears-flow') {
        await page.getByRole('button', { name: 'Update Auth JSON', exact: true }).click();
        await expect(dialog.getByRole('button', { name: 'Generate authorization link', exact: true })).toBeVisible();
        assert(!requests.some(item => item.method === 'GET' && item.path === `${base}/${flowID}`));
      }
    }
    assert(!requests.some(item => /\/sync$|\/submissions|credential-flows/.test(item.path)), 'Updating credentials must never submit an account or trigger synchronization');
    assert.deepEqual(unexpected, []); assert.deepEqual(errors, []);
    await page.screenshot({ path: resolve(output, `${role}-${width}-${scenario}.png`), fullPage: true });
    results.push({ role, width, scenario, passed: true, updateCount, requests: requests.map(item => `${item.method} ${item.path}`) });
    await context.close(); activePage = undefined;
  }
  console.log(JSON.stringify({ passed: true, scenarios: results.length, output }));
} catch (error) {
  results.push({ passed: false, error: error.stack });
  if (activePage && !activePage.isClosed()) await activePage.screenshot({ path: resolve(output, 'failure.png'), fullPage: true }).catch(() => {});
  throw error;
} finally {
  await writeFile(resolve(output, 'results.json'), JSON.stringify(results, null, 2));
  await browser?.close(); await new Promise(ok => server.close(ok));
}
