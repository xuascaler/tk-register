import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { mkdir } from 'node:fs/promises';
import { chromium, expect } from '@playwright/test';

const port = Number(process.env.TK_TEST_PORT || 4199);
const server = spawn(process.execPath, ['node_modules/vite/bin/vite.js', '--host', '127.0.0.1', '--port', String(port), '--strictPort'], { stdio: 'pipe' });
let browser;
const proxy = 'http://fixture-user:fixture-proxy-secret@proxy.example:8080';
const check = { ok: true, ip: '203.0.113.99', checked_at: '2026-11-18T00:00:00Z', elapsed_ms: 428 };
const boundCheck = { ...check, ip: '203.0.113.24' };
const auth = { id_token: '', access_token: 'fixture-access-secret', refresh_token: 'fixture-refresh-secret', last_refresh: '2026-11-18T00:00:00Z', email: 'authorized@example.com', account_uuid: 'account-id', organization_uuid: 'org-id', organization_name: 'Fixture org', claude_device_ids: ['device-id'], type: 'claude', expired: '2027-11-18T00:00:00Z', proxy_url: proxy, verification_status: 'verified', disabled: false };
try {
  await new Promise((resolve, reject) => {
    server.stdout.on('data', chunk => { if (String(chunk).includes('Local:')) resolve(); });
    server.stderr.on('data', chunk => process.stderr.write(chunk));
    server.on('exit', code => reject(new Error(`Vite exited: ${code}`)));
    setTimeout(() => reject(new Error('Vite startup timed out')), 15000).unref();
  });
  browser = await chromium.launch({ headless: true, ...(process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH ? { executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH } : {}) });
  const context = await browser.newContext({ viewport: { width: 1280, height: 1000 } });
  const page = await context.newPage();
  const calls = [], errors = [], unexpected = [], failedTests = [], flows = new Map();
  let nextOutcome = { check }, heldTest, nextID = 0;
  page.on('pageerror', error => errors.push(error.message));
  page.on('requestfailed', request => { if (request.url().endsWith('/credential-flows/proxy-test')) failedTests.push(request.failure()); });
  page.on('dialog', dialog => dialog.type() === 'beforeunload' ? dialog.accept() : dialog.dismiss());
  await context.route('**/tk-register/api/**', async route => {
    const req = route.request(), path = new URL(req.url()).pathname.replace('/tk-register/api', '');
    const body = req.method() === 'POST' ? req.postDataJSON() : undefined;
    calls.push({ path, method: req.method(), body, csrf: req.headers()['x-csrf-token'] });
    const ok = data => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ success: true, data }) });
    if (path === '/auth/status') return ok({ registration_enabled: false, password_login_enabled: true, email_code_login_enabled: true });
    if (path === '/auth/me') return ok({ user: { id: 1, email: 'member@example.com', role: 'member' }, csrf_token: 'fixture-csrf' });
    if (path === '/members/sources') return ok({ sources: [{ id: 'myself', label: 'myself' }] });
    if (path === '/vendors') return ok({ vendors: [{ id: '1', name: 'TK Register 0', enabled: true, configured: true, timezone: 'Asia/Shanghai', protocols: [{ id: '2', version_id: '3', name: 'Push', operation: 'push', version: 1 }] }] });
    if (path === '/credential-flows/proxy-test') {
      const outcome = nextOutcome, held = heldTest;
      if (held) { heldTest = undefined; held.started(); await held.release; }
      try {
        if (outcome.code) return await route.fulfill({ status: outcome.status || 502, contentType: 'application/json', body: JSON.stringify({ success: false, code: outcome.code, message: `UNSAFE ${proxy}` }) });
        return await ok({ proxy_check: outcome.check });
      } catch (cause) { if (!req.failure()) throw cause; }
      return;
    }
    if (path === '/credential-flows/start') {
      const flow = { flow_id: `fixture-proxy-flow-${++nextID}`, auth_url: 'https://claude.ai/oauth/authorize?fixture=true', expires_at: new Date(Date.now() + 30 * 60 * 1000).toISOString(), status: 'awaiting_code', proxy_check: boundCheck, notes: [] };
      flows.set(flow.flow_id, flow); return ok(flow);
    }
    if (path === '/credential-flows/exchange') { const flow = flows.get(body.flow_id); flow.auth = auth; flow.status = 'ready'; return ok(flow); }
    if (path === '/credential-flows/submit') return ok({ submission: { id: '42', account: auth.email, status: 'accepted', platform_account_id: '482761', parameters: {} }, executions: [], metrics: null, bills: null });
    const restored = path.match(/^\/credential-flows\/([^/]+)$/);
    if (restored) return ok(flows.get(restored[1]));
    unexpected.push(`${req.method()} ${path}`);
    return route.fulfill({ status: 404, contentType: 'application/json', body: JSON.stringify({ success: false, message: 'Unexpected fixture request' }) });
  });
  const count = suffix => calls.filter(call => call.path === `/credential-flows/${suffix}`).length;
  const holdNextTest = () => {
    let started, release;
    const didStart = new Promise(resolve => { started = resolve; });
    heldTest = { started, release: new Promise(resolve => { release = resolve; }) };
    return { started: didStart, release };
  };
  await page.goto(`http://127.0.0.1:${port}/tk-register/#/submit`);
  await page.getByRole('tab', { name: 'Authorize & submit', exact: true }).click();
  const panel = page.locator('#authorize-submit-panel');
  const proxyInput = panel.getByLabel('Account proxy', { exact: true });
  const email = panel.getByLabel('Account email', { exact: false });
  const test = panel.getByRole('button', { name: 'Test connection', exact: true });
  const status = panel.locator('#auto-proxy-status');
  const progress = panel.locator('[aria-current="step"]');
  const generate = panel.getByRole('button', { name: 'Generate authorization link', exact: true });
  await expect(generate).toBeEnabled();

  // Local validation must not start an API call or an authorization.
  await test.click(); await expect(status).toContainText('Enter the account proxy.');
  assert.equal(count('proxy-test'), 0);
  await proxyInput.fill(`  ${proxy}  `);
  const pending = holdNextTest();
  await test.click(); await pending.started;
  await expect(panel.getByRole('button', { name: 'Testing…', exact: true })).toBeDisabled();
  await expect(generate).toBeDisabled(); await expect(proxyInput).toBeEnabled();
  await expect(progress).toHaveText('1Check proxy');
  await email.fill('declared@example.com');
  await expect(status).toHaveText('Checking proxy connection…');
  pending.release();
  await expect(status).toHaveText('Connected · Exit IP 203.0.113.99 · 428 ms');
  assert.deepEqual(calls.find(call => call.path.endsWith('/proxy-test')).body, { proxy_url: proxy });
  assert.equal(calls.find(call => call.path.endsWith('/proxy-test')).csrf, 'fixture-csrf');
  for (const suffix of ['start', 'exchange', 'import', 'submit', 'export']) assert.equal(count(suffix), 0);
  await email.fill('another@example.com');
  await expect(status).toHaveText('Connected · Exit IP 203.0.113.99 · 428 ms');
  await expect(progress).toHaveText('1Check proxy');
  await panel.locator('.authorize-settings > summary').click();
  await expect(panel.getByLabel('Submission password', { exact: true })).toHaveValue('');
  await panel.locator('.authorize-settings > summary').click();
  assert.equal(await page.evaluate(() => sessionStorage.getItem('tk-register:credential-flow:1')), null);

  // Proxy input, reveal control and outlined test stay in one row; Email fills
  // the next row at every supported width, including 320px.
  await mkdir('test-results/proxy-connection', { recursive: true });
  for (const width of [1280, 390, 320]) {
    await page.setViewportSize({ width, height: 1000 });
    await page.evaluate(() => { window.scrollTo({ top: 0, behavior: 'instant' }); return new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))); });
    const layout = await panel.locator('.authorize-account-fields').evaluate(fields => {
      const rect = selector => { const box = fields.querySelector(selector).getBoundingClientRect(); return { x: box.x, y: box.y, width: box.width, right: box.right, bottom: box.bottom }; };
      return { proxy: rect('.password-field input'), eye: rect('.eye-button'), test: rect('.authorize-proxy-test'), email: rect('input[type="email"]'), fields: fields.getBoundingClientRect().width, overflow: document.documentElement.scrollWidth > innerWidth };
    });
    assert.equal(layout.overflow, false, `Document overflows at ${width}px`);
    assert.ok(Math.abs(layout.proxy.y - layout.test.y) <= 1, `Proxy and test must align at ${width}px`);
    assert.ok(layout.proxy.right < layout.test.x && layout.test.right <= width, `Test must stay to the right at ${width}px`);
    assert.ok(layout.eye.x >= layout.proxy.x && layout.eye.right <= layout.proxy.right, `Reveal control must stay inside the proxy input at ${width}px`);
    assert.ok(layout.email.y > layout.test.bottom, `Email must be on the next row at ${width}px`);
    assert.ok(Math.abs(layout.email.width - layout.fields) <= 1, `Email must fill its row at ${width}px`);
    await page.screenshot({ path: `test-results/proxy-connection/stacked-${width}.png`, fullPage: true });
  }
  await page.setViewportSize({ width: 1280, height: 1000 });
  await panel.getByRole('button', { name: 'Show account proxy', exact: true }).click();
  await expect(proxyInput).toHaveAttribute('type', 'text');
  await panel.getByRole('button', { name: 'Hide account proxy', exact: true }).click();

  // Changing the input invalidates a success and aborts a pending request;
  // completing its old server response cannot render a stale result.
  await proxyInput.fill(`${proxy}/`); await expect(status).toHaveText('Check the proxy before authorization.');
  const stale = holdNextTest(), failuresBefore = failedTests.length;
  await test.click(); await stale.started;
  await proxyInput.fill(proxy);
  await expect(status).toHaveText('Check the proxy before authorization.'); await expect(generate).toBeEnabled();
  await expect.poll(() => failedTests.length).toBeGreaterThan(failuresBefore);
  stale.release(); await page.waitForTimeout(120);
  await expect(status).toHaveText('Check the proxy before authorization.');

  // Fixed safe explanations never echo the credential-bearing upstream text.
  for (const [code, message] of [['PROXY_AUTHENTICATION_FAILED', 'Proxy authentication failed.'], ['PROXY_TEST_TIMEOUT', 'Connection timed out.'], ['PROXY_TEST_UNAVAILABLE', 'temporarily unavailable']]) {
    nextOutcome = { code }; await test.click(); await expect(status).toContainText(message);
    assert.equal((await status.textContent()).includes('fixture-proxy-secret'), false);
    assert.equal((await status.textContent()).includes('UNSAFE'), false);
  }
  nextOutcome = { check: { ...check, elapsed_ms: 0 } }; await test.click();
  await expect(status).toContainText('· 0 ms');
  await email.fill('');

  // The standalone result does not bind an exit IP or replace the real check
  // performed while creating a flow.
  await generate.click(); await expect(status).toHaveText('Exit IP bound · 203.0.113.24');
  assert.equal(count('start'), 1); await expect(progress).toHaveText('2Authorize');
  await page.reload(); await page.getByRole('tab', { name: 'Authorize & submit', exact: true }).click();
  await expect(panel.getByLabel('Authorization code or callback URL', { exact: true })).toBeVisible();
  await expect(proxyInput).toHaveValue('');
  nextOutcome = { check: boundCheck }; await test.click();
  await expect(status).toHaveText('Connected · Exit IP 203.0.113.24 · 428 ms');
  assert.deepEqual(calls.filter(call => call.path.endsWith('/proxy-test')).at(-1).body, { flow_id: 'fixture-proxy-flow-1' });
  assert.equal(await panel.getByText('Exit IP bound · 203.0.113.24', { exact: true }).count(), 0, 'Only one status slot should show the current test result');
  await expect(progress).toHaveText('2Authorize');
  assert.equal(count('exchange'), 0); assert.equal(count('submit'), 0);
  const storage = await page.evaluate(() => JSON.stringify({ local: { ...localStorage }, session: { ...sessionStorage } }));
  for (const secret of ['fixture-proxy-secret', 'fixture-access-secret', 'fixture-refresh-secret', '203.0.113.99']) assert.equal(storage.includes(secret), false);

  nextOutcome = { code: 'PROXY_CHANGED', status: 409 }; await test.click();
  await expect(status).toContainText('Start a new authorization before continuing.');
  await expect(panel.getByRole('button', { name: 'Authorize & submit', exact: true })).toBeDisabled();
  await expect(progress).toHaveText('2Authorize');
  await status.getByRole('button', { name: 'Start a new authorization', exact: true }).click();
  await expect(generate).toBeEnabled(); await expect(proxyInput).toBeEnabled();
  await expect(progress).toHaveText('1Check proxy');

  // An explicitly new authorization keeps its own full checks and normal
  // exchange/submit behavior; a completed account cannot be tested again.
  await proxyInput.fill(proxy); await generate.click();
  await panel.getByLabel('Authorization code or callback URL', { exact: true }).fill('fixture-code');
  await panel.getByRole('button', { name: 'Authorize & submit', exact: true }).click();
  await expect(panel.getByRole('heading', { name: 'Account submitted', exact: true })).toBeVisible();
  await expect(test).toBeDisabled();
  assert.equal(count('start'), 2); assert.equal(count('exchange'), 1); assert.equal(count('submit'), 1);
  assert.deepEqual(errors, []); assert.deepEqual(unexpected, []);
  console.log('Proxy connection browser integration passed: standalone/flow-only request bodies, loading and concurrency guard, email independence, input-edit abort/stale result protection, safe failures/retry, zero latency, no flow advancement or secret storage, restored proxy testing, changed-IP reauthorization, completed guard, and stacked rows with inline controls at 1280/390/320px.');
  await context.close();
} finally { await browser?.close(); server.kill('SIGTERM'); }
