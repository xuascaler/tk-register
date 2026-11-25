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
const output = resolve(project, 'test-results/proxy-url');
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
const result = { updated: true, audit_id: '12', refresh_status: 'not_requested', refresh_message: 'Auth JSON updated. Account status is updated by scheduled background checks.', account: { ...account, auth_json_updated_at: '2026-11-19T11:00:00Z' }, updated_at: '2026-11-19T11:00:00Z' };
const proxyCheck = { ok: true, ip: '203.0.113.24', checked_at: new Date().toISOString(), elapsed_ms: 8 };
let browser;
let count = 0;
try {
  browser = await chromium.launch({ headless: true, executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome' });
  for (const role of ['member', 'admin']) for (const width of [1440, 390]) for (const lang of ['en-US', 'zh-CN']) for (const scenario of ['normal', 'missing', 'active', 'conflict', 'unknown']) {
    const zh = lang === 'zh-CN';
    const context = await browser.newContext({ viewport: { width, height: 1000 } });
    const page = await context.newPage();
    await page.addInitScript(lang => localStorage.setItem('tk-register.language', lang), lang);
    const scope = role === 'admin' ? '/admin/synced-accounts/13152' : '/accounts/13152';
    const unexpected = [], errors = [];
    let writes = 0, checks = 0;
    page.on('pageerror', e => errors.push(e.message));
    await page.route('**/*', async route => {
      const req = route.request(), url = new URL(req.url()), method = req.method();
      if (url.origin !== origin) { unexpected.push('external request'); return route.abort(); }
      if (!url.pathname.startsWith('/tk-register/api/')) return route.continue();
      const path = url.pathname.slice('/tk-register/api'.length);
      const ok = data => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ success: true, data }) });
      const fail = (code, status = 409) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify({ success: false, code, message: 'Changed server wording' }) });
      if (path === '/auth/status') return ok({ registration_enabled: true, password_login_enabled: true, email_code_login_enabled: true, email_send_cooldown_seconds: 60, code_length: 6 });
      if (path === '/auth/me') return ok({ user: { id: role === 'admin' ? 1 : 2, email: `${role}@example.test`, role, status: 'active', created_at: '2026-11-19T00:00:00Z' }, csrf_token: 'synthetic-proxy-test' });
      if (path === '/members/sources') return ok({ sources: [{ id: 'myself', label: 'myself' }] });
      if (method === 'GET' && path === scope) return ok({ account, snapshots: [snapshot], snapshot_count: 1, snapshot_page: 1, snapshot_page_size: 20, trend: { cost_total_usd: [], input_tokens_total: [], output_tokens_total: [] }, trend_reduced: false, interval_seconds: 300, timezone: 'Asia/Shanghai' });
      if (path === `${scope}/daily-usage`) return ok({ start_date: '2026-11-12', end_date: '2026-11-18', timezone: 'Asia/Shanghai', model_costs_enabled: false, days: [], daily_totals: [], total_cost_usd: 0, collection_time: '03:00', exchange_rate_cny_per_usd: 7 });
      if (method === 'GET' && path === `${scope}/auth-json`) return ok({ account: account.account, plan: account.plan, editable: true, reason: '', supplier_record_id: '592781' });
      if (path === `${scope}/proxy-url` && method === 'GET') {
        checks++;
        if (scenario === 'missing') return fail('ACCOUNT_CREDENTIALS_MISSING');
        return ok({ account: account.account, plan: account.plan, editable: scenario !== 'active', reason: 'This account is currently active.', supplier_record_id: '592781', revision: String(checks), proxy_url_masked: 'http://••••:••••@old-proxy.example:8080' });
      }
      if (path === `${scope}/proxy-url-test` && method === 'POST') return ok({ proxy_check: proxyCheck, test_token: 'synthetic-proxy-test-proof', expires_in: 600 });
      if (path === `${scope}/proxy-url` && method === 'POST') {
        writes++;
        const body = req.postDataJSON();
        assert.deepEqual(Object.keys(body).sort(), ['proxy_url', 'revision', 'test_token']);
        if (scenario === 'unknown') return fail('AUTH_JSON_UPDATE_UNCONFIRMED', 504);
        if (scenario === 'conflict') return fail('ACCOUNT_CREDENTIALS_CHANGED');
        return ok(result);
      }
      unexpected.push(`${method} ${path}`);
      return fail('UNEXPECTED_TEST_REQUEST', 500);
    });
    await page.goto(`${origin}/tk-register/#/${role === 'admin' ? 'account-pool/accounts/13152/usage' : 'accounts/13152'}`);
    const label = zh ? '更新 Proxy URL' : 'Update Proxy URL';
    await page.getByRole('button', { name: label, exact: true }).click();
    const dialog = page.locator('.account-proxy-dialog');
    await expect(dialog).toBeVisible();
    if (scenario === 'missing') {
      await expect(dialog.getByText(zh ? '未保存最新 Auth JSON' : 'Latest Auth JSON is not saved', { exact: true })).toBeVisible();
      await expect(dialog.locator('input')).toHaveCount(0);
      await dialog.getByRole('button', { name: zh ? '更新 Auth JSON' : 'Update Auth JSON', exact: true }).click();
      await expect(page.locator('.account-auth-dialog:not(.account-proxy-dialog)')).toBeVisible();
      assert.equal(writes, 0);
    } else if (scenario === 'active') {
      await expect(dialog.locator('.account-auth-unavailable')).toBeVisible();
      await expect(dialog.locator('input')).toHaveCount(0);
      await expect(dialog.getByRole('button', { name: label, exact: true })).toHaveCount(0);
      assert.equal(writes, 0);
    } else {
      const input = dialog.getByLabel(zh ? '新 Proxy URL' : 'New Proxy URL', { exact: true });
      const save = dialog.getByRole('button', { name: label, exact: true });
      const test = dialog.getByRole('button', { name: zh ? '测试连接' : 'Test connection', exact: true });
      await expect(input).toBeVisible();
      await expect(save).toBeDisabled();
      if (role === 'member' && scenario === 'normal') await page.screenshot({ path: resolve(output, `form-${lang}-${width}.png`), fullPage: true });
      await input.fill('http://demo:password@new-proxy.example:8080');
      await test.click();
      await expect(save).toBeEnabled();
      await input.fill('http://demo:password@new-proxy.example:8081');
      await expect(save).toBeDisabled();
      await test.click();
      await expect(save).toBeEnabled();
      await save.click();
      if (scenario === 'normal') {
        await expect(dialog.getByRole('heading', { name: zh ? 'Proxy URL 已更新' : 'Proxy URL updated', exact: true })).toBeVisible();
        assert(!(await dialog.innerText()).includes('demo:password'));
        await dialog.getByRole('button', { name: zh ? '返回账号' : 'Back to account', exact: true }).click();
        await expect(dialog).toHaveCount(0);
        assert.equal(writes, 1);
      } else if (scenario === 'conflict') {
        await expect(dialog.getByText(zh ? '账号凭据已更新' : 'Account credentials have changed', { exact: true })).toBeVisible();
        await dialog.getByRole('button', { name: zh ? '重新打开' : 'Reopen dialog', exact: true }).click();
        await expect(input).toHaveValue('');
        await expect(save).toBeDisabled();
        assert.equal(checks, 2);
        assert.equal(writes, 1);
      } else {
        await expect(dialog.getByText(zh ? '暂时无法确认更新结果' : 'Unable to confirm the update result', { exact: true })).toBeVisible();
        await expect(dialog.locator('.account-auth-dialog-footer button')).toHaveCount(1);
        await expect(dialog.locator('.account-auth-dialog-footer button')).toHaveText(zh ? '关闭' : 'Close');
        await expect(dialog.locator('input')).toHaveCount(0);
        if (role === 'member') await page.screenshot({ path: resolve(output, `unknown-${lang}-${width}.png`), fullPage: true });
        assert.equal(writes, 1);
      }
    }
    assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'horizontal overflow');
    assert.deepEqual(errors, []);
    assert.deepEqual(unexpected, []);
    await context.close();
    count++;
  }
  console.log(`Proxy URL browser checks passed: ${count} scenarios (roles, languages, desktop/mobile).`);
} finally { await browser?.close(); await new Promise(ok => server.close(ok)); }
