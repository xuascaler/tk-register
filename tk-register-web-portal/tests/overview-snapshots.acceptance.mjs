import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { mkdir } from 'node:fs/promises';
import { chromium } from '@playwright/test';

const port = Number(process.env.TK_TEST_PORT || 4203);
const server = spawn(process.execPath, ['node_modules/vite/bin/vite.js', '--host', '127.0.0.1', '--port', String(port), '--strictPort'], { stdio: 'pipe' });
let browser;
try {
  await new Promise((resolve, reject) => {
    server.stdout.on('data', value => { if (String(value).includes('Local:')) resolve(); });
    server.on('exit', code => reject(new Error(`Vite exit ${code}`)));
    setTimeout(() => reject(new Error('Vite timeout')), 15000).unref();
  });
  browser = await chromium.launch({ headless: true, ...(process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH ? { executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH } : {}) });
  await mkdir('test-results', { recursive: true });
  const sourceList = [{ id: 'myself', label: 'myself' }, { id: '2', label: 'ben@example.com' }];
  const yesterday = new Date(Date.parse(new Date(Date.now() + 8 * 3600_000).toISOString().slice(0, 10)) - 86400_000).toISOString().slice(0, 10);
  const dateBefore = (date, count) => new Date(Date.parse(date) - count * 86400_000).toISOString().slice(0, 10);
  const currentWeek = { start: dateBefore(yesterday, 6), end: yesterday };
  const staleWeek = { start: dateBefore(yesterday, 7), end: dateBefore(yesterday, 1) };

  for (const admin of [false, true]) {
    const page = await browser.newPage({ viewport: { width: 1440, height: 1080 } });
    const calls = [], errors = [], unexpected = [];
    let missingWeek = true;
    const endpoint = admin ? '/admin/accounts/overview' : '/accounts/overview';
    page.on('pageerror', error => errors.push(error.message));
    await page.route('**/tk-register/api/**', async route => {
      const request = route.request(), url = new URL(request.url()), path = url.pathname.replace('/tk-register/api', '');
      calls.push({ path, method: request.method(), query: url.searchParams });
      const ok = data => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ success: true, data }) });
      if (path === '/auth/status') return ok({ registration_enabled: false, password_login_enabled: true, email_code_login_enabled: true });
      if (path === '/auth/me') return ok({ user: { id: 1, email: 'alex@example.com', role: admin ? 'admin' : 'member', status: 'active' }, csrf_token: 'fake-csrf' });
      if (path === '/members/sources' || path === '/admin/accounts/overview/sources') return ok({ sources: sourceList });
      if (path === endpoint) {
        const period = url.searchParams.get('period'), allSources = url.searchParams.get('sources') === 'all', cached = allSources && period;
        const empty = cached && (period === 'all_time' || period === 'week' && missingWeek);
        const range = period === 'week' && cached ? staleWeek : period ? { start: dateBefore(yesterday, ({ day: 1, week: 7, month: 30, all_time: 60 }[period]) - 1), end: yesterday } : { start: url.searchParams.get('start_date'), end: url.searchParams.get('end_date') };
        const chosen = allSources ? sourceList : sourceList.filter(source => url.searchParams.get('sources').split(',').includes(source.id));
        const sources = empty ? [] : chosen.map(source => ({ ...source, total: source.id === 'myself' ? 2 : 3, statuses: { active: 1, offline: source.id === 'myself' ? 1 : 2 }, cost_usd: 100, cost_known: source.id === 'myself' ? 2 : 3, collected_days: 1 }));
        const days = [], rows = [], models = ['claude-sonnet', 'claude-opus'], zeroBills = period === 'day';
        if (!empty) for (let at = Date.parse(range.start); at <= Date.parse(range.end); at += 86400_000) {
          const date = new Date(at).toISOString().slice(0, 10);
          days.push({ date, status: 'complete', expected: 5, collected: 5 });
          if (!zeroBills) for (const [i, model] of models.entries()) rows.push({ date, model, input_tokens: String(100 * (i + 1)), output_tokens: '20', cache_read_tokens: '0', cache_write_tokens: '0', cost_cny: '72', cost_usd: 10 * (i + 1) });
        }
        for (const source of sources) source.cost_usd = zeroBills ? 0 : days.length * 30 / sources.length;
        return ok({
          ...(cached ? { summary_available: !empty, summary_generated_at: empty ? null : '2026-11-14T19:20:00+08:00', summary_period: period } : {}),
          // Missing all-time responses carry a fallback date, not a real history boundary.
          start_date: empty && period === 'all_time' ? yesterday : range.start, end_date: empty && period === 'all_time' ? yesterday : range.end,
          earliest_date: empty ? null : range.start, timezone: 'Asia/Shanghai', collection_time: '03:00', exchange_rate_cny_per_usd: 7.2,
          latest_account_data: '2026-11-16T00:00:00+08:00', sources, days, rows,
          model_costs: zeroBills ? [] : sources.flatMap(source => models.map((model, i) => ({ source_id: source.id, model, cost_cny: '72', cost_usd: days.length * 10 * (i + 1) / sources.length })))
        });
      }
      unexpected.push(path);
      return route.fulfill({ status: 404, contentType: 'application/json', body: JSON.stringify({ success: false, message: 'Unexpected mock request' }) });
    });
    const overviewCalls = () => calls.filter(call => call.path === endpoint);
    const cents = value => value.trim() === '—' ? null : Math.round(Number(value.replace(/[,$\s]/g, '')) * 100);
    async function assertCostDisplays() {
      const total = cents(await page.locator('.overview-summary-spent strong').innerText());
      assert.notEqual(total, null);
      assert.equal(cents(await page.locator('.overview-period-total').innerText()), total);
      assert.equal(cents(await page.locator('.overview-chart-total strong').innerText()), total);
      assert.equal(cents(await page.locator('.overview-source-table tfoot .overview-cost').last().innerText()), total);
      const cards = await page.locator('.overview-model-grid strong').allTextContents();
      assert.equal(cards.reduce((sum, value) => sum + (cents(value) || 0), 0), total);
      const rows = await page.locator('.overview-source-table tbody .overview-cost').allTextContents();
      assert.equal(rows.reduce((sum, value) => sum + (cents(value) || 0), 0), total);
      assert.equal(await page.getByText('Total spent · selected period', { exact: true }).count(), 1);
      assert.doesNotMatch(await page.locator('.accounts-overview').innerText(), /cumulative/i);
      await page.getByRole('group', { name: 'Source breakdown' }).getByRole('button', { name: 'Model costs', exact: true }).click();
      for (const row of await page.locator('.overview-source-table tbody tr').all()) {
        const costs = (await row.locator('.overview-cost').allTextContents()).map(cents);
        assert.equal(costs.slice(0, -1).reduce((sum, value) => sum + (value || 0), 0), costs.at(-1));
      }
      const footer = (await page.locator('.overview-source-table tfoot .overview-cost').allTextContents()).map(cents);
      assert.equal(footer.slice(0, -1).reduce((sum, value) => sum + (value || 0), 0), total);
      assert.equal(footer.at(-1), total);
      await page.getByRole('group', { name: 'Source breakdown' }).getByRole('button', { name: 'Status', exact: true }).click();
    }
    const trigger = page.getByRole('button', { name: /^Usage date range:/ });
    const dates = page.getByRole('dialog', { name: 'Usage dates', exact: true });
    async function choosePreset(label) {
      await trigger.click();
      const response = page.waitForResponse(response => new URL(response.url()).pathname === `/tk-register/api${endpoint}`);
      await dates.getByRole('button', { name: label, exact: true }).click(); await response;
      await page.getByRole('heading', { name: 'Account summary', exact: true }).waitFor();
    }
    await page.goto(`http://127.0.0.1:${port}/tk-register/#/${admin ? 'account-pool/accounts' : 'accounts'}`);
    await page.locator('.is-summary-unavailable').waitFor();
    await page.waitForFunction(() => document.querySelectorAll('.overview-source-table tbody tr').length === 2);
    assert.deepEqual(Object.fromEntries(overviewCalls().at(-1).query), { sources: 'all', models: 'all', period: 'all_time' });
    assert.equal((await trigger.innerText()).trim(), 'All time');
    const numbers = page.locator('.overview-stat-value, .overview-status-grid strong, .overview-source-table td, .overview-period-total, .overview-model-grid strong, .overview-chart-total strong');
    assert.ok(await numbers.count() > 20);
    assert.ok((await numbers.allTextContents()).every(text => text.trim() === '—'), 'Missing summary numbers must remain unavailable, not zero');
    const unavailableColors = await numbers.evaluateAll(elements => elements.map(element => ({ className: element.className, color: getComputedStyle(element).color })));
    assert.ok(unavailableColors.every(element => element.color === 'rgb(182, 79, 88)'), `Missing summary dashes must be red: ${JSON.stringify(unavailableColors)}`);
    assert.equal(await page.locator('.overview-chart-placeholder').count(), 1);
    assert.equal(await page.locator('.overview-chart-placeholder text,.overview-chart-placeholder path,.overview-chart-target').count(), 0);
    assert.doesNotMatch(await page.locator('.accounts-overview').innerText(), /not generated|No daily|No model|successfully collected|latest|current/i);
    assert.equal(await page.locator('#daily-model-trigger').isDisabled(), true);
    const afterMissing = overviewCalls().length;
    await page.waitForTimeout(350);
    assert.equal(overviewCalls().length, afterMissing, 'Missing snapshot must not trigger a live fallback');
    for (const width of [1440, 390]) {
      await page.setViewportSize({ width, height: 1080 });
      await page.evaluate(() => document.fonts.ready);
      await page.waitForTimeout(150);
      assert.equal(await page.locator('.header').count(), 1);
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
      await page.screenshot({ path: `test-results/overview-snapshot-missing-${admin ? 'admin' : 'member'}-${width}.png`, fullPage: width > 390 });
    }
    await page.setViewportSize({ width: 1440, height: 1080 });
    await choosePreset('Past 7 days');
    await page.locator('.is-summary-unavailable').waitFor();
    await choosePreset('All time');
    assert.equal(overviewCalls().at(-1).query.get('period'), 'all_time');
    await trigger.click();
    assert.equal(await dates.getByLabel('Start date', { exact: true }).inputValue(), '', 'Missing all-time snapshot must not invent an earliest date');
    assert.equal(await dates.getByLabel('End date', { exact: true }).inputValue(), '');
    await page.keyboard.press('Escape');
    assert.doesNotMatch(await page.locator('.overview-model-summary p').innerText(), /\d{4}-\d{2}-\d{2}/);
    assert.doesNotMatch(await page.locator('.overview-usage .overview-section-heading p').innerText(), /\d{4}-\d{2}-\d{2}/);
    assert.doesNotMatch(await page.locator('.overview-usage .usage-footnote').innerText(), /Usage through/);

    missingWeek = false;
    await choosePreset('Past 7 days');
    await page.locator('.overview-daily-chart.is-bar').waitFor();
    assert.equal((await trigger.innerText()).trim(), 'Past 7 days');
    const filterTops = await page.locator('.overview-filters .tk-select-trigger').evaluateAll(elements => elements.map(element => element.getBoundingClientRect().top));
    assert.ok(Math.max(...filterTops) - Math.min(...filterTops) <= 1, 'Filter inputs stay aligned when an older snapshot range is shown');
    assert.equal(await page.locator('.overview-period-range').innerText(), `${staleWeek.start} – ${staleWeek.end}`);
    assert.match(await page.locator('.overview-updated').innerText(), /^Summary generated/);
    assert.doesNotMatch(await page.locator('.overview-updated').innerText(), /Nov 16/);
    assert.match(await page.locator('.overview-usage .overview-section-heading p').innerText(), new RegExp(`${staleWeek.start} – ${staleWeek.end}`));
    await trigger.click();
    assert.equal(await dates.getByRole('button', { name: 'Past 7 days', exact: true }).getAttribute('aria-pressed'), 'true');
    assert.equal(await dates.getByLabel('Start date', { exact: true }).inputValue(), staleWeek.start);
    assert.equal(await dates.getByLabel('End date', { exact: true }).inputValue(), staleWeek.end);
    await page.keyboard.press('Escape');
    const countsBefore = await page.locator('.overview-summary-top > div:first-child .overview-stat-value,.overview-status-grid strong').allTextContents();
    await assertCostDisplays();
    const totalBefore = await page.locator('.overview-chart-total strong').innerText(), modelCalls = overviewCalls().length;
    await page.locator('#daily-model-trigger').click();
    await page.getByRole('checkbox', { name: 'claude-opus', exact: true }).uncheck();
    await page.getByRole('button', { name: 'Done', exact: true }).click();
    assert.equal(await page.locator('.overview-model-grid > div').count(), 1);
    assert.equal(overviewCalls().length, modelCalls);
    assert.deepEqual(await page.locator('.overview-summary-top > div:first-child .overview-stat-value,.overview-status-grid strong').allTextContents(), countsBefore);
    await assertCostDisplays();
    assert.notEqual(await page.locator('.overview-chart-total strong').innerText(), totalBefore);
    await page.screenshot({ path: `test-results/overview-snapshot-stale-${admin ? 'admin' : 'member'}.png`, fullPage: true });

    await page.locator('#overview-source').click();
    const sourceMenu = page.getByRole('dialog', { name: 'Source', exact: true });
    await sourceMenu.getByRole('checkbox', { name: 'myself', exact: true }).uncheck();
    await sourceMenu.getByRole('button', { name: 'Done', exact: true }).click();
    await page.waitForFunction(() => document.querySelector('.overview-updated')?.textContent?.startsWith('Saved account data'));
    assert.deepEqual(Object.fromEntries(overviewCalls().at(-1).query), { sources: '2', models: 'all', period: 'week' });
    assert.equal(await page.locator('.overview-source-table tbody tr').count(), 1);
    assert.match(await page.locator('.overview-usage .overview-section-heading p').innerText(), new RegExp(`${currentWeek.start} – ${currentWeek.end}`));
    assert.equal(await page.locator('.overview-period-range').count(), 0);
    await assertCostDisplays();

    // Applying dates is intentionally custom, even when exactly equal to Past 7 days.
    await trigger.click();
    await dates.getByLabel('Start date', { exact: true }).fill(currentWeek.start);
    await dates.getByLabel('End date', { exact: true }).fill(currentWeek.end);
    await dates.getByRole('button', { name: 'Apply', exact: true }).click();
    await page.waitForFunction(() => document.querySelector('#overview-period-trigger')?.textContent?.includes(' – '));
    assert.deepEqual(Object.fromEntries(overviewCalls().at(-1).query), { sources: '2', models: 'all', start_date: currentWeek.start, end_date: currentWeek.end });
    await trigger.click();
    assert.equal(await dates.locator('.overview-presets button[aria-pressed="true"]').count(), 0);
    await page.keyboard.press('Escape');
    await page.locator('#overview-source').click();
    await sourceMenu.getByRole('checkbox', { name: 'Select all', exact: true }).check();
    await sourceMenu.getByRole('button', { name: 'Done', exact: true }).click();
    await page.waitForFunction(() => document.querySelectorAll('.overview-source-table tbody tr').length === 2);
    assert.equal(overviewCalls().at(-1).query.get('sources'), 'all');
    assert.equal(overviewCalls().at(-1).query.has('period'), false, 'Custom range with all sources remains live');
    assert.match(await page.locator('.overview-updated').innerText(), /^Saved account data/);
    await choosePreset('Past month');
    await page.locator('.overview-daily-chart.is-curve').waitFor();
    assert.deepEqual(Object.fromEntries(overviewCalls().at(-1).query), { sources: 'all', models: 'all', period: 'month' });
    assert.match(await page.locator('.overview-updated').innerText(), /^Summary generated/);
    await assertCostDisplays();
    const costTotal = await page.locator('.overview-summary-spent strong').innerText();
    for (const label of ['Input tokens', 'Output tokens', 'Cost']) {
      await page.getByRole('group', { name: 'Daily chart metric' }).getByRole('button', { name: label, exact: true }).click();
      assert.equal(await page.locator('.overview-summary-spent strong').innerText(), costTotal);
      if (label === 'Cost') assert.equal(await page.locator('.overview-chart-total strong').innerText(), costTotal);
      else assert.match(await page.locator('.overview-chart-total strong').innerText(), /^[\d,]+$/);
    }
    await page.locator('#daily-model-trigger').click();
    await page.getByRole('checkbox', { name: 'Select all', exact: true }).check();
    await page.getByRole('button', { name: 'Done', exact: true }).click();
    await choosePreset('Past day');
    assert.equal(await page.locator('.overview-summary-spent strong').innerText(), '0.00 $');
    assert.equal(await page.locator('.overview-chart-total strong').innerText(), '0.00 $');
    assert.equal(await page.locator('.overview-period-total').innerText(), '0.00 $');
    assert.ok((await page.locator('.overview-source-table .overview-cost').allTextContents()).every(value => value === '0.00 $'));
    await choosePreset('Past month');
    const beforeClear = overviewCalls().length;
    await page.locator('#daily-model-trigger').click();
    await page.getByRole('button', { name: 'Clear selection', exact: true }).click();
    await page.getByRole('button', { name: 'Done', exact: true }).click();
    assert.equal(overviewCalls().length, beforeClear);
    assert.equal(await page.locator('.overview-summary-spent strong').innerText(), '—');
    assert.equal(await page.locator('.overview-chart-total strong').innerText(), '—');
    assert.ok((await page.locator('.overview-source-table .overview-cost').allTextContents()).every(value => value === '—'));
    await choosePreset('Past day');
    assert.equal(await page.locator('.overview-summary-spent strong').innerText(), '—', 'Explicit empty selection remains empty on a zero-bill day');
    assert.equal(await page.locator('.overview-chart-total strong').innerText(), '—');
    await page.getByRole('heading', { name: 'Select models to view daily usage', exact: true }).waitFor();
    assert.equal(calls.some(call => call.method !== 'GET' || call.path.endsWith('/sync')), false);
    assert.deepEqual(unexpected, []); assert.deepEqual(errors, []);
    await page.close();
  }
  console.log('Overview snapshots acceptance passed for member/admin: red missing values with no fallback, empty All time dates, stale snapshot ranges with explicit presets, saved timestamps, local model filtering, reconciled selected-period costs in every display, zero bills versus empty selection, chart metrics, live source/custom routing, read-only requests and responsive layouts.');
} finally { await browser?.close(); server.kill('SIGTERM'); }
