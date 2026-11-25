import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { mkdir } from 'node:fs/promises';
import { chromium, expect } from '@playwright/test';

const port = Number(process.env.TK_TEST_PORT || 4198);
const server = spawn(process.execPath, ['node_modules/vite/bin/vite.js', '--host', '127.0.0.1', '--port', String(port), '--strictPort'], { stdio: 'pipe' });
let browser;
const auth = { id_token: '', access_token: 'fixture-access-secret', refresh_token: 'fixture-refresh-secret', last_refresh: '2026-11-18T00:00:00Z', email: 'authorized@example.com', account_uuid: 'account-id', organization_uuid: 'org-id', organization_name: 'Fixture org', claude_device_ids: ['device-id'], type: 'claude', expired: '2027-11-18T00:00:00Z', proxy_url: 'http://proxy-user:proxy-secret@proxy.example:8080', verification_status: 'verified', disabled: false };
const vendor = { id: '1', code: 'fixture', name: 'TK Register 0', enabled: true, configured: true, timezone: 'Asia/Shanghai', protocols: [{ id: '2', version_id: '3', code: 'push', name: 'Push', operation: 'push', version: 1 }] };
try {
  await new Promise((resolve, reject) => {
    server.stdout.on('data', chunk => { if (String(chunk).includes('Local:')) resolve(); });
    server.stderr.on('data', chunk => process.stderr.write(chunk));
    server.on('exit', code => reject(new Error(`Vite exited: ${code}`)));
    setTimeout(() => reject(new Error('Vite startup timed out')), 15000).unref();
  });
  browser = await chromium.launch({ headless: true, ...(process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH ? { executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH } : {}) });
  await mkdir('test-results/credential-flow', { recursive: true });
  async function fixture(scenario = 'normal') {
    const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
    const page = await context.newPage();
    const calls = [], errors = [], unexpected = [], flows = new Map(), flowProxies = new Map();
    let nextID = 0, submitCount = 0;
    page.on('pageerror', error => errors.push(error.message));
    page.on('dialog', dialog => dialog.type() === 'beforeunload' || (scenario === 'proxy-restart' && dialog.message().includes('discard these credentials')) ? dialog.accept() : dialog.dismiss());
    await context.route('**/tk-register/api/**', async route => {
      const req = route.request(), path = new URL(req.url()).pathname.replace('/tk-register/api', '');
      const body = req.method() === 'POST' ? req.postDataJSON() : undefined;
      calls.push({ path, method: req.method(), body });
      const ok = data => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ success: true, data }) });
      if (path === '/auth/status') return ok({ registration_enabled: false, password_login_enabled: true, email_code_login_enabled: true });
      if (path === '/auth/me') return ok({ user: { id: 1, email: 'member@example.com', role: 'member' }, csrf_token: 'fixture-csrf' });
      if (path === '/members/sources') return ok({ sources: [{ id: 'myself', label: 'myself' }] });
      if (path === '/vendors') return ok({ vendors: [vendor] });
      if (path === '/credential-flows/start' || path === '/credential-flows/import') {
        const id = `fixture-flow-${++nextID}`;
        const value = { flow_id: id, expires_at: new Date(Date.now() + 30 * 60 * 1000).toISOString(), status: 'awaiting_code', auth_url: 'https://claude.ai/oauth/authorize?fixture=true', proxy_check: { ok: true, ip: '203.0.113.24', checked_at: new Date().toISOString() }, notes: [] };
        if (path.endsWith('/import')) { value.status = 'ready'; value.auth = { ...auth, email: scenario === 'multi' ? body.document.accounts[body.account_index].email : auth.email }; }
        flows.set(id, value); flowProxies.set(id, body.proxy_url); return ok(value);
      }
      if (path === '/credential-flows/exchange') {
        const value = flows.get(body.flow_id);
        value.status = 'ready'; value.auth = { ...auth, proxy_url: flowProxies.get(body.flow_id), ...(['missing', 'partial-export'].includes(scenario) ? { refresh_token: '' } : {}) };
        if (scenario === 'partial-export') value.notes = ['The credential contains no refresh_token; it remains empty.'];
        return ok(value);
      }
      const draft = path.match(/^\/credential-flows\/([^/]+)\/draft$/);
      if (draft) { const value = flows.get(draft[1]); Object.assign(value.auth, body.auth_patch); return ok(value); }
      if (path === '/credential-flows/export') return ok(flows.get(body.flow_id));
      if (path === '/credential-flows/submit') {
        submitCount++;
        if (scenario === 'proxy-restart' && submitCount === 1) return route.fulfill({ status: 409, contentType: 'application/json', body: JSON.stringify({ success: false, code: 'PROXY_CHANGED', message: 'The proxy no longer matches this authorization. Start again with the intended proxy.' }) });
        if ((scenario === 'network-loss' || scenario === 'mismatch-network') && submitCount === 1) return route.abort('failed');
        const status = scenario === 'known-unknown' && submitCount === 1 ? 'unknown' : 'accepted';
        return ok({ submission: { id: String(42 + submitCount - 1), account: body.parameters.account, status, platform_account_id: '482761', remote_id: 'vendor-id', parameters: {} }, executions: [], metrics: null, bills: null });
      }
      const restored = path.match(/^\/credential-flows\/([^/]+)$/);
      if (restored) return ok(flows.get(restored[1]));
      unexpected.push(`${req.method()} ${path}`);
      return route.fulfill({ status: 404, contentType: 'application/json', body: JSON.stringify({ success: false, message: 'Unexpected fixture request' }) });
    });
    await page.goto(`http://127.0.0.1:${port}/tk-register/#/submit`);
    await page.getByRole('heading', { name: 'Submit account', exact: true }).waitFor();
    await page.getByRole('tab', { name: 'Authorize & submit', exact: true }).click();
    const panel = page.locator('#authorize-submit-panel');
    await expect(panel.getByRole('button', { name: 'Generate authorization link', exact: true })).toBeEnabled();
    const count = path => calls.filter(call => call.path === `/credential-flows/${path}`).length;
    const finish = async () => { assert.deepEqual(errors, []); assert.deepEqual(unexpected, []); await context.close(); };
    return { page, panel, context, calls, count, finish };
  }
  async function generate(f, declared = '') {
    await f.panel.getByLabel('Account proxy', { exact: true }).fill(auth.proxy_url);
    if (declared) await f.panel.getByLabel('Account email', { exact: false }).fill(declared);
    await f.panel.getByRole('button', { name: 'Generate authorization link', exact: true }).click();
    await expect(f.panel.getByText('Exit IP bound · 203.0.113.24', { exact: true })).toBeVisible();
    await expect(f.panel.getByLabel('Authorization code or callback URL', { exact: true })).toBeVisible();
  }
  async function run(f) {
    await f.panel.getByLabel('Authorization code or callback URL', { exact: true }).fill('fixture-code#fixture-state');
    await f.panel.getByRole('button', { name: 'Authorize & submit', exact: true }).click();
  }
  function noSecretStorage(values) {
    const storage = JSON.stringify(values);
    for (const secret of ['proxy-secret', 'fixture-access-secret', 'fixture-refresh-secret', 'authorized@example.com', 'fixture-code']) assert.equal(storage.includes(secret), false, `Storage leaked ${secret}`);
  }

  if (process.env.TK_CREDENTIAL_SCENARIO !== 'proxy-restart') {
  // The original manual form and the generated password survive tab changes.
  {
    const f = await fixture();
    await f.page.getByRole('tab', { name: 'Upload Auth JSON', exact: true }).click();
    await f.page.getByLabel('Account', { exact: true }).fill('manual-draft@example.com');
    await f.page.getByRole('tab', { name: 'Authorize & submit', exact: true }).click();
    await f.panel.getByLabel('Account email').fill('authorized@example.com');
    const requestCount = f.calls.length;
    await f.page.getByRole('combobox', { name: 'Language', exact: true }).click();
    await f.page.getByRole('option', { name: '简体中文', exact: true }).click();
    await expect(f.page.getByRole('tab', { name: '授权并提交', exact: true })).toHaveAttribute('aria-selected', 'true');
    await expect(f.panel.getByLabel('账号邮箱')).toHaveValue('authorized@example.com');
    await f.page.getByRole('combobox', { name: '语言', exact: true }).click();
    await f.page.getByRole('option', { name: 'English', exact: true }).click();
    await expect(f.panel.getByLabel('Account email')).toHaveValue('authorized@example.com');
    assert.equal(f.calls.length, requestCount, 'Switching language must not start or repeat any requests');
    await expect(f.panel.locator('.authorize-settings')).not.toHaveAttribute('open');
    await generate(f);
    await f.panel.locator('.authorize-settings > summary').click();
    await f.panel.getByRole('combobox', { name: 'Plan', exact: true }).click();
    await f.page.getByRole('option', { name: 'Claude Team Premium', exact: true }).click();
    await expect(f.panel.locator('.authorize-default-summary')).toContainText('Team Premium');
    const password = await f.panel.getByLabel('Submission password', { exact: true }).inputValue();
    assert.equal(password.length, 24);
    await f.page.getByRole('tab', { name: 'Upload Auth JSON', exact: true }).click();
    await expect(f.page.getByLabel('Account', { exact: true })).toHaveValue('manual-draft@example.com');
    await f.page.getByRole('tab', { name: 'Authorize & submit', exact: true }).click();
    await expect(f.panel.getByLabel('Submission password', { exact: true })).toHaveValue(password);
    const dirtyPrompt = f.page.waitForEvent('dialog');
    await f.page.getByRole('button', { name: 'Accounts', exact: true }).click();
    assert.equal((await dirtyPrompt).type(), 'confirm');
    assert.equal(new URL(f.page.url()).hash, '#/submit', 'Dismissing the unsaved draft prompt keeps both drafts');
    noSecretStorage(await f.page.evaluate(() => ({ local: { ...localStorage }, session: { ...sessionStorage } })));
    await f.panel.locator('.authorize-settings > summary').click();
    for (const width of [1280, 390, 320]) {
      await f.page.setViewportSize({ width, height: 1000 });
      assert.equal(await f.page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, `Authorization overflow at ${width}px`);
      await f.page.screenshot({ path: `test-results/credential-flow/oauth-${width}.png`, fullPage: true });
    }
    await f.page.setViewportSize({ width: 1440, height: 1000 });
    await run(f);
    await expect(f.panel.getByRole('heading', { name: 'Account submitted', exact: true })).toBeVisible();
    assert.equal(new URL(f.page.url()).hash, '#/submit', 'Success stays on the form');
    assert.equal(f.count('exchange'), 1); assert.equal(f.count('submit'), 1);
    const submitted = f.calls.find(call => call.path === '/credential-flows/submit').body;
    assert.equal(submitted.parameters.password, password);
    assert.equal(submitted.parameters.plan, 'claude/cc-Team Premium');
    assert.equal(submitted.parameters.account, auth.email);
    assert.equal(submitted.parameters.payment_method, 'physical_credit_card');
    assert.deepEqual(submitted.parameters.tags, ['速刷']);
    assert.equal(submitted.parameters.auth_json.verification_status, 'verified');
    const dates = [submitted.parameters.register_time, submitted.parameters.plan_start_time, submitted.parameters.plan_end_time];
    await f.panel.getByRole('button', { name: 'Process next account', exact: true }).click();
    await expect(f.panel.getByLabel('Account proxy', { exact: true })).toHaveValue('');
    await f.panel.locator('.authorize-settings > summary').click();
    await expect(f.panel.getByLabel('Submission password', { exact: true })).toHaveValue('');
    assert.deepEqual(await Promise.all(['Registration date', 'Plan start date', 'Plan end date'].map(label => f.panel.getByLabel(label, { exact: true }).inputValue())), dates);
    assert.equal(f.count('submit'), 1);
    await f.finish();
  }
  // Missing fields halt the chain until the missing-only draft patch is saved.
  {
    const f = await fixture('missing'); await generate(f); await run(f);
    await expect(f.panel.getByRole('heading', { name: 'Complete the missing credential fields', exact: true })).toBeVisible();
    assert.equal(f.count('submit'), 0);
    const editor = f.panel.getByLabel('Standard Auth JSON', { exact: true });
    await editor.fill(JSON.stringify(auth));
    await f.panel.getByRole('button', { name: 'Submit account', exact: true }).click();
    await expect(f.panel.getByRole('heading', { name: 'Account submitted', exact: true })).toBeVisible();
    const patches = f.calls.filter(call => call.path.endsWith('/draft'));
    assert.deepEqual(patches[0].body.auth_patch, { refresh_token: auth.refresh_token });
    assert.equal(f.count('exchange'), 1); assert.equal(f.count('submit'), 1); await f.finish();
  }
  // Email mismatch requires one confirmation; a lost submit response retries the
  // same key without another exchange or another email confirmation.
  {
    const f = await fixture('mismatch-network'); await generate(f, 'declared@example.com'); await run(f);
    await expect(f.panel.getByRole('heading', { name: 'Confirm the authorized account', exact: true })).toBeVisible();
    assert.equal(f.count('submit'), 0);
    await f.panel.getByRole('button', { name: 'Submit authorized account', exact: true }).click();
    await expect(f.panel.getByRole('button', { name: 'Retry submission', exact: true })).toBeVisible();
    await f.panel.getByRole('button', { name: 'Retry submission', exact: true }).click();
    await expect(f.panel.getByRole('heading', { name: 'Account submitted', exact: true })).toBeVisible();
    const attempts = f.calls.filter(call => call.path === '/credential-flows/submit');
    assert.equal(attempts.length, 2); assert.equal(attempts[0].body.client_request_key, attempts[1].body.client_request_key);
    assert.equal(attempts[0].body.parameters.password, attempts[1].body.parameters.password);
    assert.equal(f.count('exchange'), 1); await f.finish();
  }
  {
    const f = await fixture('known-unknown'); await generate(f); await run(f);
    await expect(f.panel.getByRole('button', { name: 'Retry submission', exact: true })).toBeVisible();
    await f.panel.getByRole('button', { name: 'Retry submission', exact: true }).click();
    await expect(f.panel.getByRole('heading', { name: 'Account submitted', exact: true })).toBeVisible();
    const attempts = f.calls.filter(call => call.path === '/credential-flows/submit');
    assert.notEqual(attempts[0].body.client_request_key, attempts[1].body.client_request_key);
    assert.equal(f.count('exchange'), 1); await f.finish();
  }
  // Restoring an awaiting-code flow uses its server-held proxy.
  {
    const f = await fixture(); await generate(f); await f.page.reload();
    await f.page.getByRole('tab', { name: 'Authorize & submit', exact: true }).click();
    await expect(f.panel.getByLabel('Authorization code or callback URL', { exact: true })).toBeVisible();
    await expect(f.panel.getByLabel('Account proxy', { exact: true })).toHaveValue('');
    await run(f); await expect(f.panel.getByRole('heading', { name: 'Account submitted', exact: true })).toBeVisible();
    assert.equal(f.count('start'), 1); assert.equal(f.count('exchange'), 1); assert.equal(f.count('submit'), 1); await f.finish();
  }
  // Setup Token's selected scope is forwarded, then export mode never submits.
  {
    const f = await fixture('partial-export');
    await f.panel.getByRole('combobox', { name: 'After authorization', exact: true }).click();
    await f.page.getByRole('option', { name: 'Generate JSON only', exact: true }).click();
    await f.panel.getByRole('tab', { name: 'Setup Token', exact: true }).click();
    await f.panel.getByRole('combobox', { name: 'Authorization type', exact: true }).click();
    await f.page.getByRole('option', { name: 'Inference only · Long-lived token', exact: true }).click();
    await generate(f);
    await f.panel.getByLabel('Authorization code or callback URL', { exact: true }).fill('fixture-code');
    await f.panel.getByRole('button', { name: 'Generate Auth JSON', exact: true }).click();
    await expect(f.panel.getByRole('heading', { name: 'Auth JSON is ready', exact: true })).toBeVisible();
    await expect(f.panel.getByText('The credential contains no refresh_token; it remains empty.', { exact: true })).toBeVisible();
    assert.equal(f.count('submit'), 0); assert.equal(f.count('export'), 1);
    const started = f.calls.find(call => call.path === '/credential-flows/start').body;
    assert.equal(started.mode, 'setup'); assert.equal(started.setup_kind, 'official');
    const downloadPromise = f.page.waitForEvent('download');
    await f.panel.getByRole('button', { name: 'Download auth.json', exact: true }).first().click();
    assert.equal((await downloadPromise).suggestedFilename(), 'auth.json');
    noSecretStorage(await f.page.evaluate(() => ({ local: { ...localStorage }, session: { ...sessionStorage } }))); await f.finish();
  }
  // Import JSON validates the file cap and forwards the selected account index.
  {
    const f = await fixture('multi');
    await f.panel.getByRole('tab', { name: 'Import JSON', exact: true }).click();
    await f.panel.getByLabel('Account proxy', { exact: true }).fill(auth.proxy_url);
    await f.panel.getByLabel('Upload source JSON file', { exact: true }).setInputFiles({ name: 'oversize.json', mimeType: 'application/json', buffer: Buffer.alloc(2 * 1024 * 1024 + 1, 32) });
    await expect(f.panel.getByRole('alert')).toContainText('2 MB'); assert.equal(f.count('import'), 0);
    await f.panel.getByLabel('Upload source JSON file', { exact: true }).setInputFiles({ name: 'accounts.json', mimeType: 'application/json', buffer: Buffer.from(JSON.stringify({ accounts: [{ email: 'one@example.com' }, { email: 'two@example.com' }] })) });
    await f.panel.getByRole('combobox', { name: 'Account to import', exact: true }).click();
    await f.page.getByRole('option', { name: '2 · two@example.com', exact: true }).click();
    await f.panel.getByRole('button', { name: 'Import & submit', exact: true }).click();
    await expect(f.panel.getByRole('heading', { name: 'Account submitted', exact: true })).toBeVisible();
    assert.equal(f.calls.find(call => call.path === '/credential-flows/import').body.account_index, 1);
    assert.equal(f.calls.find(call => call.path === '/credential-flows/submit').body.parameters.account, 'two@example.com'); await f.finish();
  }
  }
  // A changed proxy can recover through a deliberately new flow, keeping the
  // account settings and generated submission password while clearing credentials.
  {
    const f = await fixture('proxy-restart');
    const progress = f.panel.getByRole('list', { name: 'Account submission progress', exact: true });
    await expect(progress.getByRole('listitem')).toHaveCount(4);
    await expect(progress.locator('[aria-current="step"]')).toHaveText('1Check proxy');
    await generate(f);
    await expect(progress.locator('[aria-current="step"]')).toHaveText('2Authorize');
    for (const width of [1280, 390, 320]) {
      await f.page.setViewportSize({ width, height: 1000 });
      assert.equal(await f.page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, `Four-step progress overflow at ${width}px`);
      await f.page.screenshot({ path: `test-results/credential-flow/oauth-${width}.png`, fullPage: true });
    }
    await f.page.setViewportSize({ width: 1440, height: 1000 });
    await run(f);
    await expect(f.panel.getByRole('alert')).toContainText('proxy no longer matches');
    await expect(progress.locator('[aria-current="step"]')).toHaveText('3Submit account');
    await f.panel.locator('.authorize-settings > summary').click();
    const password = await f.panel.getByLabel('Submission password', { exact: true }).inputValue();
    const start = await f.panel.getByLabel('Plan start date', { exact: true }).inputValue();
    await f.panel.getByRole('button', { name: 'Start a new authorization', exact: true }).click();
    await expect(f.panel.getByLabel('Account proxy', { exact: true })).toBeEnabled();
    await expect(f.panel.getByLabel('Submission password', { exact: true })).toHaveValue(password);
    await expect(f.panel.getByLabel('Plan start date', { exact: true })).toHaveValue(start);
    assert.equal(await f.panel.getByLabel('Standard Auth JSON', { exact: true }).count(), 0);
    const replacement = 'http://another-user:another-secret@another-proxy.example:8080';
    await f.panel.getByLabel('Account proxy', { exact: true }).fill(replacement);
    await f.panel.getByRole('button', { name: 'Generate authorization link', exact: true }).click();
    await run(f);
    await expect(f.panel.getByRole('heading', { name: 'Account submitted', exact: true })).toBeVisible();
    await expect(progress.locator('[aria-current="step"]')).toHaveText('Result');
    assert.equal(f.count('start'), 2); assert.equal(f.count('exchange'), 2); assert.equal(f.count('submit'), 2);
    const attempts = f.calls.filter(call => call.path === '/credential-flows/submit');
    assert.notEqual(attempts[0].body.flow_id, attempts[1].body.flow_id);
    assert.equal(attempts[1].body.parameters.password, password);
    assert.equal(attempts[1].body.parameters.auth_json.proxy_url, replacement); await f.finish();
  }
  console.log(process.env.TK_CREDENTIAL_SCENARIO === 'proxy-restart' ? 'Credential flow focused browser integration passed: PROXY_CHANGED → new authorization, credentials cleared, settings/password retained, replacement proxy submitted.' : 'Credential flow browser integration passed: manual/tab draft retention, defaults and password, OAuth automatic submit, missing-field patch, email confirmation, ambiguous and known-result retry, server-held proxy restore, partial Setup Token export-only, upload cap/multi-account selection, secret-free storage, 1280/390/320px layout, proxy-change recovery.');
} finally { await browser?.close(); server.kill('SIGTERM'); }
