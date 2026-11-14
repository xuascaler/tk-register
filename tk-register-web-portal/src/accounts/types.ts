export type AccountSnapshot = {
  id: string;
  account_id: string;
  snapshot_date: string;
  collected_at: string;
  source_updated_at: string | null;
  account_status: string | null;
  upstream_status_raw?: string | null;
  upstream_error_message?: string | null;
  input_tokens_total: number | null;
  output_tokens_total: number | null;
  cost_total: number | null;
  currency: string | null;
  cost_total_usd: number | null;
  exchange_rate_cny_per_usd: number;
  usage_5h_percent: number | null;
  reset_5h_at: string | null;
  usage_7d_percent: number | null;
  reset_7d_at: string | null;
  data_source: string;
};

export type SyncedAccount = {
  id: string;
  user_id?: string;
  user_email?: string;
  account: string;
  plan: string;
  vendor_id: string;
  vendor_name: string;
  current_submission_id: string | null;
  latest_submission_id: string;
  latest_submitted_at?: string | null;
  submission_status: string;
  remote_id: string | null;
  platform_account_id: string | null;
  supplier_record_id?: string | null;
  auth_json_updated_at?: string | null;
  deletion_status?: string;
  deletion_operation_id?: string | null;
  deleted_at?: string | null;
  last_success_at: string | null;
  last_attempt_at: string | null;
  last_error: string;
  snapshot: AccountSnapshot | null;
};

export type AccountPage = {
  items: SyncedAccount[];
  total: number;
  page: number;
  page_size: number;
};

export type AccountTrendPoint = { snapshot_id: string; collected_at: string; value: number };
export type AccountTrend = Record<'cost_total_usd' | 'input_tokens_total' | 'output_tokens_total', AccountTrendPoint[][]>;
export type SnapshotPage = { items: AccountSnapshot[]; total: number; page: number; page_size: number };

export type AccountDetails = {
  account: SyncedAccount;
  snapshots: AccountSnapshot[];
  snapshot_count: number;
  snapshot_page: number;
  snapshot_page_size: number;
  trend: AccountTrend;
  trend_reduced: boolean;
  interval_seconds: number;
  timezone: string;
};

export type SyncConfig = {
  enabled: boolean;
  interval_seconds: number;
  timezone: string;
  data_source: 'website';
  website_username: string;
  password_configured: boolean;
  running: boolean;
  last_run_at: string | null;
  last_success_at: string | null;
  last_error: string;
  next_run_at: string | null;
};
