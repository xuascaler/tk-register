import type { Setting, SettingValue } from './api';

export function validEmail(email: string) {
  return email.length <= 50 && /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email);
}
export function validCode(code: string) {
  return /^[a-zA-Z0-9]{6}$/.test(code);
}
export function passwordWithinLimit(password: string) {
  return new TextEncoder().encode(password).length <= 72;
}
export function readableDuration(value: SettingValue) {
  const seconds = Number(value);
  if (!Number.isInteger(seconds) || seconds < 60) return '';
  for (const [unit, factor] of [
    ['day', 86400],
    ['hour', 3600],
    ['minute', 60],
  ] as const) {
    if (seconds % factor === 0)
      return `${seconds / factor} ${unit}${seconds / factor === 1 ? '' : 's'}`;
  }
  return '';
}
export function settingChanged(setting: Setting, draft: SettingValue) {
  if (setting.secret || setting.type === 'secret') return String(draft) !== '';
  return String(setting.value) !== String(draft);
}
export function settingsPayload(
  settings: Setting[],
  draft: Record<string, SettingValue>,
) {
  return Object.fromEntries(
    settings
      .filter((s) => !s.read_only && settingChanged(s, draft[s.key]))
      .map((s) => [s.key, String(draft[s.key])]),
  );
}
export function validateSetting(setting: Setting, value: SettingValue): string {
  if (setting.read_only || setting.secret || setting.type === 'secret')
    return '';
  if (setting.type === 'integer') {
    const number = Number(value);
    if (String(value).trim() === '' || !Number.isSafeInteger(number))
      return 'Enter a whole number.';
    if (number < (setting.min ?? 1))
      return `Enter ${setting.min ?? 1} or more.`;
    if (setting.max !== undefined && number > setting.max)
      return `Enter ${setting.max} or less.`;
  }
  if (
    setting.type === 'email' &&
    !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(String(value))
  )
    return 'Enter a valid email address.';
  if (setting.options && !setting.options.includes(String(value)))
    return 'Select an available value.';
  return '';
}
