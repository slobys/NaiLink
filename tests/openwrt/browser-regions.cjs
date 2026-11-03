/* Region hints and card-state presentation against native LuCI + real RPC.
 * Node data is isolated fixture data; this does not validate exit locations. */
const assert = require('node:assert/strict')
const fs = require('node:fs'),
  path = require('node:path'),
  { chromium } = require('playwright')
const base = process.env.DAED_PANEL_URL || 'http://localhost:18080'
const output = process.env.DAED_REGION_OUTPUT || '.build/region-browser'
;(async () => {
  fs.mkdirSync(output, { recursive: true })
  const browser = await chromium.launch({
    headless: true,
    ...(process.env.DAED_BROWSER_EXECUTABLE ? { executablePath: process.env.DAED_BROWSER_EXECUTABLE } : {}),
  })
  try {
    const context = await browser.newContext({ viewport: { width: 1800, height: 1100 }, locale: 'zh-CN' })
    const page = await context.newPage(),
      errors = []
    page.on('pageerror', (e) => errors.push(e.message))
    if (process.env.DAED_RESOURCE_PREVIEW_DIST) {
      const dist = path.resolve(process.env.DAED_RESOURCE_PREVIEW_DIST)
      await page.route('**/cgi-bin/daed-router/**', (route) => {
        if (route.request().method() !== 'GET') return route.continue()
        const file = path.resolve(
          dist,
          new URL(route.request().url()).pathname.split('/cgi-bin/daed-router/')[1] || 'index.html',
        )
        return file.startsWith(dist + path.sep) && fs.existsSync(file)
          ? route.fulfill({ path: file })
          : route.continue()
      })
    }
    await page.goto(base + '/cgi-bin/luci/admin/services/daed/dashboard')
    await page.locator('input[name=luci_username]').fill('root')
    await page.locator('input[name=luci_password]').fill('fixture-password')
    await page.getByRole('button', { name: /Log in|登录/ }).click()
    const f = page.frameLocator('iframe')
    await f.locator('[data-workspace="dashboard"]').waitFor({ timeout: 45000 })
    const gql = async (query, variables = {}) => {
      const b = await (
        await context.request.post(base + '/cgi-bin/daed-router/graphql', {
          headers: { Origin: base },
          data: { query, variables },
        })
      ).json()
      assert.ok(!b.errors, JSON.stringify(b.errors))
      return b.data
    }
    const examples = [
      ['HK 000', 'hk'],
      ['香港800 专线 BGP', 'hk'],
      ['JP 001', 'jp'],
      ['SG 011', 'sg'],
      ['TW 001', 'tw'],
      ['US 013', 'us'],
      ['英国751 中继', 'gb'],
      ['俄罗斯752 中继', 'ru'],
      ['马来753 中继', 'my'],
      ['韩国754 中继', 'kr'],
      ['阿根廷756 中继', 'ar'],
      ['土耳其757 中继', 'tr'],
      ['20260629-hk', 'hk'],
      ['hysteria2-hk1', 'hk'],
      ['🇭🇰 香港节点', 'hk'],
      ['官网 example.test', 'unknown'],
      ['剩余流量 19GB', 'unknown'],
      ['HK → JP 双地区', 'unknown'],
      ['香港 专线 完整名称查看 '.repeat(6), 'hk'],
    ]
    const existing = (await gql('query{nodes{edges{id tag link}}}')).nodes.edges
    const imported = []
    for (const [i, [tag]] of examples.entries()) {
      const link = `socks5://192.0.2.99:${4000 + i}#Region-${i}`,
        found = existing.find((n) => n.link === link && n.tag === tag)
      imported.push(
        found?.id ||
          (
            await gql('mutation($args:[ImportArgument!]!){importNodes(rollbackError:true,args:$args){node{id}}}', {
              args: [{ tag, link }],
            })
          ).importNodes[0].node.id,
      )
    }
    const stamp = Date.now()
    const group = (await gql(`mutation{createGroup(name:"RegionPreview${stamp}",policy:fixed,policyParams:[]){id}}`))
      .createGroup.id
    const sub = (
      await gql(
        `mutation{importSubscription(rollbackError:true,arg:{tag:"地区订阅预览${stamp}",link:"http://127.0.0.1:18081/sub-layout?region=${stamp}"}){sub{id}}}`,
      )
    ).importSubscription.sub.id
    const query = 'query{nodes{edges{id name tag}} subscriptions{id tag link} groups{id nodes{id}}}'
    const baseline = await gql(query)
    await page.reload()
    await f.locator('[data-workspace="dashboard"]').waitFor()
    await page.addStyleTag({ content: '.container{width:1500px!important;max-width:calc(100vw - 40px)!important}' })
    await f.locator(`[data-group-card-id="${group}"]`).getByRole('button', { name: '添加节点', exact: true }).click()
    const picker = f.locator('[data-node-picker]')
    await picker.getByRole('combobox', { name: '节点来源', exact: true }).selectOption('manual')
    for (const [index, [, region]] of examples.entries()) {
      const card = picker.locator(`[data-picker-node="${imported[index]}"]`)
      assert.equal(await card.locator('[data-node-region]').getAttribute('data-node-region'), region)
    }
    await picker.locator('img[data-node-region]').evaluateAll((es) => Promise.all(es.map((e) => e.decode())))
    const first = picker.locator(`[data-picker-node="${imported[0]}"]`)
    await first.getByRole('checkbox').check()
    await picker.locator(`[data-picker-node="${imported[1]}"]`).getByRole('checkbox').check()
    const colorEvidence = []
    for (const dark of [false, true]) {
      await page.emulateMedia({ colorScheme: dark ? 'dark' : 'light' })
      for (const width of [1800, 390, 320]) {
        await page.setViewportSize({ width, height: width > 500 ? 1100 : 844 })
        await page.waitForTimeout(200)
        assert.ok(await picker.evaluate((e) => e.scrollWidth <= e.clientWidth + 1))
        const result = await first.evaluate((e) => {
          const canvas = document.createElement('canvas')
          canvas.width = canvas.height = 1
          const ctx = canvas.getContext('2d')
          const rgb = (color) => {
            ctx.fillStyle = color
            ctx.fillRect(0, 0, 1, 1)
            return [...ctx.getImageData(0, 0, 1, 1).data].slice(0, 3)
          }
          const lum = (color) =>
            rgb(color)
              .map((v) => {
                v /= 255
                return v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4
              })
              .reduce((n, v, i) => n + v * [0.2126, 0.7152, 0.0722][i], 0)
          const bg = getComputedStyle(e).backgroundColor,
            b = lum(bg)
          const contrast = (selector) => {
            const a = lum(getComputedStyle(e.querySelector(selector)).color)
            return (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05)
          }
          return {
            background: rgb(bg),
            name: contrast('.picker-node-select'),
            meta: contrast('.picker-node-meta'),
            normal: rgb(getComputedStyle(e.parentElement.querySelector('[data-selected=false]')).backgroundColor),
          }
        })
        assert.ok(
          result.background[1] > result.background[0] && result.background[1] > result.background[2],
          'Selected cards must be green in both themes',
        )
        assert.ok(result.name >= 4.5 && result.meta >= 4.5, JSON.stringify(result))
        assert.notDeepEqual(result.background, result.normal)
        colorEvidence.push({ dark, width, ...result })
        await picker.screenshot({ path: path.join(output, `picker-${dark ? 'dark' : 'light'}-${width}.png`) })
      }
    }
    await picker.getByRole('button', { name: '取消', exact: true }).click()
    await page.setViewportSize({ width: 1800, height: 1100 })
    await page.goto(base + '/cgi-bin/luci/admin/services/daed/resources')
    await f.locator('[data-workspace="resources"]').waitFor()
    await page.addStyleTag({ content: '.container{width:1500px!important;max-width:calc(100vw - 40px)!important}' })
    await f
      .locator(`[data-subscription-card="subscription-${sub}"]`)
      .getByRole('button', { name: '查看节点', exact: true })
      .click()
    const viewer = f.locator('[data-subscription-browser]')
    assert.equal(await viewer.locator('[data-subscription-node]').count(), 72)
    assert.equal((await viewer.locator('[data-node-region=hk]').count()) > 0, true)
    await viewer.locator('img[data-node-region]').evaluateAll((es) => Promise.all(es.map((e) => e.decode())))
    for (const dark of [false, true]) {
      await page.emulateMedia({ colorScheme: dark ? 'dark' : 'light' })
      for (const width of [1800, 390, 320]) {
        await page.setViewportSize({ width, height: width > 500 ? 1100 : 844 })
        await page.waitForTimeout(150)
        assert.ok(await viewer.evaluate((e) => e.scrollWidth <= e.clientWidth + 1))
        await viewer.screenshot({ path: path.join(output, `subscription-${dark ? 'dark' : 'light'}-${width}.png`) })
      }
    }
    await viewer.getByRole('button', { name: 'Close', exact: true }).click()
    assert.deepEqual(
      await gql(query),
      baseline,
      'Region icons and local selection must not rename nodes or add bindings',
    )
    assert.deepEqual(errors, [])
    fs.writeFileSync(
      path.join(output, 'evidence.json'),
      JSON.stringify({ flags: true, colorEvidence, resourcesUnchanged: true, errors }, null, 2),
    )
    console.log(
      'PASS: local region flags, unknown/multi-region fallback, real green checked states, contrast>=4.5, 1800/390/320 light/dark, unchanged resource names/bindings',
    )
  } finally {
    await browser.close()
  }
})().catch((e) => {
  console.error(e)
  process.exitCode = 1
})
