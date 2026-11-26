const assert = require('node:assert/strict');
const { readFile } = require('node:fs/promises');
const path = require('node:path');
const { chromium } = require('playwright');

const origin = process.argv[2];
const htmx = 'https://cdn.jsdelivr.net/npm/htmx.org@2.0.4/dist/htmx.min.js';
const d3 = 'https://cdn.jsdelivr.net/npm/d3@7.9.0/dist/d3.min.js';
const options = { timeout: 8000 };

async function useLocalAssets(context) {
  const directory = process.env.URPHIN_DASHBOARD_ASSETS;
  if (!directory) return;
  for (const url of [htmx, d3]) {
    const body = await readFile(path.join(directory, new URL(url).pathname.split('/').at(-1)));
    await context.route(url, route => route.fulfill({
      body,
      contentType: 'application/javascript',
      headers: { 'Access-Control-Allow-Origin': '*' }
    }));
  }
}

async function expectRows(page, expected) {
  await page.waitForFunction(rows => {
    return JSON.stringify([...document.querySelectorAll('.positions-panel .ticker')].map(node => node.textContent).sort()) === JSON.stringify(rows);
  }, [...expected].sort(), options);
}

async function live(page) {
  await page.locator('.connection-status.is-live').waitFor(options);
}

async function counts(context) {
  return (await context.request.get(`${origin}/test/counts`)).json();
}

async function action(context, name) {
  const response = await context.request.post(`${origin}/test/${name}`);
  assert.equal(response.status(), 204);
}

async function run() {
  const browser = await chromium.launch();
  try {
    const context = await browser.newContext({ viewport: { width: 1400, height: 1000 } });
    await useLocalAssets(context);
    const first = await context.newPage();
    const second = await context.newPage();
    await Promise.all([first.goto(origin), second.goto(origin)]);
    await Promise.all([live(first), live(second)]);
    assert.equal(await first.evaluate(() => typeof window.htmx), 'object');
    assert.ok(await first.locator('#holdings-chart path').count());
    await expectRows(first, ['LONG', 'SHORT', 'BOND', 'UNPRICED']);
    assert.deepEqual(await counts(context), { refreshes: 2, streams: 2 });

    await first.selectOption('#scope', 'list:ALL');
    await expectRows(first, ['LONG']);
    await second.selectOption('#scope', 'list:__all');
    await expectRows(second, ['SHORT']);
    const specialStream = second.waitForRequest(request => {
      const url = new URL(request.url());
      return url.pathname === '/events' && url.searchParams.get('scope') === 'list:Growth & bonds+#';
    });
    await second.selectOption('#scope', 'list:Growth & bonds+#');
    await specialStream;
    await expectRows(second, ['BOND']);
    assert.equal(new URL(second.url()).searchParams.get('scope'), 'list:Growth & bonds+#');
    await live(second);
    await second.selectOption('#scope', 'list:No quote');
    await expectRows(second, ['UNPRICED']);
    assert.equal(await second.locator('#sector-chart rect').count(), 0);
    assert.equal(await second.locator('#holdings-chart path').count(), 0);

    // Two changes in one dispatch cycle exercise replacement of the in-flight request.
    await second.locator('#scope').evaluate(select => {
      for (const value of ['list:__all', 'list:Growth & bonds+#']) {
        select.value = value;
        select.dispatchEvent(new Event('change', { bubbles: true }));
      }
    });
    await expectRows(second, ['BOND']);
    await live(second);
    await second.waitForFunction(async () => {
      const result = await (await fetch('/test/counts')).json();
      return result.streams === 2;
    }, null, options);
    assert.deepEqual(await counts(context), { refreshes: 2, streams: 2 });

    await first.locator('#scope').focus();
    await first.locator('#scope option').first().evaluate(option => { window.originalOption = option; });
    await action(context, 'quote');
    await first.locator('.positions-panel').getByText('+26.00').first().waitFor(options);
    assert.equal(await first.locator('#scope option').first().evaluate(option => option === window.originalOption), true);
    assert.equal(await first.locator('#scope').evaluate(select => select === document.activeElement), true);

    await action(context, 'membership');
    await second.locator('option[value="list:New list"]').waitFor({ ...options, state: 'attached' });
    await expectRows(second, ['LONG', 'SHORT', 'BOND', 'UNPRICED']);
    await live(second);
    assert.equal(await first.locator('#scope').inputValue(), 'list:ALL');
    assert.equal(await second.locator('#scope').inputValue(), '__all');
    assert.equal((await counts(context)).refreshes, 2);

    await action(context, 'fail');
    await first.getByRole('button', { name: 'Refresh portfolio' }).click();
    await first.getByText('test watchlist unavailable').waitFor(options);
    await second.getByText('test watchlist unavailable').waitFor(options);
    await expectRows(first, ['LONG']);
    await second.reload();
    await second.getByText('test watchlist unavailable').waitFor(options);
    await expectRows(second, ['LONG', 'SHORT', 'BOND', 'UNPRICED']);
    await action(context, 'quote');
    await live(first);
    assert.equal(await first.getByText('test watchlist unavailable').count(), 1);
    await action(context, 'recover');
    await first.getByRole('button', { name: 'Refresh portfolio' }).click();
    await second.getByText('test watchlist unavailable').waitFor({ ...options, state: 'detached' });

    await action(context, 'disconnect');
    await first.locator('.connection-status.is-reconnecting').waitFor(options);
    await expectRows(first, ['LONG']);
    await live(first);
    for (const width of [360, 1400]) {
      await second.setViewportSize({ width, height: 900 });
      assert.ok(await second.locator('.table-scroll').isVisible());
      assert.ok(await second.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
    }

    const noScripts = await browser.newContext({ javaScriptEnabled: false });
    const fallback = await noScripts.newPage();
    await fallback.goto(origin);
    await fallback.locator('#scope').selectOption('list:New list');
    await Promise.all([
      fallback.waitForURL(url => url.searchParams.get('scope') === 'list:New list'),
      fallback.getByRole('button', { name: 'Refresh portfolio' }).click()
    ]);
    assert.equal(await fallback.locator('.positions-panel .ticker').textContent(), 'BOND');
    assert.equal(await fallback.locator('.connection-status').textContent(), 'Live updates pending');

    const mismatch = await browser.newContext();
    await useLocalAssets(mismatch);
    for (const url of [htmx, d3]) {
      await mismatch.route(url, route => route.fulfill({
        body: 'window.sriMismatchExecuted = true;',
        contentType: 'application/javascript',
        headers: { 'Access-Control-Allow-Origin': '*' }
      }));
    }
    await mismatch.route('**/static/dashboard.js', route => route.abort());
    const page = await mismatch.newPage();
    await page.goto(origin);
    assert.equal(await page.evaluate(() => window.sriMismatchExecuted), undefined);
    assert.equal(await page.evaluate(() => typeof window.htmx), 'undefined');
    assert.equal(await page.evaluate(() => typeof window.d3), 'undefined');
    assert.equal(await page.locator('.connection-status').textContent(), 'Live updates pending');
    await expectRows(page, ['LONG', 'SHORT', 'BOND', 'UNPRICED']);
    console.log('Dashboard Chromium checks passed');
  } finally {
    await browser.close();
  }
}

run().catch(error => {
  console.error(error);
  process.exitCode = 1;
});
