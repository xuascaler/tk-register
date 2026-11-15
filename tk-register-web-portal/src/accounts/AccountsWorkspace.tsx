import { MemberAccounts } from './Accounts';
import AccountsOverview from './AccountsOverview';

export default function AccountsWorkspace({ tab, onTab, onOpen }: { tab: 'overview' | 'list'; onTab: (tab: 'overview' | 'list') => void; onOpen: (id: string) => void }) {
  return <>
    <div className='title-row'><div><h1>Accounts</h1><p className='subtitle'>Your accounts and accounts available through your members.</p></div></div>
    <div className='member-settings-tabs account-usage-tabs' role='tablist' aria-label='Accounts views'>
      {(['overview','list'] as const).map(value => <button key={value} type='button' role='tab' id={`accounts-tab-${value}`} aria-selected={tab === value} aria-controls={`accounts-panel-${value}`} tabIndex={tab === value ? 0 : -1} onClick={() => onTab(value)} onKeyDown={e => { if (['ArrowLeft','ArrowRight','Home','End'].includes(e.key)) { e.preventDefault(); const next = e.key === 'Home' ? 'overview' : e.key === 'End' ? 'list' : tab === 'overview' ? 'list' : 'overview'; onTab(next); requestAnimationFrame(() => document.getElementById(`accounts-tab-${next}`)?.focus()); } }}>{value === 'overview' ? 'Overview' : 'Account list'}</button>)}
    </div>
    <div role='tabpanel' id={`accounts-panel-${tab}`} aria-labelledby={`accounts-tab-${tab}`}>
      {tab === 'overview' ? <AccountsOverview /> : <MemberAccounts embedded onOpen={onOpen} />}
    </div>
  </>;
}
