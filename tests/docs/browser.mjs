import assert from 'node:assert/strict'
import { mkdir } from 'node:fs/promises'
import process from 'node:process'
import { chromium } from 'playwright'

async function main() {
  const base = (process.env.DOCS_TEST_URL || 'http://localhost:4173/').replace(/\/?$/, '/')
  const output = '.build/docs-check'
  await mkdir(output, { recursive: true })
  const browser = await chromium.launch({ channel: process.env.DOCS_BROWSER_CHANNEL || undefined })
  const errors = []
  try {
    const desktop = await browser.newContext({ viewport: { width: 1440, height: 1024 }, reducedMotion: 'reduce' })
    const page = await desktop.newPage()
    page.on('pageerror', (error) => errors.push(error.message))
    await page.goto(base)
    await page.getByRole('heading', { name: /连接有序/ }).waitFor()
    await page.screenshot({ path: `${output}/desktop-home.png`, fullPage: true })
    await page.keyboard.press('Control+k')
    await page.locator('#search-input').fill('测试超时')
    await page.locator('.search-result').first().waitFor()
    assert.match(await page.locator('.search-result').first().getAttribute('href'), /troubleshooting.html#test-timeout/)
    await page.keyboard.press('ArrowDown')
    assert.match(await page.locator(':focus').getAttribute('class'), /search-result/)
    await page.keyboard.press('Enter')
    await page.waitForURL('**/troubleshooting.html#test-timeout')
    await page.locator('#sidebar').getByRole('link', { name: '内核与接管方式', exact: true }).click()
    await page.waitForURL('**/cores.html')
    await page.getByRole('heading', { name: '在面板中完成切换' }).waitFor()
    await page.getByRole('button', { name: '搜索文档' }).click()
    await page.locator('#search-input').fill('切换内核')
    await page.locator('.search-result[href^="cores.html"]').first().waitFor()
    await page.getByRole('button', { name: '关闭搜索' }).click()
    await page.screenshot({ path: `${output}/desktop-cores.png`, fullPage: true })
    await page.goto(`${base}installation.html`)
    // Clipboard is stubbed only in this test: the production button must send the exact visible command.
    await page.evaluate(() => {
      Object.defineProperty(navigator, 'clipboard', {
        configurable: true,
        value: {
          writeText: async (text) => {
            window.copiedCommand = text
          },
        },
      })
    })
    await page.locator('.copy-button').first().click()
    assert.equal(await page.evaluate(() => window.copiedCommand), await page.locator('pre code').first().textContent())
    await page.getByRole('button', { name: '切换到深色主题' }).click()
    await page.reload()
    assert.equal(await page.locator('html').getAttribute('data-theme'), 'dark')
    await page.evaluate(() => {
      document.activeElement?.blur()
      window.scrollTo(0, 0)
    })
    await page.screenshot({ path: `${output}/desktop-install-dark.png`, fullPage: true })
    for (const name of [
      'index',
      'installation',
      'quick-start',
      'cores',
      'nodes',
      'groups',
      'network',
      'dashboard',
      'maintenance',
      'troubleshooting',
      'development',
    ]) {
      const response = await page.goto(`${base}${name}.html`)
      assert.equal(response.status(), 200, name)
      assert.equal(
        await page.locator('body').evaluate((el) => el.scrollWidth > innerWidth),
        false,
        `desktop overflow: ${name}`,
      )
      assert.equal(
        await page.locator('img').evaluateAll((imgs) => imgs.every((img) => img.complete && img.naturalWidth > 0)),
        true,
        `images: ${name}`,
      )
    }
    const mobile = await browser.newContext({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true })
    const phone = await mobile.newPage()
    phone.on('pageerror', (error) => errors.push(error.message))
    for (const name of [
      'index',
      'installation',
      'quick-start',
      'cores',
      'nodes',
      'groups',
      'network',
      'dashboard',
      'maintenance',
      'troubleshooting',
      'development',
    ]) {
      await phone.goto(`${base}${name}.html`)
      assert.equal(
        await phone.locator('body').evaluate((el) => el.scrollWidth > innerWidth),
        false,
        `mobile overflow: ${name}`,
      )
    }
    await phone.goto(base)
    await phone.getByRole('button', { name: '目录', exact: true }).click()
    assert.equal(await phone.locator('#menu-toggle').getAttribute('aria-expanded'), 'true')
    await phone.locator('#sidebar').getByRole('link', { name: '群组与多选' }).click()
    await phone.waitForURL('**/groups.html')
    await phone.screenshot({ path: `${output}/mobile-groups.png`, fullPage: true })
    await phone.getByRole('button', { name: '搜索文档' }).click()
    await phone.locator('#search-input').fill('DNS')
    await phone.locator('.search-result').first().waitFor()
    assert.match(await phone.locator('.search-result').first().getAttribute('href'), /network.html/)
    await phone.screenshot({ path: `${output}/mobile-search.png` })
    await phone.getByRole('button', { name: '关闭搜索' }).click()
    await phone.goto(`${base}cores.html`)
    await phone.getByRole('heading', { name: '在面板中完成切换' }).waitFor()
    await phone.screenshot({ path: `${output}/mobile-cores.png`, fullPage: true })
    await phone.setViewportSize({ width: 320, height: 740 })
    assert.equal(
      await phone.locator('body').evaluate((el) => el.scrollWidth > innerWidth),
      false,
      '320px cores overflow',
    )
    await phone.goto(base)
    await phone.screenshot({ path: `${output}/mobile-home.png`, fullPage: true })
    // A failed index request must show recovery guidance, then permit retry.
    const broken = await browser.newContext()
    const failedPage = await broken.newPage()
    await failedPage.route('**/search-index.json', (route) => route.abort())
    await failedPage.goto(base)
    await failedPage.getByRole('button', { name: '搜索文档' }).click()
    await failedPage.locator('#search-input').fill('安装')
    await failedPage.getByText('搜索索引加载失败，请重新输入重试，或使用左侧目录。').waitFor()
    await failedPage.unroute('**/search-index.json')
    await failedPage.locator('#search-input').fill('安装环境')
    await failedPage.locator('.search-result').first().waitFor()
    assert.deepEqual(errors, [])
    process.stdout.write(
      'Passed: 11 pages desktop/mobile, compact core guide, Chinese search, keyboard, copy, theme persistence, navigation and search recovery.\n',
    )
  } finally {
    await browser.close()
  }
}

main().catch((error) => {
  console.error(error)
  process.exitCode = 1
})
