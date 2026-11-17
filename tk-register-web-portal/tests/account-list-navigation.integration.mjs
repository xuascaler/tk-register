import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { chromium, expect } from '@playwright/test';

const port = Number(process.env.TK_TEST_PORT || 4197);
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
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
  const page = await context.newPage();
  const requests = [], unexpected = [], errors = [];
  let total = 180;
  let heldListResponse;
  function account(id) {
    return { id: String(id), account: `sample${id}@example.com`, user_id: '7', user_email: 'ben@example.com', account_source: { id: '7', label: 'ben@example.com' }, can_manage: true, plan: 'claude/cc-max-20x', vendor_id: '1', vendor_name: 'TK Register 0', current_submission_id: '22', latest_submission_id: '22', submission_status: 'accepted', remote_id: '456', platform_account_id: String(id), last_success_at: '2026-11-16T00:00:00Z', last_error: '', snapshot: { id: String(id), account_id: String(id), collected_at: '2026-11-16T00:00:00Z', account_status: 'active', cost_total_usd: 123.45, input_tokens_total: 100, output_tokens_total: 200, usage_5h_percent: 10, usage_7d_percent: 20 } };
  }
  await context.route('**/tk-register/api/**', async route => {
    const req = route.request(), url = new URL(req.url()), path = url.pathname.replace('/tk-register/api', '');
    requests.push({ method: req.method(), path, params: Object.fromEntries(url.searchParams) });
    const ok = data => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ success: true, data }) });
    if (path === '/auth/status') return ok({ registration_enabled: false, password_login_enabled: true, email_code_login_enabled: true });
    if (path === '/auth/me') return ok({ user: { id: 1, email: 'admin@example.com', role: 'admin' }, csrf_token: 'fixture-csrf' });
    if (path === '/members/sources' || path === '/admin/accounts/overview/sources') return ok({ sources: [{ id: 'myself', label: 'myself' }, { id: '7', label: 'ben@example.com' }] });
    if (path === '/accounts' || path === '/admin/synced-accounts') {
      if (heldListResponse?.path === path) {
        const held = heldListResponse;
        heldListResponse = undefined;
        held.started();
        await held.release;
      }
      const current = Number(url.searchParams.get('page') || 1), start = (current - 1) * 20;
      return ok({ items: Array.from({ length: Math.max(0, Math.min(20, total - start)) }, (_, n) => account(start + n + 1)), page: current, page_size: 20, total });
    }
    if (path.endsWith('/daily-usage')) return ok({ start_date: url.searchParams.get('start_date'), end_date: url.searchParams.get('end_date'), timezone: 'Asia/Shanghai', collection_time: '03:00', exchange_rate_cny_per_usd: 7.2, days: [], rows: [] });
    const details = path.match(/^\/(?:accounts|admin\/synced-accounts)\/(\d+)$/);
    if (details) return ok({ account: account(details[1]), snapshots: [], snapshot_count: 0, snapshot_page: 1, snapshot_page_size: 50, trend_reduced: false, trend: { cost_total_usd: [], input_tokens_total: [], output_tokens_total: [] }, interval_seconds: 300, timezone: 'Asia/Shanghai' });
    if (path === '/submissions/22' || path === '/admin/submissions/22') return route.fulfill({ status: 404, contentType: 'application/json', body: JSON.stringify({ success: false, message: 'Fixture submission unavailable.' }) });
    if (path === '/vendors') return ok({ vendors: [] });
    unexpected.push(`${req.method()} ${path}`);
    return route.fulfill({ status: 404, contentType: 'application/json', body: JSON.stringify({ success: false, message: 'Unexpected request.' }) });
  });
  page.on('pageerror', error => errors.push(error.message));
  const base = `http://127.0.0.1:${port}/tk-register/`;
  async function loaded(expectedPage, count = 20) {
    await expect(page.locator('.usage-accounts-table tbody tr')).toHaveCount(count);
    await expect(page.locator('.usage-list-shell')).toHaveAttribute('aria-busy', 'false');
    await expect(page.locator('.usage-pagination')).toContainText(`Page ${expectedPage} of`);
  }
  async function returned(hash, scrollY) {
    await loaded(8);
    assert.equal(new URL(page.url()).hash, hash);
    await expect(page.getByRole('searchbox', { name: 'Search accounts', exact: true })).toHaveValue('sample');
    await expect(page.getByRole('combobox', { name: 'Account status', exact: true })).toHaveText('Active');
    await expect.poll(async () => Math.abs(await page.evaluate(() => window.scrollY) - scrollY), { message: 'Original scroll offset must be restored after rows render' }).toBeLessThanOrEqual(3);
  }
  function lastListRequest(endpoint) {
    return requests.filter(request => request.path === endpoint).at(-1);
  }
  function assertPageFilters(endpoint, expectedPage, filters) {
    const request = lastListRequest(endpoint);
    const urlFilters = new URLSearchParams(new URL(page.url()).hash.split('?')[1]);
    assert.equal(request.params.page, String(expectedPage));
    for (const name of ['search', 'status', 'sources']) {
      assert.equal(request.params[name], filters[name], `A page change must preserve request ${name}`);
      assert.equal(urlFilters.get(name) || (name === 'sources' ? 'all' : ''), filters[name], `A page change must preserve URL ${name}`);
    }
    assert.equal(urlFilters.get('page'), String(expectedPage));
  }
  async function responsivePagination(pagination, scope) {
    for (const width of [390, 320]) {
      await page.setViewportSize({ width, height: 1000 });
      await pagination.scrollIntoViewIfNeeded();
      await expect(pagination.getByLabel('Go to page', { exact: true })).toBeVisible();
      await expect(pagination.getByRole('button', { name: 'Go', exact: true })).toBeVisible();
      const layout = await pagination.evaluate(nav => {
        const rect = nav.getBoundingClientRect();
        return { left: rect.left, right: rect.right, scrollWidth: nav.scrollWidth, clientWidth: nav.clientWidth, documentWidth: document.documentElement.scrollWidth, viewportWidth: innerWidth, controls: [...nav.querySelectorAll('button, input')].map(control => { const box = control.getBoundingClientRect(); return { left: box.left, right: box.right }; }) };
      });
      assert.ok(layout.left >= 0 && layout.right <= width + 1, `${scope} pagination must fit the ${width}px viewport`);
      assert.ok(layout.scrollWidth <= layout.clientWidth + 1, `${scope} pagination must not overflow internally at ${width}px`);
      assert.ok(layout.documentWidth <= layout.viewportWidth + 1, `${scope} account list must not overflow horizontally at ${width}px`);
      for (const control of layout.controls) assert.ok(control.left >= layout.left && control.right <= layout.right + 1, `${scope} pagination controls must fit at ${width}px`);
    }
    await page.setViewportSize({ width: 1440, height: 1000 });
  }

  for (const admin of [true, false]) {
    const path = admin ? 'account-pool/accounts' : 'accounts';
    const endpoint = admin ? '/admin/synced-accounts' : '/accounts';
    const backName = admin ? 'Account pool / Accounts' : 'Accounts';
    await page.goto(`${base}#/${path}?tab=list`);
    await loaded(1);
    await page.getByRole('searchbox', { name: 'Search accounts', exact: true }).fill('sample');
    await expect.poll(() => new URL(page.url()).hash.includes('search=sample')).toBe(true);
    await page.getByRole('combobox', { name: 'Account status', exact: true }).click();
    await page.getByRole('option', { name: 'Active', exact: true }).click();
    if (!admin) {
      await page.locator('#account-source').click();
      const source = page.getByRole('dialog', { name: 'Source', exact: true });
      await source.getByRole('checkbox', { name: 'Select all', exact: true }).uncheck();
      await source.getByRole('checkbox', { name: 'ben@example.com', exact: true }).check();
      await source.getByRole('button', { name: 'Done', exact: true }).click();
    }
    await loaded(1);
    const pagination = page.getByRole('navigation', { name: 'Account list pagination', exact: true });
    const goTo = pagination.getByLabel('Go to page', { exact: true });
    const go = pagination.getByRole('button', { name: 'Go', exact: true });
    const filters = { ...lastListRequest(endpoint).params };
    assert.equal(filters.search, 'sample');
    assert.equal(filters.status, 'active');
    if (!admin) assert.equal(filters.sources, '7');

    await goTo.fill('6');
    await go.click();
    await loaded(6);
    assertPageFilters(endpoint, 6, filters);
    await goTo.fill('8');
    await goTo.press('Enter');
    await loaded(8);
    assertPageFilters(endpoint, 8, filters);

    // Invalid values must be inert for both Enter and the Go button.
    for (const invalid of ['', '0', '1.5', '10']) {
      const requestCount = requests.length, unchangedHash = new URL(page.url()).hash;
      await goTo.fill(invalid);
      await goTo.press('Enter');
      if (await go.isEnabled()) await go.click();
      await page.waitForTimeout(120);
      assert.equal(requests.length, requestCount, `Invalid page ${JSON.stringify(invalid)} must not request data`);
      assert.equal(new URL(page.url()).hash, unchangedHash, `Invalid page ${JSON.stringify(invalid)} must not navigate`);
      await loaded(8);
    }

    // Keep a response pending to inspect the real loading state deterministically.
    let releaseResponse, requestStarted;
    const started = new Promise(resolve => { requestStarted = resolve; });
    heldListResponse = { path: endpoint, started: requestStarted, release: new Promise(resolve => { releaseResponse = resolve; }) };
    await goTo.fill('7');
    await go.click();
    await started;
    try {
      await expect(page.locator('.usage-list-shell')).toHaveAttribute('aria-busy', 'true');
      await expect(goTo).toBeDisabled();
      await expect(go).toBeDisabled();
      await expect(pagination.getByRole('button', { name: 'Previous', exact: true })).toBeDisabled();
      await expect(pagination.getByRole('button', { name: 'Next', exact: true })).toBeDisabled();
    } finally { releaseResponse(); }
    await loaded(7);
    assertPageFilters(endpoint, 7, filters);
    await pagination.getByRole('button', { name: 'Next', exact: true }).click();
    await loaded(8);
    assertPageFilters(endpoint, 8, filters);
    await responsivePagination(pagination, admin ? 'Admin' : 'Member');

    const hash = new URL(page.url()).hash;
    assert.equal(new URLSearchParams(hash.split('?')[1]).get('page'), '8');
    await page.evaluate(() => window.scrollTo({ top: 700, behavior: 'instant' }));
    const scrollY = await page.evaluate(() => window.scrollY);
    await page.getByRole('button', { name: 'sample145@example.com', exact: true }).click();
    await page.getByRole('heading', { name: 'sample145@example.com', exact: true }).waitFor();
    await page.getByRole('tab', { name: 'Daily usage', exact: true }).click();
    await page.reload();
    await page.getByRole('heading', { name: 'sample145@example.com', exact: true }).waitFor();
    await page.getByRole('tab', { name: 'Overview', exact: true }).click();
    await page.getByRole('button', { name: 'View submission', exact: true }).click();
    await page.getByRole('button', { name: 'Account details', exact: true }).click();
    await page.getByRole('heading', { name: 'sample145@example.com', exact: true }).waitFor();
    await page.getByRole('button', { name: backName, exact: true }).last().click();
    await returned(hash, scrollY);
    if (!admin) assert.equal(requests.filter(r => r.path === endpoint).at(-1).params.sources, '7');

    // Browser Back and Forward must restore URL filters while the list stays mounted.
    const previous = page.getByRole('button', { name: 'Previous', exact: true });
    await previous.scrollIntoViewIfNeeded();
    const paginationScroll = await page.evaluate(() => window.scrollY);
    await previous.click();
    await loaded(7);
    assertPageFilters(endpoint, 7, filters);
    await page.goBack();
    await returned(hash, paginationScroll);
    await page.goForward();
    await loaded(7);
    await page.goBack();
    await returned(hash, paginationScroll);
    await page.reload();
    await returned(hash, paginationScroll);
  }

  // Original page disappears while inspecting an account: use the last valid page.
  await page.getByRole('button', { name: 'sample145@example.com', exact: true }).click();
  await page.getByRole('heading', { name: 'sample145@example.com', exact: true }).waitFor();
  total = 45;
  await page.getByRole('button', { name: 'Accounts', exact: true }).last().click();
  await loaded(3, 5);
  assert.equal(new URLSearchParams(new URL(page.url()).hash.split('?')[1]).get('page'), '3');

  // A direct detail link in a separate tab has no list return context.
  const direct = await context.newPage();
  await direct.goto(`${base}#/account-pool/accounts/999/usage`);
  await direct.getByRole('heading', { name: 'sample999@example.com', exact: true }).waitFor();
  await direct.getByRole('button', { name: 'Account pool / Accounts', exact: true }).click();
  await expect(direct.locator('.usage-pagination')).toContainText('Page 1 of');
  assert.equal(new URL(direct.url()).hash, '#/account-pool/accounts?tab=list');
  await direct.close();
  assert.equal(requests.some(r => r.method !== 'GET'), false, 'Navigation must not trigger Sync or another mutation');
  assert.deepEqual(unexpected, []);
  assert.deepEqual(errors, []);
  console.log('Account list navigation passed: Go/Enter page jumps, invalid input, loading controls, search/status/Source, responsive pagination at 390/320px, scroll, detail tabs/reload/submission chain, browser Back/Forward, separate scopes, shrinking pagination and direct-link fallback; no mutations.');
} finally { await browser?.close(); server.kill('SIGTERM'); }
