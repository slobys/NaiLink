/* Connectivity UI against real LuCI/rpcd and the router build. Public target
 * responses are deterministic here; actual routed requests need hardware. */
const assert = require('node:assert/strict')
const fs = require('node:fs')
const path = require('node:path')
const { chromium } = require('playwright')
const base = process.env.DAED_PANEL_URL || 'http://localhost:18080'
const output = process.env.DAED_CORE_OUTPUT || '.build/connectivity-browser'
const delay = (ms) => new Promise((r) => setTimeout(r, ms))
async function until(check) {
  for (let i = 0; i < 150; i++) {
    if (await check()) return
    await delay(100)
  }
  assert.fail('UI condition not reached')
}
function sample() {
  const at = Date.now()
  return {
    core: 'dae',
    state: 'ready',
    checking: false,
    checkedAt: at,
    targets: ['aliyun', 'google', 'openai', 'github'].map((id, index) => ({
      id,
      samples: Array.from({ length: 18 }, (_, i) => ({
        at: at - (17 - i) * 30000,
        status: index === 2 ? 'restricted' : index === 3 ? 'error' : 'ok',
        ...(index === 3
          ? { code: 'timeout' }
          : { latencyMs: 31 + index * 100 + i, httpStatus: index === 2 ? 403 : 204 }),
      })),
    })),
  }
}
;(async () => {
  fs.mkdirSync(output, { recursive: true })
  const browser = await chromium.launch({
    headless: true,
    ...(process.env.DAED_BROWSER_EXECUTABLE ? { executablePath: process.env.DAED_BROWSER_EXECUTABLE } : {}),
  })
  const errors = []
  try {
    const desktop = await browser.newContext({ viewport: { width: 1600, height: 1000 }, locale: 'zh-CN' })
    const login = await desktop.newPage()
    await login.goto(`${base}/cgi-bin/luci/admin/services/daed/dashboard`)
    const user = login.locator('input[name=luci_username]')
    if (await user.count()) {
      await user.fill('root')
      await login.locator('input[name=luci_password]').fill('fixture-password')
      await login.getByRole('button', { name: /Log in|登录/ }).click()
    }
    await login.frameLocator('iframe').locator('[data-workspace="dashboard"]').waitFor({ timeout: 45000 })
    const gql = async (query) => {
      const r = await desktop.request.post(`${base}/cgi-bin/daed-router/graphql`, {
        headers: { Origin: base },
        data: { query },
      })
      const body = await r.json()
      assert.ok(!body.errors, JSON.stringify(body.errors))
      assert.ok(body.data, 'Authenticated GraphQL endpoint must return data')
      return body.data
    }
    const config = (
      await gql(
        'mutation{createConfig(name:"Connectivity fixture",global:{checkInterval:"30s",tcpCheckUrl:["http://127.0.0.1:18081/"]}){id}}',
      )
    ).createConfig.id
    const dns = (
      await gql(
        'mutation{createDns(name:"Connectivity fixture",dns:"upstream { test: \'udp://127.0.0.1:53\' } routing { request { fallback: test } }"){id}}',
      )
    ).createDns.id
    const routing = (await gql('mutation{createRouting(name:"Connectivity fixture",routing:"fallback: direct"){id}}'))
      .createRouting.id
    await gql(
      `mutation{selectConfig(id:${JSON.stringify(config)}) selectDns(id:${JSON.stringify(dns)}) selectRouting(id:${JSON.stringify(routing)}) run(dry:false)}`,
    )
    await login.close()
    const auth = await desktop.storageState()
    for (const width of [1600, 390, 320]) {
      const context =
        width === 1600
          ? desktop
          : await browser.newContext({
              storageState: auth,
              viewport: { width, height: 844 },
              isMobile: true,
              hasTouch: true,
              locale: 'zh-CN',
              colorScheme: width === 320 ? 'dark' : 'light',
            })
      const page = await context.newPage()
      if (process.env.DAED_RESOURCE_PREVIEW_DIST) {
        const dist = path.resolve(process.env.DAED_RESOURCE_PREVIEW_DIST)
        await page.route('**/cgi-bin/daed-router/**', async (route) => {
          if (route.request().method() !== 'GET') return route.continue()
          const file = path.resolve(
            dist,
            new URL(route.request().url()).pathname.split('/cgi-bin/daed-router/')[1] || 'index.html',
          )
          if (!file.startsWith(dist + path.sep) || !fs.existsSync(file)) return route.continue()
          return route.fulfill({ path: file })
        })
      }
      page.on('pageerror', (e) => errors.push(e.message))
      let fail = false,
        force = 0,
        automatic = 0,
        release
      await page.route('**/cgi-bin/daed-router/graphql', async (route) => {
        const body = route.request().postDataJSON()
        if (!/query ConnectivityStatus\b/.test(body?.query || '')) return route.continue()
        if (body.variables.force) {
          force++
          if (release) await release.promise
        } else if (body.variables.refresh) automatic++
        await route.fulfill({
          contentType: 'application/json',
          body: JSON.stringify(
            fail
              ? { errors: [{ message: 'core_not_ready', path: ['connectivityStatus'] }] }
              : { data: { connectivityStatus: JSON.stringify(sample()) } },
          ),
        })
      })
      await page.goto(`${base}/cgi-bin/luci/admin/services/daed/dashboard`)
      const frame = page.frameLocator('iframe'),
        section = frame.locator('[data-connectivity]')
      await section.getByText('HTTP 403', { exact: true }).waitFor({ timeout: 45000 })
      assert.equal(await section.locator('[data-connectivity-target]').count(), 4)
      assert.equal(
        await section.locator('[data-connectivity-target="github"]').getByText('超时', { exact: true }).count(),
        1,
      )
      assert.ok(await section.getByText('阿里云', { exact: true }).isVisible())
      assert.equal(
        await section
          .locator('.service-logo img')
          .evaluateAll((es) =>
            es.filter((e) => getComputedStyle(e).display !== 'none').every((e) => e.complete && e.naturalWidth > 0),
          ),
        true,
      )
      assert.ok(
        (await section.locator('[data-connectivity-target]').first().boundingBox()).height <= 54,
        'Compact cards must retain readable results without extra vertical space',
      )
      assert.ok(
        await section
          .locator('.connection-history')
          .evaluateAll((es) => es.every((e) => e.firstElementChild.getBoundingClientRect().width > 0)),
        'Every narrow card must retain visible history bars next to long HTTP results',
      )
      const columns = await section
        .locator(':scope > div')
        .nth(1)
        .evaluate((e) => getComputedStyle(e).gridTemplateColumns.split(' ').length)
      await page.screenshot({ path: path.join(output, `connectivity-initial-${width}.png`), fullPage: true })
      console.log(
        JSON.stringify({
          width,
          columns,
          section: await section.boundingBox(),
          iframe: await frame.locator('body').evaluate(() => innerWidth),
        }),
      )
      assert.equal(columns, width === 1600 ? 4 : 2)
      const box = await section.boundingBox(),
        monitor = await frame.locator('#panel-monitor').boundingBox()
      assert.ok(box.y + box.height <= monitor.y + 1, 'Strip must precede both dashboard panels')
      const dimensions = await frame
        .locator('body')
        .evaluate(() => ({ scroll: document.documentElement.scrollWidth, view: document.documentElement.clientWidth }))
      assert.ok(dimensions.scroll <= dimensions.view + 1, JSON.stringify(dimensions))
      assert.ok(automatic > 0, 'Default visible dashboard must probe automatically')
      const toggle = section.getByRole('switch', { name: '自动检测' })
      await toggle.click()
      await until(async () => (await toggle.getAttribute('aria-checked')) === 'false')
      await page.reload()
      await section.waitFor()
      assert.equal(await toggle.getAttribute('aria-checked'), 'false', 'Auto preference survives refresh')
      let resolve
      release = {
        promise: new Promise((r) => {
          resolve = r
        }),
      }
      const button = section.getByRole('button', { name: '立即检测' })
      await button.click()
      await until(() => force > 0)
      assert.equal(await section.getByRole('button', { name: '检测中' }).isDisabled(), true)
      assert.equal(
        await frame.getByText('正在保存更改', { exact: true }).count(),
        0,
        'Read-only check is not a configuration save',
      )
      resolve()
      release = undefined
      await button.waitFor()
      await until(() => button.isEnabled())
      fail = true
      await button.click()
      await section.getByRole('status').waitFor()
      assert.equal(await frame.getByText('代理内核尚未就绪，请稍后重试。', { exact: true }).count(), 0)
      assert.equal(
        await section.getByText('HTTP 403', { exact: true }).count(),
        1,
        'Failed reads retain the previous sample',
      )
      fail = false
      await button.click()
      await section.getByRole('status').waitFor({ state: 'detached' })
      await page.screenshot({ path: path.join(output, `connectivity-${width}.png`), fullPage: true })
      if (width === 1600) {
        await toggle.click()
        await delay(500)
        await frame.locator('body').evaluate(() => {
          Object.defineProperty(document, 'hidden', { configurable: true, value: true })
          document.dispatchEvent(new Event('visibilitychange'))
        })
        const before = automatic
        await delay(31000)
        assert.equal(automatic, before, 'Hidden dashboards must stop scheduled network refreshes')
      }
      await page.close()
      if (context !== desktop) await context.close()
    }
    assert.deepEqual(errors, [])
    console.log(
      'Connectivity: desktop 4 columns; touch 2 columns at 390/320px; dark theme; manual/auto/persistence; single pending request; error recovery; hidden-page pause; no overflow. Passed.',
    )
  } finally {
    await browser.close()
  }
})().catch((e) => {
  console.error(e)
  process.exitCode = 1
})
