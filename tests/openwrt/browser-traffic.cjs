/* UI rendering regression with controlled nonzero responses, not a router traffic measurement. */
const { chromium } = require('playwright');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const base = process.env.DAED_PANEL_URL || 'http://localhost:18080';
const output = process.env.DAED_TRAFFIC_OUTPUT || '.build/traffic-browser';
fs.mkdirSync(output, { recursive: true });

(async () => {
  const browser = await chromium.launch({ headless: true, ...(process.env.DAED_BROWSER_EXECUTABLE ? { executablePath: process.env.DAED_BROWSER_EXECUTABLE } : {}) });
  try {
    for (const width of [1600, 390]) {
      const context = await browser.newContext({ viewport: { width, height: 1000 }, locale: 'zh-CN', ...(width < 500 ? { isMobile: true, hasTouch: true } : {}) });
      let idle = false;
      let provideHistory = false;
      const windows = new Set();
      let summaryOnly = 0;
      await context.route('**/cgi-bin/daed-router/graphql', async route => {
        const request = route.request();
        const body = request.method() === 'POST' ? request.postDataJSON() : null;
        if (!body?.query?.includes('query TrafficOverview(')) return route.continue();
        if (body.variables.includeHistory) windows.add(body.variables.windowSec);
        else summaryOnly++;
        const now = Date.now();
        const overview = {
          updatedAt: new Date(now).toISOString(), uploadRate: idle ? 0 : 65536, downloadRate: idle ? 0 : 131072,
          uploadTotal: '2097152', downloadTotal: '8388608', activeConnections: 32, udpSessions: 16,
          ...(body.variables.includeHistory ? {
            samples: provideHistory ? Array.from({ length: 30 }, (_, i) => ({ timestamp: new Date(now - (29 - i) * 1000).toISOString(), uploadRate: i % 3 * 32768, downloadRate: i % 4 * 65536 })) : [],
          } : {}),
        };
        return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ data: { general: { runtimeOverview: overview } } }) });
      });
      const page = await context.newPage();
      const errors = [];
      const chartRequests = [];
      page.on('pageerror', err => errors.push(err.message));
      page.on('request', request => {
        if (/\/assets\/TrafficHistoryChart-.*\.js$/.test(request.url())) chartRequests.push(request.url());
      });
      await page.goto(base + '/cgi-bin/luci/admin/services/daed/dashboard');
      if (await page.locator('input[name=luci_username]').count()) {
        await page.locator('input[name=luci_username]').fill('root');
        await page.locator('input[name=luci_password]').fill('fixture-password');
        await page.getByRole('button', { name: /Log in|登录/ }).click();
      }
      const frame = page.frameLocator('iframe');
      const monitor = frame.locator('#panel-monitor');
      await monitor.getByRole('heading', { name: '运行概况', exact: true }).waitFor({ timeout: 45000 });
      await monitor.getByText('64', { exact: true }).waitFor();
      const text = await monitor.innerText();
      for (const pattern of [/上传速度\s+64\s+KB\/s/, /下载速度\s+128\s+KB\/s/, /本次流量\s+10\s+MB/, /累计上传\s+2\.0\s+MB/, /累计下载\s+8\.0\s+MB/, /活跃连接\s+32/, /UDP 会话\s+16/]) assert.match(text, pattern);
      await monitor.getByText('暂无运行采样').waitFor();
      assert.deepEqual(chartRequests, [], 'Live totals must not wait for the graph bundle');
      provideHistory = true;
      await monitor.locator('.recharts-area-curve').first().waitFor();
      assert.equal(chartRequests.length, 1, 'Load the graph bundle once when samples arrive');
      assert.ok((await monitor.locator('.recharts-area-curve').first().getAttribute('d')).length > 100, 'Nonzero chart must draw the history');
      for (const label of ['10 分钟', '30 分钟', '1 小时', '1 分钟']) {
        await monitor.getByRole('button', { name: label, exact: true }).click();
        await page.waitForTimeout(100);
      }
      assert.deepEqual([...windows].sort((a, b) => a - b), [60, 600, 1800, 3600]);
      idle = true;
      await monitor.getByText('0', { exact: true }).first().waitFor({ timeout: 10000 });
      assert.ok(summaryOnly > 0, 'Graph history must not be sent on each summary refresh');
      assert.ok(await monitor.locator('.recharts-area-curve').first().isVisible(), 'Summary-only refreshes retain history');
      assert.match(await monitor.innerText(), /本次流量\s+10\s+MB/, 'Idle rates must not clear cumulative traffic');
      assert.ok(await frame.locator('body').evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1));
      await page.screenshot({ path: path.join(output, 'traffic-' + width + '.png'), fullPage: true });
      assert.deepEqual(errors, []);
      await context.close();
      console.log('PASS: nonzero speeds, totals, history, four ranges and idle totals at width ' + width);
    }
  } finally { await browser.close(); }
})().catch(err => { console.error(err); process.exitCode = 1; });
