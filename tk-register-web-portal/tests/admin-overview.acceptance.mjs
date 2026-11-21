import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { mkdir } from 'node:fs/promises';
import { chromium } from '@playwright/test';

const port = Number(process.env.TK_TEST_PORT || 4202);
const server = spawn(process.execPath, ['node_modules/vite/bin/vite.js', '--host', '127.0.0.1', '--port', String(port), '--strictPort'], { stdio: 'pipe' });
let browser;
try {
  await new Promise((resolve, reject) => {
    server.stdout.on('data', value => { if (String(value).includes('Local:')) resolve(); });
    server.stderr.on('data', value => process.stderr.write(value));
    server.on('exit', code => reject(new Error(`Vite exit ${code}`)));
    setTimeout(() => reject(new Error('Vite timeout')), 15000).unref();
  });
  browser = await chromium.launch({ headless: true, ...(process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH ? { executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH } : {}) });
  const page = await browser.newPage({ viewport: { width: 1440, height: 1080 } });
  const calls = [], errors = [], unexpected = [];
  let role = 'admin';
  page.on('pageerror', error => errors.push(error.message));
  const members = [{ id: 'myself', label: 'myself' }, { id: '2', label: 'ben@example.com' }];
  const users = [...members, { id: '3', label: 'clara@example.com' }, { id: '4', label: 'long-email-address-for-layout-validation@another-example-domain.com' }, ...Array.from({ length: 40 }, (_, index) => ({ id: String(index + 5), label: `member${index + 5}@example.com` }))];
  const yesterday = new Date(Date.parse(new Date(Date.now() + 8 * 3600_000).toISOString().slice(0, 10)) - 86400_000).toISOString().slice(0, 10);
  const snapshot = { id: '1', account_id: '100', collected_at: '2026-11-15T02:00:00+08:00', account_status: 'active', cost_total: 72, currency: 'CNY', cost_total_usd: 10, exchange_rate_cny_per_usd: 7.2, input_tokens_total: 10000, output_tokens_total: 20000, usage_5h_percent: 15, usage_7d_percent: 42, reset_5h_at: null, reset_7d_at: null, data_source: 'website' };
  const account = { id: '100', account: 'alice@example.com', user_id: '2', user_email: 'ben@example.com', plan: 'claude/cc-max-20x', vendor_id: '1', vendor_name: 'TK Register 0', current_submission_id: '22', latest_submission_id: '22', latest_submitted_at: snapshot.collected_at, submission_status: 'accepted', remote_id: '456', platform_account_id: '789', last_success_at: snapshot.collected_at, last_attempt_at: snapshot.collected_at, last_error: '', snapshot };
  await page.route('**/tk-register/api/**', async route => {
    const req = route.request(), url = new URL(req.url()), path = url.pathname.replace('/tk-register/api', '');
    calls.push({ path, method: req.method(), query: url.searchParams });
    const ok = data => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ success: true, data }) });
    if (path === '/auth/status') return ok({ registration_enabled: false, password_login_enabled: true, email_code_login_enabled: true });
    if (path === '/auth/me') return ok({ user: { id: 1, email: 'alex@example.com', role, status: 'active' }, csrf_token: 'fake-csrf' });
    if (path === '/members/sources') return ok({ sources: members });
    if (path === '/admin/accounts/overview/sources') return ok({ sources: users });
    if (path === '/accounts/overview' || path === '/admin/accounts/overview') {
      const admin = path.startsWith('/admin/'), available = admin ? users : members;
      const chosen = url.searchParams.get('sources') === 'all' ? available : available.filter(source => url.searchParams.get('sources')?.split(',').includes(source.id));
      const period = url.searchParams.get('period'), count = { day: 1, week: 7, month: 30 }[period];
      const start = period === 'all_time' ? '2026-10-01' : count ? new Date(Date.parse(yesterday) - (count - 1) * 86400_000).toISOString().slice(0, 10) : url.searchParams.get('start_date');
      const end = period ? yesterday : url.searchParams.get('end_date');
      // Admin rows are direct-owner totals; personal ben aggregates ben and clara.
      const sources = chosen.map(source => {
        const total = source.id === 'myself' ? 1 : source.id === '2' ? admin ? 2 : 5 : source.id === '3' ? 3 : 0;
        return { ...source, total, statuses: { active: total }, cost_usd: total * 10, cost_known: total, collected_days: 1 };
      });
      const total = sources.reduce((sum, source) => sum + source.total, 0), days = [], rows = [];
      for (let at = Date.parse(start); at <= Date.parse(end); at += 86400_000) {
        const date = new Date(at).toISOString().slice(0, 10);
        days.push({ date, status: total ? 'complete' : 'missing', expected: total, collected: total });
        if (total) for (const model of ['claude-sonnet-4-5', 'claude-opus-4-1']) rows.push({ date, model, input_tokens: String(total * 100), output_tokens: String(total * 200), cache_read_tokens: '0', cache_write_tokens: '0', cost_cny: String(total * 7.2), cost_usd: total });
      }
      for (const source of sources) source.cost_usd = source.total * days.length * 2;
      return ok({ model_costs_enabled: true, start_date: start, end_date: end, earliest_date: '2026-10-01', timezone: 'Asia/Shanghai', collection_time: '03:00', exchange_rate_cny_per_usd: 7.2, latest_account_data: snapshot.collected_at, sources, days, rows, model_costs: sources.flatMap(source => ['claude-sonnet-4-5', 'claude-opus-4-1'].map(model => ({ source_id: source.id, model, cost_cny: String(source.total * days.length * 7.2), cost_usd: source.total * days.length }))) });
    }
    if (path === '/accounts' || path === '/admin/synced-accounts') return ok({ items: [account], total: 1, page: 1, page_size: 20 });
    if (path === '/admin/synced-accounts/100') return ok({ account, snapshots: [snapshot], snapshot_count: 1, snapshot_page: 1, snapshot_page_size: 50, trend_reduced: false, trend: { cost_total_usd: [], input_tokens_total: [], output_tokens_total: [] }, interval_seconds: 300, timezone: 'Asia/Shanghai' });
    if (path.endsWith('/daily-usage')) return ok({ model_costs_enabled: true, start_date: url.searchParams.get('start_date'), end_date: url.searchParams.get('end_date'), timezone: 'Asia/Shanghai', collection_time: '03:00', exchange_rate_cny_per_usd: 7.2, days: [], rows: [] });
    if (path === '/admin/synced-accounts/sync') {
      assert.equal(req.method(), 'POST'); assert.deepEqual(req.postDataJSON().account_ids, ['100']);
      return ok({ accounts: [account], results: [{ account_id: '100', status: 'updated' }] });
    }
    unexpected.push(`${req.method()} ${path}`);
    return route.fulfill({ status: 404, contentType: 'application/json', body: '{"success":false}' });
  });
  const overviewCalls = admin => calls.filter(call => call.path === (admin ? '/admin/accounts/overview' : '/accounts/overview'));
  await page.goto(`http://127.0.0.1:${port}/tk-register/#/accounts`);
  await page.getByRole('heading', { name: 'Accounts by source', exact: true }).waitFor();
  assert.match(await page.locator('#overview-source-explanation').textContent(), /including their sub-users/);
  await page.locator('#overview-source').click();
  await page.getByRole('checkbox', { name: 'ben@example.com', exact: true }).uncheck();
  await page.getByRole('button', { name: 'Done', exact: true }).click();
  await page.waitForFunction(() => document.querySelectorAll('.overview-source-table tbody tr').length === 1);
  assert.equal(overviewCalls(false).at(-1).query.get('sources'), 'myself');

  await page.evaluate(() => { window.location.hash = '#/account-pool/accounts'; });
  await page.getByRole('heading', { name: 'Accounts by user', exact: true }).waitFor();
  assert.equal(await page.getByText('Admin only', { exact: true }).count(), 1);
  const initial = overviewCalls(true).at(-1).query;
  assert.equal(initial.get('sources'), 'all');
  assert.equal(initial.get('period'), 'all_time');
  assert.equal(initial.get('models'), 'all');
  assert.equal((await page.getByRole('button', { name: /^Usage date range:/ }).innerText()).trim(), 'All time');
  assert.match(await page.locator('#overview-source-explanation').textContent(), /only accounts they directly own/);
  assert.match(await page.locator('#overview-source-explanation').textContent(), /sub-users are listed separately/);
  assert.equal(await page.locator('.overview-source-table tbody tr').count(), 44);
  assert.equal(await page.locator('.overview-source-table tbody tr').filter({ has: page.getByRole('rowheader', { name: 'ben@example.com', exact: true }) }).locator('td').first().textContent(), '2');
  assert.equal(await page.locator('.overview-source-table tfoot td').first().textContent(), '6');
  assert.equal(await page.locator('.overview-status-grid a,.overview-status-grid button,.overview-source-table a,.overview-source-table button').count(), 0);
  assert.equal(await page.locator('.overview-model-grid > div').count(), 2);
  const scroll = page.locator('.overview-table-shell > .usage-table-scroll');
  assert.ok(await scroll.evaluate(element => element.clientHeight <= 480 && element.scrollHeight > element.clientHeight));
  await scroll.evaluate(element => { element.scrollTop = 500; element.scrollLeft = 250; });
  const sticky = await scroll.evaluate(element => {
    const box = element.getBoundingClientRect();
    return { top: element.querySelector('thead th').getBoundingClientRect().top - box.top, bottom: box.bottom - element.querySelector('tfoot th').getBoundingClientRect().bottom, left: element.querySelector('tbody th').getBoundingClientRect().left - box.left };
  });
  assert.ok(Math.abs(sticky.top) <= 1 && Math.abs(sticky.bottom) <= 1 && Math.abs(sticky.left) <= 1, `Sticky headings, total and owner column: ${JSON.stringify(sticky)}`);
  await scroll.evaluate(element => { element.scrollTop = 0; element.scrollLeft = 0; });

  await page.locator('#overview-source').click();
  const sourceDialog = page.getByRole('dialog', { name: 'Source', exact: true });
  await sourceDialog.getByRole('checkbox', { name: 'Select all', exact: true }).uncheck();
  await sourceDialog.getByRole('searchbox', { name: 'Search users', exact: true }).fill('ben@');
  assert.equal(await sourceDialog.locator('.member-source-options input').count(), 1);
  await sourceDialog.getByRole('checkbox', { name: 'ben@example.com', exact: true }).check();
  await sourceDialog.getByRole('button', { name: 'Done', exact: true }).click();
  await page.waitForFunction(() => document.querySelectorAll('.overview-source-table tbody tr').length === 1);
  assert.equal(overviewCalls(true).at(-1).query.get('sources'), '2');
  assert.equal(await page.locator('.overview-source-table tfoot td').first().textContent(), '2');
  await page.locator('#overview-source').click();
  assert.equal(await sourceDialog.getByRole('searchbox').inputValue(), '');
  await sourceDialog.getByRole('searchbox').fill('no-matching-user');
  await sourceDialog.getByText('No matching users.', { exact: true }).waitFor();
  // Select all always covers every user, even while a search is active.
  await sourceDialog.getByRole('checkbox', { name: 'Select all', exact: true }).check();
  await sourceDialog.getByRole('button', { name: 'Done', exact: true }).click();
  await page.waitForFunction(() => document.querySelectorAll('.overview-source-table tbody tr').length === 44);
  assert.equal(overviewCalls(true).at(-1).query.get('sources'), 'all');
  const beforeModels = overviewCalls(true).length;
  await page.locator('#daily-model-trigger').click();
  await page.getByRole('checkbox', { name: 'claude-opus-4-1', exact: true }).uncheck();
  await page.getByRole('button', { name: 'Done', exact: true }).click();
  assert.equal(await page.locator('.overview-model-grid > div').count(), 1);
  assert.equal(overviewCalls(true).length, beforeModels);
  await page.getByRole('group', { name: 'Source breakdown' }).getByRole('button', { name: 'Model costs', exact: true }).click();
  assert.deepEqual(await page.locator('.overview-source-table thead th').allTextContents(), ['User', 'claude-sonnet-4-5', 'Model subtotal']);
  await page.getByRole('button', { name: /^Usage date range:/ }).click();
  await page.getByRole('dialog', { name: 'Usage dates', exact: true }).getByRole('button', { name: 'All time', exact: true }).click();
  await page.locator('.overview-daily-chart.is-curve').waitFor();
  assert.equal(overviewCalls(true).at(-1).query.get('period'), 'all_time');
  assert.match(await page.locator('.overview-daily-chart path').first().getAttribute('d'), /C/);
  await page.getByRole('button', { name: /^Usage date range:/ }).click();
  const dates = page.getByRole('dialog', { name: 'Usage dates', exact: true });
  await dates.getByLabel('Start date', { exact: true }).fill('2026-11-01');
  await dates.getByLabel('End date', { exact: true }).fill('2026-11-07');
  await dates.getByRole('button', { name: 'Apply', exact: true }).click();
  await page.locator('.overview-daily-chart.is-bar').waitFor();
  assert.equal(overviewCalls(true).at(-1).query.get('start_date'), '2026-11-01');
  assert.equal(overviewCalls(true).at(-1).query.get('end_date'), '2026-11-07');
  await page.getByRole('group', { name: 'Source breakdown' }).getByRole('button', { name: 'Status', exact: true }).click();
  await mkdir('test-results', { recursive: true });
  for (const width of [1440, 1024, 768, 390, 320]) {
    await page.setViewportSize({ width, height: 1080 });
    await page.evaluate(() => document.fonts.ready);
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, `Overview overflows at ${width}`);
    if (width === 1440 || width === 390) await page.screenshot({ path: `test-results/admin-overview-${width}.png`, fullPage: true });
    await page.locator('#overview-source').click();
    const box = await sourceDialog.boundingBox();
    assert.ok(box.x >= 0 && box.x + box.width <= width, `Source menu overflows at ${width}`);
    await sourceDialog.getByRole('searchbox').fill('long-email');
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, `Source search overflows at ${width}`);
    await page.keyboard.press('Escape');
  }
  await page.setViewportSize({ width: 1440, height: 1080 });
  await page.locator('#overview-source').click();
  await sourceDialog.getByRole('checkbox', { name: 'ben@example.com', exact: true }).uncheck();
  await sourceDialog.getByRole('button', { name: 'Done', exact: true }).click();
  await page.getByRole('tab', { name: 'Account list', exact: true }).click();
  await page.getByRole('button', { name: 'alice@example.com', exact: true }).waitFor();
  assert.match(page.url(), /account-pool\/accounts\?tab=list$/);
  const listCall = calls.filter(call => call.path === '/admin/synced-accounts').at(-1);
  assert.ok(!listCall.query.has('sources') || listCall.query.get('sources') === 'all', 'Admin list must retain independent all-account scope');
  assert.equal(await page.getByRole('button', { name: 'Sync alice@example.com', exact: true }).isEnabled(), true);
  assert.equal(await page.getByRole('button', { name: 'Delete alice@example.com', exact: true }).count(), 0);
  assert.equal(calls.some(call => call.method !== 'GET'), false, 'Overview/filter changes must never trigger mutations');
  await page.getByRole('button', { name: 'Sync alice@example.com', exact: true }).click();
  await page.getByText('Updated', { exact: true }).waitFor();
  assert.equal(calls.filter(call => call.method !== 'GET').length, 1);
  await page.getByRole('button', { name: 'alice@example.com', exact: true }).click();
  await page.getByRole('heading', { name: 'alice@example.com', exact: true }).waitFor();
  await page.getByRole('button', { name: 'Account pool / Accounts', exact: true }).click();
  await page.getByRole('button', { name: 'alice@example.com', exact: true }).waitFor();
  assert.match(page.url(), /account-pool\/accounts\?tab=list$/);
  assert.equal(await page.getByRole('tab', { name: 'Account list', exact: true }).getAttribute('aria-selected'), 'true');

  await page.evaluate(() => { window.location.hash = '#/accounts'; });
  await page.getByRole('heading', { name: 'Accounts by source', exact: true }).waitFor();
  assert.equal(overviewCalls(false).at(-1).query.get('sources'), 'myself', 'Administrator source selection must not contaminate personal scope');
  assert.match(await page.locator('#overview-source-explanation').textContent(), /including their sub-users/);
  await page.locator('#overview-source').click();
  assert.equal(await page.getByRole('searchbox', { name: 'Search users', exact: true }).count(), 0);
  await page.getByRole('checkbox', { name: 'Select all', exact: true }).check();
  await page.getByRole('button', { name: 'Done', exact: true }).click();
  await page.waitForFunction(() => document.querySelectorAll('.overview-source-table tbody tr').length === 2);
  assert.equal(await page.locator('.overview-source-table tbody tr').filter({ has: page.getByRole('rowheader', { name: 'ben@example.com', exact: true }) }).locator('td').first().textContent(), '5');
  await page.getByRole('tab', { name: 'Account list', exact: true }).click();
  await page.getByRole('button', { name: 'alice@example.com', exact: true }).waitFor();
  assert.equal(await page.getByRole('button', { name: 'Sync alice@example.com', exact: true }).count(), 1);
  assert.equal(await page.getByRole('button', { name: 'Delete alice@example.com', exact: true }).count(), 0);
  assert.equal(calls.filter(call => call.method !== 'GET').length, 1);
  role = 'member'; const beforeDenied = calls.length;
  await page.goto(`http://127.0.0.1:${port}/tk-register/?member-fixture=1#/account-pool/accounts`);
  await page.getByRole('heading', { name: 'Admin access required', exact: true }).waitFor();
  assert.equal(calls.slice(beforeDenied).some(call => call.path.startsWith('/admin/')), false);
  assert.deepEqual(unexpected, []); assert.deepEqual(errors, []);
  console.log('Admin Overview acceptance passed: flat owner totals, separate recursive personal scope, admin-only endpoints, default All time, searchable multi-select sources, models, custom dates/All time, smooth chart, nonlinked counts, responsive layouts, independent list, Sync without deletion controls, detail back to list, member denial.');
} finally { await browser?.close(); server.kill('SIGTERM'); }
