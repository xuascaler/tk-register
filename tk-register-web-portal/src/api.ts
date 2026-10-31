export type User = {
  id: number;
  email: string;
  role: 'admin' | 'member';
  status: string;
  created_at: string;
};
export type AuthStatus = {
  registration_enabled: boolean;
  password_login_enabled: boolean;
  email_code_login_enabled: boolean;
  email_send_cooldown_seconds: number;
  registration_code_ttl_seconds: number;
  login_code_ttl_seconds: number;
  code_length: number;
  email_max_length: number;
  password_min_length: number;
  password_max_length: number;
};
export type Session = { user: User; csrf_token: string };
export type Challenge = {
  challenge_required: true;
  challenge_id: string;
  expires_in: number;
  code_expires_in: number;
  retry_after: number;
  email?: string;
};
export type CodeDelivery = { retry_after: number; expires_in: number };
export type SettingValue = string | boolean | number;
export type Setting = {
  key: string;
  value: SettingValue;
  category: string;
  type: 'string' | 'email' | 'secret' | 'boolean' | 'integer' | 'enum';
  description: string;
  secret?: boolean;
  configured?: boolean;
  min?: number;
  max?: number;
  options?: string[];
  read_only?: boolean;
};
export type Settings = { settings: Setting[] };

let csrfToken = '';
export function setCSRFToken(value: string) {
  csrfToken = value;
}

export class APIError extends Error {
  constructor(
    message: string,
    public status: number,
    public code = '',
    public retryAfter = 0,
  ) {
    super(message);
    this.name = 'APIError';
  }
}

export async function request<T>(
  path: string,
  options: { method?: string; body?: unknown; signal?: AbortSignal } = {},
): Promise<T> {
  const method = options.method ?? 'GET';
  const headers: Record<string, string> = { Accept: 'application/json' };
  if (options.body !== undefined) headers['Content-Type'] = 'application/json';
  if (method !== 'GET' && csrfToken) headers['X-CSRF-Token'] = csrfToken;
  let response: Response;
  try {
    response = await fetch(`/tk-register/api${path}`, {
      method,
      headers,
      credentials: 'same-origin',
      cache: 'no-store',
      signal: options.signal,
      body:
        options.body === undefined ? undefined : JSON.stringify(options.body),
    });
  } catch (error) {
    if (error instanceof DOMException && error.name === 'AbortError')
      throw error;
    throw new APIError(
      'Unable to connect. Please check your connection and try again.',
      0,
    );
  }
  const payload = await response.json().catch(() => null);
  if (!response.ok || payload?.success === false) {
    const retryAfter =
      Number(
        response.headers.get('Retry-After') ||
          payload?.retry_after ||
          payload?.data?.retry_after,
      ) || 0;
    const error = new APIError(
      payload?.message ||
        'The request could not be completed. Please try again.',
      response.status,
      payload?.code || '',
      retryAfter,
    );
    if (response.status === 401 && csrfToken) {
      csrfToken = '';
      window.dispatchEvent(new Event('tk-session-expired'));
    }
    throw error;
  }
  if (!payload || typeof payload !== 'object')
    throw new APIError(
      'An unexpected response was received. Please try again.',
      response.status,
    );
  return payload.data as T;
}

export function errorMessage(error: unknown) {
  return error instanceof Error
    ? error.message
    : 'Something went wrong. Please try again.';
}
