/* Native LuCI UI checks with controlled runtime/latency responses only.
 * No configuration writes, node tests or network takeover are requested. */
const fs = require('node:fs'),
  path = require('node:path'),
  assert = require('node:assert/strict'),
  { chromium } = require('playwright')
const base = process.env.DAED_PANEL_URL || 'http://localhost:18080',
  output = process.env.DAED_FEEDBACK_OUTPUT || '.build/feedback-browser'
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
    await page.goto(base + '/cgi-bin/luci/admin/services/daed/dashboard')
    await page.locator('input[name=luci_username]').fill('root')
    await page.locator('input[name=luci_password]').fill('fixture-password')
    await page.getByRole('button', { name: /Log in|登录/ }).click()
    const f = page.frameLocator('iframe')
    await f.locator('[data-workspace="dashboard"]').waitFor({ timeout: 45000 })
    const gql = async (query) => {
      const body = await (
        await context.request.post(base + '/cgi-bin/daed-router/graphql', {
          headers: { Origin: base },
          data: { query },
        })
      ).json()
      assert.ok(!body.errors, JSON.stringify(body.errors))
      return body.data
    }
    const query = 'query{nodes{edges{id tag}} subscriptions{id tag nodes{edges{id}}} groups{id name nodes{id}}}'
    const before = await gql(query),
      nodes = before.nodes.edges,
      group = before.groups.find((g) => g.name.startsWith('RegionPreview'))
    const active = nodes.find((n) => n.tag === 'HK 000').id,
      fast = nodes.find((n) => n.tag === 'JP 001').id,
      failed = nodes.find((n) => n.tag === 'SG 011').id
    const sub = before.subscriptions.find((s) => s.tag.startsWith('地区订阅预览')),
      subNode = sub.nodes.edges[0].id
    let fail = false,
      reads = 0
    await page.route('**/cgi-bin/daed-router/graphql', async (route) => {
      const q = route.request().postDataJSON().query
      assert.ok(!/\bmutation\b/.test(q), 'This check must never write resources or start tests')
      if (q.includes('coreRuntimeGroups')) {
        reads++
        return route.fulfill({
          json: fail
            ? { errors: [{ message: 'core_not_ready' }] }
            : {
                data: {
                  coreRuntimeGroups: JSON.stringify({
                    core: 'mihomo',
                    ready: true,
                    selectionAvailable: true,
                    groups: [
                      {
                        name: 'proxy',
                        nodes: ['applied-name'],
                        selected: 'applied-name',
                        selectedNodeId: active,
                        selectionKind: 'native',
                      },
                      {
                        name: 'AI',
                        nodes: ['subscription-applied-name'],
                        selectedNodeId: subNode,
                        selectionKind: 'single',
                      },
                    ],
                  }),
                },
              },
        })
      }
      if (q.includes('nodeLatencies'))
        return route.fulfill({
          json: {
            data: {
              nodeLatencies: [
                { id: active, alive: true, latencyMs: 91, testedAt: new Date().toISOString() },
                { id: fast, alive: true, latencyMs: 17, testedAt: new Date().toISOString() },
                { id: failed, alive: false, latencyMs: 0, message: 'timeout', testedAt: new Date().toISOString() },
              ],
            },
          },
        })
      return route.continue()
    })
    await page.reload()
    await f.locator('[data-workspace="dashboard"]').waitFor()
    await f.locator(`[data-group-card-id="${group.id}"]`).getByRole('button', { name: '添加节点', exact: true }).click()
    const picker = f.locator('[data-node-picker]'),
      cards = picker.locator('[data-picker-node]'),
      a = picker.locator(`[data-picker-node="${active}"]`),
      b = picker.locator(`[data-picker-node="${fast}"]`)
    await a.locator('[aria-label="当前出口: proxy"]').waitFor()
    assert.equal(await a.getAttribute('data-active'), 'true')
    assert.equal(await a.getByRole('checkbox').isChecked(), false)
    const source = picker.getByRole('combobox', { name: '节点来源', exact: true }),
      sort = picker.getByRole('combobox', { name: '节点排序', exact: true })
    await source.selectOption('manual')
    const order = await cards.evaluateAll((es) => es.map((e) => e.dataset.pickerNode))
    await b.getByRole('checkbox').check()
    await sort.selectOption('latency')
    assert.deepEqual((await cards.evaluateAll((es) => es.map((e) => e.dataset.pickerNode))).slice(0, 2), [fast, active])
    assert.ok(await b.getByRole('checkbox').isChecked())
    assert.equal(await a.getAttribute('data-active'), 'true')
    await sort.selectOption('custom')
    assert.deepEqual(await cards.evaluateAll((es) => es.map((e) => e.dataset.pickerNode)), order)
    const evidence = []
    for (const dark of [false, true]) {
      await page.emulateMedia({ colorScheme: dark ? 'dark' : 'light' })
      for (const width of [1800, 390, 320]) {
        await page.setViewportSize({ width, height: width > 500 ? 1100 : 844 })
        await page.mouse.move(1, 1)
        await page.waitForTimeout(180)
        const normal = picker.locator(`[data-picker-node="${failed}"]`)
        const color = async (el) =>
          el.evaluate((e) => {
            const c = document.createElement('canvas'),
              x = c.getContext('2d')
            x.fillStyle = getComputedStyle(e).backgroundColor
            x.fillRect(0, 0, 1, 1)
            return [...x.getImageData(0, 0, 1, 1).data].slice(0, 3)
          })
        const plain = await color(normal)
        await normal.hover()
        await page.waitForTimeout(180)
        const hover = await color(normal)
        assert.ok(Math.max(...plain.map((v, i) => Math.abs(v - hover[i]))) >= 4, JSON.stringify({ plain, hover }))
        assert.equal(await normal.evaluate((e) => getComputedStyle(e).outlineStyle), 'none')
        assert.equal(await normal.evaluate((e) => getComputedStyle(e).translate), '0px -2px')
        await page.emulateMedia({ reducedMotion: 'reduce' })
        assert.equal(await normal.evaluate((e) => getComputedStyle(e).translate), 'none')
        await page.emulateMedia({ reducedMotion: 'no-preference' })
        for (const card of [a, b]) {
          const green = await color(card)
          assert.ok(green[1] > green[0] && green[1] > green[2])
        }
        assert.ok(await picker.evaluate((e) => e.scrollWidth <= e.clientWidth + 1))
        await picker.screenshot({ path: path.join(output, `picker-${dark ? 'dark' : 'light'}-${width}.png`) })
        evidence.push({ width, dark, plain, hover })
      }
    }
    await picker.getByRole('button', { name: '取消', exact: true }).click()
    await page.setViewportSize({ width: 1800, height: 1100 })
    await page.goto(base + '/cgi-bin/luci/admin/services/daed/resources')
    await f.locator('[data-workspace="resources"]').waitFor()
    const manual = f.locator('.panel-node-row').first()
    await manual.hover()
    await page.waitForTimeout(220)
    const gutter = await manual.evaluate((e) => {
      const r = e.getBoundingClientRect(),
        v = e.closest('[data-node-scroll]').getBoundingClientRect(),
        s = getComputedStyle(e)
      return {
        top: r.top - v.top,
        left: r.left - v.left,
        right: v.right - r.right,
        outline: s.outlineStyle,
        height: r.height,
        translate: s.translate,
      }
    })
    assert.ok(gutter.top >= 3 && gutter.left >= 5 && gutter.right >= 5, JSON.stringify(gutter))
    assert.equal(gutter.height, 92)
    assert.equal(gutter.outline, 'none')
    assert.equal(gutter.translate, '0px -2px')
    await page.mouse.move(1, 1)
    await page.waitForTimeout(220)
    assert.equal(await manual.evaluate((e) => getComputedStyle(e).translate), 'none')
    await manual.locator('[data-node-details]').focus()
    assert.equal(await manual.locator('[data-node-details]').evaluate((e) => getComputedStyle(e).outlineWidth), '2px')
    await f
      .locator(`[data-subscription-card="subscription-${sub.id}"]`)
      .getByRole('button', { name: '查看节点', exact: true })
      .click()
    const viewer = f.locator('[data-subscription-browser]'),
      current = viewer.locator(`[data-subscription-node="${subNode}"]`)
    await current.locator('[aria-label="当前出口: AI"]').waitFor()
    assert.equal(await current.getAttribute('data-active'), 'true')
    const search = viewer.locator('.subscription-search-row input')
    await search.focus()
    assert.equal(await search.evaluate((e) => getComputedStyle(e).outlineStyle), 'none')
    assert.equal(await search.evaluate((e) => getComputedStyle(e.closest('label')).outlineWidth), '2px')
    await viewer.screenshot({ path: path.join(output, 'subscription-current.png') })
    fail = true
    await page.reload()
    await f.locator('[data-workspace="resources"]').waitFor()
    await f
      .locator(`[data-subscription-card="subscription-${sub.id}"]`)
      .getByRole('button', { name: '查看节点', exact: true })
      .click()
    await f.locator('[data-subscription-browser] [data-subscription-node]').first().waitFor()
    assert.equal(
      await f.locator('[data-subscription-node][data-active="true"]').count(),
      0,
      'Failed reads must clear old current exits',
    )
    assert.deepEqual(await gql(query), before)
    assert.deepEqual(errors, [])
    fs.writeFileSync(
      path.join(output, 'evidence.json'),
      JSON.stringify(
        {
          controlledRuntime: true,
          controlledLatencies: true,
          sortAndSelectionPreserved: true,
          currentReadFailureCleared: true,
          resourcesUnchanged: true,
          reads,
          evidence,
          manualViewport: gutter,
          errors,
        },
        null,
        2,
      ),
    )
    console.log(
      'PASS: soft hover without outer ring, lift/reset/reduced motion, unclipped manual rows, keyboard focus, current/checked green states, sorting, light/dark 1800/390/320, resources preserved',
    )
  } finally {
    await browser.close()
  }
})().catch((e) => {
  console.error(e)
  process.exitCode = 1
})
