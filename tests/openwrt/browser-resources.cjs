const assert = require('node:assert/strict')
const fs = require('node:fs')
const path = require('node:path')
/* Native LuCI resources regression; optional static preview, real daemon API throughout. */
const process = require('node:process')
const { chromium } = require('playwright')
const output = process.env.DAED_RESOURCE_OUTPUT || '.build/resource-browser'
const base = process.env.DAED_PANEL_URL || 'http://localhost:18080'
fs.mkdirSync(output, { recursive: true })
;(async () => {
  const browser = await chromium.launch({
    headless: true,
    ...(process.env.DAED_BROWSER_EXECUTABLE ? { executablePath: process.env.DAED_BROWSER_EXECUTABLE } : {}),
  })
  let page
  try {
    const context = await browser.newContext({ viewport: { width: 1800, height: 1100 }, locale: 'zh-CN' })
    page = await context.newPage()
    const errors = []
    page.on('pageerror', (e) => errors.push(e.message))
    page.on('console', (m) => {
      if (m.type() === 'error' && /@hello-pangea|Invariant|nested scroll/.test(m.text())) errors.push(m.text())
    })
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
    await page.goto(`${base}/cgi-bin/luci/admin/services/daed`)
    if (await page.locator('input[name=luci_username]').count()) {
      await page.locator('input[name=luci_username]').fill('root')
      await page.locator('input[name=luci_password]').fill('fixture-password')
      await page.getByRole('button', { name: /Log in|登录/ }).click()
    }
    const f = page.frameLocator('iframe')
    await f.getByRole('button', { name: '主题', exact: true }).waitFor()
    const gql = async (query, variables = {}) => {
      const body = await (
        await context.request.post(`${base}/cgi-bin/daed-router/graphql`, {
          headers: { Origin: base },
          data: { query, variables },
        })
      ).json()
      assert.ok(!body.errors, JSON.stringify(body.errors))
      return body.data
    }
    const prefix = `Dense${Date.now()}`
    const ids = (
      await gql('mutation($args:[ImportArgument!]!){importNodes(rollbackError:true,args:$args){node{id}}}', {
        args: Array.from({ length: 40 }, (_, i) => ({
          tag: `${prefix}-${String(i).padStart(2, '0')}${i === 0 ? ` · ${'香港LongNodeName'.repeat(12)}` : ''}`,
          link: `socks5://192.0.2.90:${String(3000 + i)}#Dense-${i}`,
        })),
      })
    ).importNodes.map((x) => x.node.id)
    const group = (
      await gql('mutation($name:String!){createGroup(name:$name,policy:fixed,policyParams:[]){id}}', { name: prefix })
    ).createGroup.id
    const navigate = async (section) => {
      await page.goto(`${base}/cgi-bin/luci/admin/services/daed/${section}`)
      await f.getByRole('button', { name: '主题', exact: true }).waitFor()
      await page.addStyleTag({ content: '.container{width:1500px!important;max-width:calc(100vw - 40px)!important}' })
    }
    await navigate('dashboard')
    await f.locator(`[data-group-card-id="${group}"]`).getByRole('button', { name: '添加节点', exact: true }).click()
    const picker = f.locator('[data-node-picker]')
    const list = picker.locator('[data-picker-list]')
    await picker.getByRole('searchbox').fill(prefix)
    const first = picker.locator(`[data-picker-node="${ids[0]}"]`)
    const name = first.locator('.picker-node-select')
    for (const [width, columns] of [
      [1800, 7],
      [1100, 5],
      [800, 3],
      [390, 2],
    ]) {
      await page.setViewportSize({ width, height: 1100 })
      await page.waitForTimeout(250)
      assert.equal(await list.evaluate((e) => getComputedStyle(e).gridTemplateColumns.split(' ').length), columns)
      assert.ok(await picker.evaluate((e) => e.scrollWidth <= e.clientWidth + 1))
    }
    await page.setViewportSize({ width: 1800, height: 1100 })
    await page.waitForTimeout(250)
    const normal = await first.evaluate((e) => getComputedStyle(e).backgroundColor)
    await first.hover({ position: { x: 3, y: 3 } })
    await page.waitForTimeout(200)
    assert.notEqual(await first.evaluate((e) => getComputedStyle(e).backgroundColor), normal)
    assert.equal(await name.locator('.node-name').getAttribute('data-overflow'), 'true')
    assert.equal(
      await name.locator('.node-name-text').evaluate((e) => getComputedStyle(e).animationName),
      'node-name-pan',
    )
    await page.waitForTimeout(1800)
    assert.notEqual(
      await name.locator('.node-name-text').evaluate((e) => getComputedStyle(e).transform),
      'matrix(1, 0, 0, 1, 0, 0)',
    )
    await page.mouse.move(0, 0)
    assert.equal(
      await name.locator('.node-name-text').evaluate((e) => getComputedStyle(e).animationName),
      'none',
      'Leaving the whole card restores its name',
    )
    assert.equal(await name.locator('.node-name-text').evaluate((e) => getComputedStyle(e).transform), 'none')
    await first.hover({ position: { x: 3, y: 3 } })
    await page.emulateMedia({ reducedMotion: 'reduce' })
    assert.equal(await name.locator('.node-name-text').evaluate((e) => getComputedStyle(e).animationName), 'none')
    await page.emulateMedia({ reducedMotion: 'no-preference' })
    await name.click()
    assert.ok(await first.getByRole('checkbox').isChecked())
    assert.equal(await first.getAttribute('data-selected'), 'true')
    await picker.locator(`[data-picker-node="${ids[1]}"]`).click({ position: { x: 5, y: 5 } })
    await picker.getByRole('button', { name: '只看已选', exact: true }).click()
    assert.equal(await list.locator('[data-picker-node]').count(), 2)
    await picker.getByRole('button', { name: '只看已选', exact: true }).click()
    for (const width of [1800, 390, 320]) {
      await page.setViewportSize({ width, height: width === 1800 ? 1100 : 844 })
      await page.waitForTimeout(150)
      await picker.screenshot({ path: path.join(output, `picker-selected-${width}.png`) })
      assert.ok(await picker.evaluate((e) => e.scrollWidth <= e.clientWidth + 1))
    }
    await page.setViewportSize({ width: 1800, height: 1100 })
    await picker.getByRole('button', { name: '清空选择', exact: true }).click()
    await name.click()
    await first.getByRole('checkbox').uncheck()
    assert.equal(await first.getAttribute('data-selected'), 'false')
    await page.keyboard.press('Tab')
    await name.focus()
    assert.equal(await name.evaluate((e) => getComputedStyle(e).outlineStyle), 'solid')
    await picker.screenshot({ path: path.join(output, 'picker-four-columns.png') })
    await page.keyboard.press('Escape')
    process.stdout.write(
      'PASS: picker 7/5/3/2 columns, actual overflow-only name motion, reduced-motion opt-out, hover/focus/checked states\n',
    )
    await navigate('resources')
    const manual = f.locator('#workspace-nodes')
    const scroll = manual.locator('[data-node-scroll]')
    await manual.getByRole('searchbox').fill(prefix)
    await manual.locator('.manual-node-columns[data-columns="2"]').waitFor()
    const left = manual.locator('[data-rfd-droppable-id="node-list"]')
    const right = manual.locator('[data-rfd-droppable-id="node-list-column-1"]')
    assert.equal(await right.getAttribute('data-group-column-offset'), '20')
    assert.ok(Math.abs((await left.boundingBox()).y - (await right.boundingBox()).y) < 2)
    const card = left.locator('[data-node-id]').first()
    const cardBox = await card.boundingBox()
    assert.ok(cardBox.height <= 100, 'Desktop node cards should keep a compact visual height')
    const nextCardBox = await left.locator('[data-node-id]').nth(1).boundingBox()
    const nameBox = await card.locator('[data-node-details]').boundingBox()
    const probeBox = await card.locator('.panel-node-probe').boundingBox()
    const joinBox = await card.locator('.panel-node-join').boundingBox()
    assert.ok(nextCardBox.y - cardBox.y - cardBox.height >= 10, 'Cards need a visible vertical gap')
    const dragMargin = await card.evaluate((element) => Number.parseFloat(getComputedStyle(element).marginBottom))
    assert.ok(
      Math.abs(nextCardBox.y - cardBox.y - cardBox.height - dragMargin) < 1,
      'Drag measurements must include the card gap',
    )
    const contrast = await card.locator('.panel-node-protocol').evaluate((element) => {
      const style = getComputedStyle(element)
      const canvas = document.createElement('canvas')
      canvas.width = canvas.height = 1
      const context = canvas.getContext('2d')
      const luminance = (color) => {
        context.fillStyle = color
        context.fillRect(0, 0, 1, 1)
        const channels = [...context.getImageData(0, 0, 1, 1).data].slice(0, 3).map((value) => {
          const srgb = value / 255
          return srgb <= 0.04045 ? srgb / 12.92 : ((srgb + 0.055) / 1.055) ** 2.4
        })
        return channels[0] * 0.2126 + channels[1] * 0.7152 + channels[2] * 0.0722
      }
      const foreground = luminance(style.color)
      const background = luminance(style.backgroundColor)
      return (Math.max(foreground, background) + 0.05) / (Math.min(foreground, background) + 0.05)
    })
    assert.ok(contrast >= 4.5, `Small protocol labels must remain readable (contrast ${contrast})`)
    assert.ok(nameBox.width > cardBox.width * 0.55, 'Names should retain most of the card width')
    assert.ok(probeBox.y > nameBox.y + nameBox.height, 'Actions must not squeeze the name into a side column')
    assert.ok(Math.abs(joinBox.y - probeBox.y) < 2, 'Test and join actions should share a separate row')
    const allManual = (await gql('query{nodes{edges{id}}}')).nodes.edges.map((x) => x.id)
    const persisted = async () =>
      [
        ...new Set([
          ...(await f
            .locator('body')
            .evaluate(() => JSON.parse(localStorage.getItem('APP_STATEnodeSortableKeys')) || [])),
          ...allManual,
        ]),
      ].filter((id) => allManual.includes(id))
    const selected = async () => (await persisted()).filter((id) => ids.includes(id))
    const keyDrag = async (handle, key) => {
      await handle.scrollIntoViewIfNeeded()
      await handle.focus()
      await page.keyboard.press('Space')
      await page.waitForTimeout(200)
      await page.keyboard.press(key)
      await page.waitForTimeout(200)
      await page.keyboard.press('Space')
      await page.waitForTimeout(400)
    }
    let before = await selected()
    await keyDrag(left.locator('[data-rfd-drag-handle-draggable-id]').first(), 'ArrowRight')
    let after = await selected()
    assert.equal(after[20], before[0], 'Keyboard drag into right column must map to full-library index')
    const hiddenBefore = (await persisted()).filter((id) => !ids.includes(id))
    before = after
    await keyDrag(right.locator('[data-rfd-drag-handle-draggable-id]').first(), 'ArrowLeft')
    after = await selected()
    assert.equal(after[0], before[20])
    assert.deepEqual(
      (await persisted()).filter((id) => !ids.includes(id)),
      hiddenBefore,
    )
    const handle = right.locator('[data-rfd-drag-handle-draggable-id]').first()
    const target = left.locator('[data-node-id]').nth(2)
    const dragged = (await handle.getAttribute('data-rfd-drag-handle-draggable-id')).replace('node-', '')
    await handle.scrollIntoViewIfNeeded()
    await target.scrollIntoViewIfNeeded()
    const a = await handle.boundingBox()
    const b = await target.boundingBox()
    await page.mouse.move(a.x + a.width / 2, a.y + a.height / 2)
    await page.mouse.down()
    await page.mouse.move(a.x + a.width / 2 + 8, a.y + a.height / 2 + 8, { steps: 5 })
    await page.waitForTimeout(150)
    await page.mouse.move(b.x + b.width / 2, b.y + b.height / 2, { steps: 20 })
    await page.waitForTimeout(200)
    await page.mouse.up()
    await page.waitForTimeout(400)
    assert.ok((await selected()).indexOf(dragged) < 20, 'Pointer drag must reorder across columns')
    assert.equal(new Set(await selected()).size, 40)
    await scroll.evaluate((e) => (e.scrollTop = e.scrollHeight))
    await page.waitForTimeout(200)
    assert.ok((await manual.locator('[data-node-id]').count()) < 40, 'Both columns must retain bounded virtualization')
    assert.ok(await manual.locator(`[data-node-id="${ids[39]}"]`).isVisible())
    await scroll.evaluate((e) => (e.scrollTop = 0))
    await page.screenshot({ path: path.join(output, 'manual-two-columns.png'), fullPage: true })
    const order = await selected()
    await page.reload()
    await manual.getByRole('searchbox').fill(prefix)
    assert.deepEqual(await selected(), order)
    assert.ok(
      (await gql('query{groups{id nodes{id}}}')).groups.find((x) => x.id === group).nodes.length === 0,
      'UI selection and sorting must not add bindings',
    )
    assert.deepEqual(errors, [])
    process.stdout.write(
      'PASS: two real virtual columns, keyboard and pointer cross-column sorting, filtered hidden order, reload persistence and bounded DOM\n',
    )
  } catch (e) {
    if (page) await page.screenshot({ path: path.join(output, 'failure.png'), fullPage: true })
    throw e
  } finally {
    await browser.close()
  }
})().catch((e) => {
  console.error(e)
  process.exit(1)
})
