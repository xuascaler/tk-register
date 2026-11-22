import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { mkdir, readFile } from 'node:fs/promises';
import { dirname, extname, resolve, sep } from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium, expect } from '@playwright/test';

// Exercise built UI with synthetic responses only. No vendor or live TK traffic.
const project = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const dist = resolve(project, 'dist');
const output = resolve(project, 'test-results/bill-exports');
await mkdir(output, { recursive: true });
const mime = { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.svg': 'image/svg+xml' };
const server = createServer(async (req, res) => {
  const pathname = new URL(req.url, 'http://localhost').pathname;
  const path = resolve(dist, pathname === '/tk-register/' ? 'index.html' : pathname.replace(/^\/tk-register\//, ''));
  if (!path.startsWith(dist + sep)) return res.writeHead(404).end();
  try { const data = await readFile(path); res.writeHead(200, { 'Content-Type': mime[extname(path)] || 'application/octet-stream' }); res.end(data); }
  catch { res.writeHead(404).end(); }
});
await new Promise((resolveListening, reject) => { server.once('error', reject); server.listen(0, '127.0.0.1', resolveListening); });
const origin = `http://127.0.0.1:${server.address().port}`;
let browser;
try {
  browser = await chromium.launch({ headless: true, executablePath: process.env.CHROME_PATH || (process.platform === 'darwin' ? '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome' : undefined) });
  const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
  const unexpected = [], errors = [], requests = [], jobs = new Map();
  page.on('pageerror', error => errors.push(error.message));
  const admin = { id: 1, email: 'admin@example.test', role: 'admin', status: 'active', created_at: '2026-11-19T00:00:00Z' };
  const owner = { id: '2', email: 'team@example.test' };
  const member = { id: '3', email: 'member@example.test' };
  const nested = { id: '4', email: 'nested@example.test' };
  const other = { id: '5', email: 'other@example.test' };
  const bulk = Array.from({ length: 21 }, (_, index) => ({ id: String(100 + index), email: `bulk${String(index + 1).padStart(2, '0')}@example.test` }));
  const catalog = [owner, member, nested, other, ...bulk];
  const account = (user, email, id) => ({ username: user.email, account: email, platform_account_id: id, latest_submitted_at: '2026-11-15T00:00:00Z', plan: 'claude/cc-max-20x', row_count: 2, amount_cny: '100.00', status: 'matched' });
  const accounts = new Map([
    ['2', account(owner, 'claude-parent@example.test', '101')],
    ['3', account(member, 'claude-child@example.test', '102')],
    ['4', account(nested, 'claude-nested@example.test', '103')],
    ['5', account(other, 'claude-other@example.test', '104')],
  ]);
  const selectedUsers = job => job.users || [job.user];
  const scopeIds = job => new Set(selectedUsers(job).flatMap(user => job.include_sub_users ? ({ '2': ['2', '3', '4'], '3': ['3', '4'] }[user.id] || [user.id]) : [user.id]));
  const summaryFor = job => [...scopeIds(job)].map(id => accounts.get(id)).filter(Boolean);
  await page.route('**/*', async route => {
    const req = route.request(), url = new URL(req.url());
    if (url.origin !== origin) { unexpected.push(`External request ${url.origin}`); return route.abort(); }
    if (!url.pathname.startsWith('/tk-register/api/')) return route.continue();
    const path = url.pathname.slice('/tk-register/api'.length), method = req.method();
    const ok = data => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ success: true, data }) });
    if (path === '/auth/me') return ok({ user: admin, csrf_token: 'bill-export-ui-test' });
    if (path === '/auth/status') return ok({ registration_enabled: true, password_login_enabled: true, email_code_login_enabled: true });
    if (path === '/members/sources') return ok({ sources: [{ id: 'myself', label: 'myself' }] });
    if (path === '/admin/settings') return ok({ settings: [] });
    if (path === '/admin/users') {
      const search = (url.searchParams.get('search') || '').toLowerCase(), current = Number(url.searchParams.get('page') || '1');
      const users = catalog.filter(user => user.email.toLowerCase().includes(search));
      return ok({ items: users.slice((current - 1) * 10, current * 10).map(user => ({ ...admin, ...user })), page: current, page_size: 10, total: users.length });
    }
    if (path === '/admin/bill-exports' && method === 'POST') {
      const body = req.postDataJSON(); requests.push(body);
      assert.equal(Object.hasOwn(body, 'user_id'), false, 'new requests must use user_ids only');
      assert.ok(body.user_ids.length > 0 && body.user_ids.length <= 20);
      assert.equal(new Set(body.user_ids).size, body.user_ids.length, 'each user is requested once');
      const users = body.user_ids.map(id => catalog.find(user => user.id === id));
      const job = { id: String(requests.length), users, ...(users.length === 1 ? { user: users[0] } : {}), ...(admin.is_super_admin ? { model_usage_source: body.model_usage_source } : {}), include_sub_users: body.include_sub_users, start_date: body.start_date, end_date: body.end_date, status: 'queued', currency: 'CNY', created_at: '2026-11-22T00:00:00Z' };
      const items = summaryFor(job), review = job.include_sub_users && scopeIds(job).has('2') ? 1 : 0;
      Object.assign(job, { download_filename: `claude-bills_${users.length}-users_${job.id}_${body.start_date}_to_${body.end_date}.xlsx`, account_count: items.length + review, summary_count: items.length, matched_account_count: items.length, review_account_count: review, row_count: items.length * 2, total_cny: `${items.length * 100}.00` });
      if (requests.length === 1) delete job.users; // Legacy single-user DTO remains readable.
      jobs.set(job.id, { ...job, status: 'completed' }); return ok(job);
    }
    const match = path.match(/^\/admin\/bill-exports\/(\d+)(?:\/(results|download))?$/);
    if (match) {
      const job = jobs.get(match[1]);
      if (!match[2]) return ok(job);
      if (match[2] === 'download') return route.fulfill({ status: 200, contentType: 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet', body: 'Synthetic download fixture' });
      const items = summaryFor(job), view = url.searchParams.get('view');
      const columns = ['Username', 'Account', 'Platform ID', 'Submitted', 'Model', 'Input tokens', 'Output tokens', 'Cache read', 'Cache write', 'Amount (CNY)'];
      const rows = view === 'summary' ? items : view === 'details' ? items.flatMap(item => ['fable', 'opus'].map(model => [item.username, item.account, item.platform_account_id, '2026-11-15 08:00:00', model, '10', '20', '30', '40', job.model_usage_source === 'raw' ? (model === 'fable' ? '70.00' : '30.00') : '50.00'])) : job.review_account_count ? [{ username: '', account: 'ambiguous@example.test', platform_account_id: '105', reason: 'Account ownership is ambiguous.' }] : [];
      return ok({ items: rows, columns, total: rows.length, page: 1, page_size: 20 });
    }
    unexpected.push(`${method} ${path}`);
    return route.fulfill({ status: 500, contentType: 'application/json', body: JSON.stringify({ message: 'Unexpected test request' }) });
  });
  await page.goto(`${origin}/tk-register/#/config/bill_exports`);
  const picker = page.getByRole('button', { name: 'Users', exact: true });
  const dialog = page.getByRole('dialog', { name: 'Select users', exact: true });
  const search = page.getByRole('searchbox', { name: 'Search user email', exact: true });
  const scope = page.getByRole('switch', { name: 'Include all sub-users', exact: true });
  const generate = page.getByRole('button', { name: 'Generate export', exact: true });
  const changed = page.getByText('Settings changed. Generate a new export to apply them.', { exact: true });
  const selectUsers = async users => {
    await picker.click();
    for (const user of users) {
      await search.fill(user.email);
      await page.getByRole('checkbox', { name: user.email, exact: true }).check();
      await expect(dialog).toBeVisible();
    }
    await page.getByRole('button', { name: 'Done', exact: true }).click();
  };
  const dates = async () => {
    await page.getByLabel('Start date', { exact: true }).fill('2026-11-15');
    await page.getByLabel('End date', { exact: true }).fill('2026-11-21');
  };
  const assertMobile = async () => {
    const trigger = await picker.boundingBox(), chips = await page.locator('.bill-selected-users').boundingBox(), start = await page.locator('.bill-start-field').boundingBox();
    assert.ok(trigger.y + trigger.height <= chips.y && chips.y + chips.height <= start.y, 'mobile selected users immediately follow Users, before dates');
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, 'mobile page overflow');
  };
  await expect(page.getByRole('heading', { name: 'Claude bill exports', exact: true })).toBeVisible();
  await expect(page.getByText('Prepare a Claude bill export', { exact: true })).toBeVisible();
  await expect(page.getByText('Recent exports', { exact: true })).toHaveCount(0);
  await expect(picker).toContainText('Select users');
  await expect(scope).not.toBeChecked();
  await expect(page.getByRole('combobox', { name: 'Data source', exact: true })).toHaveCount(0);
  const scopeFont = await page.locator('#bill-include-sub-users-label').evaluate(element => getComputedStyle(element).fontFamily);
  assert.doesNotMatch(scopeFont, /monospace|SFMono/i, 'scope label uses the product sans-serif font');
  console.log('Scope label font:', scopeFont);
  await generate.click();
  await expect(page.getByText('Select at least one user.', { exact: true })).toBeVisible();

  // Checkbox selections survive searches; keyboard navigation never submits the form.
  await picker.click();
  await search.fill('team');
  await search.press('Enter');
  await page.getByRole('checkbox', { name: owner.email, exact: true }).check();
  await expect(dialog).toBeVisible();
  await search.fill('member');
  await page.getByRole('checkbox', { name: member.email, exact: true }).check();
  await search.fill('');
  await expect(page.getByRole('checkbox', { name: owner.email, exact: true })).toBeChecked();
  await expect(page.getByRole('checkbox', { name: member.email, exact: true })).toBeChecked();
  await search.press('ArrowDown');
  await expect(page.getByRole('checkbox', { name: owner.email, exact: true })).toBeFocused();
  await page.keyboard.press('Enter');
  assert.equal(requests.length, 0, 'Enter in the picker must not generate an export');
  await expect(dialog).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(dialog).toHaveCount(0);
  await expect(picker).toBeFocused();
  await expect(page.locator('.bill-user-chip')).toHaveCount(2);
  await page.getByRole('button', { name: `Remove ${member.email}`, exact: true }).click();
  await expect(page.locator('.bill-user-chip')).toHaveCount(1);
  await page.getByRole('button', { name: 'Clear all', exact: true }).click();
  await expect(page.locator('.bill-user-chip')).toHaveCount(0);
  await search.press('Escape');

  // The cap applies across result pages and still allows selected users to be removed.
  await picker.click();
  await search.fill('bulk');
  for (const user of bulk.slice(0, 10)) await page.getByRole('checkbox', { name: user.email, exact: true }).check();
  await page.getByRole('button', { name: 'Load more users', exact: true }).click();
  for (const user of bulk.slice(10, 20)) await page.getByRole('checkbox', { name: user.email, exact: true }).check();
  await page.getByRole('button', { name: 'Load more users', exact: true }).click();
  await expect(page.getByRole('checkbox', { name: bulk[20].email, exact: true })).toBeDisabled();
  await expect(page.getByText('20 of 20 selected', { exact: true })).toBeVisible();
  await page.getByRole('checkbox', { name: bulk[0].email, exact: true }).uncheck();
  await page.getByRole('checkbox', { name: bulk[20].email, exact: true }).check();
  await expect(page.locator('.bill-user-chip')).toHaveCount(20);
  await page.getByRole('button', { name: 'Done', exact: true }).click();
  await expect(dialog).toHaveCount(0);
  await page.getByRole('button', { name: 'Clear all', exact: true }).click();
  await search.press('Escape');

  await selectUsers([owner]);
  await dates();
  await generate.click();
  await expect(scope).toBeDisabled();
  await expect(picker).toBeDisabled();
  await expect(page.getByRole('button', { name: `Remove ${owner.email}`, exact: true })).toBeDisabled();
  await expect(page.getByRole('button', { name: 'Clear all', exact: true })).toBeDisabled();
  await expect(page.getByLabel('Start date', { exact: true })).toBeDisabled();
  await expect(page.getByRole('button', { name: 'Download Excel', exact: true })).toBeVisible();
  await expect(page.locator('.bill-summary-table tbody tr')).toHaveCount(1);
  await expect(page.locator('.bill-result-title h2')).toHaveText(owner.email);
  assert.deepEqual(requests[0].user_ids, ['2']);
  assert.equal(requests[0].include_sub_users, false);
  assert.equal(Object.hasOwn(requests[0], 'model_usage_source'), false, 'ordinary admins do not send a source');
  await expect(page.locator('.bill-summary-table th').first()).toHaveText('Username');
  await expect(page.locator('.bill-summary-table tbody td').first()).toHaveText(owner.email);
  // Form edits must not silently change the completed result or its saved scope.
  await scope.check();
  await expect(changed).toBeVisible();
  await expect(page.locator('.bill-result-scope')).toHaveText('Selected user only');
  await expect(page.locator('.bill-summary-table tbody tr')).toHaveCount(1);
  await generate.click();
  await expect(page.locator('.bill-summary-table tbody tr')).toHaveCount(3);
  assert.equal(requests[1].include_sub_users, true);
  assert.equal(Object.hasOwn(requests[1], 'model_usage_source'), false, 'ordinary admins still omit source with all sub-users');
  await expect(page.locator('.bill-result-scope')).toHaveText('Includes all sub-users');
  await expect(page.locator('.bill-summary-table tbody')).toContainText(nested.email);
  await expect(changed).toHaveCount(0);
  await page.getByRole('tab', { name: /Bill details/ }).click();
  await expect(page.locator('.bill-details-table tbody tr')).toHaveCount(6);
  await expect(page.locator('.bill-details-table th').first()).toHaveText('Username');
  await expect(page.locator('.bill-details-table tbody')).toContainText(nested.email);
  await page.getByRole('tab', { name: /Needs review/ }).click();
  await expect(page.locator('.bill-review-table tbody tr')).toHaveCount(1);
  await expect(page.locator('.bill-review-table tbody td').first()).toHaveText('—');
  await page.getByRole('tab', { name: /Account summary/ }).click();
  await expect(page.locator('.bill-summary-table tbody tr')).toHaveCount(3);

  // Multiple overlapping roots still produce one task/file and a saved user snapshot.
  await selectUsers([member, other]);
  await expect(picker).toContainText('3 users selected');
  await expect(changed).toBeVisible();
  await expect(page.locator('.bill-result-title h2')).toHaveText(owner.email);
  await generate.click();
  await expect(page.locator('.bill-summary-table tbody tr')).toHaveCount(4);
  assert.deepEqual(requests[2].user_ids, ['2', '3', '5']);
  await expect(page.locator('.bill-result-title h2')).toHaveText('3 users');
  await expect(page.locator('.bill-result-users')).toHaveText(`${owner.email} · ${member.email} · ${other.email}`);
  await expect(page.getByRole('button', { name: 'Download Excel', exact: true })).toHaveCount(1);
  const downloadPromise = page.waitForEvent('download');
  await page.getByRole('button', { name: 'Download Excel', exact: true }).click();
  assert.equal((await downloadPromise).suggestedFilename(), jobs.get('3').download_filename);
  await page.getByRole('button', { name: `Remove ${owner.email}`, exact: true }).click();
  await expect(picker).toContainText('2 users selected');
  await expect(changed).toBeVisible();
  await expect(page.locator('.bill-result-title h2')).toHaveText('3 users');
  await expect(page.locator('.bill-result-users')).toContainText(owner.email);
  await expect(page.locator('.bill-summary-table tbody tr')).toHaveCount(4);
  await selectUsers([owner]);
  await expect(changed).toHaveCount(0); // Identical selected IDs, even in a different order.
  assert.equal(requests.length, 3, 'selection edits do not create or mutate jobs');
  await page.screenshot({ path: resolve(output, 'desktop.png'), fullPage: true });
  await picker.click();
  await search.fill('');
  await expect(page.getByRole('checkbox', { name: owner.email, exact: true })).toBeChecked();
  await page.screenshot({ path: resolve(output, 'desktop-picker.png'), fullPage: true });
  await search.press('Escape');
  await page.setViewportSize({ width: 390, height: 844 });
  await assertMobile();
  await page.screenshot({ path: resolve(output, 'mobile.png'), fullPage: true });
  await page.setViewportSize({ width: 320, height: 700 });
  await assertMobile();
  await picker.click();
  const userRect = await dialog.boundingBox();
  assert.ok(userRect.x >= 0 && userRect.x + userRect.width <= 320, 'users picker fits narrow viewport');
  await search.press('Escape');
  await page.reload();
  await expect(page.getByText('Prepare a Claude bill export', { exact: true })).toBeVisible();
  await expect(scope).not.toBeChecked();
  await expect(picker).toContainText('Select users');
  await expect(page.locator('.bill-export-result')).toHaveCount(0);

  // Superadmins can choose each new task's source; previous results stay immutable.
  admin.is_super_admin = true;
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.reload();
  const source = page.getByRole('combobox', { name: 'Data source', exact: true });
  await expect(source).toHaveText('Raw');
  await selectUsers([owner, member]);
  await dates();
  await scope.check();
  await generate.click();
  await expect(source).toBeDisabled();
  await expect(page.locator('.bill-summary-table tbody tr')).toHaveCount(3);
  assert.equal(requests[3].model_usage_source, 'raw');
  assert.equal(requests[3].include_sub_users, true);
  assert.deepEqual(requests[3].user_ids, ['2', '3']);
  await expect(page.locator('.bill-result-title h2')).toHaveText('2 users');
  await expect(page.locator('.bill-result-heading')).toContainText('Raw');
  await page.getByRole('tab', { name: /Bill details/ }).click();
  await expect(page.locator('.bill-details-table tbody tr').first().locator('td').last()).toHaveText('70.00');
  await source.click();
  await page.getByRole('option', { name: 'Cache', exact: true }).click();
  await expect(source).toHaveText('Cache');
  await expect(changed).toBeVisible();
  await expect(page.locator('.bill-result-heading')).toContainText('Raw');
  await expect(page.locator('.bill-details-table tbody tr').first().locator('td').last()).toHaveText('70.00');
  assert.equal(requests.length, 4, 'changing source does not create or mutate a job');
  await generate.click();
  await expect(source).toBeDisabled();
  await expect(page.locator('.bill-summary-table tbody tr')).toHaveCount(3);
  assert.equal(requests[4].model_usage_source, 'cache');
  assert.equal(requests[4].include_sub_users, true);
  await expect(page.locator('.bill-result-heading')).toContainText('Cache');
  await expect(changed).toHaveCount(0);
  await page.getByRole('tab', { name: /Bill details/ }).click();
  await expect(page.locator('.bill-details-table tbody tr').first().locator('td').last()).toHaveText('50.00');
  await page.getByRole('tab', { name: /Account summary/ }).click();
  await expect(page.locator('.bill-summary-table tbody tr')).toHaveCount(3);
  await page.screenshot({ path: resolve(output, 'superadmin-desktop.png'), fullPage: true });
  await page.setViewportSize({ width: 390, height: 844 });
  await assertMobile();
  await expect(source).toBeVisible();
  await page.evaluate(() => window.scrollTo(0, 0));
  await page.screenshot({ path: resolve(output, 'superadmin-mobile.png'), fullPage: true });
  await picker.click();
  await search.fill('');
  await expect(page.getByRole('checkbox', { name: owner.email, exact: true })).toBeChecked();
  await page.screenshot({ path: resolve(output, 'mobile-picker.png'), fullPage: true });
  await search.press('Escape');
  await page.setViewportSize({ width: 320, height: 700 });
  await source.click();
  const menu = page.getByRole('listbox', { name: 'Data source', exact: true });
  await expect(menu).toBeVisible();
  const rect = await menu.boundingBox();
  assert.ok(rect.x >= 0 && rect.x + rect.width <= 320, 'source picker fits narrow viewport');
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, 'superadmin narrow page overflow');
  await source.press('Escape');
  await page.reload();
  await expect(source).toHaveText('Raw');
  await expect(picker).toContainText('Select users');
  await expect(page.locator('.bill-export-result')).toHaveCount(0);
  assert.deepEqual(unexpected, [], 'No recent-export lookup or unexpected API requests');
  assert.deepEqual(errors, []);
  console.log('Bill export UI passed: searchable multi-user selection, persistent selections across searches/pages, 20-user cap, keyboard controls, locked generation, single-user DTO compatibility, immutable multi-user snapshot/file, complete hierarchy scope, Username in all result tabs, no history lookup, Raw/Cache source immutability, ordinary source omission, responsive chips and menus.');

} finally {
  await browser?.close();
  await new Promise(resolveClosed => server.close(resolveClosed));
}
