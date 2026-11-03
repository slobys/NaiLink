/* Native subscription presentation, menus and touch-name regression. */
const assert = require('node:assert/strict')
const fs = require('node:fs')
const path = require('node:path')
const process = require('node:process')
const { chromium } = require('playwright')
const output = process.env.DAED_SUB_OUTPUT || '.build/subscription-browser'
const base = process.env.DAED_PANEL_URL || 'http://localhost:18080'
fs.mkdirSync(output, { recursive: true })
async function preview(page) {
  if (!process.env.DAED_RESOURCE_PREVIEW_DIST) return
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
;(async () => {
  const browser = await chromium.launch({
    headless: true,
    ...(process.env.DAED_BROWSER_EXECUTABLE ? { executablePath: process.env.DAED_BROWSER_EXECUTABLE } : {}),
  })
  let page
  try {
    const context = await browser.newContext({ viewport: { width: 1800, height: 1100 }, locale: 'zh-CN' })
    page = await context.newPage()
    await preview(page)
    const errors = []
    page.on('pageerror', (e) => errors.push(e.message))
    await page.goto(`${base}/cgi-bin/luci/admin/services/daed`)
    await page.locator('input[name=luci_username]').fill('root')
    await page.locator('input[name=luci_password]').fill('fixture-password')
    await page.getByRole('button', { name: /Log in|登录/ }).click()
    const f = page.frameLocator('iframe')
    await f.getByRole('button', { name: '主题', exact: true }).waitFor()
    const gql = async (query, variables = {}) => {
      const b = await (
        await context.request.post(`${base}/cgi-bin/daed-router/graphql`, {
          headers: { Origin: base },
          data: { query, variables },
        })
      ).json()
      assert.ok(!b.errors, JSON.stringify(b.errors))
      return b.data
    }
    const prefix = `Comfort${Date.now()}`
    const subs = []
    for (const [index, label] of ['主力订阅', '备用线路', '长名称订阅 · '.repeat(12)].entries()) {
      subs.push(
        (
          await gql('mutation($arg:ImportArgument!){importSubscription(rollbackError:true,arg:$arg){sub{id}}}', {
            arg: {
              tag: label + prefix,
              link: `http://127.0.0.1:18081/${index === 0 ? 'sub-layout' : 'sub-a'}?comfort=${prefix}-${index}`,
            },
          })
        ).importSubscription.sub.id,
      )
    }
    const longManual = (
      await gql('mutation($args:[ImportArgument!]!){importNodes(rollbackError:true,args:$args){node{id}}}', {
        args: [
          {
            tag: `${prefix} · ${'香港专线完整名称 LongNodeName '.repeat(9)}`,
            link: 'socks5://192.0.2.80:5888#Comfort-node',
          },
        ],
      })
    ).importNodes[0].node.id
    const group = (
      await gql('mutation($name:String!){createGroup(name:$name,policy:fixed,policyParams:[]){id}}', { name: prefix })
    ).createGroup.id
    const query =
      'query{nodes{edges{id name tag}} subscriptions{id tag link cronExp cronEnable nodes{edges{id}}} groups{id nodes{id} subscriptions{subscription{id} nameFilterRegex}}}'
    const baseline = await gql(query)
    const navigate = async (section) => {
      await page.goto(`${base}/cgi-bin/luci/admin/services/daed/${section}`)
      await f.getByRole('button', { name: '主题', exact: true }).waitFor()
      await page.addStyleTag({ content: '.container{width:1500px!important;max-width:calc(100vw - 40px)!important}' })
    }
    await navigate('resources')
    await f.getByRole('button', { name: '主题', exact: true }).click()
    const theme = f.locator('[data-theme-picker]')
    await theme.getByRole('button', { name: '晴空', exact: true }).click()
    await theme.getByRole('button', { name: 'Close', exact: true }).click()
    const card = f.locator(`[data-subscription-card="subscription-${subs[0]}"]`)
    await card.waitFor()
    assert.ok((await card.boundingBox()).height < 180, 'Subscription sources should show a compact overview')
    assert.equal(
      await card.getByRole('button', { name: '点击查看', exact: true }).count(),
      0,
      'Sensitive link remains in collapsed details',
    )
    await card.getByRole('button', { name: '订阅详情', exact: true }).click()
    const details = card.locator('.subscription-card-details')
    assert.ok(await details.locator('code').isVisible())
    await details.getByRole('button', { name: '点击查看', exact: true }).click()
    assert.ok(
      await details.getByText(`http://127.0.0.1:18081/sub-layout?comfort=${prefix}-0`, { exact: true }).isVisible(),
    )
    await card.getByRole('button', { name: '订阅详情', exact: true }).click()
    assert.equal(await details.count(), 0)
    const menu = card.getByRole('button', { name: /^更多操作 / })
    await menu.click()
    await f.getByRole('menuitem', { name: '编辑', exact: true }).click()
    await f.getByRole('dialog').getByRole('textbox', { name: /标签/ }).waitFor()
    await page.keyboard.press('Escape')
    await f.getByRole('dialog').waitFor({ state: 'hidden' })
    await menu.click()
    await f.getByRole('menuitem', { name: '查看二维码', exact: true }).click()
    await f.getByRole('dialog').locator('canvas').waitFor()
    await page.keyboard.press('Escape')
    await menu.click()
    await f.getByRole('menuitem', { name: '移除', exact: true }).click()
    await f.getByRole('dialog').getByRole('button', { name: '取消', exact: true }).click()
    assert.ok(await card.isVisible())
    assert.deepEqual(await gql(query), baseline, 'Canceled subscription actions must not change existing data')
    await card.getByRole('button', { name: '更新订阅', exact: true }).click()
    await f.getByText('订阅更新成功', { exact: true }).last().waitFor()
    const afterUpdate = await gql(query)
    await page.screenshot({ path: path.join(output, 'sources-desktop.png'), fullPage: true })
    await card.getByRole('button', { name: '查看节点', exact: true }).click()
    const viewer = f.locator('[data-subscription-browser]')
    const tiles = viewer.locator('[data-subscription-node]')
    assert.equal(await tiles.count(), 72)
    const tile = tiles.first()
    const name = tile.locator('.node-name')
    await page.waitForTimeout(250)
    assert.ok((await tile.boundingBox()).height <= 115)
    const secondary = tile.locator('.subscription-node-secondary').first()
    await viewer.getByRole('searchbox').focus()
    await page.mouse.move(0, 0)
    await page.waitForTimeout(180)
    assert.equal(
      await secondary.evaluate((e) => getComputedStyle(e).opacity),
      '1',
      'Actions stay discoverable without precise hovering',
    )
    await tile.hover({ position: { x: 4, y: 4 } })
    await page.waitForTimeout(200)
    assert.equal(await secondary.evaluate((e) => getComputedStyle(e).opacity), '1')
    assert.equal(await name.getAttribute('data-overflow'), 'true')
    assert.equal(
      await name.locator('.node-name-text').evaluate((e) => getComputedStyle(e).animationName),
      'node-name-pan',
    )
    await page.mouse.move(0, 0)
    assert.equal(await name.locator('.node-name-text').evaluate((e) => getComputedStyle(e).transform), 'none')
    await tile.getByRole('button', { name: /^更多操作 / }).click()
    await f.getByRole('menuitem', { name: '查看二维码', exact: true }).waitFor()
    await page.keyboard.press('Escape')
    assert.ok(await viewer.isVisible(), 'Escape from a menu must preserve its subscription browser')
    await tile.getByRole('button', { name: /^更多操作 / }).click()
    await f.getByRole('menuitem', { name: '查看二维码', exact: true }).click()
    await f.getByRole('dialog').locator('canvas').waitFor()
    await page.keyboard.press('Escape')
    assert.ok(await viewer.isVisible(), 'Closing QR preserves the subscription browser')
    await tile.getByRole('button', { name: '加入群组', exact: true }).click()
    await f.getByRole('dialog').getByRole('heading', { name: '加入群组', exact: true }).waitFor()
    await page.keyboard.press('Escape')
    await f.getByRole('heading', { name: '加入群组', exact: true }).waitFor({ state: 'hidden' })
    assert.ok(await viewer.isVisible())
    await viewer.screenshot({ path: path.join(output, 'nodes-desktop.png') })
    await viewer.evaluate((e) => (e.ownerDocument.documentElement.style.fontSize = '20px'))
    assert.ok(await tile.evaluate((e) => e.scrollWidth <= e.clientWidth + 1))
    await viewer.evaluate((e) => (e.ownerDocument.documentElement.style.fontSize = ''))
    await page.keyboard.press('Escape')
    assert.deepEqual(
      await gql(query),
      afterUpdate,
      'Browsing and canceled node actions must preserve the updated resources and bindings',
    )
    const touchContext = await browser.newContext({
      viewport: { width: 390, height: 844 },
      isMobile: true,
      hasTouch: true,
      locale: 'zh-CN',
      storageState: await context.storageState(),
    })
    const touchPage = await touchContext.newPage()
    await preview(touchPage)
    await touchPage.goto(`${base}/cgi-bin/luci/admin/services/daed`)
    const tf = touchPage.frameLocator('iframe')
    const touchGroup = tf.locator(`[data-group-card-id="${group}"]`)
    await touchGroup.getByRole('button', { name: '添加节点', exact: true }).tap()
    const picker = tf.locator('[data-node-picker]')
    await picker.getByRole('searchbox').fill(prefix)
    const pickName = picker.locator(`[data-picker-node="${longManual}"] .node-name`)
    assert.equal(await pickName.evaluate((e) => getComputedStyle(e).whiteSpace), 'normal')
    assert.equal(await pickName.evaluate((e) => getComputedStyle(e).webkitLineClamp), '2')
    const pickRow = picker.locator(`[data-picker-node="${longManual}"]`)
    await pickRow.getByRole('button', { name: /^展开完整名称 / }).tap()
    assert.equal(await pickRow.getByRole('checkbox').isChecked(), false, 'Viewing a name must not select the node')
    assert.ok(
      await pickName.evaluate((e) => e.scrollWidth <= e.clientWidth + 1 && e.scrollHeight <= e.clientHeight + 1),
      'Touch selection expands the complete name without clipping',
    )
    await picker.screenshot({ path: path.join(output, 'picker-touch-full-name.png') })
    await picker.getByRole('button', { name: '取消', exact: true }).tap()
    await touchPage.goto(`${base}/cgi-bin/luci/admin/services/daed/resources`)
    await tf.getByRole('tab', { name: /订阅/ }).tap()
    const touchCard = tf.locator(`[data-subscription-card="subscription-${subs[0]}"]`)
    await touchCard.getByRole('button', { name: /^更多操作 / }).tap()
    await touchPage.waitForTimeout(250)
    assert.ok((await tf.getByRole('menuitem', { name: '编辑', exact: true }).boundingBox()).height >= 44)
    await touchPage.keyboard.press('Escape')
    await touchCard.getByRole('button', { name: '查看节点', exact: true }).tap()
    const tv = tf.locator('[data-subscription-browser]')
    const touchTile = tv.locator('[data-subscription-node]').first()
    assert.equal(
      await touchTile
        .locator('.subscription-node-secondary')
        .first()
        .evaluate((e) => getComputedStyle(e).opacity),
      '1',
    )
    await touchTile.getByRole('button', { name: /^节点详情 / }).tap()
    const sheet = tf.locator('[data-slot=sheet-content]')
    assert.ok(await sheet.getByText(/完整名称查看验证/).isVisible())
    await sheet.getByRole('button', { name: 'Close', exact: true }).tap()
    await sheet.waitFor({ state: 'hidden' })
    await tv.screenshot({ path: path.join(output, 'nodes-touch.png') })
    assert.ok(await tv.evaluate((e) => e.scrollWidth <= e.clientWidth + 1))
    await tv.getByRole('button', { name: 'Close', exact: true }).tap()
    await touchCard.getByRole('button', { name: '订阅详情', exact: true }).tap()
    await touchPage.screenshot({ path: path.join(output, 'sources-touch.png'), fullPage: true })
    assert.ok(await tf.locator('body').evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1))
    assert.deepEqual(errors, [])
    process.stdout.write(
      'PASS: quiet subscription overview, folded sensitive details, edit/QR/delete cancellation, real update, node card hover/reset, contextual actions, nested menu/QR/group closing and touch full names\n',
    )
    await touchContext.close()
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
