import { LoaderCircle, Trash2, X } from 'lucide-react';
import { useEffect, useId, useRef, useState } from 'react';
import { APIError, errorMessage } from '../api';
import { ErrorNotice } from '../ui';
import { checkAccountDeletion, deletionFinished, deletionInProgress, startAccountDeletion, type AccountDeletionResult } from './delete-account';
import type { AccountListEndpoint } from './row-sync';
import type { SyncedAccount } from './types';
import './delete-account.css';

export function DeleteAccountDialog({ account, endpoint, onClose, onProgress, onDeleted }: {
  account: SyncedAccount;
  endpoint: AccountListEndpoint;
  onClose: () => void;
  onProgress: (result: AccountDeletionResult) => void;
  onDeleted: () => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const headingID = useId(), descriptionID = useId();
  const controller = useRef<AbortController | null>(null);
  const mounted = useRef(false), posting = useRef(false), completed = useRef(false);
  const requestID = useRef(crypto.randomUUID());
  const operationID = useRef(deletionInProgress(account) ? account.deletion_operation_id || '' : '');
  const callbacks = useRef({ onClose, onProgress, onDeleted });
  callbacks.current = { onClose, onProgress, onDeleted };
  const [started, setStarted] = useState(Boolean(operationID.current));
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<AccountDeletionResult | null>(null);
  const [error, setError] = useState('');
  const [rejected, setRejected] = useState(false);
  const [monitor, setMonitor] = useState(0);
  const [polling, setPolling] = useState(Boolean(operationID.current));

  useEffect(() => {
    mounted.current = true;
    const previous = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const overflow = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
    const element = dialog.current; element?.showModal();
    return () => {
      mounted.current = false; controller.current?.abort(); element?.close();
      document.body.style.overflow = overflow;
      if (previous?.isConnected) previous.focus();
    };
  }, []);

  function received(next: AccountDeletionResult) {
    operationID.current = next.operation_id; setResult(next); setError('');
    callbacks.current.onProgress(next);
    if (next.status === 'completed' && !completed.current) {
      completed.current = true; callbacks.current.onDeleted();
    }
  }

  useEffect(() => {
    if (!polling || completed.current) return;
    let stopped = false;
    let timer: number | undefined;
    const pollingController = new AbortController();
    const deadline = Date.now() + 60_000;
    async function check() {
      try {
        const next = await checkAccountDeletion(endpoint, account.id, {
          operationID: operationID.current || undefined, requestID: requestID.current,
        }, pollingController.signal);
        if (stopped || !mounted.current) return;
        received(next);
        if (deletionFinished(next)) { setPolling(false); return; }
        const delay = Math.max(2, next.retry_after_seconds || 0) * 1000;
        if (Date.now() + delay >= deadline) { setPolling(false); return; }
        timer = window.setTimeout(() => void check(), delay);
      } catch (cause) {
        if (!stopped && mounted.current) { setError(errorMessage(cause)); setPolling(false); }
      }
    }
    void check();
    return () => { stopped = true; pollingController.abort(); window.clearTimeout(timer); };
  }, [polling, monitor, endpoint, account.id]);

  async function confirm() {
    if (posting.current || started || deletionInProgress(account)) return;
    posting.current = true; setBusy(true); setError(''); setStarted(true);
    const pending = new AbortController(); controller.current = pending;
    try {
      const next = await startAccountDeletion(endpoint, account.id, requestID.current, pending.signal);
      if (!mounted.current || pending.signal.aborted) return;
      received(next);
      if (!deletionFinished(next)) setPolling(true);
    } catch (cause) {
      if (mounted.current && !pending.signal.aborted) {
        setError(errorMessage(cause));
        // A definite validation/auth/rate rejection did not start an operation.
        // Connection failures and ambiguous server responses still use GET recovery.
        if (cause instanceof APIError && [400, 401, 403, 404, 405, 409, 415, 422, 429].includes(cause.status)) setRejected(true);
      }
    } finally {
      posting.current = false;
      if (mounted.current) setBusy(false);
    }
  }

  const failed = rejected || result?.status === 'failed';
  const active = started && !failed;
  return <dialog ref={dialog} className='account-delete-dialog' aria-labelledby={headingID} aria-describedby={descriptionID}
    onCancel={event => { event.preventDefault(); if (!busy) callbacks.current.onClose(); }}>
    <div className='account-delete-heading'><div className='account-delete-icon'><Trash2 aria-hidden='true' /></div><button className='account-delete-close' type='button' aria-label='Close deletion dialog' disabled={busy} onClick={() => callbacks.current.onClose()}><X aria-hidden='true' /></button></div>
    <div className='account-delete-body'><h2 id={headingID}>{failed ? 'Account not removed' : active ? 'Deleting account' : 'Delete account?'}</h2>
      <p className='account-delete-email'>{account.account}</p>
      {endpoint.startsWith('/admin/') && account.user_email && <p className='account-delete-owner'>User <span>{account.user_email}</span></p>}
      <p id={descriptionID}>This account will be removed from Accounts. Submission and usage history will be retained.</p>
      <ErrorNotice>{error || (failed ? result?.message || 'The account could not be removed. Close this dialog and try again later.' : '')}</ErrorNotice>
      {active && <div className='account-delete-progress' role='status'>{(busy || polling) && <LoaderCircle className='usage-spinning' aria-hidden='true' />}<span>{busy ? 'Sending your request…' : result?.status === 'awaiting_verification' ? 'Checking the deletion result. Your account is retained until this is confirmed.' : result ? 'Deletion is processing. You can close this dialog; processing will continue.' : 'The request result has not been confirmed. Check its status before trying again.'}</span></div>}
    </div>
    <div className='account-delete-footer'><button type='button' className='button pale' disabled={busy} onClick={() => callbacks.current.onClose()}>{started ? 'Close' : 'Cancel'}</button>
      {!started ? <button type='button' className='button account-delete-button' onClick={() => void confirm()} disabled={busy || deletionInProgress(account)}><Trash2 aria-hidden='true' />Delete</button> : !failed && <button type='button' className='button pale' disabled={busy || polling} onClick={() => { setError(''); setMonitor(value => value + 1); setPolling(true); }}>{polling ? 'Checking…' : 'Check status'}</button>}
    </div>
  </dialog>;
}
