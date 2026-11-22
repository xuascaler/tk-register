import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { mkdir } from 'node:fs/promises';
import { chromium } from '@playwright/test';

const port = Number(process.env.TK_TEST_PORT || 4208);
const server = spawn(process.execPath, ['node_modules/vite/bin/vite.js', '--host', '127.0.0.1', '--port', String(port), '--strictPort'], { stdio: 'pipe' });
let browser;
try {
  await new Promise((resolve, reject) => { server.stdout.on('data', data => { if (String(data).includes('Local:')) resolve(); }); server.on('exit', code => reject(new Error(`Vite exit ${code}`))); setTimeout(() => reject(new Error('Vite timeout')), 15000).unref(); });
  browser = await chromium.launch({ headless: true, ...(process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH ? { executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH } : {}) });
  const page = await browser.newPage({ viewport: { width: 1440, height: 1080 } });
  const errors = [], calls = [], unknown = [];
  let enabled = false, apiModelCostsEnabled = false, modelSource = 'modified', superAdmin = true, historicalOnly = false;
  const delegate = { id: '2', email: 'delegate@example.com', role: 'admin', is_super_admin: false, status: 'active', created_at: '2026-11-01T08:00:00Z' };
  page.on('pageerror', error => errors.push(error.message));
  const yesterday = new Date(Date.parse(new Date(Date.now() + 8 * 3600_000).toISOString().slice(0, 10)) - 86400_000).toISOString().slice(0, 10);
  const before = (date, days) => new Date(Date.parse(date) - days * 86400_000).toISOString().slice(0, 10);
  const sources = [{ id: 'myself', label: 'myself', total: 2, statuses: { active: 2 }, cost_usd: 0.03, cost_known: 2, collected_days: 1 }];
  const account = { id: '1', account: 'example.account@gmail.com', plan: 'claude/cc-max-20x', vendor_id: '1', vendor_name: 'TK Register 0', latest_submission_id: '1', current_submission_id: '1', submission_status: 'accepted', platform_account_id: '24103', last_success_at: new Date().toISOString(), last_error: '', snapshot: { account_status: 'active', cost_total_usd: 0.03, input_tokens_total: 10, output_tokens_total: 20 } };
  await page.route('**/tk-register/api/**', async route => {
    const req = route.request(), url = new URL(req.url()), path = url.pathname.replace('/tk-register/api', '');
    calls.push({ path, method: req.method(), query: url.searchParams });
    const ok = data => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ success: true, data }) });
    if (path === '/auth/status') return ok({ registration_enabled: false, password_login_enabled: true, email_code_login_enabled: true });
    if (path === '/auth/me') return ok({ user: { id: 1, email: 'alex@example.com', role: 'admin', is_super_admin: superAdmin, status: 'active' }, csrf_token: 'fake-csrf' });
    if (path === '/members/sources' || path === '/admin/accounts/overview/sources' || path === '/admin/raw-accounts/overview/sources') return ok({ sources: sources.map(({ id, label }) => ({ id, label })) });
    if (path === '/admin/users') return ok({items:[delegate], total:1,page:1,page_size:20});
    if (path === '/admin/users/role') {
      assert.deepEqual(req.postDataJSON(), {email: delegate.email, role:'super_admin'});
      delegate.is_super_admin = true;
      return ok({user:delegate});
    }
    if (path === '/admin/settings') {
      if (req.method() === 'PUT') {
        const patch = req.postDataJSON().settings;
        if ('user_overview_model_costs_enabled' in patch) enabled = patch.user_overview_model_costs_enabled === 'true';
        if ('api_user_overview_model_costs_enabled' in patch) apiModelCostsEnabled = patch.api_user_overview_model_costs_enabled === 'true';
        modelSource = patch.model_usage_source || modelSource;
        assert.ok(modelSource !== 'raw' || !enabled);
      }
      return ok({ settings: [{ key: 'user_overview_model_costs_enabled', category: 'overview', type: 'boolean', value: String(enabled), description: 'Enable model costs on personal Overview pages.', read_only: false }, { key: 'model_usage_source', category: 'overview', type: 'enum', options: ['modified', 'raw'], value: modelSource, description: 'Model usage source' }, { key: 'api_user_overview_model_costs_enabled', category: 'overview', type: 'boolean', value: String(apiModelCostsEnabled), description: 'Include model costs in the Daily usage API.', read_only: false }] });
    }
    if (historicalOnly && (path.endsWith('/daily-usage') || path.endsWith('/overview'))) {
      const date = yesterday;
      const daily = path.endsWith('/daily-usage');
      const raw = path.startsWith('/admin/raw-accounts');
      const model = 'claude-fable-example';
      const bill = {date, model, input_tokens:'100',output_tokens:'17',cache_write_tokens:'0',cache_read_tokens:'3',cost_usd:400,cost_cny:'2880'};
      return ok({
        model_costs_enabled:true, total_cost_usd:raw?400:null, total_cost_days:raw?1:0,
        start_date:date,end_date:date,earliest_date:date,timezone:'Asia/Shanghai',collection_time:'03:00',exchange_rate_cny_per_usd:7.2,
        latest_account_data:null,daily_costs:[{date,collected:raw?1:0,expected:1,cost_usd:raw?400:null}],
        sources:[{...sources[0],cost_usd:raw?400:null,collected_days:raw?1:0}], rows:raw?[bill]:[],
        model_costs:raw?[{source_id:'myself',model,cost_cny:'2880',cost_usd:400}]:[],
        days:daily?[{date,status:raw?'complete':'missing',collected_at:raw?new Date().toISOString():null}]:[{date,status:raw?'complete':'missing',expected:1,collected:raw?1:0}]
      });
    }
    if (path === '/accounts/overview' || path === '/admin/accounts/overview' || path === '/admin/raw-accounts/overview' || path === '/accounts/1/daily-usage') {
      const admin = path.startsWith('/admin/'), daily = path.endsWith('/daily-usage'), details = admin || enabled;
      const period = url.searchParams.get('period') || 'week', count = ({ day: 1, week: 7, month: 30, all_time: 60 })[period];
      const start = daily ? url.searchParams.get('start_date') : before(yesterday, count - 1), end = daily ? url.searchParams.get('end_date') : yesterday;
      const days = [], rows = [], daily_totals = [];
      for (let at = Date.parse(start); at <= Date.parse(end); at += 86400_000) {
        const date = new Date(at).toISOString().slice(0, 10);
        days.push(daily ? { date, status: 'complete', collected_at: new Date().toISOString() } : { date, status: 'complete', expected: 2, collected: 2 });
        const total = { date, cost_cny: '0.216', cost_usd: 0.03, input_tokens: '9007199254740993', output_tokens: '20', cache_read_tokens: '3', cache_write_tokens: '4' };
        daily_totals.push(total);
        rows.push({ ...total, model: 'claude-private-example' });
      }
      return ok({ start_date: start, end_date: end, earliest_date: start, timezone: 'Asia/Shanghai', collection_time: '03:00', exchange_rate_cny_per_usd: 7.2, latest_account_data: null, days,
        sources: sources.map(source => ({ ...source, cost_usd: days.length * 3 / 100 })),
        ...(admin ? {} : { model_costs_enabled: enabled }),
        ...(details ? { rows, model_costs: [{ source_id: 'myself', model: 'claude-private-example', cost_cny: '0.216', cost_usd: days.length * 3 / 100 }] } : { daily_totals, total_cost_usd: days.length * 3 / 100 }) });
    }
    if (['/accounts/1','/admin/raw-accounts/1','/admin/synced-accounts/1'].includes(path)) return ok({ account, interval_seconds: 300, timezone: 'Asia/Shanghai', snapshots: [], snapshot_count: 0, trend: { cost_total_usd: [], input_tokens_total: [], output_tokens_total: [] } });
    if (req.method() === 'GET' && path === '/admin/synced-accounts/1/auth-json') return ok({ account: account.account, plan: account.plan, editable: true });
    if (req.method() === 'GET' && path === '/admin/synced-accounts/1/deactivate') return ok({ account: account.account, plan: account.plan, available: true });
    unknown.push(path);
    return route.fulfill({ status: 404, contentType: 'application/json', body: JSON.stringify({ success: false, message: 'Unexpected mock request' }) });
  });
  const goto = async hash => { await page.goto(`http://127.0.0.1:${port}/tk-register/#/${hash}`); };
  const assertUsageLabels = async section => {
    const metrics = section.getByRole('group', { name: 'Daily chart metric' });
    for (const name of ['Cost', 'Input tokens', 'Output tokens']) {
      await metrics.getByRole('button', { name, exact: true }).waitFor();
    }
    assert.doesNotMatch(await section.innerText(), /Allocated costs and estimated tokens|Upstream costs and tokens/i);
  };
  const assertAccountUsageLabels = async () => {
    await goto('accounts/1/usage');
    await assertUsageLabels(page.locator('.daily-compact.daily-usage'));
    await page.getByRole('button', { name: 'View daily details' }).click();
    await page.getByRole('heading', { name: 'Daily breakdown', exact: true }).waitFor();
    await assertUsageLabels(page.locator('.daily-usage'));
  };
  const assertPrivate = async () => {
    await page.locator('.overview-source-table.is-status').waitFor();
    assert.equal(await page.locator('#daily-model-trigger,.overview-model-summary,.overview-chart-legend').count(), 0);
    assert.equal(await page.getByRole('group', { name: 'Source breakdown' }).count(), 0);
    assert.doesNotMatch(await page.locator('.accounts-overview').innerText(), /claude-private-example|Model costs/);
    assert.equal(await page.locator('.overview-source-table.is-status').count(), 1);
  };
  await goto('accounts');
  await page.getByRole('heading', { name: 'Account summary', exact: true }).waitFor();
  await assertPrivate();
  assert.equal(await page.locator('.overview-summary-spent strong').innerText(), '1.80 $');
  assert.equal(await page.locator('.overview-chart-total strong').innerText(), '1.80 $');
  assert.equal(await page.locator('.overview-daily-chart.is-curve path[stroke-width="3.5"]').count(), 1);
  await page.getByRole('group', { name: 'Daily chart metric' }).getByRole('button', { name: 'Input tokens', exact: true }).click();
  assert.equal(await page.locator('.overview-chart-total strong').innerText(), (9007199254740993n * 60n).toLocaleString('en-US'));
  await page.locator('.overview-chart-target').focus();
  assert.match(await page.locator('.daily-chart-tooltip').innerText(), /9,007,199,254,740,993/);
  await page.keyboard.press('Escape');
  const trigger = page.getByRole('button', { name: /^Usage date range:/ });
  const periodSelect = page.getByRole('combobox', { name: 'Usage period', exact: true });
  const periodList = page.getByRole('listbox', { name: 'Usage period', exact: true });
  const personalCalls = () => calls.filter(call => call.path === '/accounts/overview');
  const assertPeriodList = async width => {
    assert.equal(await periodSelect.getAttribute('aria-expanded'), 'true');
    assert.deepEqual(await periodList.getByRole('option').allTextContents(), ['Past day', 'Past 7 days', 'Past month', 'All time']);
    assert.equal(await periodList.locator('input,footer,form').count(), 0);
    assert.equal(await periodList.getByRole('option', { selected: true }).locator('svg.lucide-check').count(), 1);
    assert.equal(await periodList.locator('svg.lucide-check').count(), 1);
    const menu = await periodList.boundingBox(), field = await periodSelect.boundingBox();
    assert.ok(menu && field && Math.abs(menu.width - field.width) <= 1, 'Period menu matches its trigger width');
    assert.ok(menu.x >= 0 && menu.x + menu.width <= width, `Period menu fits ${width}px width`);
    assert.ok(menu.y >= 0 && menu.y + menu.height <= 1080, `Period menu fits ${width}px viewport`);
    const rows = await periodList.getByRole('option').evaluateAll(options => options.map(option => ({ x: option.getBoundingClientRect().x, top: option.getBoundingClientRect().top, bottom: option.getBoundingClientRect().bottom })));
    assert.ok(rows.every((row, index) => Math.abs(row.x - rows[0].x) <= 1 && (!index || row.top >= rows[index - 1].bottom)), 'Four options form one vertical list');
  };
  const choosePeriod = async (label, activate) => {
    const response = page.waitForResponse(response => new URL(response.url()).pathname === '/tk-register/api/accounts/overview');
    await activate(); await response;
    await page.getByRole('heading', { name: 'Account summary', exact: true }).waitFor();
    const days = ({ 'Past day': 1, 'Past 7 days': 7, 'Past month': 30, 'All time': 60 })[label];
    await page.waitForFunction(amount => document.querySelector('.overview-summary-spent strong')?.textContent === amount, `${(days * 0.03).toFixed(2)} $`);
    assert.equal((await periodSelect.innerText()).trim(), label);
    assert.equal(await periodSelect.getAttribute('aria-expanded'), 'false');
    await assertPrivate();
  };
  assert.equal((await periodSelect.innerText()).trim(), 'All time');
  assert.equal(await periodSelect.locator('svg.lucide-chevron-down').count(), 1);
  assert.equal(await periodSelect.locator('svg.lucide-calendar-days').count(), 0);
  await periodSelect.click(); await assertPeriodList(1440);
  const beforeSame = personalCalls().length;
  await periodList.getByRole('option', { name: 'All time', exact: true }).click();
  await page.waitForTimeout(100);
  assert.equal(await periodSelect.getAttribute('aria-expanded'), 'false');
  assert.equal(personalCalls().length, beforeSame, 'Selecting the current period does not repeat the GET');
  for (const label of ['Past day', 'Past 7 days', 'Past month', 'All time']) {
    await periodSelect.click();
    await choosePeriod(label, () => periodList.getByRole('option', { name: label, exact: true }).click());
  }
  const beforeDismiss = personalCalls().length;
  await periodSelect.click(); await periodSelect.press('Escape');
  assert.equal(await periodSelect.getAttribute('aria-expanded'), 'false');
  assert.equal(await periodSelect.evaluate(element => element === document.activeElement), true);
  await periodSelect.click(); await page.getByRole('heading', { name: 'Accounts', exact: true }).click();
  assert.equal(await periodSelect.getAttribute('aria-expanded'), 'false');
  assert.equal((await periodSelect.innerText()).trim(), 'All time');
  assert.equal(personalCalls().length, beforeDismiss, 'Dismissing the list leaves the selected period unchanged');
  await periodSelect.press('Home');
  await choosePeriod('Past day', () => periodSelect.press('Enter'));
  await periodSelect.press('Home'); await periodSelect.press('ArrowDown');
  await choosePeriod('Past 7 days', () => periodSelect.press('Enter'));
  await periodSelect.press('End'); await periodSelect.press('ArrowUp');
  await choosePeriod('Past month', () => periodSelect.press('Enter'));
  await periodSelect.press('End');
  await choosePeriod('All time', () => periodSelect.press('Enter'));
  assert.ok(calls.filter(call => call.path === '/accounts/overview').every(call => !call.query.has('models') && !call.query.has('start_date')));
  await mkdir('test-results', { recursive: true });
  for (const width of [1440, 390, 320]) {
    await page.setViewportSize({ width, height: 1080 });
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
    const chartLabels = await page.locator('.overview-daily-chart').evaluate(svg => [...svg.querySelectorAll('text')].map(label => ({ text: label.textContent, x: label.getBBox().x })));
    assert.ok(chartLabels.every(label => label.x >= 0), `Chart labels must fit at ${width}px: ${JSON.stringify(chartLabels)}`);
    await page.screenshot({ path: `test-results/overview-disabled-${width}.png`, fullPage: true });
    await page.locator('.overview-usage').screenshot({ path: `test-results/overview-disabled-chart-${width}.png` });
    await page.evaluate(() => window.scrollTo(0, 0));
    await periodSelect.click(); await assertPeriodList(width);
    await page.screenshot({ path: `test-results/period-dropdown-${width}.png` });
    await periodSelect.press('Escape');
  }
  await page.setViewportSize({ width: 1440, height: 1080 });
  await goto('accounts/1/usage?tab=daily-usage');
  await page.getByRole('heading', { name: 'Daily breakdown', exact: true }).waitFor();
  assert.equal(await page.locator('#daily-model-trigger').count(), 0);
  assert.equal(await page.getByRole('columnheader', { name: 'Model', exact: true }).count(), 0);
  assert.equal(await page.locator('.daily-table tbody tr').count(), 7);
  assert.doesNotMatch(await page.locator('.daily-usage').innerText(), /claude-private-example/);
  await page.getByRole('button', { name: 'Last 30 days', exact: true }).click();
  await page.locator('.overview-daily-chart.is-curve').waitFor();
  await goto('config/overview');
  const toggle = page.locator('#value-user_overview_model_costs_enabled');
  const apiToggle = page.locator('#value-api_user_overview_model_costs_enabled');
  await toggle.waitFor();
  assert.equal(await page.getByRole('tab', { name: 'Overview', exact: false }).getAttribute('aria-selected'), 'true');
  assert.equal(await toggle.isChecked(), false);
  assert.equal(await apiToggle.isChecked(), false);
  assert.ok(await apiToggle.isEnabled());
  await page.screenshot({ path: 'test-results/overview-config-disabled.png', fullPage: true });
  await toggle.check();
  await apiToggle.check();
  await page.getByRole('button', { name: 'Save changes', exact: true }).click();
  await page.getByText('Changes saved.', { exact: true }).waitFor();
  assert.equal(enabled, true);
  assert.equal(apiModelCostsEnabled, true);
  await goto('accounts');
  await page.locator('.overview-model-summary').waitFor();
  await assertUsageLabels(page.locator('.accounts-overview'));
  assert.equal(await page.locator('#daily-model-trigger').count(), 1);
  await trigger.click();
  assert.equal(await page.getByRole('dialog', { name: 'Usage dates' }).locator('input[type=date]').count(), 0);
  await page.keyboard.press('Escape');
  await assertAccountUsageLabels();
  await goto('config/overview');
  const sourcePicker = page.locator('#value-model_usage_source');
  const selectSource = async name => {
    await sourcePicker.click();
    await page.getByRole('option', {name, exact: true}).click();
  };
  await selectSource('Raw data');
  assert.ok(await toggle.isDisabled() && !(await toggle.isChecked()));
  assert.ok(await apiToggle.isEnabled() && await apiToggle.isChecked(), 'Raw source must not disable API model details');
  await page.getByRole('button', {name:'Discard',exact:true}).click();
  assert.ok(await toggle.isEnabled() && await toggle.isChecked());
  await selectSource('Raw data');
  await page.getByRole('button', {name:'Save changes',exact:true}).click();
  await page.getByText('Changes saved.', {exact:true}).waitFor();
  assert.equal(modelSource, 'raw'); assert.equal(enabled, false);
  assert.equal(apiModelCostsEnabled, true, 'Source changes must preserve the API setting');
  await page.reload(); await toggle.waitFor();
  assert.ok(await toggle.isDisabled() && !(await toggle.isChecked()));
  assert.ok(await apiToggle.isEnabled() && await apiToggle.isChecked(), 'Raw source must not disable API model details');
  await apiToggle.uncheck();
  await page.getByRole('button', {name:'Save changes',exact:true}).click();
  await page.getByText('Changes saved.', {exact:true}).waitFor();
  assert.equal(apiModelCostsEnabled, false);
  assert.equal(modelSource, 'raw'); assert.equal(enabled, false);
  await goto('accounts');
  await assertPrivate();
  await assertUsageLabels(page.locator('.accounts-overview'));
  await assertAccountUsageLabels();
  await goto('config/overview');
  await selectSource('Modified data');
  assert.ok(await toggle.isEnabled() && !(await toggle.isChecked()));
  await page.getByRole('button', {name:'Save changes',exact:true}).click();
  await page.getByText('Changes saved.', {exact:true}).waitFor();

  await goto('account-pool/raw-accounts');
  await page.getByRole('heading', {name:'Raw account',exact:true}).waitFor();
  await page.locator('.overview-model-summary').waitFor();
  await assertUsageLabels(page.locator('.accounts-overview'));
  await goto('config/users');
  await page.getByRole('button', {name:'Add super admin',exact:true}).click();
  const dialog = page.getByRole('dialog');
  await dialog.getByLabel('Email address').fill(delegate.email);
  await dialog.getByRole('button',{name:'Add super admin',exact:true}).click();
  await page.getByText(`${delegate.email} is now a super admin.`,{exact:true}).waitFor();
  await page.getByRole('cell',{name:'Super admin',exact:true}).waitFor();

  historicalOnly = true;
  for (const raw of [false,true]) {
    const pool = `account-pool/${raw ? 'raw-accounts' : 'accounts'}`;
    await goto(pool);
    await page.getByRole('heading',{name:'Account summary',exact:true}).waitFor();
    assert.equal(await page.locator('.overview-summary-spent strong').innerText(),raw?'400.00 $':'—');
    assert.equal(await page.locator('.overview-model-grid strong').count(),raw?1:0);
    if (!raw) await page.getByText('No model usage data for this period.',{exact:true}).waitFor();
    const detailStart = calls.length;
    await goto(`${pool}/1/usage?tab=daily-usage`);
    await page.getByRole('heading',{name:'Daily breakdown',exact:true}).waitFor();
    assert.equal(await page.locator('.daily-summary strong').first().innerText(),raw?'400.00 $':'—');
    assert.equal(await page.locator('.daily-table tbody tr').count(),raw?1:0);
    if (raw) {
      const detailReads = calls.slice(detailStart).filter(call => call.method === 'GET').map(call => call.path);
      assert.ok(detailReads.includes('/admin/raw-accounts/1'));
      assert.ok(detailReads.includes('/admin/raw-accounts/1/daily-usage'));
      assert.ok(!detailReads.includes('/admin/synced-accounts/1'));
      await page.getByRole('tab', { name: 'Overview', exact: true }).click();
      for (const [button, action] of [['Update Auth JSON', 'auth-json'], ['Deactivate', 'deactivate']]) {
        const [response] = await Promise.all([
          page.waitForResponse(res => new URL(res.url()).pathname === `/tk-register/api/admin/synced-accounts/1/${action}` && res.request().method() === 'GET'),
          page.getByRole('button', { name: button, exact: true }).click(),
        ]);
        assert.equal(response.status(), 200);
        await page.getByRole('dialog').getByRole('button', { name: 'Close dialog' }).click();
      }
    } else {
      assert.equal(await page.locator('.daily-summary strong').nth(1).innerText(),'—');
      await page.getByRole('heading',{name:'No model usage data for this period.'}).waitFor();
    }
  }
  historicalOnly = false;
  const rawCalls = calls.filter(call => call.path.startsWith('/admin/raw-accounts')).length;
  superAdmin = false; await page.reload();
  await page.getByRole('heading', {name:'Super admin access required'}).waitFor();
  assert.equal(await page.getByRole('button', {name:'Raw account',exact:true}).count(),0);
  assert.equal(calls.filter(call => call.path.startsWith('/admin/raw-accounts')).length, rawCalls);
  await goto('config/overview');
  await page.getByRole('tab', {name:/Email delivery/}).waitFor();
  assert.equal(await page.getByRole('tab', {name:/Overview|Daily usage/}).count(),0);
  await goto('config/users');
  await page.getByRole('heading',{name:'Users',exact:true}).waitFor();
  assert.equal(await page.getByRole('button',{name:'Add super admin',exact:true}).count(),0);
  await goto('accounts');
  await page.reload();
  await page.getByRole('heading', { name: 'Account summary', exact: true }).waitFor();
  await assertPrivate();
  await goto('account-pool/accounts');
  await page.locator('.overview-model-summary').waitFor();
  assert.equal(await page.locator('#daily-model-trigger').count(), 1);
  assert.equal(await page.getByRole('group', { name: 'Source breakdown' }).count(), 1);
  await trigger.click();
  assert.equal(await page.getByRole('dialog', { name: 'Usage dates' }).locator('input[type=date]').count(), 2);
  assert.deepEqual(unknown, []); assert.deepEqual(errors, []);
  console.log('Overview visibility acceptance passed: disabled personal admin scope, vertical period dropdown with current-item check, immediate selection and no redundant GET, keyboard/dismissal and responsive menu bounds, backend total chart and exact counters, account aggregate table, Config save and enabled restoration, Management details/custom dates preserved.');
} finally { await browser?.close(); server.kill('SIGTERM'); }
