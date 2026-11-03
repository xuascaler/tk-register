import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { mkdir } from 'node:fs/promises';
import { chromium } from '@playwright/test';

const port = Number(process.env.TK_TEST_PORT || 4194);
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
  const errors = [], calls = [], operations = new Map();
  let hold = false, rejectNext = false, failNextOperation = false;
  const deletionRequestIDs = [];
  page.on('pageerror', error => errors.push(error.message));
  function account(id, status = 'active') {
    return { id, account: `account${id}@example.com`, user_id: '7', user_email: 'owner@example.com', plan: 'claude/cc-max-5x', vendor_id: '1', vendor_name: 'TK Register 0', current_submission_id: id === '3' ? null : id, latest_submission_id: id, submission_status: id === '3' ? 'failed' : 'accepted', remote_id: id === '3' ? null : `37${id}`, platform_account_id: null, last_success_at: '2026-11-04T00:00:00Z', last_attempt_at: null, last_error: '', snapshot: id === '3' ? null : { id, account_id: id, collected_at: '2026-11-04T00:00:00Z', account_status: status, cost_total_usd: 24.25, input_tokens_total: 100, output_tokens_total: 100, usage_5h_percent: 7, usage_7d_percent: 14 } };
  }
  let records = [account('1'), account('2', 'sync_failed'), account('3')];
  await page.route('**/tk-register/api/**', async route => {
    const req = route.request(), url = new URL(req.url()), path = url.pathname.replace('/tk-register/api', '');
    calls.push({ method: req.method(), path, query: url.search });
    const ok = (data, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify({ success: true, data }) });
    if (path === '/auth/status') return ok({ registration_enabled: true, password_login_enabled: true, email_code_login_enabled: true });
    if (path === '/auth/me') return ok({ user: { id: 1, email: 'admin@example.com', role: 'admin' }, csrf_token: 'fixture-csrf' });
    if (path === '/vendors') return ok({ vendors: [] });
    if (path === '/accounts' || path === '/admin/synced-accounts') {
      const filtered = records.filter(row => !url.searchParams.get('status') || row.snapshot?.account_status === url.searchParams.get('status'));
      const current = Number(url.searchParams.get('page') || 1), size = 2;
      return ok({ items: filtered.slice((current - 1) * size, current * size), total: filtered.length, page: current, page_size: size });
    }
    const target = path.match(/^\/(?:admin\/synced-accounts|accounts)\/(\d+)\/delete$/);
    if (target) {
      const id = target[1];
      if (req.method() === 'POST') {
        assert.equal(req.headers()['x-csrf-token'], 'fixture-csrf');
        const body = req.postDataJSON(); assert.match(body.request_id, /^[0-9a-f-]{36}$/);
        assert.deepEqual(Object.keys(body), ['request_id']);
        deletionRequestIDs.push(body.request_id);
        if (rejectNext) { rejectNext = false; return route.fulfill({ status: 409, contentType: 'application/json', body: JSON.stringify({ success: false, code: 'ACCOUNT_UNCONFIRMED', message: 'Confirm the submission result before deleting this account.' }) }); }
        const op = { operation_id: `op-${id}`, status: 'pending', id, requestID: body.request_id, reads: 0, fail: failNextOperation };
        failNextOperation = false;
        operations.set(op.operation_id, op);
        records = records.map(row => row.id === id ? { ...row, deletion_status: 'pending', deletion_operation_id: op.operation_id } : row);
        if (id === '2') return route.fulfill({ status: 503, contentType: 'application/json', body: JSON.stringify({ success: false, message: 'The request result could not be confirmed.' }) });
        return ok({ operation_id: op.operation_id, status: 'pending' }, 202);
      }
      const op = operations.get(url.searchParams.get('operation_id')) || [...operations.values()].find(item => item.requestID === url.searchParams.get('request_id'));
      assert.ok(op, 'Status check must identify its original operation'); assert.equal(op.id, id);
      op.reads++;
      if (op.fail) { op.status = 'failed'; records = records.map(row => row.id === id ? { ...row, deletion_status: 'failed' } : row); return ok({ operation_id: op.operation_id, status: 'failed', message: 'The account was retained. Please try again later.' }); }
      if (!hold && (id === '2' || op.reads > 1)) { op.status = 'completed'; records = records.filter(row => row.id !== id); }
      return ok({ operation_id: op.operation_id, status: op.status });
    }
    throw new Error(`Unexpected request ${req.method()} ${path}`);
  });
  await mkdir('test-results', { recursive: true });
  await page.goto(`http://127.0.0.1:${port}/tk-register/#/account-pool/accounts`);
  await page.getByRole('button', { name: 'account2@example.com', exact: true }).waitFor();
  assert.equal(await page.getByText('Upstream sync failed', { exact: true }).count(), 1);
  assert.equal(await page.locator('.usage-status.negative').textContent(), 'Upstream sync failed');
  assert.equal(await page.locator('.usage-accounts-table th').last().textContent(), 'Actions');
  assert.equal(calls.filter(call => call.method === 'POST').length, 0);
  for (const width of [1440, 1024, 390, 320]) {
    await page.setViewportSize({ width, height: 1000 });
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, `List overflow at ${width}`);
    await page.screenshot({ path: `test-results/accounts-delete-${width}.png`, fullPage: true });
  }
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.getByRole('combobox', { name: 'Account status' }).click();
  await page.getByRole('option', { name: 'Upstream sync failed', exact: true }).click();
  await page.getByText('1 account · Page 1 of 1', { exact: true }).waitFor();
  assert.ok(calls.some(call => call.query.includes('status=sync_failed')));
  await page.getByRole('combobox', { name: 'Account status' }).click();
  await page.getByRole('option', { name: 'All account statuses', exact: true }).click();
  await page.getByText('3 accounts · Page 1 of 2', { exact: true }).waitFor();
  await page.getByRole('button', { name: 'Next', exact: true }).click();
  await page.getByRole('button', { name: 'account3@example.com', exact: true }).waitFor();
  assert.equal(await page.getByRole('button', { name: 'Sync account3@example.com', exact: true }).isDisabled(), true);
  assert.equal(await page.getByRole('button', { name: 'Delete account3@example.com', exact: true }).isEnabled(), true);
  await page.getByRole('button', { name: 'Delete account3@example.com', exact: true }).click();
  const dialog = page.getByRole('dialog');
  await dialog.getByRole('heading', { name: 'Delete account?', exact: true }).waitFor();
  assert.match(await dialog.textContent(), /owner@example.com/);
  assert.match(await dialog.textContent(), /Submission and usage history will be retained/);
  assert.doesNotMatch(await dialog.textContent(), /2020|plan_start|plan_end|auth_json|upstream/i);
  await page.screenshot({ path: 'test-results/account-delete-confirmation.png' });
  await dialog.getByRole('button', { name: 'Cancel', exact: true }).click();
  assert.equal(calls.filter(call => call.method === 'POST').length, 0, 'Cancel must not submit deletion');
  await page.getByRole('button', { name: 'Delete account3@example.com', exact: true }).click();
  await dialog.getByRole('button', { name: 'Delete', exact: true }).evaluate(button => { button.click(); button.click(); });
  await dialog.getByRole('heading', { name: 'Deleting account', exact: true }).waitFor();
  assert.equal(await page.getByRole('button', { name: 'Delete account3@example.com', exact: true }).isDisabled(), true);
  await page.getByText('account3@example.com was removed. Submission and usage history have been retained.', { exact: true }).waitFor();
  await page.getByText('2 accounts · Page 1 of 1', { exact: true }).waitFor();
  assert.equal(calls.filter(call => call.method === 'POST').length, 1, 'Double click must create one request');
  assert.equal(await page.getByRole('button', { name: 'account3@example.com', exact: true }).count(), 0);

  // A lost POST response is checked by request_id; it must never send a second POST.
  await page.getByRole('button', { name: 'Delete account2@example.com', exact: true }).click();
  await dialog.getByRole('button', { name: 'Delete', exact: true }).click();
  await dialog.getByText('The request result could not be confirmed.', { exact: true }).waitFor();
  await dialog.getByRole('button', { name: 'Check status', exact: true }).click();
  await page.getByText('account2@example.com was removed. Submission and usage history have been retained.', { exact: true }).waitFor();
  assert.equal(calls.filter(call => call.method === 'POST' && call.path.endsWith('/2/delete')).length, 1);
  assert.ok(calls.some(call => call.method === 'GET' && call.path.endsWith('/2/delete') && call.query.includes('request_id=')));

  // Background processing survives closing and re-opening the progress dialog.
  hold = true;
  await page.getByRole('button', { name: 'Delete account1@example.com', exact: true }).click();
  await dialog.getByRole('button', { name: 'Delete', exact: true }).click();
  await dialog.getByText(/processing will continue/).waitFor();
  await dialog.getByRole('button', { name: 'Close', exact: true }).click();
  await page.getByRole('button', { name: 'View progress', exact: true }).waitFor();
  assert.equal(await page.getByRole('button', { name: 'Sync account1@example.com', exact: true }).isDisabled(), true);
  assert.equal(await page.getByRole('button', { name: 'Delete account1@example.com', exact: true }).isDisabled(), true);
  hold = false;
  await page.getByRole('button', { name: 'View progress', exact: true }).click();
  await page.getByRole('heading', { name: 'No accounts yet', exact: true }).waitFor();
  assert.equal(calls.filter(call => call.method === 'POST' && call.path.endsWith('/1/delete')).length, 1);
  assert.equal(calls.some(call => call.path.endsWith('/sync')), false, 'Deletion must not trigger a supplier Sync request');

  records = [account('4')];
  await page.goto(`http://127.0.0.1:${port}/tk-register/#/accounts`);
  await page.getByRole('button', { name: 'Delete account4@example.com', exact: true }).click();
  assert.doesNotMatch(await dialog.textContent(), /owner@example.com/);
  await page.setViewportSize({ width: 390, height: 844 });
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
  await page.screenshot({ path: 'test-results/account-delete-confirmation-mobile.png' });
  await dialog.getByRole('button', { name: 'Delete', exact: true }).click();
  await page.getByRole('heading', { name: 'No accounts yet', exact: true }).waitFor();
  assert.ok(calls.some(call => call.method === 'POST' && call.path === '/accounts/4/delete'));

  // Definite rejection is not reported as processing and never starts polling.
  records = [account('5')]; rejectNext = true;
  await page.reload();
  await page.getByRole('button', { name: 'Delete account5@example.com', exact: true }).click();
  await dialog.getByRole('button', { name: 'Delete', exact: true }).click();
  await dialog.getByRole('heading', { name: 'Account not removed', exact: true }).waitFor();
  assert.equal(await dialog.getByRole('button', { name: 'Check status', exact: true }).count(), 0);
  assert.equal(calls.some(call => call.method === 'GET' && call.path.endsWith('/5/delete')), false);
  await dialog.getByRole('button', { name: 'Close', exact: true }).click();

  // A terminal failed operation also requires a new explicit confirmation/UUID.
  failNextOperation = true;
  await page.getByRole('button', { name: 'Delete account5@example.com', exact: true }).click();
  await dialog.getByRole('button', { name: 'Delete', exact: true }).click();
  await dialog.getByRole('heading', { name: 'Account not removed', exact: true }).waitFor();
  assert.match(await dialog.textContent(), /account was retained/i);
  await dialog.getByRole('button', { name: 'Close', exact: true }).click();
  await page.getByRole('button', { name: 'Delete account5@example.com', exact: true }).click();
  await dialog.getByRole('heading', { name: 'Delete account?', exact: true }).waitFor();
  await dialog.getByRole('button', { name: 'Delete', exact: true }).click();
  await page.getByRole('heading', { name: 'No accounts yet', exact: true }).waitFor();
  assert.equal(new Set(deletionRequestIDs).size, deletionRequestIDs.length, 'Each explicit new attempt must have a fresh UUID');
  assert.deepEqual(errors, []);
  console.log('Deletion UI passed: admin/member scope, failed-only account, confirmation, status mapping, double-click protection, GET-only recovery/polling, retained pending state, pagination, no automatic Sync, and responsive layouts.');
} finally { await browser?.close(); server.kill('SIGTERM'); }
