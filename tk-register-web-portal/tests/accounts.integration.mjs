import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { mkdir } from 'node:fs/promises';
import { chromium } from '@playwright/test';

const port = Number(process.env.TK_TEST_PORT || 4183);
const server = spawn(process.execPath, ['node_modules/vite/bin/vite.js', '--host', '127.0.0.1', '--port', String(port), '--strictPort'], { stdio: 'pipe' });
let browser;
try {
  await new Promise((resolve, reject) => {
    server.stdout.on('data', chunk => { if (String(chunk).includes('Local:')) resolve(); });
    server.stderr.on('data', chunk => process.stderr.write(chunk));
    server.on('exit', code => reject(new Error(`Vite exited: ${code}`)));
    setTimeout(() => reject(new Error('Vite startup timed out')), 15000).unref();
  });
  browser = await chromium.launch({ headless: true, ...(process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH ? { executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH } : {}) });
  const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
  const failures = [], requests = [], pageErrors = [];
  let role = 'admin';
  page.on('pageerror', error => pageErrors.push(error.message));
  const snapshot = { id: '1', account_id: '100', collected_at: '2026-11-01T02:00:00+08:00', account_status: 'active', cost_total: 1234.56, currency: 'CNY', input_tokens_total: 120000, output_tokens_total: 200000, usage_5h_percent: 15, usage_7d_percent: 42, reset_5h_at: null, reset_7d_at: null, data_source: 'website' };
  const account = { id: '100', account: 'alice@example.com', user_id: '7', user_email: 'owner@example.com', plan: 'claude/cc-max-20x', vendor_id: '1', vendor_name: 'TK Register 0', current_submission_id: '22', latest_submission_id: '23', submission_status: 'accepted', remote_id: '456', platform_account_id: '789', last_success_at: snapshot.collected_at, last_attempt_at: snapshot.collected_at, last_error: '', snapshot };
  const accounts = [account, { ...account, id: '101', account: 'zero@example.com', snapshot: { ...snapshot, cost_total: 0, usage_5h_percent: 0 } }, { ...account, id: '102', account: 'missing@example.com', snapshot: null, last_success_at: null }];
  await page.route('**/tk-register/api/**', async route => {
    const req = route.request(), url = new URL(req.url()), path = url.pathname.replace('/tk-register/api', '');
    requests.push(`${req.method()} ${path}${url.search}`);
    const ok = data => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ success: true, data }) });
    const fail = status => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify({ success: false, message: 'Fixture unavailable.' }) });
    if (path === '/auth/status') return ok({ registration_enabled: true, password_login_enabled: true, email_code_login_enabled: true });
    if (path === '/auth/me') return ok({ user: { id: 1, email: 'admin@example.com', role }, csrf_token: 'fixture-csrf' });
    if (path === '/vendors') return ok({ vendors: [] });
    if (path === '/accounts' || path === '/admin/synced-accounts') {
      const search = url.searchParams.get('search') || '', status = url.searchParams.get('status') || '';
      const items = accounts.filter(a => `${a.account} ${path.startsWith('/admin/') ? a.user_email : ''}`.includes(search) && (!status || a.snapshot?.account_status === status));
      return ok({ items, page: 1, page_size: 20, total: items.length });
    }
    if (path === '/accounts/100' || path === '/admin/synced-accounts/100') return ok({ account, snapshots: [snapshot], interval_hours: 2, timezone: 'Asia/Shanghai' });
    if (path === '/admin/synced-accounts/100/sync' || path === '/accounts/100/sync') {
      assert.equal(req.method(), 'POST'); assert.equal(req.headers()['x-csrf-token'], 'fixture-csrf'); return ok({ account });
    }
    // Exercise the back link without involving upstream status/billing calls.
    if (path === '/admin/submissions/22' || path === '/submissions/22' || path === '/admin/accounts/42') return fail(404);
    failures.push(`${req.method()} ${path}`); return fail(404);
  });
  await mkdir('test-results', { recursive: true });
  await page.goto(`http://127.0.0.1:${port}/tk-register/#/account-pool/accounts`);
  await page.getByRole('button', { name: 'alice@example.com', exact: true }).waitFor();
  assert.deepEqual(await page.locator('.usage-accounts-table th').allTextContents(), ['Account / plan', 'User', 'Status', 'Usage windows', 'Total spent', 'Last updated', 'Sync']);
  assert.equal(await page.locator('.usage-owner-email').first().textContent(), 'owner@example.com');
  assert.match(await page.locator('.usage-accounts-table tr').filter({ hasText: 'zero@example.com' }).textContent(), /CNY\s0\.00/);
  assert.match(await page.locator('.usage-accounts-table tr').filter({ hasText: 'missing@example.com' }).textContent(), /No data collected/);
  assert.equal(requests.some(r => r.startsWith('POST')), false, 'Opening the list must only read saved snapshots');
  for (const width of [1440, 1024, 768, 390, 320]) {
    await page.setViewportSize({ width, height: 1000 });
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, `List overflow at ${width}`);
    assert.equal(await page.locator('#root').count(), 1);
    assert.equal(await page.locator('.header').count(), 1);
    assert.equal(await page.locator('main').count(), 1);
    assert.equal(await page.locator('h1').filter({hasText:'Accounts'}).count(), 1);
    await page.screenshot({ path: `test-results/accounts-admin-${width}.png`, fullPage: true });
    if (width === 390) {
      await page.evaluate(() => window.scrollTo(0, 0));
      await page.screenshot({ path: 'test-results/accounts-admin-390-viewport-top.png' });
      await page.evaluate(() => window.scrollTo(0, 700));
      await page.screenshot({ path: 'test-results/accounts-admin-390-viewport-scrolled.png' });
      console.log('390px real DOM', await page.evaluate(() => ({headers:document.querySelectorAll('.header').length,mains:document.querySelectorAll('main').length,titles:[...document.querySelectorAll('h1')].map(x=>x.textContent),rows:document.querySelectorAll('.usage-accounts-table tbody tr').length,scrollY,bodyHeight:document.body.scrollHeight})));
      await page.evaluate(() => window.scrollTo(0, 0));
    }
  }
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.getByRole('searchbox').fill('owner@example.com');
  await page.waitForRequest(req => req.url().includes('search=owner%40example.com'));
  await page.getByRole('searchbox').fill('no-match');
  await page.getByRole('heading', { name: 'No matching accounts' }).waitFor();
  await page.getByRole('searchbox').fill('');
  await page.getByRole('button', { name: 'alice@example.com', exact: true }).waitFor();
  await page.getByRole('combobox', { name: 'Account status' }).click();
  await page.getByRole('option', { name: 'Banned', exact: true }).click();
  await page.getByRole('heading', { name: 'No matching accounts' }).waitFor();
  assert.ok(requests.some(r => r.includes('/admin/synced-accounts?') && r.includes('status=banned')));
  await page.getByRole('combobox', { name: 'Account status' }).click();
  await page.getByRole('option', { name: 'All account statuses', exact: true }).click();
  await page.getByRole('button', { name: 'alice@example.com', exact: true }).click();
  assert.match(page.url(), /account-pool\/accounts\/100\/usage$/);
  await page.locator('.usage-owner-meta').waitFor();
  assert.match(await page.locator('.usage-owner-meta').textContent(), /owner@example.com/);
  await page.getByRole('button', { name: 'Sync now', exact: true }).click();
  await page.getByText('Account data updated.', { exact: true }).waitFor();
  await page.getByRole('button', { name: 'View submission' }).click();
  assert.match(page.url(), /account-pool\/submissions\/22\?synced_account=100$/);
  await page.getByRole('button', { name: 'Account details', exact: true }).click();
  assert.match(page.url(), /account-pool\/accounts\/100\/usage$/);
  await page.locator('.usage-owner-meta').waitFor();
  for (const width of [1440, 390, 320]) {
    await page.setViewportSize({ width, height: 1000 });
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, `Detail overflow at ${width}`);
    await page.screenshot({ path: `test-results/accounts-admin-detail-${width}.png`, fullPage: true });
  }
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.goto(`http://127.0.0.1:${port}/tk-register/#/account-pool/submissions/22?account=42`);
  await Promise.all([page.waitForRequest(req => req.url().includes('/admin/accounts/42')), page.getByRole('button', { name: 'Account details', exact: true }).click()]);
  assert.match(page.url(), /account-pool\/accounts\/42$/);
  await page.goto(`http://127.0.0.1:${port}/tk-register/#/accounts`);
  await page.getByRole('button', { name: 'alice@example.com', exact: true }).waitFor();
  assert.deepEqual(await page.locator('.usage-accounts-table th').allTextContents(), ['Account / plan', 'Status', 'Usage windows', 'Total spent', 'Last updated', 'Sync']);
  assert.equal(await page.locator('.usage-owner-email').count(), 0);
  await page.getByRole('button', { name: 'alice@example.com', exact: true }).click();
  await page.getByRole('button', { name: 'Sync now', exact: true }).waitFor();
  assert.equal(await page.locator('.usage-owner-meta').count(), 0);
  role = 'member'; const before = requests.length;
  await page.goto(`http://127.0.0.1:${port}/tk-register/?member-fixture=1#/account-pool/accounts`);
  await page.getByRole('heading', { name: 'Admin access required' }).waitFor();
  assert.equal(requests.slice(before).some(r => r.includes('/admin/synced-accounts')), false, 'Member must not mount or fetch admin Accounts');
  assert.deepEqual(failures, []); assert.deepEqual(pageErrors, []);
  console.log('Accounts UI: admin/member shared columns, data states, search, scope, sync, new/legacy back routes and responsive layouts passed.');
} finally { await browser?.close(); server.kill('SIGTERM'); }
