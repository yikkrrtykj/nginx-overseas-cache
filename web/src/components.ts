import type { State } from './api.js';

export function element<T extends HTMLElement>(id: string, type: new () => T): T {
  const node = document.getElementById(id);
  if (!(node instanceof type)) throw new Error('Missing element ' + id);
  return node;
}
export const input = (id: string): HTMLInputElement => element(id, HTMLInputElement);
export const form = (id: string): HTMLFormElement => element(id, HTMLFormElement);
export const button = (id: string): HTMLButtonElement => element(id, HTMLButtonElement);
export const dialog = (id: string): HTMLDialogElement => element(id, HTMLDialogElement);

export function node<K extends keyof HTMLElementTagNameMap>(
  kind: K, content?: string | number, className?: string,
): HTMLElementTagNameMap[K] {
  const el = document.createElement(kind);
  if (content !== undefined) el.textContent = String(content);
  if (className) el.className = className;
  return el;
}

export function write(id: string, value: string | number | undefined): void {
  element(id, HTMLElement).textContent = value === undefined ? '—' : String(value);
}
const labels: Record<State, string> = {
  loading: '读取中', healthy: '正常', degraded: '降级', unknown: '未知',
  empty: '暂无数据', unconfigured: '未配置', stale: '数据过期',
  unavailable: '不可用', error: '异常',
};
export function state(el: HTMLElement, tone: State, label = labels[tone]): void {
  el.dataset.tone = tone;
  el.textContent = label;
}
export function notice(id: string, message: string, tone: State = 'error'): void {
  const target = element(id, HTMLElement);
  target.hidden = !message;
  state(target, tone, message);
}
export function description(label: string, value: string | number): HTMLDivElement {
  const row = node('div');
  row.append(node('dt', label), node('dd', value));
  return row;
}
export function confirmDialog(title: string, lines: readonly [string, string][]): Promise<boolean> {
  const modal = dialog('confirm');
  write('confirm-title', title);
  element('diff', HTMLElement).replaceChildren(...lines.map(([name, value]) => description(name, value)));
  modal.returnValue = 'cancel';
  modal.showModal();
  return new Promise(resolve => modal.addEventListener('close', () => resolve(modal.returnValue === 'confirm'), { once: true }));
}
export function message(err: unknown): string {
  return err instanceof Error && err.name !== 'TypeError' ? err.message : '连接失败，请重试';
}
