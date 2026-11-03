/* Native LuCI/API fixture, controlled service responses; no LAN takeover. */
const assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path'),{chromium}=require('playwright')
const base=process.env.DAED_PANEL_URL||'http://localhost:18080',output=process.env.DAED_SERVICE_OUTPUT||'.build/service71/browser'
;(async()=>{
 fs.mkdirSync(output,{recursive:true})
 const browser=await chromium.launch({headless:true,...(process.env.DAED_BROWSER_EXECUTABLE?{executablePath:process.env.DAED_BROWSER_EXECUTABLE}:{})})
 try{
  const context=await browser.newContext({viewport:{width:1800,height:1100},locale:'zh-CN'}),page=await context.newPage(),errors=[]
  page.on('pageerror',e=>errors.push(e.message))
  await page.goto(base+'/cgi-bin/luci/admin/services/daed/dashboard')
  await page.locator('input[name=luci_username]').fill('root');await page.locator('input[name=luci_password]').fill('fixture-password');await page.getByRole('button',{name:/Log in|登录/}).click()
  const f=page.frameLocator('iframe');await f.locator('[data-workspace="dashboard"]').waitFor({timeout:45000})
  const gql=async(query,variables={})=>{const r=await(await context.request.post(base+'/cgi-bin/daed-router/graphql',{headers:{Origin:base},data:{query,variables}})).json();assert.ok(!r.errors,JSON.stringify(r.errors));return r.data}
  let data=await gql('{groups{id name nodes{id}} routings{id name selected routing{string}} groupServices}')
  assert.deepEqual(data.groups.map(g=>g.name).sort(),['AI','Netflix','proxy'])
  assert.equal(data.routings.find(r=>r.selected).name,'分流');assert.equal(JSON.parse(data.groupServices).find(g=>g.id===data.groups.find(g=>g.name==='AI').id).profile,'ai')
  const ai=data.groups.find(g=>g.name==='AI').id
  const existing=await gql('{nodes{edges{tag}} subscriptions{tag}}')
  for(let i=0;i<12;i++){const tag=i===0?'香港 专线 很长的完整名称 '.repeat(5):'HK '+String(i).padStart(3,'0');if(existing.nodes.edges.some(n=>n.tag===tag))continue;await gql('mutation($args:[ImportArgument!]!){importNodes(rollbackError:true,args:$args){node{id}}}',{args:[{tag,link:`socks5://192.0.2.98:${4100+i}#service-${i}`}]})}
  for(const name of ['紧凑订阅 A','另一个较长的订阅组 B'])if(!existing.subscriptions.some(s=>s.tag===name))await gql('mutation($arg:ImportArgument!){importSubscription(rollbackError:true,arg:$arg){sub{id}}}',{arg:{tag:name,link:'http://127.0.0.1:18081/sub-layout?service='+encodeURIComponent(name)}})
  const snapshotQuery='{nodes{edges{id tag}} subscriptions{id tag nodes{edges{id}}} groups{id name nodes{id} subscriptions{subscription{id} nameFilterRegex}} routings{id name selected routing{string}}}'
  const before=await gql(snapshotQuery)
  let starts=0,stops=0,hold=false,job='b'.repeat(32),jobResults=[]
  const intercept=async route=>{
    const b=route.request().postDataJSON(),q=b?.query||''
    if(q.includes('serviceResults('))return route.fulfill({json:{data:{serviceResults:'[]'}}})
    if(q.includes('startServiceTest(')){starts++;jobResults=b.variables.ids.map((id,i)=>({id,alive:i%3!==0,message:i%3===0?'service_challenge':'service_accessible',testedAt:new Date().toISOString()}));return route.fulfill({json:{data:{startServiceTest:JSON.stringify({id:job,phase:hold?'running':'completed',completed:hold?0:jobResults.length,cursor:hold?0:jobResults.length,results:hold?[]:jobResults})}}})}
    if(q.includes('latencyTest('))return route.fulfill({json:{data:{latencyTest:JSON.stringify({id:job,phase:'running',completed:0,cursor:0,results:[]})}}})
    if(q.includes('stopServiceTest(')){stops++;return route.fulfill({json:{data:{stopServiceTest:1}}})}
    return route.continue()
  }
  await page.route('**/cgi-bin/daed-router/graphql',intercept)
  await page.reload();await f.locator('[data-workspace="dashboard"]').waitFor()
  await f.locator(`[data-group-card-id="${ai}"]`).getByRole('button',{name:'添加节点',exact:true}).click()
  let picker=f.locator('[data-node-picker]');await picker.locator('.service-result[data-state="pass"]').first().waitFor()
  assert.equal(await picker.getByRole('combobox',{name:'群组用途',exact:true}).inputValue(),'ai')
  const all=await picker.locator('[data-picker-node]').count(),passed=await picker.locator('.service-result[data-state="pass"]').count()
  assert.ok(passed>0&&passed<all)
  await picker.getByLabel('只看通过',{exact:true}).check();assert.equal(await picker.locator('[data-picker-node]').count(),passed)
  await picker.getByRole('button',{name:'勾选通过',exact:true}).click();assert.equal(await picker.getByRole('checkbox').filter({visible:true}).count()>0,true)
  assert.equal(await picker.locator('[data-picker-node][data-selected="true"]').count(),passed)
  await picker.getByLabel('只看通过',{exact:true}).uncheck()
  const evidence=[]
  for(const dark of [false,true])for(const width of [1800,390,320]){
    await page.emulateMedia({colorScheme:dark?'dark':'light'});await page.setViewportSize({width,height:width>500?1100:844});await page.waitForTimeout(220)
    assert.ok(await picker.evaluate(e=>e.scrollWidth<=e.clientWidth+1))
    if(width>500){const card=picker.locator('[data-picker-node][data-selected="false"]').first();await page.mouse.move(1,1);await page.waitForTimeout(200);const plain=await card.evaluate(e=>getComputedStyle(e).backgroundColor);await card.hover();await page.waitForTimeout(220);assert.notEqual(await card.evaluate(e=>getComputedStyle(e).backgroundColor),plain);assert.equal(await card.evaluate(e=>getComputedStyle(e).outlineStyle),'none');assert.equal(await card.evaluate(e=>getComputedStyle(e).translate),'0px -2px')}
    await picker.screenshot({path:path.join(output,`nodes-${dark?'dark':'light'}-${width}.png`)});evidence.push({width,dark,all,passed})
  }
  await picker.getByRole('button',{name:'取消',exact:true}).click();await page.setViewportSize({width:1800,height:1100})
  await f.locator(`[data-group-card-id="${ai}"]`).getByRole('button',{name:'添加订阅组',exact:true}).click()
  const sub=f.locator('[data-subscription-picker]');await sub.locator('[data-subscription-service-summary]').first().waitFor()
  await sub.getByRole('checkbox',{name:/选择 紧凑订阅 A/}).check()
  await sub.getByRole('button',{name:/仅添加通过的节点/}).waitFor()
  const tile=await sub.locator('[data-picker-subscription]').first().boundingBox();assert.ok(tile.height<170,JSON.stringify(tile))
  assert.equal(await sub.locator('details').getAttribute('open'),null)
  for(const width of [1800,390,320]){await page.setViewportSize({width,height:width>500?1100:844});await page.waitForTimeout(150);assert.ok(await sub.evaluate(e=>e.scrollWidth<=e.clientWidth+1));const check=await sub.getByRole('checkbox',{name:/选择 紧凑订阅 A/}).boundingBox();assert.equal(check.width,18);assert.equal(check.height,18);if(width<500){const hit=await sub.locator('.subscription-picker-check').first().boundingBox();assert.ok(hit.width>=44&&hit.height>=44)}await sub.screenshot({path:path.join(output,`subscriptions-${width}.png`)})}
  const keyboardCheck=sub.getByRole('checkbox',{name:/选择 紧凑订阅 A/})
  for(let i=0;i<30;i++){await page.keyboard.press('Tab');if(await keyboardCheck.evaluate(e=>e===document.activeElement&&e.matches(':focus-visible')))break}
  assert.ok(await keyboardCheck.evaluate(e=>e===document.activeElement&&e.matches(':focus-visible')&&(getComputedStyle(e).outlineStyle!=='none'||getComputedStyle(e).boxShadow!=='none')),'Keyboard checkbox focus must be visibly indicated')
  await sub.screenshot({path:path.join(output,'subscriptions-focus-320.png')})
  hold=true;await sub.getByRole('button',{name:'重新检测',exact:true}).click();await sub.getByRole('button',{name:'暂停检测',exact:true}).waitFor();await sub.getByRole('button',{name:'暂停检测',exact:true}).click();await page.waitForTimeout(1500);assert.ok(stops>0,'Pause must stop its service job')
  await sub.getByRole('button',{name:'取消',exact:true}).click()
  await page.setViewportSize({width:1600,height:950});await page.emulateMedia({colorScheme:'light'});await page.waitForTimeout(500)
  const overview=await f.locator('#panel-monitor').evaluate(e=>({bottom:e.getBoundingClientRect().bottom,height:innerHeight,totals:e.querySelector('dl').getBoundingClientRect().bottom}))
  assert.ok(overview.bottom<=overview.height+1&&overview.totals<=overview.height,JSON.stringify(overview))
  await page.screenshot({path:path.join(output,'dashboard-1600.png'),fullPage:true})
  hold=false
  const touch=await browser.newContext({viewport:{width:390,height:844},isMobile:true,hasTouch:true,locale:'zh-CN',storageState:await context.storageState()}),tp=await touch.newPage()
  await tp.route('**/cgi-bin/daed-router/graphql',intercept);await tp.goto(base+'/cgi-bin/luci/admin/services/daed/dashboard')
  const tf=tp.frameLocator('iframe');await tf.locator(`[data-group-card-id="${ai}"]`).getByRole('button',{name:'添加订阅组',exact:true}).tap()
  const ts=tf.locator('[data-subscription-picker]'),target=ts.locator('[data-picker-subscription]').first();await target.locator('.subscription-picker-check').tap();assert.ok(await target.getByRole('checkbox').isChecked());await ts.screenshot({path:path.join(output,'subscriptions-touch-390.png')});await touch.close()
  assert.deepEqual(await gql(snapshotQuery),before,'Pickers must not mutate resources before submitting')
  assert.deepEqual(errors,[])
  fs.writeFileSync(path.join(output,'result.json'),JSON.stringify({starts,stops,evidence,overview,preserved:true},null,2))
  console.log('PASS: fresh presets/routing, automatic purpose checks, passed filtering/selection, compact subscription cards, cancellation, light/dark desktop/mobile widths, complete overview and preserved resources')
  await context.close()
 }finally{await browser.close()}
})().catch(e=>{console.error(e);process.exitCode=1})
