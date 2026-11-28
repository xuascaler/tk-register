export type BillExportUser = { id: string; email: string };
export type BillExportJob = {
  id: string;
  download_filename: string;
  model_usage_source?: 'raw' | 'cache';
  status: 'queued' | 'fetching' | 'filtering' | 'completed' | 'failed' | 'expired';
  users?: BillExportUser[];
  user?: BillExportUser;
  include_sub_users: boolean;
  start_date: string;
  end_date: string;
  created_at: string;
  finished_at?: string;
  expires_at?: string;
  account_count: number;
  summary_count: number;
  matched_account_count: number;
  review_account_count: number;
  row_count: number;
  total_cny: string;
  currency: 'CNY';
  error?: string;
};
export type BillExportSummary = {
  username: string;
  account: string;
  platform_account_id: string;
  latest_submitted_at?: string | null;
  plan: string;
  row_count: number;
  amount_cny: string | null;
  status: 'matched' | 'no_bills' | 'needs_review';
  reason?: string;
};
export type BillExportReview = { username: string; account: string; platform_account_id: string; reason: string };
export type BillExportView = 'summary' | 'details' | 'review';
export type BillExportResults = {
  items: (BillExportSummary | BillExportReview | string[])[];
  total: number;
  page: number;
  page_size: number;
  columns?: string[];
};

function uniqueUsers(users: BillExportUser[]) {
  const seen = new Set<string>();
  return users.filter(user => {
    if (seen.has(user.id)) return false;
    seen.add(user.id);
    return true;
  });
}

export function billExportUsers(job: Pick<BillExportJob, 'users' | 'user'> | null) {
  if (!job) return [];
  return uniqueUsers(job.users?.length ? job.users : job.user ? [job.user] : []);
}

export function toggleBillExportUser(users: BillExportUser[], user: BillExportUser) {
  const selected = uniqueUsers(users);
  if (selected.some(item => item.id === user.id)) return selected.filter(item => item.id !== user.id);
  return [...selected, user];
}

export function sameBillExportUsers(left: BillExportUser[], right: BillExportUser[]) {
  const leftIds = new Set(left.map(user => user.id));
  const rightIds = new Set(right.map(user => user.id));
  return leftIds.size === rightIds.size && [...leftIds].every(id => rightIds.has(id));
}

export function beijingDate(now = new Date()) {
  return new Intl.DateTimeFormat('en-CA', {
    timeZone: 'Asia/Shanghai', year: 'numeric', month: '2-digit', day: '2-digit',
  }).format(now);
}

export function offsetDate(date: string, days: number) {
  const parsed = new Date(`${date}T00:00:00Z`);
  parsed.setUTCDate(parsed.getUTCDate() + days);
  return parsed.toISOString().slice(0, 10);
}

export function exportDateError(start: string, end: string, today = beijingDate()) {
  const valid = (value: string) => /^\d{4}-\d{2}-\d{2}$/.test(value) &&
    Number.isFinite(Date.parse(`${value}T00:00:00Z`)) && new Date(`${value}T00:00:00Z`).toISOString().slice(0, 10) === value;
  if (!valid(start) || !valid(end)) return 'Choose a valid start and end date.';
  if (start > end) return 'The end date must be on or after the start date.';
  if (end > today) return 'The end date cannot be in the future.';
  if ((Date.parse(`${end}T00:00:00Z`) - Date.parse(`${start}T00:00:00Z`)) / 86400000 >= 60) return 'Choose a date range of 60 days or less, including both dates.';
  return '';
}

export function exportRunning(job: BillExportJob | null) {
  return !!job && ['queued', 'fetching', 'filtering'].includes(job.status);
}

export function billAmount(value: string | null | undefined) {
  if (value === null || value === undefined || value === '') return '—';
  const number = Number(value);
  if (!Number.isFinite(number)) return '—';
  return new Intl.NumberFormat('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 6 }).format(number);
}

export function billSubmittedTime(value: string | null | undefined) {
  if (!value) return '—';
  const date = new Date(value);
  if (!Number.isFinite(date.valueOf()) || date.getUTCFullYear() < 1900) return '—';
  const parts = new Intl.DateTimeFormat('en-CA', {
    timeZone: 'Asia/Shanghai', year: 'numeric', month: '2-digit', day: '2-digit',
    hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23',
  }).formatToParts(date);
  const part = (type: string) => parts.find(item => item.type === type)?.value || '';
  return `${part('year')}-${part('month')}-${part('day')} ${part('hour')}:${part('minute')}:${part('second')}`;
}
