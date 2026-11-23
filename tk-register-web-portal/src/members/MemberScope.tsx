import { t } from '../i18n';
import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from 'react';
import { errorMessage, request } from '../api';

export type AccountSource = { id: string; label: string };
type Scope = { sources: AccountSource[]; selection: string; setSelection: (value: string) => void; loading: boolean; error: string; reload: () => void };
const MemberContext = createContext<Scope | null>(null);
export function MemberScopeProvider({ children, endpoint = '/members/sources' }: { children: ReactNode; endpoint?: string }) {
  const [sources, setSources] = useState<AccountSource[]>([{ id: 'myself', label: 'myself' }]);
  const [selection, setSelection] = useState('all');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [revision, setRevision] = useState(0);
  const reload = useCallback(() => setRevision(value => value + 1), []);
  useEffect(() => {
    const abort = new AbortController(); setLoading(true); setError('');
    request<{ sources: AccountSource[] }>(endpoint, { signal: abort.signal })
      .then(result => { if (!abort.signal.aborted) setSources(result.sources); })
      .catch(cause => { if (!abort.signal.aborted) setError(errorMessage(cause)); })
      .finally(() => { if (!abort.signal.aborted) setLoading(false); });
    return () => abort.abort();
  }, [endpoint, revision]);
  return <MemberContext.Provider value={{ sources, selection, setSelection, loading, error, reload }}>{children}</MemberContext.Provider>;
}
export function useMemberScope() {
  const scope = useContext(MemberContext);
  if (!scope) throw new Error('Member scope is unavailable.');
  return scope;
}
export function selectedSources(selection: string, sources: AccountSource[]) {
  return selection === 'all' ? sources.map(source => source.id) : selection === 'none' ? [] : selection.split(',');
}
export function sourceSummary(selection: string, sources: AccountSource[]) {
  const selected = selectedSources(selection, sources);
  if (selection === 'all' || sources.length > 0 && selected.length === sources.length) return t('All sources');
  if (!selected.length) return t('No sources');
  if (selected.length === 1) return selected[0] === 'myself' ? t('myself') : sources.find(source => source.id === selected[0])?.label || t('Selected source');
  return t('Selected sources');
}
