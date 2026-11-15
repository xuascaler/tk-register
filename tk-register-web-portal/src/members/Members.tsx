import { ArrowUpRight, ChevronLeft, ChevronRight, Link2, Search, UserPlus, UsersRound } from 'lucide-react';
import { useEffect, useRef, useState } from 'react';
import { errorMessage, request } from '../api';
import { ErrorNotice, Loading } from '../ui';
import CreateUserDialog from '../admin/CreateUserDialog';
import AddSubAccountsDialog from './AddSubAccountsDialog';
import { dateTime } from '../submissions/shared';
import { useMemberScope } from './MemberScope';

type Member = { id: string; email: string; status: string; created_at: string; visible_accounts: number };
type MemberPage = { items: Member[]; total: number; page: number; page_size: number };
export default function Members({ onViewAccounts, currentEmail }: { onViewAccounts: () => void; currentEmail: string }) {
  const scope = useMemberScope();
  const [data,setData] = useState<MemberPage | null>(null);
  const [search,setSearch] = useState('');
  const [query,setQuery] = useState('');
  const [page,setPage] = useState(1);
  const [revision,setRevision] = useState(0);
  const [loading,setLoading] = useState(true);
  const [error,setError] = useState('');
  const [notice,setNotice] = useState('');
  const [creating,setCreating] = useState(false);
  const [adding,setAdding] = useState(false);
  const deadlines = useRef<Record<string,number>>({});
  useEffect(() => { const timer=window.setTimeout(() => { setQuery(search.trim()); setPage(1); },300); return () => window.clearTimeout(timer); },[search]);
  useEffect(() => {
    const abort=new AbortController(); setLoading(true); setError('');
    request<MemberPage>(`/members?${new URLSearchParams({ page:String(page),search:query })}`,{ signal:abort.signal })
      .then(result => { if (!abort.signal.aborted) setData(result); })
      .catch(cause => { if (!abort.signal.aborted) setError(errorMessage(cause)); })
      .finally(() => { if (!abort.signal.aborted) setLoading(false); });
    return () => abort.abort();
  },[page,query,revision]);
  return <section className='members-panel' aria-labelledby='members-title'>
    <div className='members-heading'><div><h2 id='members-title'>Members</h2><p className='subtitle'>Your direct members and the accounts available through them.</p></div><div className='members-actions'><button type='button' className='button pale' onClick={() => setCreating(true)}><UserPlus aria-hidden='true' />Create user</button><button type='button' className='button pale' onClick={() => setAdding(true)}><Link2 aria-hidden='true' />Add sub accounts</button></div></div>
    <label className='usage-search member-search'><Search aria-hidden='true' /><span className='sr-only'>Search member email</span><input type='search' value={search} maxLength={50} placeholder='Search member email' onChange={event => setSearch(event.target.value)} /></label>
    <ErrorNotice>{error}</ErrorNotice>{error && <button className='button pale' onClick={() => setRevision(value => value+1)}>Try again</button>}
    {notice && <p className='notice success' role='status'>{notice}</p>}
    {!data && loading ? <Loading label='Loading members…' /> : data && <div className='panel members-table-panel' aria-busy={loading}>
      {data.items.length ? <table className='members-table'><thead><tr><th>Email</th><th>Visible accounts</th><th>Joined</th><th><span className='sr-only'>Actions</span></th></tr></thead><tbody>{data.items.map(member => <tr key={member.id}>
        <td><strong>{member.email}</strong><span className='muted small'>Direct member{member.status !== 'active' ? ' · Inactive' : ''}</span></td>
        <td data-label='Visible accounts'>{member.visible_accounts.toLocaleString()}</td><td data-label='Joined'>{dateTime(member.created_at)}</td>
        <td><button type='button' className='usage-text-button' onClick={() => { scope.setSelection(member.id); onViewAccounts(); }}>View accounts<ArrowUpRight aria-hidden='true' /></button></td>
      </tr>)}</tbody></table> : <div className='usage-empty'><UsersRound aria-hidden='true' /><h3>{query ? 'No matching members' : 'No members yet'}</h3><p>{query ? 'Try another email address.' : 'Create a new user or add an existing user as a direct member.'}</p></div>}
      {data.total > 0 && <div className='pagination'><span className='muted small'>{data.total} direct {data.total === 1 ? 'member' : 'members'} · Page {page} of {Math.max(1,Math.ceil(data.total/data.page_size))}</span><div><button className='button pale' disabled={loading || page<=1} onClick={() => setPage(value=>value-1)}><ChevronLeft aria-hidden='true' />Previous</button><button className='button pale' disabled={loading || page*data.page_size>=data.total} onClick={() => setPage(value=>value+1)}>Next<ChevronRight aria-hidden='true' /></button></div></div>}
    </div>}
    <p className='members-help'>Visible accounts include accounts available through each member. Only your direct members are shown here.</p>
    {creating && <CreateUserDialog memberMode deadlines={deadlines} onClose={() => setCreating(false)} onSaved={user => { setCreating(false); setNotice(`${user.email} was created as your direct member.`); setSearch(''); setQuery(''); setPage(1); setRevision(value=>value+1); scope.reload(); }} />}
    {adding && <AddSubAccountsDialog currentEmail={currentEmail} deadlines={deadlines} onSaved={user => { setAdding(false); setNotice(`${user.email} was added as your direct member.`); setSearch(user.email); setQuery(user.email); setPage(1); setRevision(value=>value+1); scope.reload(); }} directEmails={scope.sources.filter(source => source.id !== 'myself').map(source => source.label)} onClose={() => setAdding(false)} />}
  </section>;
}
