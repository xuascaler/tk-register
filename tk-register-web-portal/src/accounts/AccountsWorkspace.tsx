import { MemberAccounts } from './Accounts';
import AccountsOverview from './AccountsOverview';
import { AdminBadge } from '../admin/shared';
import { MemberScopeProvider } from '../members/MemberScope';

type WorkspaceProps = { userID: number; tab: 'overview' | 'list'; onTab: (tab: 'overview' | 'list') => void; onOpen: (id: string) => void; admin?: boolean; raw?: boolean };

export default function AccountsWorkspace(props: WorkspaceProps) {
  // Administrator selections must never change the personal, recursive member scope.
  return props.admin ? <MemberScopeProvider endpoint={props.raw ? '/admin/raw-accounts/overview/sources' : '/admin/accounts/overview/sources'}><Workspace {...props} /></MemberScopeProvider> : <Workspace {...props} />;
}

function Workspace({ userID, tab, onTab, onOpen, admin = false, raw = false }: WorkspaceProps) {
  return <>
    <div className='title-row'><div>{admin && <p className='eyebrow admin-breadcrumb'>Account pool</p>}<h1>{raw ? 'Raw account' : 'Accounts'}</h1><p className='subtitle'>{admin ? 'All accounts, with status and usage grouped by their current owner.' : 'Your accounts and accounts available through your members.'}</p></div>{raw ? <span className='badge'>Super admin only</span> : admin && <AdminBadge />}</div>
    <div className='member-settings-tabs account-usage-tabs' role='tablist' aria-label='Accounts views'>
      {(['overview','list'] as const).map(value => <button key={value} type='button' role='tab' id={`accounts-tab-${value}`} aria-selected={tab === value} aria-controls={`accounts-panel-${value}`} tabIndex={tab === value ? 0 : -1} onClick={() => onTab(value)} onKeyDown={e => { if (['ArrowLeft','ArrowRight','Home','End'].includes(e.key)) { e.preventDefault(); const next = e.key === 'Home' ? 'overview' : e.key === 'End' ? 'list' : tab === 'overview' ? 'list' : 'overview'; onTab(next); requestAnimationFrame(() => document.getElementById(`accounts-tab-${next}`)?.focus()); } }}>{value === 'overview' ? 'Overview' : 'Account list'}</button>)}
    </div>
    <div role='tabpanel' id={`accounts-panel-${tab}`} aria-labelledby={`accounts-tab-${tab}`}>
      {tab === 'overview' ? <AccountsOverview raw={raw} admin={admin} /> : <MemberAccounts raw={raw} userID={userID} embedded admin={admin} onOpen={onOpen} />}
    </div>
  </>;
}
