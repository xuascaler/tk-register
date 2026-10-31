import {
  ArrowRight,
  Check,
  ChevronDown,
  FileJson,
  LockKeyhole,
  Upload,
} from 'lucide-react';
import { useEffect, useRef, useState, type FormEvent } from 'react';
import { APIError, errorMessage, request, type User } from '../api';
import { ErrorNotice, Loading, PasswordField } from '../ui';
import type { SubmissionDetail, Vendor } from './types';
import Select from './Select';
import {
  emptyDraft,
  MAX_AUTH_BYTES,
  parseAuthJSON,
  paymentMethods,
  plans,
  registrationLocations,
  submissionParameters,
  validateDraft,
  type AccountDraft,
} from './validation';

export default function SubmitAccount({
  user,
  onDirty,
  onCreated,
  onHistory,
  onConfig,
}: {
  user: User;
  onDirty: (dirty: boolean) => void;
  onCreated: (id: string) => void;
  onHistory: () => void;
  onConfig: () => void;
}) {
  const [vendors, setVendors] = useState<Vendor[] | null>(null);
  const [vendorID, setVendorID] = useState('');
  const [protocolID, setProtocolID] = useState('');
  const [draft, setDraft] = useState(emptyDraft);
  const [errors, setErrors] = useState<
    Partial<Record<keyof AccountDraft, string>>
  >({});
  const [error, setError] = useState('');
  const [loadingError, setLoadingError] = useState('');
  const [busy, setBusy] = useState(false);
  const [uncertain, setUncertain] = useState(false);
  const [attempt, setAttempt] = useState(0);
  const [fileName, setFileName] = useState('');
  const [additionalOpen, setAdditionalOpen] = useState(true);
  const submitting = useRef(false);
  const formRef = useRef<HTMLFormElement>(null);
  const uploadRef = useRef<HTMLInputElement>(null);
  const alive = useRef(true);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);
  const dirty = JSON.stringify(draft) !== JSON.stringify(emptyDraft());
  useEffect(() => {
    onDirty(dirty || busy || uncertain);
    return () => onDirty(false);
  }, [dirty, busy, uncertain, onDirty]);
  useEffect(() => {
    const abort = new AbortController();
    setLoadingError('');
    request<{ vendors: Vendor[] }>('/vendors', { signal: abort.signal })
      .then(({ vendors: list }) => {
        setVendors(list);
        const selected = list.find((item) => item.id === vendorID) || list[0];
        setVendorID(selected?.id || '');
        setProtocolID(
          selected?.protocols.find(
            (item) =>
              item.operation === 'push' && item.version_id === protocolID,
          )?.version_id ||
            selected?.protocols.find((item) => item.operation === 'push')
              ?.version_id ||
            '',
        );
      })
      .catch((cause) => {
        if (!abort.signal.aborted) setLoadingError(errorMessage(cause));
      });
    return () => abort.abort();
  }, [attempt]);
  const vendor = vendors?.find((v) => v.id === vendorID);
  const pushes = vendor?.protocols.filter((p) => p.operation === 'push') || [];
  const ready = vendor?.enabled && vendor.configured && Boolean(protocolID);
  const locked = busy;
  function update(key: keyof AccountDraft, value: string) {
    setDraft((old) => ({ ...old, [key]: value }));
    setErrors((old) => ({ ...old, [key]: undefined }));
    if (key === 'auth_json') setFileName('');
    setError('');
    setUncertain(false);
  }
  async function readFile(file?: File) {
    if (!file) return;
    if (uploadRef.current) uploadRef.current.value = '';
    if (file.size > MAX_AUTH_BYTES) {
      setErrors((old) => ({
        ...old,
        auth_json: 'Choose a JSON file no larger than 2 MB.',
      }));
      return;
    }
    try {
      const value = await file.text();
      parseAuthJSON(value);
      if (!alive.current) return;
      update('auth_json', value);
      setFileName(file.name);
    } catch (cause) {
      setErrors((old) => ({ ...old, auth_json: errorMessage(cause) }));
    }
    if (uploadRef.current) uploadRef.current.value = '';
  }
  async function submit(event?: FormEvent) {
    event?.preventDefault();
    if (submitting.current || !ready) return;
    const invalid = validateDraft(draft);
    setErrors(invalid);
    if (Object.keys(invalid).length) {
      if (
        [
          'balance',
          'payment_method',
          'register_device',
          'recharge_time',
          'warranty_period',
          'tags',
          'remark',
        ].some((key) => invalid[key as keyof AccountDraft])
      )
        setAdditionalOpen(true);
      const first = Object.keys(invalid)[0];
      requestAnimationFrame(() =>
        formRef.current
          ?.querySelector<HTMLElement>(`[name="${first}"]`)
          ?.focus(),
      );
      return;
    }
    // Each deliberate attempt gets a new submission. Do not replay an uncertain
    // attempt or automatically retry a request that may have reached the vendor.
    const payload = {
      vendor_id: vendorID,
      protocol_version_id: protocolID,
      client_request_key: crypto.randomUUID(),
      parameters: submissionParameters(draft),
    };
    submitting.current = true;
    setBusy(true);
    setError('');
    try {
      const result = await request<SubmissionDetail>('/submissions', {
        method: 'POST',
        body: payload,
      });
      if (!alive.current) return;
      if (typeof result?.submission?.id !== 'string' || !result.submission.id) {
        throw new APIError(
          'The submission result could not be confirmed.',
          200,
        );
      }
      setDraft(emptyDraft());
      setUncertain(false);
      onDirty(false);
      onCreated(result.submission.id);
    } catch (cause) {
      if (!alive.current) return;
      const ambiguous =
        !(cause instanceof APIError) ||
        cause.status === 0 ||
        (cause.status >= 200 && cause.status < 400) ||
        cause.status >= 500;
      setUncertain(ambiguous);
      setError(
        ambiguous
          ? 'We could not confirm the result. Check Submission history or submit again. Submitting again creates a new submission; the previous attempt is kept.'
          : errorMessage(cause),
      );
      if (!ambiguous) {
        if (
          cause instanceof APIError &&
          ['PROTOCOL_VERSION_CHANGED', 'VENDOR_NOT_CONFIGURED'].includes(
            cause.code,
          )
        )
          setAttempt((value) => value + 1);
      }
    } finally {
      submitting.current = false;
      if (alive.current) setBusy(false);
    }
  }
  const fieldError = (key: keyof AccountDraft) =>
    errors[key] ? (
      <p className='field-error' id={`error-${key}`}>
        {errors[key]}
      </p>
    ) : null;
  const attrs = (key: keyof AccountDraft) => ({
    name: key,
    id: `submit-${key}`,
    value: draft[key],
    disabled: locked,
    'aria-invalid': Boolean(errors[key]),
    'aria-describedby': errors[key] ? `error-${key}` : undefined,
    onChange: (
      e: React.ChangeEvent<
        HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement
      >,
    ) => update(key, e.target.value),
  });
  const selectAttrs = (key: keyof AccountDraft) => ({
    name: key,
    id: `submit-${key}`,
    value: draft[key],
    disabled: locked,
    invalid: Boolean(errors[key]),
    describedBy: errors[key] ? `error-${key}` : undefined,
    onChange: (value: string) => update(key, value),
  });
  return (
    <div className='submission-page'>
      <div className='title-row'>
        <div>
          <h1>Submit account</h1>
          <p className='subtitle'>Add an account to your selected platform.</p>
        </div>
      </div>
      {loadingError ? (
        <>
          <ErrorNotice>{loadingError}</ErrorNotice>
          <button
            className='button secondary'
            onClick={() => setAttempt((n) => n + 1)}
          >
            Try again
          </button>
        </>
      ) : !vendors ? (
        <Loading label='Loading platforms…' />
      ) : !vendors.length ? (
        <div className='notice'>
          No platforms are available yet. Please contact your administrator.
        </div>
      ) : (
        <>
          {!ready && (
            <div className='notice setup-notice'>
              <LockKeyhole aria-hidden='true' />
              <div>
                <strong>
                  {vendor?.enabled === false
                    ? 'This platform is currently unavailable.'
                    : 'This platform is not ready for submissions.'}
                </strong>
                <p>
                  {user.role === 'admin'
                    ? 'Configure the platform credential and enable it in Config to accept submissions.'
                    : 'Your administrator needs to finish setting up this platform.'}
                </p>
                {user.role === 'admin' && (
                  <button className='text-link' onClick={onConfig}>
                    Open vendor configuration <ArrowRight aria-hidden='true' />
                  </button>
                )}
              </div>
            </div>
          )}
          <form
            ref={formRef}
            className='submission-form panel'
            onSubmit={submit}
            noValidate
            autoComplete='off'
          >
            <section className='form-section'>
              <div className='section-heading'>
                <h2>Platform</h2>
              </div>
              <div className='form-grid'>
                <Select
                  id='submission-vendor'
                  label='Vendor'
                  disabled={locked}
                  value={vendorID}
                  options={vendors.map((vendor) => ({
                    value: vendor.id,
                    label: vendor.name,
                  }))}
                  placeholder='No vendors available'
                  onChange={(value) => {
                    setVendorID(value);
                    setProtocolID(
                      vendors
                        .find((vendor) => vendor.id === value)
                        ?.protocols.find(
                          (protocol) => protocol.operation === 'push',
                        )
                        ?.version_id || '',
                    );
                    setUncertain(false);
                  }}
                />
                <Select
                  id='submission-protocol'
                  label='Protocol'
                  disabled={locked || !pushes.length}
                  value={protocolID}
                  options={pushes.map((protocol) => ({
                    value: protocol.version_id,
                    label: `${protocol.name} · v${protocol.version}`,
                  }))}
                  placeholder='No submission protocol'
                  onChange={(value) => {
                    setProtocolID(value);
                    setUncertain(false);
                  }}
                />
              </div>
            </section>
            <section className='form-section'>
              <div className='section-heading'>
                <h2>Account details</h2>
                <span className='muted small'>All fields required</span>
              </div>
              <div className='form-grid'>
                <div className='field'>
                  <label htmlFor='submit-account'>Account</label>
                  <input
                    {...attrs('account')}
                    placeholder='Account email or login'
                    autoCapitalize='none'
                    spellCheck={false}
                  />
                  {fieldError('account')}
                </div>
                <div>
                  <PasswordField
                    {...attrs('password')}
                    label='Account password'
                    autoComplete='new-password'
                  />
                  {fieldError('password')}
                </div>
                <div className='field'>
                  <Select
                    {...selectAttrs('plan')}
                    label='Plan'
                    required
                    options={plans.map((plan) => ({ value: plan, label: plan }))}
                  />
                  {fieldError('plan')}
                </div>
                <div className='field'>
                  <Select
                    {...selectAttrs('register_location')}
                    label='Registration location'
                    required
                    options={registrationLocations}
                  />
                  {fieldError('register_location')}
                </div>
                <div className='field full-width auth-json-field'>
                  <div className='field-heading'>
                    <label htmlFor='submit-auth_json'>Auth JSON</label>
                    <button
                      type='button'
                      className='button secondary compact'
                      disabled={locked}
                      onClick={() => uploadRef.current?.click()}
                    >
                      <Upload aria-hidden='true' />
                      Upload JSON
                    </button>
                  </div>
                  <input
                    ref={uploadRef}
                    type='file'
                    accept='.json,application/json'
                    className='sr-only'
                    tabIndex={-1}
                    onChange={(e) => void readFile(e.target.files?.[0])}
                    aria-label='Upload authentication JSON file'
                  />
                  <textarea
                    {...attrs('auth_json')}
                    placeholder='Paste your account authentication JSON here'
                    spellCheck={false}
                    autoCapitalize='none'
                    aria-describedby={
                      errors.auth_json ? 'error-auth_json' : 'auth-json-help'
                    }
                  />
                  {fieldError('auth_json')}
                  {fileName ? (
                    <p className='file-feedback'>
                      <Check aria-hidden='true' />
                      <span>{fileName}</span>
                      <button
                        type='button'
                        disabled={locked}
                        className='text-link'
                        onClick={() => update('auth_json', '')}
                      >
                        Remove
                      </button>
                    </p>
                  ) : (
                    <p id='auth-json-help' className='field-help'>
                      Paste an object or upload a JSON file up to 2 MB.
                      Credentials are hidden from submission history.
                    </p>
                  )}
                </div>
              </div>
              <div className='form-grid date-grid'>
                {(
                  [
                    ['register_time', 'Registration date'],
                    ['plan_start_time', 'Plan start date'],
                    ['plan_end_time', 'Plan end date'],
                  ] as const
                ).map(([key, label]) => (
                  <div className='field' key={key}>
                    <label htmlFor={`submit-${key}`}>{label}</label>
                    <input {...attrs(key)} type='date' />
                    {fieldError(key)}
                  </div>
                ))}
              </div>
            </section>
            <details
              className='additional-details'
              open={additionalOpen}
              onToggle={(event) => setAdditionalOpen(event.currentTarget.open)}
            >
              <summary>
                <span>
                  Additional details{' '}
                  <span className='muted small'>Optional</span>
                </span>
                <ChevronDown aria-hidden='true' />
              </summary>
              <div className='form-grid'>
                <div className='field'>
                  <Select
                    {...selectAttrs('payment_method')}
                    label='Payment method'
                    options={[
                      { value: '', label: 'Not specified' },
                      ...paymentMethods.map((method) => ({
                        value: method,
                        label: {
                          physical_credit_card: 'Physical credit card',
                          virtual_credit_card: 'Virtual credit card',
                          paypal: 'PayPal',
                          apple_pay: 'Apple Pay',
                          google_pay: 'Google Pay',
                          third_party_payment: 'Third-party payment',
                        }[method],
                      })),
                    ]}
                  />
                  {fieldError('payment_method')}
                </div>
                <div className='field'>
                  <Select
                    {...selectAttrs('register_device')}
                    label='Registration device'
                    options={[
                      { value: '', label: 'Not specified' },
                      ...['Mac', 'Windows', 'Linux'].map((device) => ({
                        value: device,
                        label: device,
                      })),
                    ]}
                  />
                  {fieldError('register_device')}
                </div>
                <div className='field'>
                  <label htmlFor='submit-balance'>Remaining balance (%)</label>
                  <div className='suffix-field'>
                    <input
                      {...attrs('balance')}
                      inputMode='decimal'
                      placeholder='0–100'
                    />
                    <span>%</span>
                  </div>
                  {fieldError('balance')}
                  <p className='field-help'>
                    Account balance percentage, not a currency amount.
                  </p>
                </div>
                <div className='field'>
                  <label htmlFor='submit-recharge_time'>Recharge date</label>
                  <input {...attrs('recharge_time')} type='date' />
                  {fieldError('recharge_time')}
                </div>
                <div className='field'>
                  <Select
                    {...selectAttrs('warranty_period')}
                    label='Warranty period'
                    options={[
                      { value: '', label: 'Not specified' },
                      ...['1', '7', '30'].map((day) => ({
                        value: day,
                        label: `${day} ${day === '1' ? 'day' : 'days'}`,
                      })),
                    ]}
                  />
                  {fieldError('warranty_period')}
                </div>
                <div className='field'>
                  <Select
                    {...selectAttrs('tags')}
                    label='Tag'
                    options={[
                      { value: '', label: 'Not specified' },
                      { value: '速刷', label: '速刷' },
                      { value: '慢蒸', label: '慢蒸' },
                    ]}
                  />
                  {fieldError('tags')}
                </div>
                <div className='field full-width'>
                  <label htmlFor='submit-remark'>Remark</label>
                  <textarea
                    {...attrs('remark')}
                    rows={3}
                    placeholder='Optional notes about this account'
                  />
                </div>
              </div>
            </details>
            <ErrorNotice>{error}</ErrorNotice>
            <footer className='form-footer'>
              <button className='text-link' type='button' onClick={onHistory}>
                View submission history
              </button>
              <button
                className='button primary'
                type='submit'
                disabled={busy || !ready}
              >
                {busy ? (
                  <Loading label='Submitting…' />
                ) : uncertain ? (
                  'Submit again'
                ) : (
                  <>
                    <FileJson aria-hidden='true' />
                    Submit account
                  </>
                )}
              </button>
            </footer>
          </form>
        </>
      )}
    </div>
  );
}
