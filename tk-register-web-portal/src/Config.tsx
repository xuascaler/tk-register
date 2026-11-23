import { t, translateMessage, useLanguage } from './i18n';
import { Check, CircleDot, LockKeyhole } from 'lucide-react';
import { useEffect, useState, type FormEvent, type KeyboardEvent } from 'react';
import {
  displayError, type DisplayError,
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

const allGroups = [
  ['email', 'Email delivery'],
  ['access', 'Access'],
  ['verification', 'Verification'],
  ['sessions', 'Sessions'],
  ['vendors', 'Vendors'],
  ['account_sync', 'Account sync'],
  ['daily_usage', 'Daily usage'],
  ['overview', 'Overview'],
  ['users', 'Users'],
  ['bill_exports', 'Bill exports'],
  ['exchange_rate', 'Exchange rate'],
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
  useLanguage();
  const [settings, setSettings] = useState<Setting[] | null>(null);
  const [draft, setDraft] = useState<Record<string, SettingValue>>({});
  const groups = allGroups.filter(([key]) => currentUser.is_super_admin || (key !== 'overview' && key !== 'daily_usage'));
  const [category, setCategory] = useState(groups.some(([key]) => key === initialCategory) ? initialCategory : 'email');
  const rawSource = draft.model_usage_source === 'raw';
  const [vendorDirty, setVendorDirty] = useState(false);
  const [syncDirty, setSyncDirty] = useState(false);
  const [error, setError] = useState<DisplayError>('');
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
        if (!controller.signal.aborted) setError(displayError(cause));
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
    if (category === 'account_sync' && syncDirty && !window.confirm(t('Discard your unsaved account sync changes?'))) return;
    if (
      category === 'vendors' &&
      vendorDirty &&
      !window.confirm(t('Discard your unsaved vendor changes?'))
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
    setDraft((current) => setting.key === 'model_usage_source' && value === 'raw'
      ? { ...current, model_usage_source: value, user_overview_model_costs_enabled: 'false' }
      : { ...current, [setting.key]: value });
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
    if (category === 'daily_usage' || settings.some(s => s.category === 'daily_usage' && settingChanged(s, draft[s.key]))) {
      const min = Number(draft.fable_min_percent);
      if (min >= Number(draft.fable_trigger_percent) || min >= Number(draft.fable_max_percent))
        errors.fable_min_percent = 'The minimum must be below the trigger and the maximum.';
    }
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
      setError(displayError(cause));
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
      disabled: busy || setting.read_only || (rawSource && setting.key === 'user_overview_model_costs_enabled'),
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
          <span aria-hidden='true'>{setting.key === 'user_overview_model_costs_enabled' || setting.key === 'api_user_overview_model_costs_enabled' ? String(value) === 'true' ? t("Enabled") : t("Disabled") : String(value)}</span>
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
            label: setting.key === 'model_usage_source' ? option === 'raw' ? t('Raw data') : t('Modified data') : option,
          }))}
        />
      );
    const secret = setting.secret || setting.type === 'secret';
    const percent = setting.category === 'daily_usage' && setting.key.endsWith('_percent');
    const decimal = setting.type === 'decimal';
    const numeric = setting.type === 'integer' || decimal;
    return (
      <>
        <div
          className={
            setting.key.endsWith('_seconds') || percent ? 'input-unit' : undefined
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
            min={numeric ? (setting.min ?? (percent ? 0 : 1)) : undefined}
            max={numeric ? setting.max : undefined}
            step={decimal ? 'any' : numeric ? 1 : undefined}
            inputMode={decimal ? 'decimal' : undefined}
            value={String(value ?? '')}
            placeholder={secret ? t("Enter new password") : undefined}
            onChange={(event) => update(setting, event.target.value)}
          />
          {percent && <span className='unit'>%</span>}
          {setting.key.endsWith('_seconds') && (
            <span className='unit'>{t("seconds")}</span>
          )}
        </div>
        {secret && (
          <p className='value-help'>
            {setting.configured
              ? t("Saved. Leave blank to keep it.")
              : t("No password saved.")}
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
          <h1>{t("Config")}</h1>
          <p className='subtitle'>{t("Manage system settings and user access.")}</p>
        </div>
        <span className='badge'>
          <LockKeyhole aria-hidden='true' />
          {category === 'overview' || category === 'daily_usage' ? t("Super admin only") : t("Admin only")}
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
              {t("Try again")}
            </button>
          ) : (
            <Loading label={t("Loading configuration…")} />
          )}
        </>
      ) : (
        <>
          <div
            className='tabs config-section-tabs'
            role='tablist'
            aria-label={t("Configuration sections")}
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
                {t(title)}
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
              label={t("Configuration section")}
              hiddenLabel
              value={category}
              onChange={selectCategory}
              options={groups.map(([key, title]) => ({
                value: key,
                label: t(title),
              }))}
            />
          </div>
          {category === 'bill_exports' ? (
            <div id='config-panel' role='tabpanel' aria-labelledby='tab-bill_exports'><BillExports isSuperAdmin={currentUser.is_super_admin === true} /></div>
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
                    {t(groups.find(([key]) => key === category)?.[1] || '')}{' '}
                    {t("configuration")}
                  </caption>
                  <colgroup>
                    <col />
                    <col />
                    <col />
                  </colgroup>
                  <thead>
                    <tr>
                      <th scope='col'>{t("Key")}</th>
                      <th scope='col'>{t("Value")}</th>
                      <th scope='col'>{t("Description")}</th>
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
                                {translateMessage(validation[setting.key])}
                              </p>
                            )}
                          </td>
                          <td
                            className='cell-description'
                            id={`description-${setting.key}`}
                          >
                            {setting.key === 'api_user_overview_model_costs_enabled' ? t("Include per-model costs in the Daily usage API. Disabled by default; account totals remain available. This API switch is independent of personal website model details and the model usage source.") : rawSource && setting.key === 'user_overview_model_costs_enabled' ? t("Personal model details are disabled when Raw data is selected. Select Modified data to enable this switch.") : t(setting.description)}
                          </td>
                        </tr>
                      ))}
                  </tbody>
                </table>
                {category === 'daily_usage' && <p className='fixed-rules'><LockKeyhole aria-hidden='true' />{t("New daily bills use the saved rules. Each account day keeps its assigned Fable share on later syncs.")}</p>}
                {category === 'access' && (
                  <p className='fixed-rules'>
                    <LockKeyhole aria-hidden='true' />
                    {t("Email is always the username. Registration requires email verification, and administrator sign-in requires both a password and an email code.")}
                  </p>
                )}
              </div>
              <ErrorNotice>{error}</ErrorNotice>
              {notice && (
                <div className='notice success' role='status'>
                  <Check aria-hidden='true' />
                  {t(notice)}
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
                    ? t(changes === 1 ? '{{count}} changed value' : '{{count}} changed values', { count: changes })
                    : t("No unsaved changes")}
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
                    {t("Discard")}
                  </button>
                  <button
                    className='button primary'
                    type='submit'
                    disabled={!changes || busy}
                  >
                    {busy ? t("Saving…") : t("Save changes")}
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
