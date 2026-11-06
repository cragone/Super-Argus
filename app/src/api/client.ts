import createClient from 'openapi-fetch';

import type { components, paths } from './schema';

export type RangeResponse = components['schemas']['RangeResponse'];
export type QueryEntry = components['schemas']['QueryEntry'];
export type Me = components['schemas']['Me'];
export type Panel = components['schemas']['Panel'];
export type Dashboard = components['schemas']['Dashboard'];
export type MetricResponse = components['schemas']['MetricResponse'];
export type SavedQuery = components['schemas']['SavedQuery'];
export type AlertRule = components['schemas']['AlertRule'];
export type AlertRuleInput = components['schemas']['AlertRuleInput'];
export type AlertEvent = components['schemas']['AlertEvent'];
export type Channel = components['schemas']['Channel'];
export type ChannelInput = components['schemas']['ChannelInput'];

/** What the API client needs from the session layer (injected to avoid an import cycle). */
export interface SessionBridge {
  /** Origin of the Argus server, e.g. "http://10.0.0.5:8080". Empty string means same origin (web). */
  getOrigin(): string;
  getAccessToken(): string | null;
  /** Obtain a fresh access token (single-flight). Returns null when the session is over. */
  refresh(): Promise<string | null>;
}

let bridge: SessionBridge | null = null;

export function configureClient(b: SessionBridge) {
  bridge = b;
}

const PLACEHOLDER = 'http://argus.invalid';
const AUTH_PATHS = ['/auth/login', '/auth/refresh', '/auth/logout'];

export function apiUrl(path: string, origin = bridge?.getOrigin() ?? ''): string {
  return `${origin.replace(/\/+$/, '')}/api/v1${path}`;
}

/**
 * Re-targets the request at the real server. The body is buffered rather than passed
 * through as a stream: browsers reject streaming upload bodies over HTTP/1.1
 * (Chrome reports ERR_ALPN_NEGOTIATION_FAILED).
 */
async function resolve(req: Request): Promise<Request> {
  const origin = bridge?.getOrigin() ?? '';
  const url = req.url.replace(PLACEHOLDER, origin.replace(/\/+$/, '') || globalOrigin());
  const hasBody = req.method !== 'GET' && req.method !== 'HEAD';
  const body = hasBody ? await req.clone().arrayBuffer() : undefined;
  return new Request(url, {
    method: req.method,
    headers: req.headers,
    body: body && body.byteLength > 0 ? body : undefined,
    signal: req.signal,
  });
}

function globalOrigin(): string {
  // Web build served by the Go binary: same origin.
  return typeof window !== 'undefined' && window.location ? window.location.origin : '';
}

async function authedFetch(input: Request): Promise<Response> {
  const isAuthPath = AUTH_PATHS.some((p) => input.url.includes(`/api/v1${p}`));
  const retryable = input.clone();
  const req = await resolve(input);
  const token = bridge?.getAccessToken();
  if (token && !isAuthPath) req.headers.set('Authorization', `Bearer ${token}`);
  // The web client's refresh token lives in an HttpOnly cookie, so cookies must be sent.
  const res = await fetch(req, { credentials: 'include' });
  if (res.status !== 401 || isAuthPath || !bridge) return res;

  const fresh = await bridge.refresh();
  if (!fresh) return res;
  const again = await resolve(retryable);
  again.headers.set('Authorization', `Bearer ${fresh}`);
  return fetch(again, { credentials: 'include' });
}

/** Typed client generated from the server's OpenAPI document (see `npm run gen:api`). */
export const api = createClient<paths>({ baseUrl: `${PLACEHOLDER}/api/v1`, fetch: authedFetch });

export interface ApiErrorBody {
  error?: { code?: string; message?: string };
}

/** Best-effort human message from an error envelope or a thrown error. */
export function errorMessage(err: unknown, fallback = 'Request failed'): string {
  if (err && typeof err === 'object') {
    const e = (err as ApiErrorBody).error;
    if (e?.message) return e.message;
    if (err instanceof Error && err.message) return err.message;
  }
  return fallback;
}

export class ApiError extends Error {
  constructor(
    message: string,
    public readonly code?: string,
    public readonly status?: number,
  ) {
    super(message);
  }
}

/** Unwraps an openapi-fetch result, throwing ApiError for non-2xx or network failures. */
export function unwrap<T>(r: { data?: T; error?: unknown; response: Response }): T {
  if (r.error !== undefined || r.data === undefined) {
    const e = (r.error as ApiErrorBody | undefined)?.error;
    throw new ApiError(e?.message ?? `Request failed (${r.response.status})`, e?.code, r.response.status);
  }
  return r.data;
}
