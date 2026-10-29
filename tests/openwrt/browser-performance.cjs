/* Native API-only fixture: saved edits must never masquerade as the applied outlet. */
const { chromium } = require('playwright')
const assert = require('node:assert/strict')
const fs = require('node:fs')
const base = process.env.DAED_PANEL_URL || 'http://localhost:18080'
;(async () => {
  const browser = await chromium.launch({
    headless: true,
    ...(process.env.DAED_BROWSER_EXECUTABLE ? { executablePath: process.env.DAED_BROWSER_EXECUTABLE } : {}),
  })
  try {
    const context = await browser.newContext({
      viewport: { width: 1400, height: 1000 },
      locale: 'zh-CN',
      hasTouch: true,
    })
    const page = await context.newPage()
    const errors = []
    page.on('pageerror', (error) => errors.push(error.message))
    await page.goto(base + '/cgi-bin/luci/admin/services/daed')
    if (await page.locator('input[name=luci_username]').count()) {
      await page.locator('input[name=luci_username]').fill('root')
      await page.locator('input[name=luci_password]').fill('fixture-password')
      await page.getByRole('button', { name: /Log in|登录/ }).click()
    }
    const frame = page.frameLocator('iframe')
    await frame.getByRole('button', { name: '主题', exact: true }).waitFor()
    const graph = async (query, variables = {}, allowError = false) => {
      const response = await context.request.post(base + '/cgi-bin/daed-router/graphql', {
        headers: { Origin: base },
        data: { query, variables },
      })
      const result = await response.json()
      if (!allowError) assert.ok(!result.errors, JSON.stringify(result.errors))
      return result
    }
    const groups = (await graph('{groups{id name nodes{id}}}')).data.groups
    const proxy = groups.find((group) => group.name === 'proxy')
    assert.ok(proxy)
    const suffix = Date.now().toString(36)
    const names = ['Applied-' + suffix, 'Pending-' + suffix]
    const ids = (
      await graph('mutation($args:[ImportArgument!]!){importNodes(rollbackError:true,args:$args){node{id}}}', {
        args: names.map((tag, index) => ({
          tag,
          link: `vless://33333333-3333-4333-8333-00000000000${index}@192.0.2.${index + 1}:443?encryption=none#Fixture`,
        })),
      })
    ).data.importNodes.map((entry) => entry.node.id)
    const initial = new Set(proxy.nodes.map((node) => node.id))
    const rejected = await graph(
      'mutation($id:ID!,$ids:[ID!]!){groupAddNodes(id:$id,nodeIDs:$ids)}',
      { id: proxy.id, ids: [ids[0], Buffer.from('cursor99999999').toString('base64').replace(/=+$/, '')] },
      true,
    )
    assert.ok(rejected.errors && /节点已变化/.test(rejected.errors[0].message))
    const afterReject = (await graph('{groups{id nodes{id}}}')).data.groups.find((group) => group.id === proxy.id)
    assert.deepEqual(new Set(afterReject.nodes.map((node) => node.id)), initial)
    if (initial.size)
      await graph('mutation($id:ID!,$ids:[ID!]!){groupDelNodes(id:$id,nodeIDs:$ids)}', {
        id: proxy.id,
        ids: [...initial],
      })
    await graph('mutation($id:ID!,$ids:[ID!]!){groupAddNodes(id:$id,nodeIDs:$ids)}', { id: proxy.id, ids: [ids[0]] })
    await graph('mutation{run(dry:false)}')
    const applied = JSON.parse((await graph('{coreRuntimeGroups}')).data.coreRuntimeGroups)
    assert.ok(applied.groups.find((group) => group.name === 'proxy').nodes.includes(names[0]))
    await graph('mutation($id:ID!,$ids:[ID!]!){groupAddNodes(id:$id,nodeIDs:$ids)}', { id: proxy.id, ids: [ids[1]] })
    await graph('mutation($id:ID!,$ids:[ID!]!){groupDelNodes(id:$id,nodeIDs:$ids)}', { id: proxy.id, ids: [ids[0]] })
    const pending = JSON.parse((await graph('{coreRuntimeGroups general{dae{modified}}}')).data.coreRuntimeGroups)
    assert.deepEqual(pending.groups.find((group) => group.name === 'proxy').nodes, [names[0]])
    const results = []
    for (const width of [1400, 390, 320]) {
      await page.setViewportSize({ width, height: 1000 })
      await page.reload()
      await frame.getByRole('button', { name: '主题', exact: true }).waitFor()
      const card = frame.locator(`[data-group-card-id="${proxy.id}"]`)
      const trigger = card.getByRole('button', { name: '查看生效出口', exact: true })
      if (width < 500) {
        await trigger.scrollIntoViewIfNeeded()
        const box = await trigger.boundingBox()
        await page.touchscreen.tap(box.x + box.width / 2, box.y + box.height / 2)
      } else await trigger.click()
      const content = frame.locator('[data-group-runtime="proxy"]')
      await content.getByText(names[0], { exact: true }).waitFor()
      assert.ok(!(await content.innerText()).includes(names[1]))
      assert.ok(
        await content.evaluate(
          (element) =>
            element.getBoundingClientRect().right <= innerWidth + 1 && element.getBoundingClientRect().left >= 0,
        ),
      )
      await page.screenshot({ path: `.build/perf57-outlet-${width}.png`, fullPage: true })
      await page.keyboard.press('Escape')
      results.push({ width, onlyAppliedNodeShown: true, popupFits: true })
    }
    const response = frame.getByRole('button', { name: '应用配置', exact: true })
    await response.click()
    await frame.getByRole('button', { name: '应用配置', exact: true }).waitFor({ state: 'hidden' })
    const final = JSON.parse((await graph('{coreRuntimeGroups}')).data.coreRuntimeGroups)
    assert.deepEqual(final.groups.find((group) => group.name === 'proxy').nodes, [names[1]])
    // Exercise the real picker and its post-save membership verification.
    await frame
      .locator(`[data-group-card-id="${proxy.id}"]`)
      .getByRole('button', { name: '添加节点', exact: true })
      .first()
      .click()
    const dialog = frame.getByRole('dialog')
    await dialog.locator(`[data-picker-node="${ids[0]}"]`).getByRole('checkbox').check()
    await dialog.getByRole('button', { name: /添加所选节点/ }).click()
    await dialog.waitFor({ state: 'hidden' })
    const saved = (await graph('{groups{id nodes{id}}}')).data.groups.find((group) => group.id === proxy.id)
    assert.deepEqual(new Set(saved.nodes.map((node) => node.id)), new Set(ids))
    const stillApplied = JSON.parse((await graph('{coreRuntimeGroups}')).data.coreRuntimeGroups)
    assert.deepEqual(stillApplied.groups.find((group) => group.name === 'proxy').nodes, [names[1]])
    assert.deepEqual(errors, [])
    fs.writeFileSync(
      '.build/perf57-browser.json',
      JSON.stringify(
        {
          results,
          staleIdRejected: true,
          unappliedEditsExcluded: true,
          appliedThroughUI: true,
          joinedThroughUI: true,
          errors,
        },
        null,
        2,
      ),
    )
    console.log('Desktop and mobile: stale IDs rejected; pending edits excluded; UI application confirmed in runtime')
    await context.close()
  } finally {
    await browser.close()
  }
})().catch((error) => {
  console.error(error)
  process.exit(1)
})
