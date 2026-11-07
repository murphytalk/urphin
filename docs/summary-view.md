# Summary view layout

The summary sits above the scrolling watchlist when `show-summary` is enabled.
It follows the selected watchlist by default; **F3** switches between that scope
and the whole portfolio.

Illustrative layout:

```text
Summary: Watchlist: Main                        EURUSD ↑ 1.1000 (00.50%)
EUR • Value: 1,200.00 (USD 1,320.00) • Day Change: ↑ 12.00 (01.01%)
  VWAP Cost:             1,000.00 • VWAP P&L:    ↑ 200.00 (20.00%)
  Avg Cost:                800.00 • Avg Cost P&L: ↑ 400.00 (50.00%)
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
```

- **Heading:** scope on the left, configured FX pairs on the right, with flexible
  space between. Pairs retain configuration order and use ` • ` separators.
  Pair names use the same bold foreground style as watchlist tickers. Rates show
  percentage change versus previous close, using the Day Change arrows and theme
  colors. Only pairs involving the base currency appear.
- **Currency blocks:** currencies sort alphabetically, with three lines each:
  value and day change; VWAP cost and P&L; average cost and P&L. Amounts and change
  values align within each block. Average cost is underlined.
- **Amounts:** totals remain in each currency. A non-base currency's value also
  shows its base-currency equivalent in parentheses when a configured FX rate is
  available. Unavailable values or changes display `N/A`.
- **Space:** one heading, three lines per currency, and one full-width separator
  give a height of `3N + 2`. Narrow metric lines first lose percentages, then use
  shorter labels, then truncate. Long headings use an ellipsis. The application
  shows a resize prompt below 80 columns or when the summary leaves too few rows.

See [themes](themes.md) for colors and [UI message flow](ui-msg-flow.md) for data
ownership and updates.
