import type { Execution, Submission, SubmissionDetail } from './types';

export type StatusRefresh = { checking?: boolean; error?: string };

export function canCheckStatus(submission: Submission) {
  return submission.status === 'accepted' && Boolean(submission.remote_id);
}

export function statusLabel(submission: Submission, refresh?: StatusRefresh) {
  if (submission.status === 'running') return 'Awaiting acceptance';
  if (submission.status === 'unknown') return 'Awaiting confirmation';
  if (submission.status !== 'accepted') return 'Unavailable';
  if (submission.remote_status) return submission.remote_status;
  return submission.status_checked_at || refresh?.error
    ? 'Status unavailable'
    : 'Not checked';
}

export function executionError(
  detail: SubmissionDetail,
  operation: Execution['operation'] = 'metrics',
) {
  const latest = detail.executions
    .filter((item) => item.operation === operation)
    .sort((a, b) => b.id.length - a.id.length || b.id.localeCompare(a.id))[0];
  if (latest?.status === 'failed' || latest?.status === 'unknown')
    return (
      latest.message ||
      `The ${operation === 'bills' ? 'billing' : 'status'} update failed. Please try again.`
    );
  return '';
}

// A page entry performs one bounded pass; no timers or background polling.
export async function syncVisibleStatuses(
  items: Submission[],
  options: {
    signal: AbortSignal;
    force: boolean;
    fetch: (
      id: string,
      force: boolean,
      signal: AbortSignal,
    ) => Promise<SubmissionDetail>;
    onChecking: (id: string) => void;
    onResult: (id: string, detail: SubmissionDetail) => void;
    onError: (id: string, error: unknown) => void;
  },
) {
  const queue = items.filter(canCheckStatus);
  let next = 0;
  async function worker() {
    while (!options.signal.aborted && next < queue.length) {
      const item = queue[next++];
      options.onChecking(item.id);
      try {
        const result = await options.fetch(
          item.id,
          options.force,
          options.signal,
        );
        if (!options.signal.aborted) options.onResult(item.id, result);
      } catch (error) {
        if (!options.signal.aborted) options.onError(item.id, error);
      }
    }
  }
  await Promise.all(Array.from({ length: Math.min(2, queue.length) }, worker));
}
