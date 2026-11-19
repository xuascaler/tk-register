import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { readFile, writeFile, mkdir } from 'node:fs/promises';
import { dirname, extname, resolve, sep } from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium, expect } from '@playwright/test';

// Exercises the actual production bundle using synthetic accounts. Every API
// call is mocked. A missed mock, supplier URL, submission or sync fails the run.
const project = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const dist = resolve(project, 'dist'), output = resolve(project, 'test-results/deactivate');
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
const proxy = 'http://demo:sample@proxy.example.com:8080';
const proxyCheck = { ok: true, ip: '203.0.113.24', checked_at: new Date().toISOString(), elapsed_ms: 8 };
const flowID = 'example-deactivate-flow-123456';
const expires = new Date(Date.now() + 30 * 60000).toISOString();
const auth = { id_token: '', access_token: 'example-access-token', refresh_token: 'example-refresh-token', last_refresh: '2026-11-20T01:00:00Z', email: 'user@example.com', account_uuid: 'example-account', organization_uuid: 'example-org', organization_name: 'Automatically read organization', claude_device_ids: ['example-device'], type: 'claude', expired: '2026-12-20T01:00:00Z', proxy_url: proxy, verification_status: 'verified' };
const snapshot = { id: '1', account_id: '13152', snapshot_date: '2026-11-20', collected_at: '2026-11-20T01:00:00Z', account_status: 'active', upstream_status_raw: 'active', upstream_error_message: '', cost_total_usd: 0, input_tokens_total: 0, output_tokens_total: 0, currency: 'USD', usage_5h_percent: 0, usage_7d_percent: 0, data_source: 'website' };
const account = { id: '13152', account_source: { id: 'myself', label: 'myself' }, user_id: '2', user_email: 'owner@example.test', can_manage: true, account: auth.email, plan: 'claude/cc-max-20x', vendor_id: '1', vendor_name: 'TK Register 0', current_submission_id: '10', latest_submission_id: '10', submission_status: 'accepted', remote_id: '592781', platform_account_id: '25935', supplier_record_id: '592781', auth_json_updated_at: '2026-11-19T01:00:00Z', last_success_at: '2026-11-20T01:00:00Z', last_attempt_at: '2026-11-20T01:00:00Z', last_error: '', snapshot };
const baseFlow = { flow_id: flowID, auth_url: 'https://claude.ai/oauth/authorize?state=example', expires_at: expires, status: 'awaiting_code', proxy_check: proxyCheck, notes: [] };
const generated = { ...baseFlow, status: 'exported', auth, generated_at: '2026-11-20T01:30:00Z', audit_id: '123' };
const allScenarios = ['oauth', 'setup', 'import-json', 'cookie', 'token', 'missing', 'mismatch', 'unknown', 'unknown-ready', 'invalid-success', 'expired', 'restore-exported', 'restore-invalid', 'parent', 'proxy-change', 'import-file', 'patch-expired', 'ineligible'];
const requestedScenarios = process.env.TK_DEACTIVATE_SCENARIOS?.split(',').map(value => value.trim()).filter(Boolean);
if (requestedScenarios?.some(value => !allScenarios.includes(value))) throw new Error('Unknown Deactivate browser scenario.');
const scenarios = requestedScenarios?.length ? allScenarios.filter(value => requestedScenarios.includes(value)) : allScenarios;
const results = [];
let browser, currentPage;
try {
  browser = await chromium.launch({ headless: true, executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome' });
  for (const role of ['member', 'admin']) for (const width of [1024, 393]) for (const scenario of scenarios) {
    const context = await browser.newContext({ viewport: { width, height: 1000 } });
    const page = currentPage = await context.newPage();
    const calls = [], unexpected = [], errors = [];
    let exports = 0, exchanged = 0, imported = 0;
    const scope = role === 'admin' ? '/admin/synced-accounts/13152' : '/accounts/13152', path = `${scope}/deactivate`, base = `${path}/flows`;
    page.on('pageerror', error => errors.push(error.message)); page.on('dialog', dialog => dialog.accept());
    await page.route('**/*', async route => {
      const request = route.request(), url = new URL(request.url());
      if (url.origin !== origin) { unexpected.push(`external ${url.origin}`); return route.abort(); }
      if (!url.pathname.startsWith('/tk-register/api/')) return route.continue();
      const endpoint = url.pathname.slice('/tk-register/api'.length), method = request.method();
      calls.push({ method, path: endpoint, body: method === 'POST' ? request.postDataJSON() : undefined });
      const ok = value => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ success: true, data: value }) });
      if (endpoint === '/auth/status') return ok({ registration_enabled: true, password_login_enabled: true, email_code_login_enabled: true, email_send_cooldown_seconds: 60, registration_code_ttl_seconds: 600, login_code_ttl_seconds: 600, code_length: 6 });
      if (endpoint === '/auth/me') return ok({ user: { id: role === 'admin' ? 1 : 2, email: `${role}@example.test`, role, status: 'active', created_at: '2026-11-20T00:00:00Z' }, csrf_token: 'synthetic-deactivate-test' });
      if (endpoint === '/members/sources') return ok({ sources: [{ id: 'myself', label: 'myself' }] });
      if (method === 'GET' && endpoint === scope) return ok({ account: { ...account, can_manage: scenario === 'parent' ? false : true }, snapshots: [snapshot], snapshot_count: 1, snapshot_page: 1, snapshot_page_size: 20, trend: { cost_total_usd: [], input_tokens_total: [], output_tokens_total: [] }, trend_reduced: false, interval_seconds: 300, timezone: 'Asia/Shanghai' });
      if (method === 'GET' && endpoint === `${scope}/daily-usage`) return ok({ start_date: '2026-11-13', end_date: '2026-11-19', timezone: 'Asia/Shanghai', model_costs_enabled: false, days: [], daily_totals: [], total_cost_usd: 0, collection_time: '03:00', exchange_rate_cny_per_usd: 7 });
      if (method === 'GET' && endpoint === path) return ok({ available: scenario !== 'ineligible', account: account.account, plan: account.plan, reason: scenario === 'ineligible' ? 'This account is being deleted. Please wait.' : '' });
      if (method === 'POST' && endpoint === `${base}/proxy-test`) return ok({ proxy_check: proxyCheck });
      if (method === 'POST' && endpoint === `${base}/start`) return ok({ ...baseFlow, expires_at: scenario === 'expired' ? new Date(Date.now() - 1000).toISOString() : expires });
      if (method === 'POST' && endpoint === `${base}/exchange`) {
        exchanged++; const value = { ...auth, email: scenario === 'mismatch' ? 'another@example.com' : auth.email };
        if (scenario === 'missing' || scenario === 'patch-expired') delete value.organization_name;
        return ok({ ...baseFlow, status: 'ready', auth: value });
      }
      if (method === 'POST' && endpoint === `${base}/import`) { imported++; return ok({ ...baseFlow, status: 'ready', auth }); }
      if (method === 'POST' && endpoint === `${base}/${flowID}/draft`) return ok({ ...baseFlow, status: 'ready', auth, expires_at: scenario === 'patch-expired' ? new Date(Date.now() - 1000).toISOString() : expires });
      if (method === 'POST' && endpoint === `${base}/${flowID}/export`) {
        exports++;
        if (scenario === 'unknown' || scenario === 'unknown-ready' && exports === 1) return route.fulfill({ status: 504, contentType: 'application/json', body: JSON.stringify({ success: false, code: 'GATEWAY_TIMEOUT', message: 'The saved result could not be loaded.' }) });
        if (scenario === 'invalid-success') return ok({ ...generated, proxy_check: { ...proxyCheck, ip: '203.0.113.25' } });
        return ok(generated);
      }
      if (method === 'GET' && endpoint === `${base}/${flowID}`) return ok(scenario === 'restore-invalid' ? { ...generated, auth: { ...auth, email: 'other@example.com' } } : scenario === 'unknown-ready' ? { ...baseFlow, status: 'ready', auth } : generated);
      unexpected.push(`${method} ${endpoint}`);
      return route.fulfill({ status: 500, contentType: 'application/json', body: JSON.stringify({ success: false, message: 'Unexpected test API request.' }) });
    });
    if (scenario.startsWith('restore-')) await page.addInitScript(({ path, flowID, expires }) => sessionStorage.setItem(`tk-register:deactivate-flow:${path}`, JSON.stringify({ flow_id: flowID, expires_at: expires, method: 'oauth', setup_kind: 'full' })), { path, flowID, expires });
    await page.goto(`${origin}/tk-register/#/${role === 'admin' ? 'account-pool/accounts/13152/usage' : 'accounts/13152'}`);
    if (scenario === 'parent' && role === 'member') {
      await expect(page.getByRole('heading', { name: auth.email, exact: true })).toBeVisible();
      await expect(page.getByRole('button', { name: 'Deactivate', exact: true })).toHaveCount(0);
    } else {
      await page.getByRole('button', { name: 'Deactivate', exact: true }).click();
      const dialog = page.locator('.account-deactivate-dialog');
      await expect(dialog).toBeVisible(); await expect(dialog.getByText('Account ID 25935', { exact: true })).toBeVisible();
      if (scenario === 'restore-exported') { await expect(dialog.getByRole('heading', { name: 'Auth JSON generated', exact: true })).toBeVisible(); assert.equal(exports, 0); }
      else if (scenario === 'restore-invalid') { await expect(dialog.getByText(/saved generation result could not be confirmed/)).toBeVisible(); await expect(dialog.getByRole('button', { name: 'Generate Auth JSON', exact: true })).toHaveCount(0); }
      else if (scenario === 'ineligible') { await expect(dialog.getByText('This account is being deleted. Please wait.', { exact: true })).toBeVisible(); await expect(dialog.getByRole('button', { name: 'Generate authorization link', exact: true })).toHaveCount(0); }
      else {
        const flow = dialog.locator('.account-auth-authorize');
        await flow.getByLabel('Account proxy', { exact: true }).fill(proxy);
        await flow.getByRole('button', { name: 'Generate authorization link', exact: true }).click();
        await expect(flow.getByText('Test connection required', { exact: true })).toBeVisible();
        assert.equal(calls.filter(call => call.path === `${base}/start` || call.path === `${base}/proxy-test`).length, 0);
        await flow.getByRole('button', { name: 'Test connection', exact: true }).click(); await expect(flow.getByText(/Connected · Exit IP/)).toBeVisible();
        if (scenario === 'setup') await flow.getByRole('tab', { name: 'Setup Token', exact: true }).click();
        if (scenario === 'import-json' || scenario === 'import-file') await flow.getByRole('tab', { name: 'Import JSON', exact: true }).click();
        if (scenario === 'cookie' || scenario === 'token') { await flow.locator('.authorize-other-methods summary').click(); await flow.getByRole('button', { name: scenario === 'cookie' ? 'Cookie' : 'Existing token', exact: true }).click(); }
        if (scenario === 'import-json') await flow.getByLabel('Source JSON', { exact: true }).fill(JSON.stringify(auth));
        else if (scenario === 'import-file') await flow.getByLabel('Upload source JSON file', { exact: true }).setInputFiles({ name: 'auth.json', mimeType: 'application/json', buffer: Buffer.from(JSON.stringify(auth)) });
        else if (scenario === 'cookie') await flow.getByLabel('sessionKey', { exact: true }).fill('example-session-key');
        else if (scenario === 'token') { await flow.getByLabel('Access Token', { exact: true }).fill('example-access-token'); await flow.getByLabel('Refresh Token', { exact: true }).fill('example-refresh-token'); }
        else {
          await flow.getByRole('button', { name: 'Generate authorization link', exact: true }).click();
          await expect(flow.getByLabel('Authorization code or callback URL', { exact: true })).toBeVisible();
          if (scenario === 'expired') { await expect(flow.getByText('This authorization has expired. Generate a new authorization.', { exact: true })).toBeVisible(); await expect(flow.getByRole('button', { name: 'Generate Auth JSON', exact: true })).toBeDisabled(); }
          else if (scenario === 'proxy-change') { await flow.getByLabel('Account proxy', { exact: true }).fill('http://changed.example:8080'); await expect(flow.getByLabel('Authorization code or callback URL', { exact: true })).toHaveCount(0); await flow.getByRole('button', { name: 'Generate authorization link', exact: true }).click(); await expect(flow.getByText('Test connection required', { exact: true })).toBeVisible(); }
          else { await expect(flow.getByRole('button', { name: 'Generate Auth JSON', exact: true })).toBeDisabled(); await flow.getByLabel('Authorization code or callback URL', { exact: true }).fill('example-code#returned-state'); }
        }
        if (!['expired', 'proxy-change'].includes(scenario)) {
          await flow.getByRole('button', { name: 'Generate Auth JSON', exact: true }).click();
          if (scenario === 'missing' || scenario === 'patch-expired') {
            await expect(flow.getByText('Complete missing credential fields', { exact: true })).toBeVisible();
            await flow.getByLabel('Standard Auth JSON', { exact: true }).fill(JSON.stringify({ ...auth, email: 'another@example.com' }));
            await flow.getByRole('button', { name: 'Generate Auth JSON', exact: true }).click();
            await expect(flow.getByText('The authorized email cannot be replaced. Complete missing fields only.', { exact: true })).toBeVisible();
            await flow.getByLabel('Standard Auth JSON', { exact: true }).fill(JSON.stringify(auth)); await flow.getByRole('button', { name: 'Generate Auth JSON', exact: true }).click();
          }
          if (scenario === 'mismatch') { await expect(flow.getByText('Account email does not match', { exact: true })).toBeVisible(); assert.equal(exports, 0); }
          else if (scenario === 'patch-expired') { await expect(flow.getByText(/authorization has expired/).first()).toBeVisible(); assert.equal(exports, 0); }
          else if (['unknown', 'unknown-ready', 'invalid-success'].includes(scenario)) {
            await expect(flow.getByRole('button', { name: 'Check saved generation result', exact: true })).toBeVisible();
            await flow.getByRole('button', { name: 'Check saved generation result', exact: true }).click();
            if (scenario === 'unknown-ready') { await expect(flow.getByText('No completed export was saved. You can retry generating this same Auth JSON.', { exact: true })).toBeVisible(); assert.equal(exports, 1); await flow.getByRole('button', { name: 'Generate Auth JSON', exact: true }).click(); }
            await expect(dialog.getByRole('heading', { name: 'Auth JSON generated', exact: true })).toBeVisible(); assert.equal(exports, scenario === 'unknown-ready' ? 2 : 1); assert.equal(exchanged, 1);
          }
          else { await expect(dialog.getByRole('heading', { name: 'Auth JSON generated', exact: true })).toBeVisible(); assert.equal(exports, 1); }
        }
      }
      if (await dialog.getByRole('heading', { name: 'Auth JSON generated', exact: true }).count()) {
        await expect(dialog.getByText('Your account will go offline after a while.', { exact: true })).toBeVisible();
        const before = { exports, exchanged, imported };
        const download = page.waitForEvent('download'); await dialog.getByRole('button', { name: 'Download JSON', exact: true }).click(); const file = await download; assert(file.suggestedFilename().endsWith('.json'));
        assert.deepEqual({ exports, exchanged, imported }, before);
        await dialog.getByRole('button', { name: 'Back to account', exact: true }).click();
        await expect(dialog).toHaveCount(0); await expect(page.locator('.usage-detail-title .usage-status')).toHaveText('Active');
        assert.equal(await page.evaluate(() => document.body.style.overflow), '');
      }
      const stored = await page.evaluate(() => Object.keys(sessionStorage).map(key => [key, sessionStorage.getItem(key)]));
      assert(!JSON.stringify(stored).includes('example-access-token')); assert(!JSON.stringify(stored).includes('example-refresh-token')); assert(!JSON.stringify(stored).includes(proxy));
      const overflow = await page.evaluate(() => document.documentElement.scrollWidth > innerWidth + 1); assert.equal(overflow, false);
    }
    assert.deepEqual(errors, []); assert.deepEqual(unexpected, []);
    assert(!calls.some(call => call.path.includes('/auth-json') || call.path.endsWith('/sync') || call.path === '/submissions' || call.path.startsWith('/credential-flows')));
    results.push({ role, width, scenario, exports, exchanged, imported });
    await context.close();
  }
  await writeFile(resolve(output, 'results.json'), JSON.stringify({ passed: results.length, results }, null, 2));
  console.log(JSON.stringify({ passed: results.length, output }));
} catch (error) { if (currentPage && !currentPage.isClosed()) await currentPage.screenshot({ path: resolve(output, 'failure.png'), fullPage: true }); throw error; }
finally { await browser?.close(); await new Promise(ok => server.close(ok)); }
