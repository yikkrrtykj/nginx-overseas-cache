import { request, RequestError, type Candidate, type Discovery, type RendererConfig } from './api.js';
import { button, confirmDialog, dialog, element, form, input, message, node, notice, state, write } from './components.js';

type WriteConfig = Omit<RendererConfig, 'tokenPresent'> & { token: string };
type Changed = { config: RendererConfig; backup?: string | null };
let config: RendererConfig | undefined;
let pending = false;
let identified = false;
let reidentify = false;
let generation = 0;
let scanner: AbortController | undefined;

function controls(): void {
  button('save').disabled = pending || reidentify || !!scanner || !config;
  button('discover').disabled = pending || !!scanner;
}
function configState(): HTMLElement { return element('config-state', HTMLElement); }
function fill(data: RendererConfig): void {
  config = data;
  input('origin').value = data.originHost;
  input('cdn').value = data.cdnHost;
  input('channel').value = data.channelId;
  input('account').value = data.accountId;
  input('render-token').value = '';
  write('token-hint', data.tokenPresent ? '留空保留已配置的 Token' : '请输入上游 Token');
  state(configState(), data.tokenPresent ? 'healthy' : 'unconfigured', data.tokenPresent ? '已读取' : 'Token 未配置');
  controls();
}
async function load(): Promise<void> {
  try { fill((await request<{config: RendererConfig}>('config')).config); }
  catch (err) {
    state(configState(), 'error');
    notice('operation', message(err));
  }
}
async function mutation<T extends object>(path: string, payload: object, signal?: AbortSignal): Promise<T & { message?: string }> {
  try { return await request<T>(path, payload, signal); }
  catch (err) {
    if (err instanceof RequestError && err.status === 403) {
      await request<{config: RendererConfig}>('config');
    }
    throw err;
  }
}
function describeCandidate(candidate: Candidate): HTMLDivElement {
  const row = node('div');
  const pick = node('button', candidate.host, 'link mono');
  pick.type = 'button';
  pick.disabled = !candidate.compatible || (candidate.verified && !candidate.valid) || candidate.host === input('origin').value;
  pick.addEventListener('click', () => { input('cdn').value = candidate.host; });
  const label = !candidate.compatible ? '路径待适配' : candidate.valid ? '已验证' : candidate.verified ? '验证失败' : '未验证';
  const badge = node('span', undefined, 'status');
  state(badge, candidate.valid ? 'healthy' : 'unknown', label);
  row.append(pick, badge);
  const hint = node('small', candidate.types.join(' / '));
  if (candidate.samples.length) pick.title = candidate.samples.join(' · ');
  row.append(hint);
  return row;
}
function present(result: Discovery): void {
  identified = true;
  reidentify = false;
  input('origin').value = result.sourceHost;
  input('cdn').value = result.recommendedHost;
  input('channel').value = result.channelId;
  input('account').value = result.accountId;
  input('render-token').value = result.token;
  write('token-hint', '网页识别后须填写本次渲染 Token');
  const area = element('discovery-result', HTMLElement);
  const warnings = result.warnings.map(warning => node('p', warning, 'notice'));
  const candidates = node('div', undefined, 'data-list');
  candidates.append(...result.candidates.map(describeCandidate));
  area.replaceChildren(...warnings, candidates);
}
function changedSource(): void {
  generation++;
  scanner?.abort();
  scanner = undefined;
  if (identified || reidentify) {
    reidentify = true;
    element('discovery-result', HTMLElement).replaceChildren(node('p', '识别地址已变更。重新识别后可保存。', 'notice'));
  }
  controls();
}
async function discover(event: SubmitEvent): Promise<void> {
  event.preventDefault();
  if (scanner || pending) return;
  reidentify = true;
  const current = ++generation;
  const abort = new AbortController();
  scanner = abort;
  controls();
  const result = element('discovery-result', HTMLElement);
  result.replaceChildren(node('p', '正在验证网页与静态资源…', 'notice'));
  try {
    const out = await mutation<{ result: Discovery }>('discover', {
      sourceUrl: input('source').value, evidenceUrl: input('evidence').value,
    }, abort.signal);
    if (current === generation) present(out.result);
  } catch (err) {
    if (current === generation) result.replaceChildren(node('p', message(err), 'notice'));
  } finally {
    if (current === generation) scanner = undefined;
    controls();
  }
}
function reportApplied(data: Changed & { message?: string }): void {
  if (data.backup) input('backup').value = data.backup;
  identified = reidentify = false;
  fill(data.config);
  notice('operation', (data.message ?? '操作完成') + (data.backup ? ' · 备份 ' + data.backup : ''), 'healthy');
}
async function save(event: SubmitEvent): Promise<void> {
  event.preventDefault();
  if (!config || pending || reidentify || scanner) return;
  const data: WriteConfig = {
    originHost: input('origin').value.trim(), cdnHost: input('cdn').value.trim(),
    channelId: input('channel').value.trim(), accountId: input('account').value.trim(),
    token: input('render-token').value.trim(),
  };
  if ((identified || !config.tokenPresent) && !data.token) {
    notice('operation', '请填写当前配置所需的 Token。');
    input('render-token').focus();
    return;
  }
  const old = config;
  if (!await confirmDialog('核对上游配置', [
    ['主站', old.originHost + ' → ' + data.originHost],
    ['CDN', old.cdnHost + ' → ' + data.cdnHost],
    ['频道', old.channelId + ' → ' + data.channelId],
    ['账户', old.accountId + ' → ' + data.accountId],
    ['Token', data.token ? '替换' : '保留'],
  ])) return;
  await apply('config', data);
}
async function rollback(event: SubmitEvent): Promise<void> {
  event.preventDefault();
  const backup = input('backup').value.trim();
  if (pending || scanner || !backup) return;
  if (!await confirmDialog('恢复配置备份', [['备份标识', backup]])) return;
  await apply('rollback', { backup });
}
async function apply(path: 'config' | 'rollback', data: object): Promise<void> {
  pending = true;
  controls();
  state(configState(), 'loading', '应用中');
  notice('operation', '');
  try { reportApplied(await mutation<Changed>(path, data)); }
  catch (err) {
    state(configState(), 'error', '应用失败');
    notice('operation', message(err));
  } finally {
    pending = false;
    controls();
  }
}
async function changePassword(event: SubmitEvent): Promise<void> {
  event.preventDefault();
  const modal = dialog('password-dialog');
  const submit = modal.querySelector<HTMLButtonElement>('button[type=submit]');
  if (!submit || submit.disabled || pending || scanner) return;
  submit.disabled = true;
  notice('password-error', '');
  try {
    await mutation('password', {
      currentPassword: input('current-password').value, newPassword: input('new-password').value,
    });
    location.replace('/login');
  } catch (err) { notice('password-error', message(err)); }
  finally { submit.disabled = false; }
}
export function initializeConfiguration(): void {
  void load();
  form('config-form').addEventListener('submit', event => void save(event));
  form('rollback-form').addEventListener('submit', event => void rollback(event));
  form('discover-form').addEventListener('submit', event => void discover(event));
  input('source').addEventListener('input', changedSource);
  input('evidence').addEventListener('input', changedSource);
  button('password-open').addEventListener('click', () => {
    if (pending || scanner) return;
    form('password-form').reset();
    notice('password-error', '');
    dialog('password-dialog').showModal();
  });
  button('password-cancel').addEventListener('click', () => dialog('password-dialog').close());
  form('password-form').addEventListener('submit', event => void changePassword(event));
  button('logout').addEventListener('click', async () => {
    try {
      await request('logout', {});
      location.replace('/login');
    } catch (err) { notice('operation', message(err)); }
  });
}
