import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { mkdir } from 'node:fs/promises';
import { chromium } from '@playwright/test';
const port = Number(process.env.TK_TEST_PORT || 4196);
const server = spawn(process.execPath,['node_modules/vite/bin/vite.js','--host','127.0.0.1','--port',String(port),'--strictPort'],{stdio:'pipe'});
let browser;
try {
 await new Promise((resolve,reject)=>{ server.stdout.on('data',v=>{if(String(v).includes('Local:'))resolve()});server.on('exit',code=>reject(new Error(`Vite exited ${code}`)));setTimeout(()=>reject(new Error('Vite timeout')),15000).unref(); });
 browser=await chromium.launch({headless:true,...(process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH?{executablePath:process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH}:{})});
 const page=await browser.newPage({viewport:{width:1440,height:1080}});const errors=[],calls=[];page.on('pageerror',e=>errors.push(e.message));
 const user={id:1,email:'owner@example.com',role:'admin',status:'active'};
 const account={id:'1',account:'example.account@gmail.com',plan:'claude/cc-max-20x',vendor_id:'1',vendor_name:'TK Register 0',latest_submission_id:'1',current_submission_id:'1',submission_status:'accepted',platform_account_id:'24103',last_success_at:new Date().toISOString(),last_error:'',snapshot:{account_status:'active',cost_total_usd:186.42,input_tokens_total:15234567,output_tokens_total:8765432,usage_5h_percent:38,usage_7d_percent:16}};
 await page.route('**/tk-register/api/**',async route=>{
  const req=route.request(),url=new URL(req.url()),path=url.pathname.replace('/tk-register/api','');calls.push({path,method:req.method()});
  const ok=data=>route.fulfill({status:200,contentType:'application/json',body:JSON.stringify({success:true,data})});
  if(path==='/auth/status')return ok({registration_enabled:false,password_login_enabled:true,email_code_login_enabled:true});
  if(path==='/auth/me')return ok({user,csrf_token:'fake-csrf'});
  if(path==='/members/sources')return ok({sources:[{id:'myself',label:'myself'}]});
  if(path.endsWith('/daily-usage')){
   const start=url.searchParams.get('start_date'),end=url.searchParams.get('end_date');const days=[],rows=[];
   for(let at=Date.parse(start);at<=Date.parse(end);at+=86400000){const date=new Date(at).toISOString().slice(0,10);days.push({date,status:'complete',collected_at:new Date().toISOString()});for(const [i,model] of ['claude-sonnet','claude-opus','claude-haiku'].entries()) rows.push({date,model,input_tokens:String(35000*(i+1)),output_tokens:String(12000*(i+1)),cache_write_tokens:String(2500*(i+1)),cache_read_tokens:String(8000*(i+1)),cost_cny:String(72*(i+1)),cost_usd:10*(i+1)});}
   rows.sort((a,b)=>b.date.localeCompare(a.date)||a.model.localeCompare(b.model));
   return ok({start_date:start,end_date:end,timezone:'Asia/Shanghai',collection_time:'03:00',currency:'CNY',exchange_rate_cny_per_usd:7.2,days,rows});
  }
  if(/^\/(accounts|admin\/synced-accounts)\/1$/.test(path))return ok({account,interval_seconds:300,timezone:'Asia/Shanghai',snapshots:[],snapshot_count:0,trend:{cost_total_usd:[],input_tokens_total:[],output_tokens_total:[]}});
  return route.fulfill({status:404,contentType:'application/json',body:JSON.stringify({success:false,message:'Unexpected mock request'})});
 });
 await page.goto(`http://127.0.0.1:${port}/tk-register/#/accounts/1`);
 await page.getByRole('heading',{name:'Daily usage',exact:true}).waitFor();
 await page.locator('#daily-model-trigger').waitFor();
 assert.equal(await page.getByRole('heading',{name:'Cumulative usage'}).count(),1);
 await page.getByRole('tab',{name:'Daily usage',exact:true}).click();
 await page.getByRole('heading',{name:'Daily breakdown'}).waitFor();
 assert.match(await page.locator('.daily-summary').innerText(),/420\.00 \$/);
 assert.equal(await page.locator('.daily-table tbody tr').count(),20);
 await page.getByRole('button',{name:'Next',exact:true}).click();assert.equal(await page.locator('.daily-table tbody tr').count(),1);
 await page.locator('#daily-model-trigger').click();
 await page.getByRole('checkbox',{name:'claude-opus',exact:true}).uncheck();
 await page.getByRole('button',{name:'Done',exact:true}).click();
 assert.equal(await page.locator('.daily-table tbody tr').count(),14);
 assert.match(await page.locator('.daily-summary').innerText(),/280\.00 \$/);
 await page.locator('#daily-model-trigger').click();
 await page.getByRole('button',{name:'Clear selection',exact:true}).click();
 await page.getByRole('button',{name:'Done',exact:true}).click();
 await page.getByRole('heading',{name:'No models selected',exact:true}).waitFor();
 await page.locator('#daily-model-trigger').click();await page.getByRole('checkbox',{name:'Select all',exact:true}).check();
 await page.getByRole('searchbox',{name:'Search models'}).fill('sonnet');assert.equal(await page.getByRole('checkbox',{name:'claude-sonnet'}).count(),1);assert.equal(await page.getByRole('checkbox',{name:'claude-opus'}).count(),0);
 await page.keyboard.press('Escape');assert.equal(await page.getByRole('dialog').count(),0);
 for(const metric of ['Input tokens','Output tokens','Cache read','Cache write','Cost']){await page.getByRole('group',{name:'Daily chart metric'}).getByRole('button',{name:metric,exact:true}).click();assert.equal(await page.locator('.daily-table tbody tr').count(),20);}
 await page.getByRole('button',{name:'Last 30 days',exact:true}).click();await page.getByText('90 rows · Page 1 of 5',{exact:true}).waitFor();
 await page.getByRole('button',{name:'Last 7 days',exact:true}).click();await page.getByText('21 rows · Page 1 of 2',{exact:true}).waitFor();
 await mkdir('test-results',{recursive:true});
 for(const width of [1440,1024,768,390,320]){await page.setViewportSize({width,height:1080});await page.evaluate(()=>document.fonts.ready);await page.evaluate(()=>window.scrollTo(0,0));await page.waitForTimeout(150);assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth),true,`overflow at ${width}`);await page.screenshot({path:`test-results/daily-usage-${width}.png`,fullPage:width>=768});}
 await page.setViewportSize({width:1440,height:1080});await page.getByRole('tab',{name:'Overview',exact:true}).click();await page.screenshot({path:'test-results/daily-overview.png',fullPage:true});
 await page.goto(`http://127.0.0.1:${port}/tk-register/#/account-pool/accounts/1/usage?tab=daily-usage`);await page.getByRole('heading',{name:'Daily breakdown'}).waitFor();
 assert.ok(calls.some(c=>c.path==='/admin/synced-accounts/1/daily-usage'));assert.equal(calls.some(c=>c.method!=='GET'),false,'page/filter changes must never trigger supplier mutations');assert.deepEqual(errors,[]);
 console.log('Daily usage browser acceptance passed: Overview, admin/member routes, five metrics, multiselect/search/clear, pagination, 7/30-day ranges and five responsive widths.');
} finally {if(browser)await browser.close();server.kill('SIGTERM');}
