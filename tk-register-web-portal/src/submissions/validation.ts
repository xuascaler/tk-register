export const MAX_AUTH_BYTES = 2 * 1024 * 1024;
export const plans = [
  'claude/cc-max-20x',
  'claude/cc-max-5x',
  'claude/cc-pro',
] as const;
export const registrationLocations = [
  { value: 'US', label: 'United States' },
  { value: 'MY', label: 'Malaysia' },
  { value: 'ID', label: 'Indonesia' },
  { value: 'SG', label: 'Singapore' },
  { value: 'PH', label: 'Philippines' },
] as const;
export const paymentMethods = [
  'physical_credit_card',
  'virtual_credit_card',
  'paypal',
  'apple_pay',
  'google_pay',
  'third_party_payment',
] as const;
export const authFields = [
  'id_token',
  'access_token',
  'refresh_token',
  'last_refresh',
  'email',
  'account_uuid',
  'organization_uuid',
  'organization_name',
  'claude_device_ids',
  'type',
  'expired',
  'proxy_url',
  'verification_status',
];
export type AccountDraft = {
  account: string;
  password: string;
  plan: string;
  auth_json: string;
  register_location: string;
  register_time: string;
  plan_start_time: string;
  plan_end_time: string;
  payment_method: string;
  register_device: string;
  balance: string;
  recharge_time: string;
  warranty_period: string;
  tags: string;
  remark: string;
};
export function emptyDraft(): AccountDraft {
  return {
    account: '',
    password: '',
    plan: plans[0],
    auth_json: '',
    register_location: 'US',
    register_time: '',
    plan_start_time: '',
    plan_end_time: '',
    payment_method: '',
    register_device: '',
    balance: '',
    recharge_time: '',
    warranty_period: '',
    tags: '',
    remark: '',
  };
}
export function validDate(value: string) {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(value)) return false;
  const parsed = new Date(`${value}T00:00:00Z`);
  return (
    !Number.isNaN(parsed.valueOf()) &&
    parsed.toISOString().slice(0, 10) === value
  );
}
function validTimestamp(value: string) {
  return (
    /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/.test(
      value,
    ) &&
    validDate(value.slice(0, 10)) &&
    Number.isFinite(Date.parse(value))
  );
}
export function parseAuthJSON(value: string): Record<string, unknown> {
  if (new TextEncoder().encode(value).length > MAX_AUTH_BYTES)
    throw new Error('Auth JSON must be 2 MB or smaller.');
  let parsed: unknown;
  try {
    parsed = JSON.parse(value);
    if (typeof parsed === 'string') parsed = JSON.parse(parsed);
  } catch {
    throw new Error('Enter a valid JSON object or upload a JSON file.');
  }
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed))
    throw new Error('Auth JSON must contain an object.');
  const auth = parsed as Record<string, unknown>;
  const missing = authFields.filter((key) => !(key in auth));
  if (missing.length) throw new Error(`Missing fields: ${missing.join(', ')}.`);
  for (const key of authFields) {
    if (key === 'claude_device_ids') {
      if (
        !Array.isArray(auth[key]) ||
        !auth[key].length ||
        auth[key].some((v: unknown) => typeof v !== 'string' || !v.trim())
      )
        throw new Error(
          'claude_device_ids must contain at least one non-empty device ID.',
        );
    } else if (
      typeof auth[key] !== 'string' ||
      (key !== 'id_token' && !(auth[key] as string).trim())
    )
      throw new Error(
        `${key} must be ${key === 'id_token' ? 'a string (it may be empty)' : 'a non-empty string'}.`,
      );
  }
  for (const key of ['last_refresh', 'expired'])
    if (!validTimestamp(auth[key] as string))
      throw new Error(
        `${key} must be an RFC 3339 timestamp, including its timezone.`,
      );
  if (!/^(https?|socks5h?):\/\//.test(auth.proxy_url as string))
    throw new Error(
      'proxy_url must start with http://, https://, socks5:// or socks5h://.',
    );
  return auth;
}
export function validateDraft(draft: AccountDraft) {
  const errors: Partial<Record<keyof AccountDraft, string>> = {};
  if (!draft.account || /\s/.test(draft.account))
    errors.account = 'Enter the account login without spaces.';
  if (!draft.password) errors.password = 'Enter the account password.';
  if (!(plans as readonly string[]).includes(draft.plan))
    errors.plan = 'Choose a supported plan.';
  if (
    !registrationLocations.some(
      ({ value }) => value === draft.register_location,
    )
  )
    errors.register_location = 'Choose a supported registration location.';
  try {
    parseAuthJSON(draft.auth_json);
  } catch (error) {
    errors.auth_json = (error as Error).message;
  }
  for (const key of [
    'register_time',
    'plan_start_time',
    'plan_end_time',
  ] as const)
    if (!validDate(draft[key])) errors[key] = 'Choose a valid date.';
  if (
    validDate(draft.plan_start_time) &&
    validDate(draft.plan_end_time) &&
    draft.plan_end_time < draft.plan_start_time
  )
    errors.plan_end_time = 'End date must be on or after the start date.';
  if (draft.recharge_time && !validDate(draft.recharge_time))
    errors.recharge_time = 'Choose a valid date.';
  if (
    draft.balance &&
    (!/^\d+(?:\.\d+)?$/.test(draft.balance) || Number(draft.balance) > 100)
  )
    errors.balance = 'Enter a percentage from 0 to 100.';
  if (
    draft.warranty_period &&
    !['1', '7', '30'].includes(draft.warranty_period)
  )
    errors.warranty_period = 'Choose 1, 7 or 30 days.';
  if (draft.tags && !['速刷', '慢蒸'].includes(draft.tags))
    errors.tags = 'Choose one tag.';
  if (
    draft.payment_method &&
    !(paymentMethods as readonly string[]).includes(draft.payment_method)
  )
    errors.payment_method = 'Choose a supported payment method.';
  if (
    draft.register_device &&
    !['Mac', 'Windows', 'Linux'].includes(draft.register_device)
  )
    errors.register_device = 'Choose a supported device.';
  return errors;
}
export function submissionParameters(draft: AccountDraft) {
  const parameters: Record<string, unknown> = {
    account: draft.account,
    password: draft.password,
    plan: draft.plan,
    auth_json: parseAuthJSON(draft.auth_json),
    register_location: draft.register_location,
    register_time: draft.register_time,
    plan_start_time: draft.plan_start_time,
    plan_end_time: draft.plan_end_time,
  };
  for (const key of [
    'payment_method',
    'register_device',
    'recharge_time',
    'remark',
  ] as const)
    if (draft[key]) parameters[key] = draft[key];
  if (draft.balance !== '') parameters.balance = Number(draft.balance);
  if (draft.warranty_period !== '')
    parameters.warranty_period = Number(draft.warranty_period);
  if (draft.tags) parameters.tags = [draft.tags];
  return parameters;
}
export function vendorTimestamp(date: Date, timezone: string) {
  const parts = new Intl.DateTimeFormat('en-CA', {
    timeZone: timezone,
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hourCycle: 'h23',
  }).formatToParts(date);
  const part = (type: string) => parts.find((p) => p.type === type)?.value;
  return `${part('year')}-${part('month')}-${part('day')} ${part('hour')}:${part('minute')}:${part('second')}`;
}
export function billRange(days: number, timezone: string, now = new Date()) {
  const end = vendorTimestamp(now, timezone).slice(0, 10);
  const start = new Date(`${end}T12:00:00Z`);
  start.setUTCDate(start.getUTCDate() - days + 1);
  return { start_ds: start.toISOString().slice(0, 10), end_ds: end };
}
