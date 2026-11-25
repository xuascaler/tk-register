import { chromium, expect } from '@playwright/test';
import { mkdir } from 'node:fs/promises';

const base = process.env.TK_MEMBERS_BROWSER_URL || 'http://127.0.0.1:39165';
if (!/^http:\/\/127\.0\.0\.1:\d+$/.test(base)) throw new Error('Use the isolated loopback members fixture.');
const shots = process.env.TK_SCREENSHOT_DIR || 'test-results/member-unlink';
await mkdir(shots, { recursive: true });
const browser = await chromium.launch({ executablePath: process.env.CHROME_PATH || (process.platform === 'darwin' ? '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome' : undefined), headless: true });
const context = await browser.newContext({ viewport: { width: 1440, height: 1000 }, locale: 'en-US' });
const page = await context.newPage();
const errors = [];
page.on('pageerror', error => errors.push(error.message));
let removeRequests = 0;
page.on('request', request => { if (request.url().endsWith('/members/unlink')) removeRequests++; });
const membersPage = async () => { await page.goto(base + '/tk-register#/account/members'); await expect(page.getByRole('heading', { name: 'Members', exact: true })).toBeVisible(); };
const open = async () => { await page.getByRole('button', { name: 'Del sub account', exact: true }).click(); const dialog = page.getByRole('dialog', { name: 'Del sub account' }); await expect(dialog).toBeVisible(); return dialog; };
const select = async (dialog, email) => { await dialog.getByRole('radio', { name: new RegExp(email.replaceAll('.', '\\.')) }).click(); await expect(dialog.getByRole('button', { name: 'Confirm removal', exact: true })).toBeFocused(); };

try {
  await page.goto(base + '/__test/login');
  await membersPage();
  await expect(page.locator('tbody tr')).toHaveCount(2);
  await expect(page.getByRole('button', { name: 'Add sub account', exact: true })).toBeVisible();
  await page.screenshot({ path: shots + '/toolbar.png', fullPage: true });
  let dialog = await open();
  await expect(dialog.getByRole('button', { name: 'Confirm removal', exact: true })).toBeDisabled();
  await expect(dialog.getByRole('radio')).toHaveCount(2);
  await dialog.getByRole('searchbox').fill('absent');
  await expect(dialog.getByText('No matching members', { exact: true })).toBeVisible();
  await dialog.getByRole('searchbox').fill('ben');
  await expect(dialog.getByRole('radio')).toHaveCount(1);
  await select(dialog, 'ben@example.com');
  await expect(dialog.getByText('This user can still sign in. Their accounts, data, and member relationships will be kept.', { exact: true })).toBeVisible();
  await page.screenshot({ path: shots + '/confirm-en.png', fullPage: true });
  await dialog.getByRole('button', { name: 'Cancel', exact: true }).click();
  if (removeRequests !== 0) throw new Error('Cancel sent a mutation');

  // Simulate a concurrent relationship change, then recover through the UI.
  await page.route('**/members/unlink', route => route.fulfill({ status: 409, contentType: 'application/json', body: JSON.stringify({ success: false, code: 'MEMBER_RELATION_CHANGED', message: 'This membership has changed. Refresh the member list and try again.' }) }));
  dialog = await open(); await select(dialog, 'ben@example.com');
  await dialog.getByRole('button', { name: 'Confirm removal', exact: true }).click();
  await expect(dialog.getByText('This membership has changed. Refresh the member list and try again.', { exact: true })).toBeVisible();
  await dialog.getByRole('button', { name: 'Refresh members', exact: true }).click();
  await expect(dialog.getByRole('radio')).toHaveCount(2);
  await dialog.getByRole('button', { name: 'Cancel', exact: true }).click();
  await page.unroute('**/members/unlink');

  // A saved selected branch is reset after removal.
  await page.goto(base + '/tk-register#/accounts?tab=list');
  await expect(page.locator('tbody tr')).toHaveCount(4);
  await page.locator('#account-source').click();
  const source = page.getByRole('dialog', { name: 'Source', exact: true });
  await source.getByRole('checkbox', { name: 'Select all', exact: true }).uncheck();
  await source.getByRole('checkbox', { name: 'ben@example.com', exact: true }).check();
  await source.getByRole('button', { name: 'Done', exact: true }).click();
  await expect(page.locator('tbody tr')).toHaveCount(2);
  await membersPage(); dialog = await open(); await select(dialog, 'ben@example.com');
  let release;
  const gate = new Promise(resolve => { release = resolve; });
  await page.route('**/members/unlink', async route => { await gate; await route.continue(); });
  await dialog.getByRole('button', { name: 'Confirm removal', exact: true }).click();
  await expect(dialog.getByRole('button', { name: 'Removing…', exact: true })).toBeDisabled();
  await expect(dialog.getByRole('button', { name: 'Cancel', exact: true })).toBeDisabled();
  await page.keyboard.press('Escape'); await expect(dialog).toBeVisible();
  release();
  await expect(dialog).toHaveCount(0);
  await page.unroute('**/members/unlink');
  if (removeRequests !== 2) throw new Error('Duplicate unlink request');
  await expect(page.locator('tbody tr')).toHaveCount(1);
  await expect(page.getByRole('status')).toContainText('ben@example.com was removed');
  await page.goto(base + '/tk-register#/accounts?tab=list');
  await expect(page.locator('tbody tr')).toHaveCount(2);
  await expect(page.locator('tbody')).not.toContainText('nora.chen@example.com');
  await expect(page.locator('tbody')).not.toContainText('oliver.lin@example.com');
  await page.locator('#account-source').click();
  await expect(source.getByRole('checkbox', { name: 'ben@example.com', exact: true })).toHaveCount(0);
  await source.getByRole('button', { name: 'Done', exact: true }).click();
  await membersPage();
  await page.getByRole('button', { name: 'Add sub account', exact: true }).click();
  const add = page.getByRole('dialog', { name: 'Add sub account', exact: true });
  await add.getByLabel('User email', { exact: true }).fill('ben@example.com');
  await add.getByRole('button', { name: 'Send code', exact: true }).click();
  await expect(add.getByText('Code sent. Ask the user to confirm the connection and share the code.', { exact: true })).toBeVisible();
  const code = await (await context.request.get(base + '/__test/code?email=ben@example.com&purpose=link')).json();
  await add.getByLabel('Verification code', { exact: true }).fill(code.data.code);
  await add.getByRole('button', { name: 'Add sub account', exact: true }).click();
  await expect(add).toHaveCount(0);
  await page.goto(base + '/tk-register#/accounts?tab=list');
  await expect(page.locator('tbody tr')).toHaveCount(4);

  await membersPage();
  for (const width of [390, 320]) {
    await page.setViewportSize({ width, height: 844 });
    dialog = await open(); await select(dialog, 'ben@example.com');
    await page.screenshot({ path: `${shots}/confirm-en-${width}.png`, fullPage: true });
    if (await page.evaluate(() => document.documentElement.scrollWidth > innerWidth)) throw new Error(`Page overflow at ${width}`);
    const bounds = await dialog.boundingBox();
    if (bounds.x < 0 || bounds.x + bounds.width > width) throw new Error(`Dialog overflow at ${width}`);
    await dialog.getByRole('button', { name: 'Cancel', exact: true }).click();
  }
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.locator('#site-language').click();
  await page.getByRole('option', { name: '简体中文', exact: true }).click();
  await page.getByRole('button', { name: '移除子账户', exact: true }).click();
  const zh = page.getByRole('dialog', { name: '移除子账户', exact: true });
  await zh.getByRole('radio', { name: /ben@example.com/ }).click();
  await expect(zh.getByText('该用户仍可正常登录，其账号、数据及与下级成员的关系均会保留。', { exact: true })).toBeVisible();
  await page.screenshot({ path: shots + '/confirm-zh.png', fullPage: true });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.screenshot({ path: shots + '/confirm-zh-mobile.png', fullPage: true });
  await zh.getByRole('button', { name: '取消', exact: true }).click();
  // Removing the last direct members leaves a disabled entry and the existing empty state.
  for (const email of ['ben@example.com', 'clara@example.com']) {
    await page.getByRole('button', { name: '移除子账户', exact: true }).click();
    await zh.getByRole('radio', { name: new RegExp(email.replaceAll('.', '\\.')) }).click();
    await zh.getByRole('button', { name: '确认移除', exact: true }).click();
    await expect(zh).toHaveCount(0);
  }
  await expect(page.getByRole('button', { name: '移除子账户', exact: true })).toBeDisabled();
  await expect(page.locator('tbody tr')).toHaveCount(0);
  await page.screenshot({ path: shots + '/empty-zh-mobile.png', fullPage: true });
  if (errors.length) throw new Error(errors.join('\n'));
  console.log('Passed: remove/re-add subtree, stale-dialog refresh, cancel, duplicate protection, source refresh, bilingual desktop and mobile.');
} catch (error) {
  await page.screenshot({ path: shots + '/failure.png', fullPage: true });
  console.error(await page.locator('body').innerText());
  throw error;
} finally { await browser.close(); }
