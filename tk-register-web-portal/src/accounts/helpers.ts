import { t, currentLanguage } from '../i18n';
import type { AccountSnapshot } from './types';

export type Metric = 'cost_total_usd' | 'input_tokens_total' | 'output_tokens_total';
export type TrendPoint = { time: number; value: number; snapshot: AccountSnapshot };

export function knownNumber(value: number | null | undefined): value is number {
  return typeof value === 'number' && Number.isFinite(value);
}

export function validSyncInterval(value: number): boolean {
  return Number.isInteger(value) && value >= 60 && value <= 604800;
}

export function formatCost(value: number | null | undefined, currency: string | null | undefined) {
  if (!knownNumber(value) || !currency) return '—';
  const unit = currency.trim().toUpperCase();
  if (!/^[A-Z]{3}$/.test(unit)) return '—';
  if (unit === 'USD' && currentLanguage() === 'zh-CN') return new Intl.NumberFormat('zh-CN', { style: 'currency', currency: 'USD', currencyDisplay: 'narrowSymbol' }).format(value);
  if (unit === 'USD') return `${new Intl.NumberFormat(currentLanguage(), { minimumFractionDigits: 2, maximumFractionDigits: 2 }).format(value)} $`;
  try {
    return new Intl.NumberFormat(currentLanguage(), { style: 'currency', currency: unit, currencyDisplay: 'code', minimumFractionDigits: 2, maximumFractionDigits: 2 }).format(value);
  } catch {
    return `${unit} ${value.toLocaleString(currentLanguage(), { minimumFractionDigits: 2, maximumFractionDigits: 2 })}`;
  }
}

export function formatTokens(value: number | null | undefined, compact = false) {
  if (!knownNumber(value)) return '—';
  return new Intl.NumberFormat(currentLanguage(), { notation: compact ? 'compact' : 'standard', maximumFractionDigits: compact ? 2 : 0 }).format(value);
}

export function formatPercent(value: number | null | undefined) {
  return knownNumber(value) ? `${value.toLocaleString(currentLanguage(), { maximumFractionDigits: 1 })}%` : '—';
}

export function formatTime(value: string | null | undefined, timezone?: string, short = false) {
  if (!value || !Number.isFinite(Date.parse(value))) return '—';
  return new Intl.DateTimeFormat(currentLanguage(), { month: 'short', day: 'numeric', ...(short ? {} : { year: 'numeric' }), hour: '2-digit', minute: '2-digit', ...(timezone ? { timeZone: timezone } : {}) }).format(new Date(value));
}

function statusKey(status: string | null | undefined) {
  if (!status?.trim()) return '—';
  const labels: Record<string, string> = {
    active: 'Active', in_use: 'Active', using: 'Active', '使用中': 'Active', '正常': 'Active',
    offline: 'Offline', disabled: 'Disabled', banned: 'Banned', '已封禁': 'Banned',
    pending: 'Pending', sync_failed: 'Sync error', login_failed: 'Login failed', re_login_failed: 'Re-login failed', bound_failed: 'Binding failed', cooking_failed: 'Setup failed', limited: 'Limited', unknown: 'Unknown', expired: 'Expired', idle: 'Idle',
  };
  const code = status.toLowerCase();
  return Object.hasOwn(labels, code) ? labels[code] : status.replaceAll('_', ' ');
}

export function statusLabel(status: string | null | undefined) {
  return t(statusKey(status));
}

const supplierLabels: Record<string, string> = {
  using: 'Active', ban: 'Banned', offline: 'Offline', init: 'Initializing',
  bound: 'Binding complete', cooking: 'Account warm-up in progress', ready: 'Ready to sign in',
  wait_check: 'Awaiting sync', pending_activation: 'Awaiting reactivation', re_login: 'Awaiting re-login',
  sync_failed: 'Sync error', login_failed: 'Login failed', re_login_failed: 'Repeated re-login failures',
  cooking_failed: 'Account warm-up failed', bound_failed: 'Binding failed',
};

export function supplierStatusDetail(code: string) {
  const key = code.trim().toLowerCase();
  const label = Object.hasOwn(supplierLabels, key) ? supplierLabels[key] : undefined;
  const status = label && currentLanguage() === 'zh-CN' ? `${t(label)}（${code}）` : code;
  return t('Supplier status: {{status}}', { status });
}

export function statusTone(status: string | null | undefined) {
  const label = statusKey(status);
  return label === 'Active' ? 'positive' : ['Banned', 'Expired', 'Sync error', 'Login failed', 'Re-login failed', 'Binding failed', 'Setup failed'].includes(label) ? 'negative' : label === 'Pending' ? 'pending' : 'neutral';
}

// Spend uses the server's USD value. Missing conversions and reset counters are gaps, never zeros.
export function trendSegments(snapshots: AccountSnapshot[], metric: Metric, intervalSeconds: number) {
  const ordered = [...snapshots].filter(s => Number.isFinite(Date.parse(s.collected_at))).sort((a, b) => Date.parse(a.collected_at) - Date.parse(b.collected_at));
  const segments: TrendPoint[][] = [];
  let current: TrendPoint[] = [];
  const maxGap = Math.max(1, intervalSeconds) * 1000 * 1.6;
  for (const snapshot of ordered) {
    const value = snapshot[metric];
    if (!knownNumber(value)) {
      if (current.length) segments.push(current);
      current = [];
      continue;
    }
    const time = Date.parse(snapshot.collected_at);
    const previous = current.at(-1);
    if (previous && (time - previous.time > maxGap || value < previous.value)) {
      segments.push(current);
      current = [];
    }
    current.push({ time, value, snapshot });
  }
  if (current.length) segments.push(current);
  return segments;
}
