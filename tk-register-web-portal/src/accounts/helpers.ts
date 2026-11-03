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
  if (unit === 'USD') return `${new Intl.NumberFormat('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 }).format(value)} $`;
  try {
    return new Intl.NumberFormat('en-US', { style: 'currency', currency: unit, currencyDisplay: 'code', minimumFractionDigits: 2, maximumFractionDigits: 2 }).format(value);
  } catch {
    return `${unit} ${value.toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 })}`;
  }
}

export function formatTokens(value: number | null | undefined, compact = false) {
  if (!knownNumber(value)) return '—';
  return new Intl.NumberFormat('en-US', { notation: compact ? 'compact' : 'standard', maximumFractionDigits: compact ? 2 : 0 }).format(value);
}

export function formatPercent(value: number | null | undefined) {
  return knownNumber(value) ? `${value.toLocaleString('en-US', { maximumFractionDigits: 1 })}%` : '—';
}

export function formatTime(value: string | null | undefined, timezone?: string, short = false) {
  if (!value || !Number.isFinite(Date.parse(value))) return '—';
  return new Intl.DateTimeFormat('en-US', { month: 'short', day: 'numeric', ...(short ? {} : { year: 'numeric' }), hour: '2-digit', minute: '2-digit', ...(timezone ? { timeZone: timezone } : {}) }).format(new Date(value));
}

export function statusLabel(status: string | null | undefined) {
  if (!status?.trim()) return '—';
  const labels: Record<string, string> = {
    active: 'Active', in_use: 'Active', using: 'Active', '使用中': 'Active', '正常': 'Active',
    offline: 'Offline', disabled: 'Disabled', banned: 'Banned', '已封禁': 'Banned',
    pending: 'Pending', unknown: 'Unknown', expired: 'Expired', idle: 'Idle',
  };
  return labels[status.toLowerCase()] || status.replaceAll('_', ' ');
}

export function statusTone(status: string | null | undefined) {
  const label = statusLabel(status);
  return label === 'Active' ? 'positive' : ['Banned', 'Expired'].includes(label) ? 'negative' : label === 'Pending' ? 'pending' : 'neutral';
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
