/* Real LuCI/rpcd/procd, static sing-box binary, API-only core sessions.
 * Transparent networking is independently exercised by core_netns.py. */
const assert = require('node:assert/strict')
const fs = require('node:fs')
const path = require('node:path')
const process = require('node:process')
const { chromium } = require('playwright')
const base = process.env.DAED_PANEL_URL || 'http://localhost:18080'
const directory = process.env.DAED_PANEL_CONTROL
const output = process.env.DAED_CORE_OUTPUT || '.build/core-browser'
if (!directory) throw new Error('Set DAED_PANEL_CONTROL')
fs.mkdirSync(output, { recursive: true })
async function control(action, extra = {}) {
  const id = `${Date.now()}-${Math.random()}`
  fs.writeFileSync(path.join(directory, 'command.json'), JSON.stringify({ id, action, ...extra }))
  for (let i = 0; i < 300; i++) {
    try {
      const r = JSON.parse(fs.readFileSync(path.join(directory, 'result.json')))
      if (r.id === id) return r
    } catch {}
    await new Promise((r) => setTimeout(r, 100))
  }
  throw new Error(`Control timeout ${action}`)
}
async function waitText(locator, pattern) {
  for (let i = 0; i < 250; i++) {
    if (pattern.test(await locator.textContent())) return
    await new Promise((r) => setTimeout(r, 200))
  }
  assert.match(await locator.textContent(), pattern)
}
async function checkStatisticsFailureRecovery(page) {
  const frame = page.frameLocator('iframe')
  let failures = 0
  await page.route('**/cgi-bin/daed-router/graphql', async (route) => {
    const body = route.request().postDataJSON()
    if (/query TrafficOverview\b/.test(body?.query || '')) {
      failures++
      return route.fulfill({
        contentType: 'application/json',
        body: JSON.stringify({ errors: [{ message: 'core_api_invalid', path: ['general', 'runtimeOverview'] }] }),
      })
    }
    await route.continue()
  })
  try {
    const warning = frame.getByText('暂时无法获取运行数据，已显示的数值可能不是最新状态。', { exact: true })
    await warning.waitFor({ timeout: 25000 })
    assert.ok(failures >= 2, 'Must observe repeated failures, not just one response')
    const message = '内核管理接口返回异常数据，请重新读取状态并查看运行日志；原配置保留。'
    assert.equal(await frame.getByText(message, { exact: true }).count(), 0, 'Polling must not create repeated toasts')
    assert.equal(await page.getByText(message, { exact: true }).count(), 0)
    await page.unrouteAll({ behavior: 'wait' })
    await warning.waitFor({ state: 'detached', timeout: 25000 })
  } finally {
    await page.unrouteAll({ behavior: 'wait' })
  }
}
async function checkApplyMemberGuard(page, context, gql, config, touch) {
  const label = touch ? 'ApplyMobileFixture' : 'ApplyDesktopFixture'
  const group = (await gql(`mutation{createGroup(name:${JSON.stringify(label)},policy:random){id}}`)).createGroup.id
  const node = (
    await gql(
      `mutation{importNodes(rollbackError:true,args:[{tag:${JSON.stringify(label)},link:"socks5://127.0.0.1:19092#apply"}]){node{id}}}`,
    )
  ).importNodes[0].node.id
  await gql(
    `mutation{groupAddNodes(id:${JSON.stringify(group)},nodeIDs:[${JSON.stringify(node)}]) updateConfig(id:${JSON.stringify(config)},global:{checkInterval:"32s"}){id}}`,
  )
  await page.goto(`${base}/cgi-bin/luci/admin/services/daed/dashboard`)
  const frame = page.frameLocator('iframe')
  await frame.locator('[data-workspace="dashboard"]').waitFor({ timeout: 45000 })
  await frame.getByRole('button', { name: new RegExp(`展开.*${label}`) }).click()
  const remove = frame
    .locator('[data-group-member]')
    .filter({ hasText: label })
    .getByRole('button', { name: new RegExp(label) })
  await remove.waitFor()
  let release,
    entered,
    edits = 0
  const held = new Promise((resolve) => {
    release = resolve
  })
  const started = new Promise((resolve) => {
    entered = resolve
  })
  const intercept = async (route) => {
    const body = route.request().postDataJSON()
    if (/mutation Run\(/.test(body?.query || '')) {
      entered()
      await held
    }
    if (/mutation GroupDelNodes\(/.test(body?.query || '')) edits++
    await route.continue()
  }
  await page.route('**/cgi-bin/daed-router/graphql', intercept)
  try {
    await frame.getByRole('button', { name: '应用配置', exact: true }).click()
    await Promise.race([
      started,
      new Promise((_, reject) => setTimeout(() => reject(new Error('Apply never started')), 5000)),
    ])
    await frame.getByText('正在应用配置，完成后可移除群组成员。', { exact: true }).waitFor()
    assert.equal(await remove.isDisabled(), true, 'Member removal is disabled during apply')
    await remove.evaluate((button) => button.click())
    assert.equal(edits, 0, 'Disabled member button must not submit a mutation')
    release()
    for (let i = 0; i < 200 && (await remove.isDisabled()); i++) await new Promise((r) => setTimeout(r, 100))
    assert.equal(await remove.isEnabled(), true, 'Member removal recovers without reboot')
    if (touch) await remove.tap()
    else await remove.click()
    await remove.waitFor({ state: 'detached', timeout: 15000 })
    assert.equal(edits, 1)
    const nodes = (await gql(`{groups{id nodes{id}}}`)).groups.find((g) => g.id === group).nodes
    assert.deepEqual(nodes, [])
    await page.screenshot({
      path: path.join(output, touch ? 'mobile-apply-guard.png' : 'desktop-apply-guard.png'),
      fullPage: true,
    })
  } finally {
    release()
    await page.unrouteAll({ behavior: 'wait' })
  }
  await gql(
    `mutation{removeGroup(id:${JSON.stringify(group)}) removeNodes(ids:[${JSON.stringify(node)}]) updateConfig(id:${JSON.stringify(config)},global:{checkInterval:"31s"}){id}}`,
  )
}
async function checkHorizontalChoices(dialog, selected) {
  // Measure the settled dialog, not the 95% opening-scale animation.
  await new Promise((resolve) => setTimeout(resolve, 250))
  const choices = dialog.locator('[data-core-option]')
  assert.equal(await choices.count(), 3)
  const boxes = await choices.evaluateAll((nodes) =>
    nodes.map((node) => {
      const box = node.getBoundingClientRect()
      return { x: box.x, y: box.y, width: box.width, height: box.height }
    }),
  )
  assert.ok(Math.max(...boxes.map((b) => b.y)) - Math.min(...boxes.map((b) => b.y)) < 1, JSON.stringify(boxes))
  assert.ok(boxes[0].x < boxes[1].x && boxes[1].x < boxes[2].x)
  assert.ok(
    boxes.every((b) => b.height === 44 && b.width >= 58),
    JSON.stringify(boxes),
  )
  const option = dialog.getByRole('button', { name: selected, exact: true })
  assert.equal(await option.getAttribute('aria-pressed'), 'true')
  assert.equal(await option.locator('svg').count(), 1)
  assert.ok(await option.evaluate((node) => node.scrollWidth <= node.clientWidth), 'Option label overflow')
  assert.ok(
    await option
      .locator('span')
      .first()
      .evaluate(
        (node) => node.getBoundingClientRect().height <= Number.parseFloat(getComputedStyle(node).lineHeight) + 1,
      ),
    'Option label wrapped',
  )
  assert.ok((await dialog.locator('[data-core-selection]').textContent()).startsWith(`${selected} ·`))
  const backgrounds = await choices.evaluateAll((nodes) =>
    nodes.map((node) => ({
      pressed: node.getAttribute('aria-pressed'),
      color: getComputedStyle(node).backgroundColor,
    })),
  )
  assert.ok(
    backgrounds
      .filter((b) => b.pressed !== 'true')
      .every((b) => b.color !== backgrounds.find((c) => c.pressed === 'true').color),
  )
}
async function checkManualLatency(page, context, touch, screenshot) {
  await page.goto(`${base}/cgi-bin/luci/admin/services/daed/resources`)
  const frame = page.frameLocator('iframe')
  await frame.locator('[data-workspace="resources"]').waitFor({ timeout: 45000 })
  const test = frame.getByRole('button', { name: '测试节点延迟 Preserved node', exact: true })
  if (touch) await test.tap()
  else await test.click()
  const badge = frame.locator('[data-latency-source="manual"]').first()
  await badge.waitFor({ timeout: 45000 })
  assert.match(await badge.getAttribute('title'), /包含新建连接/)
  const explanation = frame.getByRole('button', { name: '测试耗时说明', exact: true })
  if (touch) await explanation.tap()
  else await explanation.click()
  await frame.getByText(/手动测试显示首次建连耗时，包含代理握手/).waitFor()
  await page.screenshot({ path: screenshot.replace('.png', '-explanation.png'), fullPage: true })
  await page.keyboard.press('Escape')
  const response = await context.request.post(`${base}/cgi-bin/daed-router/graphql`, {
    headers: { Origin: base },
    data: { query: '{nodeLatencies{id source alive latencyMs testedAt}}' },
  })
  const data = await response.json()
  assert.ok(!data.errors, JSON.stringify(data.errors))
  assert.ok(data.data.nodeLatencies.some((result) => result.source === 'manual'))
  if (touch) {
    await badge.scrollIntoViewIfNeeded()
    const bounds = await badge.boundingBox()
    assert.ok(bounds.x >= 0 && bounds.x + bounds.width <= 390, JSON.stringify(bounds))
  }
  await page.screenshot({ path: screenshot, fullPage: true })
  await frame.getByRole('button', { name: '导入节点', exact: true }).click()
  const guide = frame.locator('[data-protocol-guide]')
  await waitText(guide, /当前内核：(?:dae|sing-box|mihomo)/)
  await guide.getByRole('button', { name: 'VLESS / REALITY', exact: true }).click()
  await guide.getByRole('status').waitFor()
  if (/当前内核：sing-box/.test(await guide.textContent())) {
    await guide.getByRole('button', { name: 'SSH', exact: true }).click()
    await waitText(guide, /内联密钥/)
    await guide.getByRole('button', { name: 'Naive HTTPS', exact: true }).click()
    await waitText(guide, /Cronet/)
    await guide.getByRole('button', { name: 'WireGuard', exact: true }).click()
    await waitText(guide, /用户态 endpoint/)
    await guide.getByRole('button', { name: 'Tor', exact: true }).click()
    await waitText(guide, /仅 TCP/)
    await guide.locator('summary').click()
    await waitText(guide, /ShadowsocksR \/ Juicity/)
  }
  const guideBox = await guide.boundingBox()
  if (touch) assert.ok(guideBox.x >= 0 && guideBox.x + guideBox.width <= 391, JSON.stringify(guideBox))
  await page.screenshot({ path: screenshot.replace('.png', '-protocol-guide.png'), fullPage: true })
  await page.keyboard.press('Escape')
}
;(async () => {
  const browser = await chromium.launch({
    headless: true,
    ...(process.env.DAED_BROWSER_EXECUTABLE ? { executablePath: process.env.DAED_BROWSER_EXECUTABLE } : {}),
  })
  let page
  try {
    const errors = []
    const context = await browser.newContext({ viewport: { width: 1600, height: 1000 }, locale: 'zh-CN' })
    page = await context.newPage()
    page.on('pageerror', (e) => errors.push(e.message))
    await page.goto(`${base}/cgi-bin/luci/admin/services/daed/dashboard`)
    await page.locator('input[name=luci_username]').fill('root')
    await page.locator('input[name=luci_password]').fill('fixture-password')
    await page.getByRole('button', { name: /Log in|登录/ }).click()
    let frame = page.frameLocator('iframe')
    await frame.locator('[data-workspace="dashboard"]').waitFor({ timeout: 45000 })
    const gql = async (query) => {
      const r = await context.request.post(`${base}/cgi-bin/daed-router/graphql`, {
        headers: { Origin: base },
        data: { query },
      })
      assert.equal(r.status(), 200)
      const v = await r.json()
      assert.ok(!v.errors, JSON.stringify(v.errors))
      return v.data
    }
    const config = (
      await gql(
        'mutation{createConfig(name:"Core UI fixture",global:{checkInterval:"30s",tcpCheckUrl:["http://127.0.0.1:18081/"]}){id}}',
      )
    ).createConfig.id
    const dns = (
      await gql(
        'mutation{createDns(name:"Core fixture",dns:"upstream { test: \'udp://127.0.0.1:53\' } routing { request { fallback: test } }"){id}}',
      )
    ).createDns.id
    const route = (await gql('mutation{createRouting(name:"Core fixture",routing:"fallback: direct"){id}}'))
      .createRouting.id
    await gql(
      `mutation{selectConfig(id:${JSON.stringify(config)}) selectDns(id:${JSON.stringify(dns)}) selectRouting(id:${JSON.stringify(route)}) run(dry:false)}`,
    )
    const nativeJSON = JSON.stringify({
      type: 'ssh',
      tag: 'Native fixture',
      server: 'fixture.invalid',
      server_port: 22,
      user: 'fixture',
      password: 'fixture',
    })
    const native = (
      await gql(
        `mutation{importNodes(rollbackError:true,args:[{link:${JSON.stringify(nativeJSON)}}]){error node{id link protocol name}}}`,
      )
    ).importNodes[0]
    assert.equal(native.error, null)
    assert.equal(native.node.protocol, 'ssh')
    assert.match(native.node.link, /^singbox:\/\//)
    await gql(`mutation{updateNode(id:${JSON.stringify(native.node.id)},newLink:${JSON.stringify(nativeJSON)}){id}}`)
    await gql(`mutation{removeNodes(ids:[${JSON.stringify(native.node.id)}])}`)
    await gql(
      'mutation{importNodes(rollbackError:true,args:[{tag:"Preserved node",link:"socks5://127.0.0.1:19092#preserved"}]){error node{id}}}',
    )
    await gql(`mutation{updateConfig(id:${JSON.stringify(config)},global:{checkInterval:"31s"}){id}}`)
    await checkApplyMemberGuard(page, context, gql, config, false)
    await checkStatisticsFailureRecovery(page)
    const applyMobile = await browser.newContext({
      storageState: await context.storageState(),
      viewport: { width: 390, height: 844 },
      isMobile: true,
      hasTouch: true,
    })
    const applyPhone = await applyMobile.newPage()
    await checkApplyMemberGuard(applyPhone, applyMobile, gql, config, true)
    await checkStatisticsFailureRecovery(applyPhone)
    await applyMobile.close()
    await page.reload()
    frame = page.frameLocator('iframe')
    await frame.locator('[data-workspace="dashboard"]').waitFor({ timeout: 45000 })
    await control('snapshot')
    await frame.getByRole('button', { name: /代理内核 dae/ }).click()
    let dialog = frame.getByRole('dialog')
    await checkHorizontalChoices(dialog, 'dae')
    assert.equal(await dialog.getByRole('button', { name: 'dae', exact: true }).getAttribute('aria-pressed'), 'true')
    await dialog.getByRole('button', { name: 'sing-box', exact: true }).click()
    await checkHorizontalChoices(dialog, 'sing-box')
    await control('maintenance', { fail: 'download' })
    await dialog.getByRole('button', { name: /下载并安装 sing-box/ }).click()
    await waitText(dialog, /GitHub 下载服务器返回异常响应/)
    await control('maintenance', { fail: '' })
    await dialog.getByRole('button', { name: /下载并安装 sing-box/ }).click()
    await waitText(dialog, /内核已安装/)
    await dialog.getByRole('button', { name: '检查兼容性' }).click()
    await waitText(dialog, /兼容性检查通过/)
    await dialog.getByRole('button', { name: '切换到 sing-box' }).click()
    await waitText(dialog, /切换完成，当前内核：sing-box/)
    assert.equal(JSON.parse((await gql('{coreStatus}')).coreStatus).current, 'sing-box')
    // A same-core mode change needs a fresh compatibility report and persists
    // the selected mode; it must not be dismissed as "already using" this core.
    const desktopMode = await dialog.locator('[data-core-mode][aria-pressed="true"]').getAttribute('data-core-mode')
    for (const mode of desktopMode === 'tun' ? ['tproxy', 'tun'] : ['tun', 'tproxy']) {
      await dialog.locator(`[data-core-mode="${mode}"]`).click()
      await dialog.getByRole('button', { name: '检查兼容性' }).click()
      await waitText(dialog, /兼容性检查通过/)
      const change = dialog.getByRole('button', { name: mode === 'tun' ? '启用 TUN' : '启用非 TUN', exact: true })
      await change.click()
      await waitText(dialog, /切换完成，当前内核：sing-box/)
      for (let i = 0; i < 60; i++) {
        if (JSON.parse((await gql('{coreStatus}')).coreStatus).mode === mode) break
        await new Promise((resolve) => setTimeout(resolve, 100))
      }
      assert.equal(JSON.parse((await gql('{coreStatus}')).coreStatus).mode, mode)
    }
    await page.screenshot({ path: path.join(output, 'desktop-singbox.png'), fullPage: true })
    await dialog.getByRole('button', { name: 'dae', exact: true }).click()
    await dialog.getByRole('button', { name: '检查兼容性' }).click()
    await waitText(dialog, /兼容性检查通过/)
    await dialog.getByRole('button', { name: '切换到 dae' }).click()
    await waitText(dialog, /切换完成，当前内核：dae/)
    await control('core_fault', { fail: true })
    await dialog.getByRole('button', { name: 'sing-box', exact: true }).click()
    await dialog.getByRole('button', { name: '检查兼容性' }).click()
    await waitText(dialog, /兼容性检查通过/)
    await dialog.getByRole('button', { name: '切换到 sing-box' }).click()
    await waitText(dialog, /已恢复原内核/)
    assert.equal(JSON.parse((await gql('{coreStatus}')).coreStatus).current, 'dae')
    await control('core_fault', { fail: false })
    await dialog.getByRole('button', { name: 'mihomo', exact: true }).click()
    await control('maintenance', { fail: 'core_checksum' })
    await dialog.getByRole('button', { name: /下载并安装 mihomo/ }).click()
    await waitText(dialog, /校验失败/)
    assert.equal(JSON.parse((await gql('{coreStatus}')).coreStatus).current, 'dae')
    await control('maintenance', { fail: '' })
    await dialog.getByRole('button', { name: /下载并安装 mihomo/ }).click()
    await waitText(dialog, /内核已安装/)
    await dialog.getByRole('button', { name: '检查兼容性' }).click()
    await waitText(dialog, /兼容性检查通过/)
    await dialog.getByRole('button', { name: '切换到 mihomo' }).click()
    await waitText(dialog, /切换完成，当前内核：mihomo/)
    await page.screenshot({ path: path.join(output, 'desktop-mihomo.png'), fullPage: true })
    for (const name of ['sing-box', 'dae']) {
      await dialog.getByRole('button', { name, exact: true }).click()
      await dialog.getByRole('button', { name: '检查兼容性' }).click()
      await waitText(dialog, /兼容性检查通过/)
      await dialog.getByRole('button', { name: `切换到 ${name}` }).click()
      await waitText(dialog, new RegExp(`切换完成，当前内核：${name}`))
    }
    await control('core_fault', { engine: 'mihomo', fail: true })
    await dialog.getByRole('button', { name: 'mihomo', exact: true }).click()
    await dialog.getByRole('button', { name: '检查兼容性' }).click()
    await waitText(dialog, /兼容性检查通过/)
    await dialog.getByRole('button', { name: '切换到 mihomo' }).click()
    await waitText(dialog, /已恢复原内核/)
    assert.equal(JSON.parse((await gql('{coreStatus}')).coreStatus).current, 'dae')
    await control('core_fault', { engine: 'mihomo', fail: false })
    assert.equal((await gql('{general{dae{modified}}}')).general.dae.modified, true)
    assert.equal((await control('compare')).unchanged, true)
    await checkManualLatency(page, context, false, path.join(output, 'desktop-manual-latency.png'))
    assert.equal((await control('compare')).unchanged, true)
    await context.close()
    const mobile = await browser.newContext({
      viewport: { width: 390, height: 844 },
      isMobile: true,
      hasTouch: true,
      locale: 'zh-CN',
    })
    page = await mobile.newPage()
    page.on('pageerror', (e) => errors.push(e.message))
    await page.goto(`${base}/cgi-bin/luci/admin/services/daed/dashboard`)
    await page.locator('input[name=luci_username]').fill('root')
    await page.locator('input[name=luci_password]').fill('fixture-password')
    await page.getByRole('button', { name: /Log in|登录/ }).tap()
    frame = page.frameLocator('iframe')
    await frame.locator('[data-workspace="dashboard"]').waitFor({ timeout: 45000 })
    await frame.getByRole('button', { name: /代理内核 dae/ }).tap()
    dialog = frame.getByRole('dialog')
    await checkHorizontalChoices(dialog, 'dae')
    await dialog.getByRole('button', { name: 'sing-box', exact: true }).tap()
    await checkHorizontalChoices(dialog, 'sing-box')
    await dialog.getByRole('button', { name: '检查兼容性' }).tap()
    await waitText(dialog, /兼容性检查通过/)
    await dialog.getByRole('button', { name: '切换到 sing-box' }).tap()
    await waitText(dialog, /切换完成，当前内核：sing-box/)
    const box = await dialog.boundingBox()
    assert.ok(box.width <= 390 && box.x >= 0, JSON.stringify(box))
    await page.screenshot({ path: path.join(output, 'mobile-singbox.png'), fullPage: true })
    await page.reload()
    await frame.locator('[data-workspace="dashboard"]').waitFor({ timeout: 45000 })
    await frame.getByRole('button', { name: /代理内核 sing-box/ }).tap()
    await waitText(frame.getByRole('dialog'), /当前内核：sing-box/)
    dialog = frame.getByRole('dialog')
    await dialog.getByRole('button', { name: 'mihomo', exact: true }).tap()
    await dialog.getByRole('button', { name: '检查兼容性' }).tap()
    await waitText(dialog, /兼容性检查通过/)
    await dialog.getByRole('button', { name: '切换到 mihomo' }).tap()
    await waitText(dialog, /切换完成，当前内核：mihomo/)
    const mobileMode = await dialog.locator('[data-core-mode][aria-pressed="true"]').getAttribute('data-core-mode')
    for (const mode of mobileMode === 'tun' ? ['tproxy', 'tun'] : ['tun', 'tproxy']) {
      await dialog.locator(`[data-core-mode="${mode}"]`).tap()
      const change = dialog.getByRole('button', { name: mode === 'tun' ? '启用 TUN' : '启用非 TUN', exact: true })
      assert.equal(await change.isDisabled(), true, 'A changed mode needs a fresh compatibility check')
      await dialog.getByRole('button', { name: '检查兼容性' }).tap()
      await waitText(dialog, /兼容性检查通过/)
      await change.tap()
      await waitText(dialog, /切换完成，当前内核：mihomo/)
      const reply = await mobile.request.post(`${base}/cgi-bin/daed-router/graphql`, {
        headers: { Origin: base },
        data: { query: '{coreStatus}' },
      })
      const state = await reply.json()
      assert.ok(!state.errors)
      assert.equal(JSON.parse(state.data.coreStatus).mode, mode)
    }
    const mhBox = await dialog.boundingBox()
    assert.ok(mhBox.x >= 0 && mhBox.x + mhBox.width <= 391, JSON.stringify(mhBox))
    await page.screenshot({ path: path.join(output, 'mobile-mihomo.png'), fullPage: true })
    await page.reload()
    await frame.locator('[data-workspace="dashboard"]').waitFor({ timeout: 45000 })
    await frame.getByRole('button', { name: /代理内核 mihomo/ }).tap()
    await waitText(frame.getByRole('dialog'), /当前内核：mihomo/)
    await checkManualLatency(page, mobile, true, path.join(output, 'mobile-manual-latency.png'))
    assert.equal((await control('compare')).unchanged, true)
    assert.deepEqual(errors, [])
    const result = {
      realLuCI: true,
      desktopMobileApplyMemberGuard: true,
      desktopMobileStatisticsRecovery: true,
      realRPC: true,
      realProcd: true,
      pinnedCompatibilityCoreInstall: true,
      downloadFailureRetry: true,
      daeSingboxDae: true,
      mihomoInstallSwitchRollback: true,
      mihomoChecksumFailureRetry: true,
      mobileMihomo: true,
      lateFailureRollback: true,
      allUserTablesPreserved: true,
      pendingEditsPreserved: true,
      mobileTouchSwitch: true,
      mobileRefreshRecovery: true,
      desktopMobileSameCoreModeSwitch: true,
      noHorizontalDialogOverflow: true,
      desktopManualLatencySource: true,
      mobileManualLatencySource: true,
      nativeJSONImportUpdate: true,
      desktopMobileProtocolGuide: true,
      apiOnly: true,
    }
    fs.writeFileSync(path.join(output, 'result.json'), JSON.stringify(result, null, 2))
    process.stdout.write(`${JSON.stringify(result)}\n`)
    await mobile.close()
  } catch (error) {
    if (page) await page.screenshot({ path: path.join(output, 'failure.png'), fullPage: true }).catch(() => {})
    throw error
  } finally {
    await browser.close()
  }
})().catch((e) => {
  console.error(e)
  process.exitCode = 1
})
