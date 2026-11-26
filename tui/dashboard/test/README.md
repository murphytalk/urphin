# Dashboard browser tests

Install Playwright and Chromium once, then run the browser test from the
repository root. Node must be able to resolve `playwright`.

```sh
npm install --prefix /tmp/urphin-browser playwright@1.59.1
/tmp/urphin-browser/node_modules/.bin/playwright install --with-deps chromium
NODE_PATH=/tmp/urphin-browser/node_modules URPHIN_DASHBOARD_BROWSER=1 \
  go test -count=1 ./tui/dashboard -run '^TestDashboardBrowser$' -v
```

The Go test starts a loopback server with synthetic positions and a controllable
quote stream. Chromium uses the pinned CDN scripts by default. Set
`URPHIN_DASHBOARD_ASSETS` to an absolute directory containing the exact
`htmx.min.js` and `d3.min.js` bytes from the URLs in the page template to run
offline. Chromium still verifies their integrity.

On Linux with Docker, set `URPHIN_DASHBOARD_BROWSER_DOCKER=1` in the test
command to use the Playwright image. This runs Chromium with the image's
libraries and uses the local Playwright npm package. Skip the Chromium
installation step in Docker mode.
