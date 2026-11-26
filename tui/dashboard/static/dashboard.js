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

    const root = d3.hierarchy({children: data.sectors}).sum(item => item.exposure ?? 0);
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

  function readData() {
    const node = document.querySelector('#dashboard-data');
    if (!node) return null;
    try {
      return JSON.parse(node.textContent);
    } catch {
      return null;
    }
  }

  function renderCharts() {
    const data = readData();
    if (!data || !window.d3) return;
    drawHoldings(data);
    drawSectors(data);
  }

  let active;
  const connectionLabel = {
    pending: 'Live updates pending',
    live: 'Live updates connected',
    reconnecting: 'Reconnecting to live updates'
  };

  function showConnectionState(panel, state) {
    const node = panel.querySelector('.connection-status');
    if (!node) return;
    node.textContent = connectionLabel[state];
    node.classList.toggle('is-live', state === 'live');
    node.classList.toggle('is-reconnecting', state === 'reconnecting');
  }

  function updateOptions(data) {
    const select = document.querySelector('#scope');
    if (!select) return;
    const options = [['__all', 'All holdings'], ...data.lists.map(name => [`list:${name}`, name])];
    const matches = options.length === select.options.length && options.every(([value, label], index) => {
      const existing = select.options[index];
      return existing.value === value && existing.text === label;
    });
    const selected = select.value;
    if (!matches) {
      select.replaceChildren(...options.map(([value, label]) => new Option(label, value)));
    }
    if (!options.some(([value]) => value === selected)) {
      select.value = data.scope;
    } else if (!matches) {
      select.value = selected;
    }
  }

  function updateDashboard() {
    const panel = document.querySelector('#portfolio');
    const data = readData();
    if (!panel || !data) return;
    updateOptions(data);
    const url = new URL(location.href);
    if (data.scope === '__all') url.searchParams.delete('scope');
    else url.searchParams.set('scope', data.scope);
    url.searchParams.delete('refresh');
    if (url.href !== location.href) history.replaceState(history.state, '', url);
    renderCharts();
    if (!window.EventSource) return;

    if (active?.url === data.stream) {
      active.panel = panel;
      showConnectionState(panel, active.state);
      return;
    }
    const state = active?.state === 'reconnecting' ? 'reconnecting' : 'pending';
    active?.source.close();
    const source = new EventSource(data.stream);
    const stream = {source, panel, url: data.stream, state};
    active = stream;
    showConnectionState(panel, stream.state);

    function current() {
      return active === stream && stream.panel.isConnected;
    }

    source.onopen = () => {
      if (!current()) return;
      stream.state = 'live';
      showConnectionState(stream.panel, stream.state);
    };
    source.onerror = () => {
      if (!current()) return;
      stream.state = 'reconnecting';
      showConnectionState(stream.panel, stream.state);
    };
    source.addEventListener('portfolio', event => {
      if (!current()) return;
      stream.panel.innerHTML = event.data;
      updateDashboard();
    });
  }

  document.addEventListener('DOMContentLoaded', updateDashboard);
  document.addEventListener('htmx:afterSwap', updateDashboard);
  document.addEventListener('change', event => {
    if (event.target.matches('#scope')) event.target.form.requestSubmit();
  });
  window.addEventListener('resize', () => window.requestAnimationFrame(renderCharts));
})();
