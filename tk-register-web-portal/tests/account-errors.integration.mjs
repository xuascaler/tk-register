import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { mkdir } from 'node:fs/promises';
import { chromium } from '@playwright/test';
const port = 4196;
const server = spawn(process.execPath, ['node_modules/vite/bin/vite.js', '--host', '127.0.0.1', '--port', String(port), '--strictPort'], { stdio: 'pipe' });
let browser;
try {
  await new Promise((resolve,reject)=>{server.stdout.on('data',b=>{if(String(b).includes('Local:'))resolve()});server.stderr.on('data',b=>process.stderr.write(b));server.on('exit',c=>reject(Error(`Vite exit ${c}`)));setTimeout(()=>reject(Error('Vite timeout')),15000).unref()});
  browser=await chromium.launch({headless:true,...(process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH?{executablePath:process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH}:{})});
  const page=await browser.newPage({viewport:{width:1600,height:1000},permissions:['clipboard-read','clipboard-write']});
  const errors=[], writes=[];page.on('pageerror',e=>errors.push(e.message));
  const snapshot={id:'1',account_id:'100',collected_at:'2026-11-14T11:40:00Z',account_status:'banned',upstream_status_raw:'ban',upstream_error_message:'The supplier disabled this account after a policy review. <script>bad()</script>',cost_total_usd:81.95,input_tokens_total:10000,output_tokens_total:20000,usage_5h_percent:37,usage_7d_percent:6};
  const account={id:'100',account:'example@example.com',user_id:'7',user_email:'owner@example.com',plan:'claude/cc-max-20x',vendor_id:'1',vendor_name:'TK Register 0',current_submission_id:'1',latest_submission_id:'1',latest_submitted_at:'2026-11-14T09:47:00Z',submission_status:'accepted',platform_account_id:'22401',last_success_at:snapshot.collected_at,last_attempt_at:'2026-11-14T11:44:00Z',last_error:'Supplier account response could not be processed (page 9): an account record was repeated across pages. Last successful data is retained.',snapshot};
  const accounts=[account,{...account,id:'101',account:'login@example.com',last_error:'',snapshot:{...snapshot,id:'2',account_id:'101',account_status:'login_failed',upstream_status_raw:'login_failed',upstream_error_message:null}},{...account,id:'102',account:'active@example.com',last_error:'',snapshot:{...snapshot,id:'3',account_id:'102',account_status:'active',upstream_status_raw:'using',upstream_error_message:null}}];
  let role='admin';
  await page.route('**/tk-register/api/**',async route=>{
    const req=route.request(),path=new URL(req.url()).pathname.replace('/tk-register/api','');
    const ok=data=>route.fulfill({status:200,contentType:'application/json',body:JSON.stringify({success:true,data})});
    if(req.method()!=='GET')writes.push(path);
    if(path==='/auth/status')return ok({registration_enabled:true,password_login_enabled:true,email_code_login_enabled:true});
    if(path==='/auth/me')return ok({user:{id:1,email:'admin@example.com',role},csrf_token:'test'});
    if(path==='/vendors')return ok({vendors:[]});
    if(path==='/accounts'||path==='/admin/synced-accounts')return ok({items:accounts,total:3,page:1,page_size:20});
    if(path.endsWith('/sync')) { assert.deepEqual(req.postDataJSON(),{account_ids:['100']});return ok({accounts:[{...account,last_error:'',snapshot}],results:[{account_id:'100',status:'updated',message:'Account data updated, but the supplier reason could not be retrieved. Please try later.'}]}); }
    return route.fulfill({status:404,contentType:'application/json',body:'{"success":false}'});
  });
  await mkdir('test-results',{recursive:true});
  for(const [admin,width] of [[true,1600],[false,1440],[false,390]]){
    role=admin?'admin':'member';await page.setViewportSize({width,height:1000});
    await page.goto(`http://127.0.0.1:${port}/tk-register/#/${admin?'account-pool/accounts':'accounts'}`);await page.reload();
    const row=page.locator('.usage-accounts-table tbody tr').first();await row.waitFor();
    const heights=()=>page.locator('.usage-accounts-table tbody tr').evaluateAll(rows=>rows.map(row=>row.getBoundingClientRect().height));
    const before=await heights();
    const trigger=page.getByRole('button',{name:'Sync failed details',exact:true});
    await trigger.scrollIntoViewIfNeeded();await trigger.hover();await page.getByRole('dialog').waitFor();
    assert.deepEqual(await heights(),before,'opening a popover must not change row height');
    assert.match(await page.getByRole('dialog').textContent(),/page 9/);
    await trigger.click();await page.mouse.move(1,1);assert.equal(await page.getByRole('dialog').count(),1,'click pins the popup');
    const box=await page.getByRole('dialog').boundingBox();assert.ok(box.x>=0 && box.x+box.width<=width && box.y>=0 && box.y+box.height<=1000,'popup stays inside viewport');
    if(width>1000){await page.getByRole('button',{name:'Copy details',exact:true}).click();assert.match(await page.evaluate(()=>navigator.clipboard.readText()),/page 9/);}
    await page.screenshot({path:`test-results/account-errors-${admin?'admin':'member'}-${width}.png`});
    await page.keyboard.press('Escape');assert.equal(await page.getByRole('dialog').count(),0);
    const banned=page.getByRole('button',{name:'Banned details',exact:true});await banned.scrollIntoViewIfNeeded();await banned.focus();await page.keyboard.press('Enter');await page.getByRole('dialog').waitFor();
    assert.match(await page.getByRole('dialog').textContent(),/policy review/);assert.equal(await page.locator('.account-error-popover script').count(),0);
    await page.locator('h1').click();assert.equal(await page.getByRole('dialog').count(),0);
    const login=page.getByRole('button',{name:'Login failed details',exact:true});await login.click();assert.match(await page.getByRole('dialog').textContent(),/did not provide/);await page.keyboard.press('Escape');
    assert.deepEqual(await heights(),before);
  }
  assert.equal(writes.length,0,'opening lists and details never calls sync');
  await page.setViewportSize({width:1440,height:1000});await page.locator('.usage-accounts-table tbody tr').first().getByRole('button',{name:'Sync example@example.com',exact:true}).click();
  await page.getByRole('button',{name:'Details unavailable details'}).waitFor();assert.equal(writes.length,1);
  assert.equal(await page.getByRole('button',{name:'Sync failed details',exact:true}).count(),0,'supplementary failure is not a metrics failure');
  assert.deepEqual(errors,[]);console.log('Passed: member/admin, desktop/mobile, hover/click/keyboard/copy/escape/outside close, no row resize, no automatic sync, partial reason failure.');
}finally{await browser?.close();server.kill();}
