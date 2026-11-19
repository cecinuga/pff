// Thin typed HTTP client for the PFF REST API. It attaches the bearer token,
// decodes the JSON error envelope and reports auth problems (expired session,
// missing second factor) to whoever registered a handler.

export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
  ) {
    super(message);
  }
}

type TokenProvider = () => Promise<string | null>;
type AuthProblemHandler = (code: string) => void;

let tokenProvider: TokenProvider = async () => null;
let authProblemHandler: AuthProblemHandler = () => {};

export function setTokenProvider(p: TokenProvider) {
  tokenProvider = p;
}

export function onAuthProblem(h: AuthProblemHandler) {
  authProblemHandler = h;
}

const AUTH_CODES = new Set(["unauthenticated", "not_whitelisted", "mfa_required", "mfa_enrollment_required"]);

type Query = Record<string, string | number | undefined | null>;

function buildURL(path: string, query?: Query): string {
  const url = new URL(`/api/v1${path}`, window.location.origin);
  for (const [k, v] of Object.entries(query ?? {})) {
    if (v !== undefined && v !== null && v !== "") url.searchParams.set(k, String(v));
  }
  return url.pathname + url.search;
}

async function request<T>(method: string, path: string, opts: { query?: Query; body?: unknown; signal?: AbortSignal } = {}): Promise<T> {
  const headers: Record<string, string> = { Accept: "application/json" };
  const token = await tokenProvider();
  if (token) headers.Authorization = `Bearer ${token}`;
  if (opts.body !== undefined) headers["Content-Type"] = "application/json";

  const res = await fetch(buildURL(path, opts.query), {
    method,
    headers,
    body: opts.body === undefined ? undefined : JSON.stringify(opts.body),
    signal: opts.signal,
    credentials: "same-origin",
  });
  if (res.status === 204) return undefined as T;

  const text = await res.text();
  const data = text ? safeJSON(text) : undefined;
  if (!res.ok) {
    const err = (data as { error?: { code?: string; message?: string } } | undefined)?.error;
    const code = err?.code ?? "http_" + res.status;
    if (AUTH_CODES.has(code)) authProblemHandler(code);
    throw new ApiError(res.status, code, err?.message ?? res.statusText);
  }
  return data as T;
}

function safeJSON(text: string): unknown {
  try {
    return JSON.parse(text);
  } catch {
    return undefined;
  }
}

export const api = {
  get: <T>(path: string, query?: Query, signal?: AbortSignal) => request<T>("GET", path, { query, signal }),
  post: <T>(path: string, body?: unknown) => request<T>("POST", path, { body: body ?? {} }),
  put: <T>(path: string, body: unknown) => request<T>("PUT", path, { body }),
  del: <T>(path: string, body?: unknown) => request<T>("DELETE", path, { body }),
};

/** Fetches the public runtime configuration (no auth). */
export async function loadPublicConfig() {
  const res = await fetch("/api/v1/public/config", { headers: { Accept: "application/json" } });
  if (!res.ok) throw new Error(`cannot load configuration (${res.status})`);
  return (await res.json()) as import("./types").PublicConfig;
}
