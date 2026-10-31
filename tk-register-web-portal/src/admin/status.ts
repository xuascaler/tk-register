import { errorMessage, request } from '../api';
import { executionError, syncVisibleStatuses, type StatusRefresh } from '../submissions/status';
import type { Submission } from '../submissions/types';
import type { AdminDetail } from './types';

// A single bounded pass per page entry/refresh. Shared current submissions are queried once.
export async function syncCurrentStatuses(items: Array<Submission | null>, signal: AbortSignal, force: boolean,
  onRefresh: (id: string, state: StatusRefresh) => void,
  onResult: (id: string, submission: Submission) => void,
) {
  const unique = new Map<string, Submission>();
  for (const item of items) if (item) unique.set(item.id, item);
  await syncVisibleStatuses([...unique.values()], {
    signal, force,
    fetch: (id, forced, requestSignal) => request<AdminDetail>(`/admin/submissions/${encodeURIComponent(id)}/status`, {
      method: 'POST', body: { force: forced }, signal: requestSignal,
    }),
    onChecking: id => onRefresh(id, { checking: true }),
    onResult: (id, result) => {
      onResult(id, result.submission);
      onRefresh(id, { error: executionError(result) });
    },
    onError: (id, cause) => onRefresh(id, { error: errorMessage(cause) }),
  });
}
