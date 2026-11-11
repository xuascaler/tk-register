import { Check, CircleDot, LockKeyhole } from 'lucide-react';
import { useEffect, useState, type FormEvent, type KeyboardEvent } from 'react';
import {
  errorMessage,
  request,
  type Setting,
  type User,
  type Settings,
  type SettingValue,
} from './api';
import { ErrorNotice, Loading } from './ui';
import VendorConfig from './submissions/VendorConfig';
import Select from './submissions/Select';
import Users from './admin/Users';
import BillExports from './admin/BillExports';
import AccountSyncConfig from './accounts/AccountSyncConfig';
import {
  readableDuration,
  settingChanged,
  settingsPayload,
  validateSetting,
} from './validation';

const groups = [
  ['email', 'Email delivery'],
  ['access', 'Access'],
  ['verification', 'Verification'],
  ['sessions', 'Sessions'],
  ['vendors', 'Vendors'],
  ['account_sync', 'Account sync'],
  ['users', 'Users'],
  ['bill_exports', 'Bill exports'],
] as const;
function initialDraft(settings: Setting[]) {
  return Object.fromEntries(
    settings.map((setting) => [
      setting.key,
      setting.secret || setting.type === 'secret' ? '' : setting.value,
    ]),
  );
}

export default function Config({
  onDirty,
  initialCategory = 'email',
  currentUser,
}: {
  onDirty: (dirty: boolean) => void;
  initialCategory?: string;
  currentUser: User;
}) {
  const [settings, setSettings] = useState<Setting[] | null>(null);
  const [draft, setDraft] = useState<Record<string, SettingValue>>({});
  const [category, setCategory] = useState(initialCategory);
  const [vendorDirty, setVendorDirty] = useState(false);
  const [syncDirty, setSyncDirty] = useState(false);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [busy, setBusy] = useState(false);
  const [validation, setValidation] = useState<Record<string, string>>({});
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    setError('');
    request<Settings>('/admin/settings', { signal: controller.signal })
      .then((result) => {
        setSettings(result.settings);
        setDraft(initialDraft(result.settings));
      })
      .catch((cause) => {
        if (!controller.signal.aborted) setError(errorMessage(cause));
      });
    return () => controller.abort();
  }, [attempt]);
  const changes =
    settings?.filter(
      (setting) =>
        !setting.read_only && settingChanged(setting, draft[setting.key]),
    ).length ?? 0;
  useEffect(() => {
    onDirty(changes > 0 || vendorDirty || syncDirty);
    return () => onDirty(false);
  }, [changes, vendorDirty, syncDirty, onDirty]);
  function selectCategory(value: string) {
    if (value === category) return;
    if (category === 'account_sync' && syncDirty && !window.confirm('Discard your unsaved account sync changes?')) return;
    if (
      category === 'vendors' &&
      vendorDirty &&
      !window.confirm('Discard your unsaved vendor changes?')
    )
      return;
    setCategory(value);
  }
  useEffect(() => {
    if (!changes) return;
    const warn = (event: BeforeUnloadEvent) => {
      event.preventDefault();
    };
    window.addEventListener('beforeunload', warn);
    return () => window.removeEventListener('beforeunload', warn);
  }, [changes]);

  function update(setting: Setting, value: SettingValue) {
    setDraft((current) => ({ ...current, [setting.key]: value }));
    setNotice('');
    setValidation((current) => ({
      ...current,
      [setting.key]: validateSetting(setting, value),
    }));
  }
  async function save(event: FormEvent) {
    event.preventDefault();
    if (!settings || busy || !changes) return;
    const errors = Object.fromEntries(
      settings
        .filter((s) => settingChanged(s, draft[s.key]))
        .map((s) => [s.key, validateSetting(s, draft[s.key])])
        .filter(([, value]) => value),
    );
    setValidation(errors);
    setError('');
    setNotice('');
    const firstKey = Object.keys(errors)[0];
    if (firstKey) {
      setCategory(settings.find((s) => s.key === firstKey)!.category);
      setError('Review the highlighted values before saving.');
      return;
    }
    setBusy(true);
    try {
      const result = await request<Settings>('/admin/settings', {
        method: 'PUT',
        body: { settings: settingsPayload(settings, draft) },
      });
      setSettings(result.settings);
      setDraft(initialDraft(result.settings));
      setNotice('Changes saved.');
      setValidation({});
    } catch (cause) {
      setError(errorMessage(cause));
    } finally {
      setBusy(false);
    }
  }
  function tabKey(event: KeyboardEvent<HTMLButtonElement>, index: number) {
    const next =
      event.key === 'ArrowRight'
        ? (index + 1) % groups.length
        : event.key === 'ArrowLeft'
          ? (index + groups.length - 1) % groups.length
          : event.key === 'Home'
            ? 0
            : event.key === 'End'
              ? groups.length - 1
              : -1;
    if (next < 0) return;
    event.preventDefault();
    selectCategory(groups[next][0]);
    document.getElementById(`tab-${groups[next][0]}`)?.focus();
  }
  function control(setting: Setting) {
    const common = {
      id: `value-${setting.key}`,
      'aria-labelledby': `key-${setting.key}`,
      'aria-describedby': `description-${setting.key}${validation[setting.key] ? ` error-${setting.key}` : ''}`,
      disabled: busy || setting.read_only,
      'aria-invalid': !!validation[setting.key],
    };
    const value = draft[setting.key];
    if (setting.type === 'boolean')
      return (
        <label className='switch'>
          <input
            {...common}
            type='checkbox'
            role='switch'
            checked={String(value) === 'true'}
            onChange={(event) => update(setting, String(event.target.checked))}
          />
          <span aria-hidden='true'>{String(value)}</span>
        </label>
      );
    if (setting.options?.length)
      return (
        <Select
          id={common.id}
          label={setting.key}
          hiddenLabel
          describedBy={common['aria-describedby']}
          disabled={common.disabled}
          invalid={common['aria-invalid']}
          value={String(value)}
          onChange={(value) => update(setting, value)}
          options={setting.options.map((option) => ({
            value: option,
            label: option,
          }))}
        />
      );
    const secret = setting.secret || setting.type === 'secret';
    const numeric = setting.type === 'integer';
    return (
      <>
        <div
          className={
            setting.key.endsWith('_seconds') ? 'input-unit' : undefined
          }
        >
          <input
            {...common}
            type={
              secret
                ? 'password'
                : numeric
                  ? 'number'
                  : setting.type === 'email'
                    ? 'email'
                    : 'text'
            }
            autoComplete={secret ? 'new-password' : 'off'}
            min={numeric ? (setting.min ?? 1) : undefined}
            max={numeric ? setting.max : undefined}
            step={numeric ? 1 : undefined}
            value={String(value ?? '')}
            placeholder={secret ? 'Enter new password' : undefined}
            onChange={(event) => update(setting, event.target.value)}
          />
          {setting.key.endsWith('_seconds') && (
            <span className='unit'>seconds</span>
          )}
        </div>
        {secret && (
          <p className='value-help'>
            {setting.configured
              ? 'Saved. Leave blank to keep it.'
              : 'No password saved.'}
          </p>
        )}
        {setting.key.endsWith('_seconds') && readableDuration(value) && (
          <p className='value-help'>{readableDuration(value)}</p>
        )}
      </>
    );
  }
  return (
    <>
      <div className='title-row'>
        <div>
          <h1>Config</h1>
          <p className='subtitle'>Manage system settings and user access.</p>
        </div>
        <span className='badge'>
          <LockKeyhole aria-hidden='true' />
          Admin only
        </span>
      </div>
      {!settings ? (
        <>
          <ErrorNotice>{error}</ErrorNotice>
          {error ? (
            <button
              className='button pale'
              onClick={() => setAttempt((value) => value + 1)}
            >
              Try again
            </button>
          ) : (
            <Loading label='Loading configuration…' />
          )}
        </>
      ) : (
        <>
          <div
            className='tabs config-section-tabs'
            role='tablist'
            aria-label='Configuration sections'
          >
            {groups.map(([key, title], index) => (
              <button
                key={key}
                id={`tab-${key}`}
                type='button'
                role='tab'
                aria-selected={category === key}
                aria-controls='config-panel'
                tabIndex={category === key ? 0 : -1}
                onKeyDown={(event) => tabKey(event, index)}
                onClick={() => selectCategory(key)}
              >
                {title}
                {key !== 'vendors' && key !== 'users' && key !== 'account_sync' && key !== 'bill_exports' && (
                  <span className='tab-count' aria-hidden='true'>
                    {settings.filter((s) => s.category === key).length}
                  </span>
                )}
              </button>
            ))}
          </div>
          <div className='mobile-section'>
            <Select
              id='config-section'
              label='Configuration section'
              hiddenLabel
              value={category}
              onChange={selectCategory}
              options={groups.map(([key, title]) => ({
                value: key,
                label: title,
              }))}
            />
          </div>
          {category === 'bill_exports' ? (
            <div id='config-panel' role='tabpanel' aria-labelledby='tab-bill_exports'><BillExports /></div>
          ) : category === 'users' ? (
            <div id='config-panel' role='tabpanel' aria-labelledby='tab-users'><Users currentUser={currentUser} registrationEnabled={String(settings.find(setting => setting.key === 'registration_enabled')?.value) === 'true'} /></div>
          ) : category === 'account_sync' ? (
            <div id='config-panel' role='tabpanel' aria-labelledby='tab-account_sync'><AccountSyncConfig onDirty={setSyncDirty} /></div>
          ) : category === 'vendors' ? (
            <div
              id='config-panel'
              role='tabpanel'
              aria-labelledby='tab-vendors'
            >
              <VendorConfig onDirty={setVendorDirty} />
            </div>
          ) : (
            <form onSubmit={save} noValidate>
              <div
                id='config-panel'
                role='tabpanel'
                aria-labelledby={`tab-${category}`}
              >
                <table className='config-table'>
                  <caption className='sr-only'>
                    {groups.find(([key]) => key === category)?.[1]}{' '}
                    configuration
                  </caption>
                  <colgroup>
                    <col />
                    <col />
                    <col />
                  </colgroup>
                  <thead>
                    <tr>
                      <th scope='col'>Key</th>
                      <th scope='col'>Value</th>
                      <th scope='col'>Description</th>
                    </tr>
                  </thead>
                  <tbody>
                    {settings
                      .filter((setting) => setting.category === category)
                      .map((setting) => (
                        <tr
                          key={setting.key}
                          className={
                            settingChanged(setting, draft[setting.key])
                              ? 'modified'
                              : ''
                          }
                        >
                          <th scope='row'>
                            <code id={`key-${setting.key}`}>
                              {setting.key.split('_').map((part, index) => (
                                <span key={index}>
                                  {index > 0 ? '_' : ''}
                                  <wbr />
                                  {part}
                                </span>
                              ))}
                            </code>
                          </th>
                          <td className='cell-value'>
                            {control(setting)}
                            {validation[setting.key] && (
                              <p
                                className='field-error'
                                id={`error-${setting.key}`}
                              >
                                {validation[setting.key]}
                              </p>
                            )}
                          </td>
                          <td
                            className='cell-description'
                            id={`description-${setting.key}`}
                          >
                            {setting.description}
                          </td>
                        </tr>
                      ))}
                  </tbody>
                </table>
                {category === 'access' && (
                  <p className='fixed-rules'>
                    <LockKeyhole aria-hidden='true' />
                    Email is always the username. Registration requires email
                    verification, and administrator sign-in requires both a
                    password and an email code.
                  </p>
                )}
              </div>
              <ErrorNotice>{error}</ErrorNotice>
              {notice && (
                <div className='notice success' role='status'>
                  <Check aria-hidden='true' />
                  {notice}
                </div>
              )}
              <div className='save-footer'>
                <span className='save-state' role='status'>
                  {changes ? (
                    <CircleDot aria-hidden='true' />
                  ) : (
                    <Check aria-hidden='true' />
                  )}
                  {changes
                    ? `${changes} changed ${changes === 1 ? 'value' : 'values'}`
                    : 'No unsaved changes'}
                </span>
                <div className='footer-actions'>
                  <button
                    type='button'
                    className='button quiet'
                    disabled={!changes || busy}
                    onClick={() => {
                      setDraft(initialDraft(settings));
                      setError('');
                      setNotice('');
                      setValidation({});
                    }}
                  >
                    Discard
                  </button>
                  <button
                    className='button primary'
                    type='submit'
                    disabled={!changes || busy}
                  >
                    {busy ? 'Saving…' : 'Save changes'}
                  </button>
                </div>
              </div>
            </form>
          )}
        </>
      )}
    </>
  );
}
