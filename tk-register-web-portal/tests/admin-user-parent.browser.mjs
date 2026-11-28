import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { mkdir, readFile } from 'node:fs/promises';
import { dirname, extname, resolve, sep } from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium, expect } from '@playwright/test';

// Exercise built UI with synthetic responses only. No vendor or live TK traffic.
const project = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const dist = resolve(project, 'dist');
const output = resolve(project, 'test-results/admin-user-parent');
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
  const page = await browser.newPage({ viewport: { width: 1440, height: 1100 } });
  const unexpected = [], errors = [], posts = [], gets = [];
  let superAdmin = true, conflict = false, gate;
  const parent = { id: '95', email: 'zhou.fang@example.com' };
  const makeUser = (id, role = 'member', root = false) => ({ id, email: `user${id}@example.com`, role, status: 'active', is_super_admin: role === 'admin', created_at: '2026-11-28T07:43:00Z', parent: root ? null : parent, membership_version: '9007199254740993' });
  let users = [makeUser('131'), makeUser('130'), makeUser('129', 'member', true), makeUser('128', 'admin')];
  page.on('pageerror', e => errors.push(e.message));
  await page.route('**/*', async route => {
    const req = route.request(), url = new URL(req.url());
    if (url.origin !== origin) { unexpected.push(`External request ${url.origin}`); return route.abort(); }
    if (!url.pathname.startsWith('/tk-register/api/')) return route.continue();
    const path = url.pathname.slice('/tk-register/api'.length);
    const ok = data => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ success: true, data }) });
    if (path === '/auth/me') return ok({ user: { id: 1, email: 'admin@example.com', role: 'admin', is_super_admin: superAdmin }, csrf_token: 'test-csrf' });
    if (path === '/auth/status') return ok({ registration_enabled: true });
    if (path === '/members/sources') return ok({ sources: [{ id: 'myself', label: 'myself' }] });
    if (path === '/admin/settings') return ok({ settings: [] });
    if (path === '/admin/users') { gets.push(Object.fromEntries(url.searchParams)); return ok({ items: users, page: Number(url.searchParams.get('page') || 1), page_size: 20, total: 44 }); }
    if (path === '/admin/users/unlink-parent') {
      const body = req.postDataJSON(); posts.push(body);
      assert.equal(req.headers()['x-csrf-token'], 'test-csrf');
      if (gate) await gate;
      if (conflict) return route.fulfill({ status: 409, contentType: 'application/json', body: JSON.stringify({ success: false, code: 'USER_PARENT_CHANGED', message: 'This user’s direct parent has changed. Refresh the list and try again.' }) });
      const user = { ...users.find(u => u.id === body.user_id), parent: null, membership_version: '9007199254740994' };
      users = users.map(u => u.id === user.id ? user : u);
      return ok({ user });
    }
    unexpected.push(`${req.method()} ${path}`); return route.abort();
  });
  const loaded = async () => { await expect(page.locator('.admin-users-table tbody tr')).toHaveCount(4); await expect(page.locator('.admin-table-panel')).toHaveAttribute('aria-busy', 'false'); };
  const row = id => page.locator('.admin-users-table tbody tr').filter({ hasText: `user${id}@example.com` });
  await page.goto(`${origin}/tk-register/#/config/users`); await loaded();
  await expect(page.getByRole('button', { name: 'Add super admin', exact: true })).toBeVisible();
  await expect(row('129')).toContainText('No direct parent');
  await expect(row('129').getByRole('button')).toHaveCount(0);
  await row('131').getByRole('button', { name: 'Unlink', exact: true }).click();
  await expect(page.getByRole('dialog')).toContainText(parent.email);
  await page.getByRole('button', { name: 'Cancel', exact: true }).click();
  assert.equal(posts.length, 0);
  // Preserve pagination/search, submit once, and retain the full uint64 string.
  await page.locator('#users-search').fill('user');
  await expect.poll(() => gets.at(-1)?.search).toBe('user');
  await page.getByRole('button', { name: 'Next', exact: true }).click(); await loaded();
  assert.equal(gets.at(-1).page, '2');
  let release; gate = new Promise(resolve => { release = resolve; });
  await row('131').getByRole('button', { name: 'Unlink', exact: true }).click();
  await page.getByRole('button', { name: 'Confirm unlink', exact: true }).click();
  await expect(page.getByRole('button', { name: 'Unlinking…', exact: true })).toBeDisabled();
  await page.keyboard.press('Escape'); await expect(page.getByRole('dialog')).toBeVisible();
  release(); gate = null; await expect(page.getByRole('dialog')).toHaveCount(0); await loaded();
  assert.deepEqual(posts, [{ user_id: '131', parent_id: '95', membership_version: '9007199254740993' }]);
  await expect(row('131')).toContainText('No direct parent');
  assert.equal(gets.at(-1).page, '2'); assert.equal(gets.at(-1).search, 'user');
  // Stale relationship cannot be silently retried with a new parent.
  conflict = true;
  await row('130').getByRole('button', { name: 'Unlink', exact: true }).click();
  await page.getByRole('button', { name: 'Confirm unlink', exact: true }).click();
  await expect(page.getByRole('dialog')).toContainText('This user’s direct parent has changed.');
  await page.getByRole('button', { name: 'Refresh list', exact: true }).click(); await loaded();
  assert.equal(posts.length, 2); conflict = false;
  // Full application layout, actual icons, both languages, narrow desktop and phones.
  for (const language of ['en', 'zh']) {
    if (language === 'zh') {
      await page.getByRole('combobox', { name: 'Language', exact: true }).click();
      await page.getByRole('option', { name: '简体中文', exact: true }).click();
    }
    for (const width of [1920, 1440, 1280, 1100, 1024, 880, 736, 701, 700, 390, 320]) {
      await page.setViewportSize({ width, height: 1100 });
      const layout = await page.locator('.admin-users-table').evaluate(table => {
        const bounds = table.getBoundingClientRect();
        return { cards: getComputedStyle(table.querySelector('tbody tr')).display === 'grid', width: innerWidth, pageWidth: document.documentElement.scrollWidth, tableRight: bounds.right, actions: [...table.querySelectorAll('.admin-parent-unlink')].map(b => { const r=b.getBoundingClientRect(), c=b.closest('td').getBoundingClientRect(); return { left:r.left, right:r.right, cellLeft:c.left, cellRight:c.right }; }), cells: [...table.querySelectorAll('td')].map(c => ({ scroll:c.scrollWidth, client:c.clientWidth, text:c.textContent })) };
      });
      assert.ok(layout.pageWidth <= width+1, `Page overflow ${language} ${width}: ${JSON.stringify(layout)}`);
      for (const b of layout.actions) {
        assert.ok(b.left >= b.cellLeft-1 && b.right <= b.cellRight+1, `Button overflow ${language} ${width}: ${JSON.stringify(b)}`);
        assert.ok(layout.tableRight - b.right >= (layout.cards ? 17 : 23), `Missing right padding ${language} ${width}: ${JSON.stringify(layout)}`);
      }
      for (const c of layout.cells) assert.ok(c.scroll <= c.client+1, `Cell overflow ${language} ${width}: ${JSON.stringify(c)}`);
      if ([1440, 390].includes(width)) await page.locator('.admin-users').screenshot({ path: resolve(output, `${language}-${width}.png`) });
    }
    await row('130').getByRole('button').click();
    await expect(page.getByRole('dialog')).toBeVisible();
    const dialog = await page.getByRole('dialog').boundingBox(); assert.ok(dialog.x >= 0 && dialog.x+dialog.width <= 321);
    await page.getByRole('dialog').screenshot({ path: resolve(output, `${language}-dialog-320.png`) });
    await page.getByRole('button', { name: language === 'zh' ? '取消' : 'Cancel', exact: true }).click();
  }
  superAdmin = false; await page.reload(); await loaded();
  await expect(page.getByRole('button', { name: '添加超级管理员', exact: true })).toHaveCount(0);
  await expect(row('128').getByRole('button')).toBeDisabled();
  await expect(row('130').getByRole('button')).toBeEnabled();
  assert.deepEqual(unexpected, []); assert.deepEqual(errors, []);
  console.log('PASS: parent rows, cancel/success/conflict, versions, CSRF, single submission, preserved search/page, roles, bilingual desktop/mobile layout');
} finally {
  await browser?.close();
  await new Promise(resolve => server.close(resolve));
}
