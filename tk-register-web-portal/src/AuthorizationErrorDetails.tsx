import { t, translateMessage, useLanguage } from './i18n';
import { Copy } from 'lucide-react';
import { useState } from 'react';
import { APIError } from './api';
import { authorizationErrorCode, authorizationStageLabel, readAuthorizationDiagnostics } from './authorization-diagnostics';
import './authorization-error-details.css';

export default function AuthorizationErrorDetails({ error }: { error: APIError | null }) {
  useLanguage();
  const [announcement, setAnnouncement] = useState('');
  if (!error) return null;
  const details = readAuthorizationDiagnostics(error.diagnostics);
  const code = authorizationErrorCode(error.code);
  if (!details && !code) return null;
  async function copyRequestID() {
    if (!details?.request_id) return;
    try { await navigator.clipboard.writeText(details.request_id); setAnnouncement('Request ID copied.'); }
    catch { setAnnouncement('Select and copy the Request ID below.'); }
  }
  return <details className='authorization-error-details' key={details?.request_id || code}>
    <summary>{t("Error details")}</summary>
    <dl>
      {details?.stage && <div><dt>{t("Step")}</dt><dd>{t(authorizationStageLabel(details.stage))}</dd></div>}
      {details?.upstream_status !== undefined && <div><dt>{t("Upstream HTTP")}</dt><dd>{details.upstream_status}</dd></div>}
      {code && <div><dt>{t("Error code")}</dt><dd><code>{code}</code></dd></div>}
      {details?.upstream_code && <div><dt>{t("Upstream code")}</dt><dd><code>{details.upstream_code}</code></dd></div>}
      {details?.response_kind && <div><dt>{t("Response format")}</dt><dd>{details.response_kind.toUpperCase()}</dd></div>}
      {details?.request_id && <div className='authorization-error-request'><dt>{t("Request ID")}</dt><dd><code tabIndex={0}>{details.request_id}</code><button type='button' className='text-link' aria-label={t("Copy Request ID")} onClick={() => void copyRequestID()}><Copy aria-hidden='true' />{t("Copy")}</button></dd></div>}
    </dl>
    <p className='authorization-error-announcement' role='status'>{translateMessage(announcement)}</p>
  </details>;
}
