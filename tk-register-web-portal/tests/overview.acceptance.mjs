import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { mkdir } from 'node:fs/promises';
import { chromium } from '@playwright/test';

const port=Number(process.env.TK_TEST_PORT||4198);
const server=spawn(process.execPath,['node_modules/vite/bin/vite.js','--host','127.0.0.1','--port',String(port),'--strictPort'],{stdio:'pipe'});
let browser;
try {
  await new Promise((resolve,reject)=>{server.stdout.on('data',v=>{if(String(v).includes('Local:'))resolve()});server.on('exit',c=>reject(new Error(`Vite exit ${c}`)));setTimeout(()=>reject(new Error('Vite timeout')),15000).unref()});
  browser=await chromium.launch({headless:true,...(process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH?{executablePath:process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH}:{})});
  const page=await browser.newPage({viewport:{width:1440,height:1080}}),errors=[],calls=[];
  page.on('pageerror',e=>errors.push(e.message));
  const sourceList=[{id:'myself',label:'myself'},{id:'2',label:'ben@example.com'},{id:'3',label:'clara@example.com'}];
  const yesterday=new Date(Date.parse(new Date(Date.now()+8*3600_000).toISOString().slice(0,10))-86400_000).toISOString().slice(0,10);
  let failNext=false;
  await page.route('**/tk-register/api/**',async route=>{
    const req=route.request(),url=new URL(req.url()),path=url.pathname.replace('/tk-register/api','');calls.push({path,method:req.method(),query:url.searchParams});
    const ok=data=>route.fulfill({status:200,contentType:'application/json',body:JSON.stringify({success:true,data})});
    if(path==='/auth/status')return ok({registration_enabled:false,password_login_enabled:true,email_code_login_enabled:true});
    if(path==='/auth/me')return ok({user:{id:1,email:'alex@example.com',role:'admin',status:'active'},csrf_token:'fake-csrf'});
    if(path==='/members/sources')return ok({sources:sourceList});
    if(path==='/accounts/overview'){
      if(failNext){failNext=false;return route.fulfill({status:503,contentType:'application/json',body:JSON.stringify({success:false,message:'Temporary test failure'})})}
      const raw=url.searchParams.get('sources'),chosen=raw==='all'?sourceList:sourceList.filter(s=>raw?.split(',').includes(s.id));
      const start=url.searchParams.get('range')==='all'?'2026-05-01':url.searchParams.get('start_date'),end=url.searchParams.get('range')==='all'?yesterday:url.searchParams.get('end_date');
      const days=[],rows=[],models=['claude-sonnet-4-5','claude-opus-4-1','claude-haiku-4-5'];
      for(let at=Date.parse(start),i=0;at<=Date.parse(end);at+=86400_000,i++){
        const date=new Date(at).toISOString().slice(0,10),missing=i===3;
        days.push({date,status:missing||!chosen.length?'missing':'complete',expected:chosen.length*3,collected:missing?0:chosen.length*3});
        if(!missing&&chosen.length)for(const [m,model]of models.entries())rows.push({date,model,input_tokens:String((m+1)*1234567),output_tokens:String((m+1)*654321),cache_read_tokens:'0',cache_write_tokens:'0',cost_cny:'72',cost_usd:chosen.length*(m+1)*(10+Math.sin(i/3)*3)});
      }
      const sources=chosen.map(s=>({...s,total:3,statuses:{active:1,login_failed:1,offline:1},cost_usd:90,cost_known:3,collected_days:days.filter(d=>d.collected).length}));
      return ok({start_date:start,end_date:end,earliest_date:chosen.length?'2026-05-01':null,timezone:'Asia/Shanghai',collection_time:'03:00',exchange_rate_cny_per_usd:7.2,latest_account_data:new Date().toISOString(),sources,days,rows,model_costs:sources.flatMap(s=>models.map(model=>({source_id:s.id,model,cost_cny:'72',cost_usd:rows.filter(r=>r.model===model).reduce((sum,r)=>sum+r.cost_usd,0)/chosen.length})))});
    }
    if(path==='/accounts')return ok({items:[],total:0,page:1,page_size:20});
    return route.fulfill({status:404,contentType:'application/json',body:JSON.stringify({success:false,message:'Unexpected mock request'})});
  });
  const dateTrigger=page.getByRole('button',{name:/^Usage date range:/});
  const dateDialog=page.getByRole('dialog',{name:'Usage dates',exact:true});
  const overviewCalls=()=>calls.filter(c=>c.path==='/accounts/overview');
  async function openDates(){
    await dateTrigger.click();await dateDialog.waitFor();
    assert.equal(await dateTrigger.getAttribute('aria-expanded'),'true');
  }
  async function datesClosed(){
    await dateDialog.waitFor({state:'hidden'});
    assert.equal(await dateTrigger.getAttribute('aria-expanded'),'false');
  }
  async function choosePreset(label){
    await openDates();
    const response=page.waitForResponse(r=>new URL(r.url()).pathname==='/tk-register/api/accounts/overview');
    await dateDialog.getByRole('button',{name:label,exact:true}).click();
    await response;await datesClosed();
    assert.equal((await dateTrigger.innerText()).trim(),label);
  }
  await page.goto(`http://127.0.0.1:${port}/tk-register/#/accounts`);
  await page.getByRole('heading',{name:'Account summary',exact:true}).waitFor();
  await datesClosed();
  assert.equal((await dateTrigger.innerText()).trim(),'Past 7 days');
  assert.equal(await page.getByLabel('Start date',{exact:true}).isVisible(),false);
  const initial=overviewCalls().at(-1).query;
  assert.equal((Date.parse(initial.get('end_date'))-Date.parse(initial.get('start_date')))/86400_000,6);
  assert.equal(initial.get('sources'),'all');
  const summary=page.locator('section[aria-labelledby="overview-summary-heading"]');
  assert.equal(await summary.getByRole('heading',{name:'Model costs',exact:true}).count(),1,'model costs belong to the account summary');
  assert.equal(await summary.locator('.overview-model-summary').count(),1);
  assert.equal(await page.locator('.overview-daily-chart.is-bar').count(),1);
  assert.equal(await page.locator('.overview-source-table tbody tr').count(),3);
  assert.equal(await page.locator('.overview-status-grid a,.overview-status-grid button,.overview-source-table a,.overview-source-table button').count(),0);
  for(const dismissal of ['outside','Escape','Cancel']){
    await openDates();
    assert.equal(await dateDialog.getByRole('button',{name:'Past 7 days',exact:true}).getAttribute('aria-pressed'),'true');
    await dateDialog.getByLabel('Start date',{exact:true}).fill('2026-10-01');
    await dateDialog.getByLabel('End date',{exact:true}).fill('2026-10-10');
    const beforeDismiss=overviewCalls().length;
    if(dismissal==='outside')await page.getByRole('heading',{name:'Daily usage',exact:true}).click();
    else if(dismissal==='Escape')await page.keyboard.press('Escape');
    else await dateDialog.getByRole('button',{name:'Cancel',exact:true}).click();
    await datesClosed();
    assert.equal(overviewCalls().length,beforeDismiss,`${dismissal} must not apply draft dates`);
    assert.equal((await dateTrigger.innerText()).trim(),'Past 7 days');
    await openDates();
    assert.equal(await dateDialog.getByLabel('Start date',{exact:true}).inputValue(),initial.get('start_date'),`${dismissal} restores the start date`);
    assert.equal(await dateDialog.getByLabel('End date',{exact:true}).inputValue(),initial.get('end_date'),`${dismissal} restores the end date`);
    await page.keyboard.press('Escape');await datesClosed();
  }
  await page.locator('#overview-source').click();await page.getByRole('checkbox',{name:'ben@example.com',exact:true}).uncheck();await page.getByRole('button',{name:'Done',exact:true}).click();
  await page.waitForFunction(()=>document.querySelectorAll('.overview-source-table tbody tr').length===2);
  assert.equal(calls.filter(c=>c.path==='/accounts/overview').at(-1).query.get('sources'),'myself,3');
  const before=calls.length;
  await page.locator('#daily-model-trigger').click();await page.getByRole('checkbox',{name:'claude-opus-4-1',exact:true}).uncheck();await page.getByRole('button',{name:'Done',exact:true}).click();
  assert.equal(await page.locator('.overview-model-grid > div').count(),2);assert.equal(calls.length,before,'model filter should use already aggregated data');
  await page.getByRole('group',{name:'Source breakdown'}).getByRole('button',{name:'Model costs',exact:true}).click();
  assert.equal(await page.locator('.overview-source-table thead th').count(),4);
  for(const label of ['Input tokens','Output tokens','Cost'])await page.getByRole('group',{name:'Daily chart metric'}).getByRole('button',{name:label,exact:true}).click();
  await choosePreset('All time');await page.locator('.overview-daily-chart.is-curve').waitFor();
  assert.equal(overviewCalls().at(-1).query.get('range'),'all');
  await openDates();
  assert.equal(await dateDialog.getByLabel('Start date',{exact:true}).inputValue(),'2026-05-01');
  assert.equal(await dateDialog.getByLabel('End date',{exact:true}).inputValue(),yesterday);
  await page.keyboard.press('Escape');await datesClosed();
  assert.ok(await page.locator('.overview-daily-chart path[stroke-width="3.5"]').count()>0);
  assert.match(await page.locator('.overview-daily-chart path').first().getAttribute('d'),/C/);
  await page.locator('.overview-chart-target').focus();await page.keyboard.press('ArrowRight');await page.locator('.daily-chart-tooltip').waitFor();await page.keyboard.press('Escape');
  await openDates();await dateDialog.getByLabel('Start date',{exact:true}).fill('2026-10-01');await dateDialog.getByLabel('End date',{exact:true}).fill('2026-10-10');await dateDialog.getByRole('button',{name:'Apply',exact:true}).click();await datesClosed();await page.locator('.overview-daily-chart.is-bar').waitFor();
  const custom=overviewCalls().at(-1).query;
  assert.equal(custom.get('start_date'),'2026-10-01');assert.equal(custom.get('end_date'),'2026-10-10');assert.equal(custom.has('range'),false);
  await openDates();assert.equal(await dateDialog.getByLabel('Start date',{exact:true}).inputValue(),'2026-10-01');assert.equal(await dateDialog.getByLabel('End date',{exact:true}).inputValue(),'2026-10-10');await page.keyboard.press('Escape');await datesClosed();
  await choosePreset('Past month');await page.locator('.overview-daily-chart.is-curve').waitFor();
  const month=overviewCalls().at(-1).query;
  assert.equal((Date.parse(month.get('end_date'))-Date.parse(month.get('start_date')))/86400_000,29);
  await choosePreset('Past 7 days');await page.locator('.overview-daily-chart.is-bar').waitFor();
  await page.getByRole('group',{name:'Source breakdown'}).getByRole('button',{name:'Status',exact:true}).click();
  await mkdir('test-results',{recursive:true});
  for(const width of [1440,1024,768,390,320]){
    await page.setViewportSize({width,height:1080});await page.evaluate(()=>document.fonts.ready);await page.waitForTimeout(120);
    await page.screenshot({path:`test-results/overview-${width}.png`,fullPage:true});
    const layout=await page.evaluate(()=>({width:document.documentElement.scrollWidth,overflow:[...document.querySelectorAll('body *')].filter(el=>el.getBoundingClientRect().right>innerWidth+1).slice(0,12).map(el=>({tag:el.tagName,className:el.getAttribute('class'),right:Math.round(el.getBoundingClientRect().right)}))}));
    assert.ok(layout.width<=width,`overflow at ${width}: ${JSON.stringify(layout)}`);
    await openDates();
    assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth),true,`date popover overflow at ${width}`);
    const bounds=await dateDialog.boundingBox();
    assert.ok(bounds&&bounds.x>=0&&bounds.x+bounds.width<=width,`date popover fits at ${width}`);
    if(width===1440||width===390)await page.screenshot({path:`test-results/overview-dates-${width}.png`,fullPage:true});
    const applyBounds=await dateDialog.getByRole('button',{name:'Apply',exact:true}).boundingBox();
    assert.ok(applyBounds&&applyBounds.y+applyBounds.height<=bounds.y+bounds.height,`date actions are visible without scrolling at ${width}×1080`);
    await page.keyboard.press('Escape');await datesClosed();
  }
  await page.setViewportSize({width:1440,height:1080});await choosePreset('All time');await page.locator('.overview-daily-chart.is-curve').waitFor();await page.screenshot({path:'test-results/overview-all-time.png',fullPage:true});
  await page.locator('#daily-model-trigger').click();await page.getByRole('button',{name:'Clear selection',exact:true}).click();await page.getByRole('button',{name:'Done',exact:true}).click();await page.getByRole('heading',{name:'Select models to view daily usage'}).waitFor();
  await page.locator('#overview-source').click();await page.getByRole('checkbox',{name:'Select all',exact:true}).check();await page.getByRole('checkbox',{name:'Select all',exact:true}).uncheck();await page.getByRole('button',{name:'Done',exact:true}).click();await page.getByText('Select one or more sources to view your accounts.',{exact:true}).waitFor();
  failNext=true;await choosePreset('Past day');await page.getByRole('button',{name:'Try again',exact:true}).waitFor();
  const day=overviewCalls().at(-1).query;assert.equal(day.get('start_date'),day.get('end_date'));
  await page.getByRole('button',{name:'Try again',exact:true}).click();await page.getByRole('heading',{name:'Account summary',exact:true}).waitFor();
  await page.getByRole('tab',{name:'Account list',exact:true}).click();await page.getByRole('heading',{name:'No accounts yet',exact:true}).waitFor();assert.match(page.url(),/tab=list/);
  await page.reload();await page.getByRole('heading',{name:'No accounts yet',exact:true}).waitFor();assert.equal(await page.getByRole('tab',{name:'Account list',exact:true}).getAttribute('aria-selected'),'true');
  assert.equal(calls.some(c=>c.method!=='GET'||c.path.endsWith('/sync')),false);assert.deepEqual(errors,[]);
  console.log('Overview acceptance passed: default 7 days, collapsed date popover and draft dismissal, merged model costs, recursive-source UI, models, custom dates, All time, smooth curves, nonlinked counts, missing data, empty filters, retries, responsive layouts and date popovers, list routing and read-only requests.');
}finally{if(browser)await browser.close();server.kill('SIGTERM')}
