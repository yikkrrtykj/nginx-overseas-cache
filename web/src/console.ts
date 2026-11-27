import { request, type Group, type State, type Status, type Traffic } from './api.js';
import { button, description, dialog, element, message, node, state, write } from './components.js';
import { initializeConfiguration } from './configuration.js';

const dashes = '—';
const timeFormat = new Intl.DateTimeFormat('zh-CN', { dateStyle: 'short', timeStyle: 'medium' });
const cacheKeys = ['HIT', 'MISS', 'BYPASS', 'STALE', 'EXPIRED', 'UPDATING', 'REVALIDATED'];
let updating = false;

function timeLabel(value: string | undefined): string {
  if (!value || value.startsWith('0001-')) return dashes;
  const time = new Date(value);
  return Number.isFinite(time.getTime()) ? timeFormat.format(time) : dashes;
}
function byteLabel(bytes: number): string {
  if (bytes < 1024) return bytes + ' B';
  if (bytes < 1024 ** 2) return (bytes / 1024).toFixed(1) + ' KiB';
  if (bytes < 1024 ** 3) return (bytes / 1024 ** 2).toFixed(1) + ' MiB';
  return (bytes / 1024 ** 3).toFixed(2) + ' GiB';
}
function durationLabel(seconds: number | undefined): string {
  if (seconds === undefined) return dashes;
  const hours = Math.floor(seconds / 3600);
  const minutes = Math.floor(seconds / 60) % 60;
  return hours + 'h ' + minutes + 'm ' + seconds % 60 + 's';
}
function inspect(group: Group): void {
  element('group-details', HTMLElement).replaceChildren(
    description('来源', group.origin || dashes),
    description('频道', group.channel || dashes),
    description('模式', group.mode || dashes),
    description('Token 指纹', group.tokenId || dashes),
    description('连接持续时间', durationLabel(group.upstreamConnected ? group.connectedSeconds : undefined)),
    description('连接代次', group.generation ?? dashes),
    description('队列深度', group.queuedMessages ?? dashes),
  );
  dialog('inspector').showModal();
}
function groupRow(group: Group, version: number): HTMLTableRowElement {
  const row = node('tr');
  const identity = node('td');
  const open = node('button', group.origin || '未知来源', 'link mono');
  open.type = 'button';
  open.addEventListener('click', () => inspect(group));
  identity.append(open, node('span', group.channel || dashes, 'secondary mono'));
  const connection = node('td');
  const badge = node('span', undefined, 'status');
  if (group.upstreamReady) state(badge, 'healthy', '已握手');
  else if (group.upstreamConnected) state(badge, 'degraded', '握手中');
  else state(badge, 'degraded', '等待上游');
  connection.append(badge);
  row.append(identity, node('td', group.mode || dashes, 'mono'), connection,
    node('td', group.acknowledgedClients + ' / ' + group.localClients, 'num'),
    node('td', version < 2 ? dashes : (group.queuedMessages ?? 0), 'num'),
    node('td', version < 2 ? dashes : (group.reconnects ?? 0), 'num'));
  return row;
}
function refreshRows(list: HTMLElement, groups: Group[], version: number, reason: string): void {
  const signature = groups.length ? JSON.stringify([version, groups]) : reason;
  if (list.dataset.signature === signature) return;
  // Keep an operator's focused inspector trigger stable during polling.
  if (list.contains(document.activeElement)) return;
  list.dataset.signature = signature;
  if (groups.length) {
    list.replaceChildren(...groups.map(group => groupRow(group, version)));
    return;
  }
  const row = node('tr');
  const cell = node('td', reason, 'empty');
  cell.colSpan = 6;
  row.append(cell);
  list.replaceChildren(row);
}
function renderStatus(data: Status | undefined, reason = ''): void {
  const available = data?.available === true;
  const metrics: Record<string, number | undefined> = {
    'local-clients': data?.localClients, 'upstream-connections': data?.upstreamConnections,
    'active-groups': data?.activeHubs, 'acknowledged': data?.acknowledgedClients, saved: data?.savedUpstreamConnections,
    reconnects: data?.version === 2 ? data.reconnects : undefined,
    'slow-clients': data?.version === 2 ? data.slowDisconnects : undefined,
    overflows: data?.version === 2 ? data.queueOverflows : undefined,
    goroutines: data?.version === 2 ? data.goroutines : undefined,
  };
  for (const [id, value] of Object.entries(metrics)) write(id, available ? value : undefined);
  write('uptime', available ? durationLabel(data.uptimeSeconds) : undefined);
  write('status-time', timeLabel(data?.generatedAt));
  write('connections-description', data?.message ?? reason);
  const tone: State = data?.state ?? 'error';
  state(element('status-badge', HTMLElement), tone);
  element('metrics', HTMLElement).setAttribute('aria-busy', 'false');
  refreshRows(element('groups', HTMLElement), available ? data.groups : [], data?.version ?? 0,
    available ? '当前没有活跃共享组。' : data?.message ?? reason);
}
function renderTraffic(data: Traffic | undefined, reason = ''): void {
  const tone: State = data?.state ?? 'error';
  const current = tone === 'healthy';
  state(element('traffic-state', HTMLElement), tone);
  write('diag-nginx', data?.nginxState === 'unavailable' ? '本机探测不可用' : current ? (data?.errors ? data.errors + ' 条 5xx' : '访问日志有近期样本') : '无近期证据');
  const counts = data?.counts ?? {};
  const denominator = cacheKeys.filter(key => key !== 'BYPASS').reduce((n, key) => n + (counts[key] ?? 0), 0);
  write('hit-rate', current && denominator ? (((counts.HIT ?? 0) / denominator) * 100).toFixed(1) + '%' : undefined);
  write('response-bytes', current && data ? byteLabel(data.responseBytes) : undefined);
  write('origin-requests', current ? data?.upstreamRequests : undefined);
  renderTrend(current ? data?.points : undefined);
  const list = element('cache-counts', HTMLElement);
  if (!current || !data) {
    list.replaceChildren(node('p', reason || (tone === 'stale' ? '日志样本已超过 60 秒。' : tone === 'empty' ? '日志暂无有效请求。' : '等待可读的 Nginx 访问日志。'), 'empty'));
    return;
  }
  const rows = cacheKeys.filter(key => counts[key] !== undefined).map(key => description(key, counts[key] ?? 0));
  rows.push(description('HIT 响应量', byteLabel(data.hitResponseBytes)));
  rows.push(description('最新请求', timeLabel(data.observedAt)));
  const details = node('dl');
  details.className = 'data-list';
  details.append(...rows);
  list.replaceChildren(details);
}
function renderTrend(points: Traffic['points']): void {
  const chart = element('upstream-trend', HTMLElement);
  chart.hidden = !points?.some(point => point.entries > 0);
  if (!points) { chart.replaceChildren(); return; }
  const max = Math.max(1, ...points.map(point => point.upstreamRequests));
  chart.replaceChildren(...points.map(point => {
    const column = node('div');
    const label = point.entries ? String(point.upstreamRequests) : dashes;
    const title = timeLabel(point.time) + ' · ' + label;
    const meter = node('meter');
    meter.max = max;
    meter.value = point.upstreamRequests;
    meter.title = title;
    meter.setAttribute('aria-label', title);
    column.append(node('span', label, 'mono'), meter);
    return column;
  }));
}
async function poll(): Promise<void> {
  if (updating) return;
  updating = true;
  const refresh = button('refresh');
  refresh.disabled = true;
  try {
    const [status, traffic] = await Promise.allSettled([
      request<{ status: Status }>('status', undefined, AbortSignal.timeout(5000)),
      request<{ traffic: Traffic }>('traffic', undefined, AbortSignal.timeout(5000)),
    ]);
    write('diag-admin', status.status === 'fulfilled' ? '正常' : '请求异常');
    if (status.status === 'fulfilled') renderStatus(status.value.status);
    else renderStatus(undefined, message(status.reason));
    if (traffic.status === 'fulfilled') renderTraffic(traffic.value.traffic);
    else renderTraffic(undefined, message(traffic.reason));
  } finally {
    updating = false;
    refresh.disabled = false;
  }
}
function highlight(): void {
  const links = document.querySelectorAll<HTMLAnchorElement>('nav a');
  for (const link of links) {
    if (link.hash === (location.hash || '#overview')) link.setAttribute('aria-current', 'location');
    else link.removeAttribute('aria-current');
  }
}
window.addEventListener('hashchange', highlight);
highlight();
button('refresh').addEventListener('click', () => void poll());
void poll();
window.setInterval(() => {
  if (document.visibilityState === 'visible') void poll();
}, 3000);
initializeConfiguration();
