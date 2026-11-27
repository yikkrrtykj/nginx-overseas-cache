export type State = 'loading' | 'healthy' | 'degraded' | 'unknown' | 'empty' | 'unconfigured' | 'stale' | 'unavailable' | 'error';

export interface RendererConfig {
  originHost: string;
  cdnHost: string;
  channelId: string;
  accountId: string;
  tokenPresent: boolean;
}
export interface Group {
  origin: string;
  channel: string;
  mode: string;
  tokenId: string;
  localClients: number;
  acknowledgedClients: number;
  upstreamConnected: boolean;
  upstreamReady: boolean;
  reconnects?: number;
  generation?: number;
  connectedSeconds?: number;
  queuedMessages?: number;
}
export interface Status {
  version: number;
  available: boolean;
  state: State;
  message: string;
  generatedAt?: string;
  uptimeSeconds: number;
  localClients: number;
  acknowledgedClients: number;
  activeHubs: number;
  upstreamConnections: number;
  savedUpstreamConnections: number;
  reconnects?: number;
  slowDisconnects?: number;
  queueOverflows?: number;
  goroutines?: number;
  groups: Group[];
}
export interface Traffic {
  nginxState?: State;
  points?: { time: string; entries: number; upstreamRequests: number }[];
  state: State;
  observedAt?: string;
  entries: number;
  errors: number;
  responseBytes: number;
  hitResponseBytes: number;
  upstreamRequests: number;
  counts?: Record<string, number>;
}
export interface Candidate {
  host: string;
  types: string[];
  samples: string[];
  compatible: boolean;
  verified: boolean;
  valid: boolean;
}
export interface Discovery {
  sourceHost: string;
  channelId: string;
  accountId: string;
  token: string;
  recommendedHost: string;
  candidates: Candidate[];
  warnings: string[];
}
interface Reply {
  ok: boolean;
  message?: string;
  csrfToken?: string;
}
export class RequestError extends Error {
  constructor(message: string, readonly status: number) { super(message); }
}
let nonce = '';
export async function request<T extends object>(
  path: string,
  body?: object,
  signal?: AbortSignal,
): Promise<T & Reply> {
  const headers: Record<string, string> = {};
  if (body) {
    headers['Content-Type'] = 'application/json';
    if (nonce) headers['X-CSRF-Token'] = nonce;
  }
  const response = await fetch('/api/v2/' + path, {
    method: body ? 'POST' : 'GET', headers, signal,
    body: body ? JSON.stringify(body) : undefined,
    credentials: 'same-origin', cache: 'no-store',
  });
  let reply: T & Reply;
  try { reply = await response.json() as T & Reply; }
  catch { throw new RequestError('服务响应不可用', response.status); }
  if (reply.csrfToken) nonce = reply.csrfToken;
  if (!response.ok || !reply.ok) {
    if (response.status === 401 && path !== 'login') location.assign('/login');
    throw new RequestError(reply.message ?? '请求失败', response.status);
  }
  return reply;
}
