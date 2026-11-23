import { t } from '../i18n';
import { useCallback, useEffect, useState } from 'react';
import type { User } from '../api';
import ManualSubmitAccount from './ManualSubmitAccount';
import AuthorizeSubmit from './AuthorizeSubmit';
import './authorize-submit.css';

type Props = {
  user: User;
  onDirty: (dirty: boolean) => void;
  onCreated: (id: string) => void;
  onHistory: () => void;
  onConfig: () => void;
};

export default function SubmitAccount(props: Props) {
  const [tab, setTab] = useState<'manual' | 'authorize'>('manual');
  const [manualDirty, setManualDirty] = useState(false);
  const [authorizeDirty, setAuthorizeDirty] = useState(false);
  const manualChanged = useCallback((dirty: boolean) => setManualDirty(dirty), []);
  const authorizeChanged = useCallback((dirty: boolean) => setAuthorizeDirty(dirty), []);
  useEffect(() => {
    props.onDirty(manualDirty || authorizeDirty);
    return () => props.onDirty(false);
  }, [manualDirty, authorizeDirty, props.onDirty]);
  return <div className='submission-page'>
    <div className='title-row'><div><h1>{t('Submit account')}</h1><p className='subtitle'>{t('Authorize your Claude account, or upload your existing Auth JSON.')}</p></div></div>
    <div className='authorize-page-tabs' role='tablist' aria-label={t('Submission method')}>
      <button type='button' role='tab' id='manual-submit-tab' aria-controls='manual-submit-panel' aria-selected={tab === 'manual'} onClick={() => setTab('manual')}>{t('Upload Auth JSON')}</button>
      <button type='button' role='tab' id='authorize-submit-tab' aria-controls='authorize-submit-panel' aria-selected={tab === 'authorize'} onClick={() => setTab('authorize')}>{t('Authorize & submit')}</button>
    </div>
    <div id='manual-submit-panel' role='tabpanel' aria-labelledby='manual-submit-tab' hidden={tab !== 'manual'}><ManualSubmitAccount {...props} onDirty={manualChanged} /></div>
    <div id='authorize-submit-panel' role='tabpanel' aria-labelledby='authorize-submit-tab' hidden={tab !== 'authorize'}><AuthorizeSubmit {...props} onDirty={authorizeChanged} /></div>
  </div>;
}
