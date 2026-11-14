export type Protocol = {
  id: string;
  version_id: string;
  code: string;
  name: string;
  operation: 'push' | 'query';
  version: number;
};
export type Vendor = {
  id: string;
  code: string;
  name: string;
  enabled: boolean;
  configured: boolean;
  timezone: string;
  protocols: Protocol[];
  credential_updated_at?: string | null;
};
export type Submission = {
  submit_mode?: 'create' | 'restore_update' | 'create_fallback';
  id: string;
  vendor_id: string;
  vendor_code: string;
  vendor_name: string;
  protocol_version_id: string;
  account: string;
  plan: string;
  status: 'running' | 'accepted' | 'failed' | 'unknown';
  remote_id: string | null;
  remote_alias: string | null;
  remote_status: string | null;
  platform_account_id: string | null;
  parameters: Record<string, unknown>;
  created_at: string;
  updated_at: string;
  last_checked_at: string | null;
  status_checked_at: string | null;
};
export type Execution = {
  error_body?: string;
  error_body_truncated?: boolean;
  error_body_incomplete?: boolean;
  error_body_omitted_reason?: string;
  id: string;
  request_id: string;
  operation: 'push' | 'metrics' | 'bills';
  protocol_name: string;
  protocol_version: number;
  status: 'running' | 'succeeded' | 'failed' | 'unknown';
  http_status: number | null;
  error_code: string | null;
  message: string | null;
  result: Record<string, unknown> | null;
  created_at: string;
  finished_at: string | null;
};
export type MetricResult = {
  begin_time: string;
  end_time: string;
  queried_at: string;
  account: {
    id: string;
    aid: string | null;
    account_alias: string | null;
    status: string | null;
    requests: number | null;
    success_rate: number | null;
    prompt_tokens: number | null;
    completion_tokens: number | null;
  } | null;
};
export type BillResult = {
  start_ds: string;
  end_ds: string;
  queried_at: string;
  currency: 'CNY';
  account: {
    id: string;
    request_count: number | null;
    prompt_tokens: number | null;
    completion_tokens: number | null;
    cache_read_input_tokens: number | null;
    cache_write_input_tokens: number | null;
    cost: number | null;
  } | null;
};
export type SubmissionDetail = {
  submission: Submission;
  executions: Execution[];
  metrics: MetricResult | null;
  bills: BillResult | null;
};
export type SubmissionList = {
  items: Submission[];
  total: number;
  page: number;
  page_size: number;
};
