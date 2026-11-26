(() => {
  const palette = window.d3?.schemeTableau10 ?? ['#58d6c2', '#8b9cff', '#f0ad68', '#d98ab7', '#76c4e2', '#a7cf71', '#d78276', '#b19be0', '#e0cb75', '#78c7ab'];
  const formatMoney = value => value.toLocaleString(undefined, {minimumFractionDigits: 2, maximumFractionDigits: 2});
  const formatPercent = value => `${value.toLocaleString(undefined, {maximumFractionDigits: 1})}%`;

  function drawHoldings(data) {
    const node = document.querySelector('#holdings-chart');
    if (!node || !window.d3) return;
    const width = Math.max(220, node.clientWidth || 280);
    const height = node.clientHeight || 260;
    const radius = Math.min(width, height) / 2 - 5;
    const chart = d3.select(node);
    chart.selectAll('*').remove();
    chart.attr('viewBox', `0 0 ${width} ${height}`);

    const rows = data.rows.filter(row => Math.abs(row.base) > 0);
    const colors = d3.scaleOrdinal(rows.map(row => row.symbol), palette);
    const legend = document.querySelectorAll('.holding-legend li[data-symbol]');
    legend.forEach(item => item.style.setProperty('--legend-color', colors(item.dataset.symbol)));
    if (!rows.length) return;

    const center = chart.append('g').attr('transform', `translate(${width / 2},${height / 2})`);
    const arc = d3.arc().innerRadius(radius * .69).outerRadius(radius).cornerRadius(3);
    const slices = d3.pie().sort(null).value(row => Math.abs(row.base))(rows);
    center.selectAll('path').data(slices).join('path')
      .attr('d', arc)
      .attr('fill', item => colors(item.data.symbol))
      .append('title').text(item => `${item.data.symbol}: ${item.data.base > 0 ? '+' : ''}${formatMoney(item.data.base)} ${data.base}`);

    center.append('text').attr('class', 'donut-center-value').attr('y', -2)
      .text(formatMoney(data.gross));
    center.append('text').attr('class', 'donut-center-label').attr('y', 17).text(`GROSS ${data.base}`);
  }

  function sectorColor(sector) {
    if (!sector.available) return '#344153';
    const intensity = Math.min(1, Math.abs(sector.change) / 8);
    if (sector.change > 0) return d3.interpolateRgb('#1d393b', '#248f78')(intensity);
    if (sector.change < 0) return d3.interpolateRgb('#432a35', '#c35c69')(intensity);
    return '#354354';
  }

  function drawSectors(data) {
    const node = document.querySelector('#sector-chart');
    if (!node || !window.d3) return;
    const width = Math.max(280, node.clientWidth || 420);
    const height = node.clientHeight || 276;
    const chart = d3.select(node);
    chart.selectAll('*').remove();
    chart.attr('viewBox', `0 0 ${width} ${height}`);
    if (!data.sectors.length) return;

    const root = d3.hierarchy({children: data.sectors}).sum(item => Math.max(item.exposure, .1));
    d3.treemap().size([width, height]).padding(4).round(true)(root);
    const tiles = chart.selectAll('g').data(root.leaves()).join('g')
      .attr('transform', item => `translate(${item.x0},${item.y0})`);
    tiles.append('rect').attr('class', 'sector-tile')
      .attr('width', item => item.x1 - item.x0)
      .attr('height', item => item.y1 - item.y0)
      .attr('fill', item => sectorColor(item.data));
    tiles.append('title').text(item => {
      const change = item.data.available ? `${item.data.change > 0 ? '+' : ''}${item.data.change.toFixed(2)}%` : 'Daily change unavailable';
      return `${item.data.name}: ${change}, ${formatPercent(item.data.exposure)} exposure`;
    });
    tiles.filter(item => item.x1 - item.x0 > 105 && item.y1 - item.y0 > 56).each(function (item) {
      const group = d3.select(this);
      const maxChars = Math.max(8, Math.floor((item.x1 - item.x0 - 18) / 7));
      const name = item.data.name.length > maxChars ? `${item.data.name.slice(0, maxChars - 1)}…` : item.data.name;
      group.append('text').attr('class', 'sector-label').attr('x', 12).attr('y', 24).text(name);
      const details = item.data.available
        ? `${item.data.change > 0 ? '+' : ''}${item.data.change.toFixed(2)}%  ·  ${formatPercent(item.data.exposure)}`
        : `Unavailable  ·  ${formatPercent(item.data.exposure)}`;
      group.append('text').attr('class', 'sector-subtitle').attr('x', 12).attr('y', 43).text(details);
    });
  }

  function renderCharts() {
    const dataNode = document.querySelector('#dashboard-data');
    if (!dataNode || !window.d3) return;
    let data;
    try {
      data = JSON.parse(dataNode.textContent);
    } catch {
      return;
    }
    drawHoldings(data);
    drawSectors(data);
  }

  function setConnectionState(connected) {
    document.querySelectorAll('.connection-status').forEach(node => {
      node.textContent = connected ? 'Live updates connected' : 'Reconnecting to live updates';
      node.classList.toggle('is-live', connected);
      node.classList.toggle('is-reconnecting', !connected);
    });
  }

  document.addEventListener('DOMContentLoaded', renderCharts);
  document.addEventListener('htmx:afterSwap', renderCharts);
  document.addEventListener('htmx:sseOpen', () => setConnectionState(true));
  document.addEventListener('htmx:sseError', () => setConnectionState(false));
  document.addEventListener('change', event => {
    if (event.target.matches('#scope')) event.target.form.requestSubmit();
  });
  window.addEventListener('resize', () => window.requestAnimationFrame(renderCharts));
})();
