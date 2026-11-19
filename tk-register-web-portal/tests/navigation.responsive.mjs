import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { access, mkdir, readFile, writeFile } from 'node:fs/promises';
import { dirname, extname, resolve, sep } from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium, expect } from '@playwright/test';

// Exercise the built CSS and JavaScript together. All account/API data below is
// synthetic; the browser may only contact this temporary loopback server.
const project = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const dist = resolve(project, 'dist');
const output = resolve(project, 'test-results/navigation');
const viewports = [
  [320, 568], [360, 800], [375, 812], [390, 844], [393, 852],
  [430, 932], [580, 900], [581, 900], [600, 900], [768, 1024],
  [820, 1180], [821, 900], [844, 390], [932, 430], [1024, 768],
  [1280, 800], [1440, 900], [1920, 1080],
];
const crossings = [
  [[393, 852], [844, 390]],
  [[768, 1024], [1024, 768]],
  [[820, 1180], [821, 900]],
];
const mime = {
  '.html': 'text/html; charset=utf-8', '.js': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8', '.svg': 'image/svg+xml',
  '.png': 'image/png', '.ico': 'image/x-icon', '.woff2': 'font/woff2',
};

await access(resolve(dist, 'index.html')).catch(() => {
  throw new Error('Build the portal first: npm run build (dist/index.html is missing).');
});
await mkdir(output, { recursive: true });
const server = createServer(async (req, res) => {
  try {
    const pathname = decodeURIComponent(new URL(req.url, 'http://localhost').pathname);
    if (pathname.startsWith('/tk-register/api/')) {
      res.writeHead(500, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ success: false, message: 'An API mock was missed.' }));
      return;
    }
    const relative = pathname === '/tk-register' || pathname === '/tk-register/'
      ? 'index.html' : pathname.replace(/^\/tk-register\//, '');
    const path = resolve(dist, relative);
    if (!pathname.startsWith('/tk-register') || !path.startsWith(dist + sep)) {
      res.writeHead(404).end();
      return;
    }
    const data = await readFile(path);
    res.writeHead(200, { 'Content-Type': mime[extname(path)] || 'application/octet-stream', 'Cache-Control': 'no-store' });
    res.end(data);
  } catch {
    res.writeHead(404).end();
  }
});
await new Promise((resolveListening, reject) => {
  server.once('error', reject);
  server.listen(0, '127.0.0.1', resolveListening);
});
const origin = `http://127.0.0.1:${server.address().port}`;
const results = [];
let browser;
let activePage;
let scenario = 'startup';

function viewport([width, height]) { return { width, height }; }

async function mockAPI(page, role, unexpected) {
  const user = { id: role === 'admin' ? 1 : 2, email: `${role}@example.test`, role, status: 'active', created_at: '2026-11-19T00:00:00Z' };
  await page.route('**/*', async route => {
    const url = new URL(route.request().url());
    if (url.origin !== origin) {
      unexpected.push(`External request: ${url.origin}${url.pathname}`);
      await route.abort();
      return;
    }
    if (!url.pathname.startsWith('/tk-register/api/')) return route.continue();
    const path = url.pathname.slice('/tk-register/api'.length);
    const method = route.request().method();
    const ok = data => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ success: true, data }) });
    if (method === 'GET' && path === '/auth/status') return ok({
      registration_enabled: true, password_login_enabled: true, email_code_login_enabled: true,
      email_send_cooldown_seconds: 60, registration_code_ttl_seconds: 600,
      login_code_ttl_seconds: 600, code_length: 6, email_max_length: 50,
      password_min_length: 8, password_max_length: 128,
    });
    if (method === 'GET' && path === '/auth/me') return ok({ user, csrf_token: 'synthetic-navigation-test' });
    if (method === 'GET' && path === '/members/sources') return ok({ sources: [{ id: 'myself', label: 'myself' }] });
    if (method === 'GET' && path === '/accounts') return ok({ items: [], page: 1, page_size: 20, total: 0 });
    unexpected.push(`${method} ${path}`);
    return route.fulfill({ status: 500, contentType: 'application/json', body: JSON.stringify({ success: false, message: `Unexpected mock endpoint: ${method} ${path}` }) });
  });
}

async function load(page, size) {
  await page.setViewportSize(viewport(size));
  await page.goto(`${origin}/tk-register/#/accounts?tab=list`);
  await expect(page.getByRole('heading', { name: 'Accounts', exact: true })).toBeVisible();
  await expect(page.getByRole('tab', { name: 'Account list', exact: true })).toHaveAttribute('aria-selected', 'true');
  await expect(page.locator('.usage-empty')).toBeVisible();
  await page.evaluate(() => document.fonts.ready);
}

async function expandManagement(page, role) {
  if (role !== 'admin') return;
  const branch = page.locator('.admin-pool-toggle');
  if (await branch.getAttribute('aria-expanded') !== 'true') await branch.click();
  await expect(page.locator('#account-pool-nav')).toBeVisible();
}

async function closed(page, oldOverflow) {
  await expect(page.locator('.sidebar')).not.toHaveClass(/is-open/);
  await expect(page.locator('#main-content')).not.toHaveAttribute('inert');
  await expect(page.locator('.sidebar')).not.toHaveAttribute('aria-modal');
  await expect(page.locator('.sidebar')).not.toHaveAttribute('role', 'dialog');
  await expect(page.locator('.header .menu-button')).toHaveAttribute('aria-expanded', 'false');
  await expect(page.locator('.sidebar-backdrop')).toHaveCount(0);
  await expect.poll(() => page.evaluate(() => document.body.style.overflow)).toBe(oldOverflow);
  await page.getByRole('tab', { name: 'Overview', exact: true }).click({ trial: true });
}

async function open(page) {
  await page.getByRole('button', { name: 'Open navigation', exact: true }).click();
  await expect(page.locator('.sidebar')).toHaveClass(/is-open/);
  await expect(page.locator('.sidebar')).toHaveAttribute('aria-modal', 'true');
  await expect(page.locator('#main-content')).toHaveAttribute('inert');
  await expect.poll(() => page.evaluate(() => document.body.style.overflow)).toBe('hidden');
}

async function geometry(page, role, isDrawer) {
  const side = page.locator('.sidebar');
  await side.evaluate(element => { element.scrollTop = 0; });
  const state = await side.evaluate(element => {
    const style = getComputedStyle(element);
    const box = element.getBoundingClientRect();
    const heading = element.querySelector('.drawer-heading').getBoundingClientRect();
    const links = [...element.querySelectorAll('.nav-item')].map(button => {
      const r = button.getBoundingClientRect();
      return { text: button.textContent.trim(), left: r.left, right: r.right, width: r.width };
    });
    const label = element.querySelector('.signout span');
    return {
      direction: style.flexDirection, align: style.alignItems,
      left: box.left, right: box.right, top: box.top, height: box.height,
      headingTop: heading.top, headingHeight: heading.height,
      scrollWidth: element.scrollWidth, clientWidth: element.clientWidth,
      signoutDisplay: getComputedStyle(label).display, links,
    };
  });
  assert.equal(state.direction, 'column', `${scenario}: sidebar must stack vertically`);
  assert.ok(state.scrollWidth <= state.clientWidth + 1, `${scenario}: drawer horizontal overflow ${JSON.stringify(state)}`);
  assert.equal(state.links.length, role === 'admin' ? 10 : 6, `${scenario}: expected all navigation items, including Sign out`);
  for (const link of state.links) {
    assert.ok(link.width > 0 && link.left >= state.left - 1 && link.right <= state.right + 1,
      `${scenario}: ${link.text} is clipped horizontally: ${JSON.stringify(link)}`);
  }
  assert.notEqual(state.signoutDisplay, 'none', `${scenario}: Sign out text must be shown`);
  if (isDrawer) {
    assert.equal(state.align, 'stretch', `${scenario}: drawer children must stretch`);
    assert.ok(state.headingHeight >= 40 && state.headingTop >= state.top && state.headingTop <= state.top + 32,
      `${scenario}: heading must remain near the top: ${JSON.stringify(state)}`);
  }
  return state;
}

async function signoutReachable(page, isDrawer) {
  const signout = page.locator('.sidebar .signout .nav-item');
  await signout.scrollIntoViewIfNeeded();
  await expect(signout.locator('span')).toBeVisible();
  const measurement = await signout.evaluate((button, drawerOpen) => {
    const rect = button.getBoundingClientRect();
    const drawer = button.closest('.sidebar').getBoundingClientRect();
    const bounds = {
      top: drawerOpen ? Math.max(0, drawer.top) : 0,
      bottom: drawerOpen ? Math.min(innerHeight, drawer.bottom) : innerHeight,
      left: drawerOpen ? Math.max(0, drawer.left) : 0,
      right: drawerOpen ? Math.min(innerWidth, drawer.right) : innerWidth,
    };
    return {
      visible: rect.top >= bounds.top - 1 && rect.bottom <= bounds.bottom + 1 && rect.left >= bounds.left - 1 && rect.right <= bounds.right + 1,
      button: rect.toJSON(), sidebar: drawer.toJSON(), viewport: { width: innerWidth, height: innerHeight }, bounds,
    };
  }, isDrawer);
  assert.ok(measurement.visible, `${scenario}: Sign out cannot be scrolled into view: ${JSON.stringify(measurement)}`);
  // A trial click verifies the button is reachable without signing out.
  await signout.click({ trial: true });
}

try {
  browser = await chromium.launch({
    headless: true,
    executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH || (process.platform === 'darwin' ? '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome' : undefined),
  });
  for (const role of ['member', 'admin']) {
    const context = await browser.newContext({ viewport: viewport([393, 852]) });
    const page = activePage = await context.newPage();
    const unexpected = [], errors = [];
    page.on('pageerror', error => errors.push(error.message));
    await mockAPI(page, role, unexpected);

    for (const size of viewports) {
      const [width, height] = size;
      scenario = `${role}-${width}x${height}`;
      await load(page, size);
      const drawer = width <= 820;
      if (drawer) await open(page);
      else {
        await closed(page, '');
        await expect(page.locator('.sidebar')).toBeVisible();
        await expect(page.getByRole('button', { name: 'Open navigation', exact: true })).toBeHidden();
      }
      await expandManagement(page, role);
      const state = await geometry(page, role, drawer);
      await page.screenshot({ path: resolve(output, `${scenario}.png`) });
      await signoutReachable(page, drawer);
      if (drawer) {
        await page.getByRole('button', { name: 'Close navigation', exact: true }).click();
        await closed(page, '');
      }
      results.push({ scenario, passed: true, ...state });
    }

    // All close paths must restore the previous inline overflow value, rather
    // than clearing a scroll lock owned by an existing modal.
    for (const [method, overflow] of [['button', 'clip'], ['escape', 'auto'], ['backdrop', 'hidden']]) {
      scenario = `${role}-close-${method}`;
      await load(page, [393, 852]);
      await page.evaluate(value => { document.body.style.overflow = value; }, overflow);
      await open(page);
      if (method === 'button') await page.getByRole('button', { name: 'Close navigation', exact: true }).click();
      if (method === 'escape') await page.keyboard.press('Escape');
      if (method === 'backdrop') await page.locator('.sidebar-backdrop').click({ position: { x: 381, y: 12 } });
      await closed(page, overflow);
      results.push({ scenario, passed: true });
    }

    for (const [narrow, wide] of crossings) {
      scenario = `${role}-resize-${narrow.join('x')}-to-${wide.join('x')}`;
      await load(page, narrow);
      await page.evaluate(() => { document.body.style.overflow = 'auto'; });
      await open(page);
      await expandManagement(page, role);
      // This control disappears on desktop; leaving focus here caused the
      // original resize bug to retain a hidden dialog/focus trap.
      await page.getByRole('button', { name: 'Close navigation', exact: true }).focus();
      await page.setViewportSize(viewport(wide));
      await closed(page, 'auto');
      await expect.poll(() => page.evaluate(() => {
        const focused = document.activeElement;
        return focused instanceof HTMLElement && focused.getClientRects().length > 0 && focused.getAttribute('aria-label') !== 'Close navigation';
      })).toBe(true);
      await page.screenshot({ path: resolve(output, `${scenario}.png`) });
      await page.setViewportSize(viewport(narrow));
      await closed(page, 'auto');
      await expect(page.locator('.sidebar')).toBeHidden();
      await open(page);
      await page.keyboard.press('Escape');
      await closed(page, 'auto');
      results.push({ scenario, passed: true });
    }
    assert.deepEqual(unexpected, [], `${role}: every API request must be mocked and all network traffic must stay local`);
    assert.deepEqual(errors, [], `${role}: browser runtime errors`);
    await context.close();
    activePage = undefined;
  }
  console.log(`Navigation responsive regression passed: ${results.length} scenarios (36 viewport checks plus close and resize coverage).`);
} catch (error) {
  results.push({ scenario, passed: false, error: error.message });
  if (activePage && !activePage.isClosed()) await activePage.screenshot({ path: resolve(output, 'failure.png') }).catch(() => {});
  throw error;
} finally {
  await writeFile(resolve(output, 'results.json'), JSON.stringify(results, null, 2) + '\n');
  await browser?.close();
  await new Promise(resolveClosed => server.close(resolveClosed));
}
