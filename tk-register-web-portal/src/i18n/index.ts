import i18next from 'i18next';
import { initReactI18next, useTranslation } from 'react-i18next';
import { commonMessages } from './common';
import { accountMessages } from './accounts';
import { adminMessages } from './admin';
import { submissionMessages } from './submissions';

export type Language = 'zh-CN' | 'en-US';
const preferenceKey = 'tk-register.language';

export function preferredLanguage(saved: string | null, browserLanguages: readonly string[]): Language {
  if (saved === 'zh-CN' || saved === 'en-US') return saved;
  for (const tag of browserLanguages) {
    const language = tag.toLowerCase().split('-')[0];
    if (language === 'zh') return 'zh-CN';
    if (language === 'en') return 'en-US';
  }
  return 'en-US';
}

function initialLanguage(): Language {
  if (typeof window === 'undefined') return 'en-US';
  let saved: string | null = null;
  try { saved = window.localStorage.getItem(preferenceKey); } catch { /* Storage may be disabled. */ }
  const browser = window.navigator;
  return preferredLanguage(saved, browser.languages?.length ? browser.languages : [browser.language]);
}

const chinese = { ...adminMessages, ...submissionMessages, ...accountMessages, ...commonMessages };
const language = initialLanguage();
const instance = i18next.createInstance();
void instance.use(initReactI18next).init({
  lng: language,
  supportedLngs: ['zh-CN', 'en-US'],
  load: 'currentOnly',
  fallbackLng: 'en-US',
  keySeparator: false,
  nsSeparator: false,
  initAsync: false,
  resources: { 'zh-CN': { translation: chinese }, 'en-US': { translation: {} } },
  interpolation: { escapeValue: false },
  react: { useSuspense: false },
});

export function currentLanguage(): Language {
  return instance.language === 'zh-CN' ? 'zh-CN' : 'en-US';
}

export function useLanguage(): Language {
  useTranslation(undefined, { i18n: instance });
  return currentLanguage();
}

export function setLanguage(next: Language) {
  try { window.localStorage.setItem(preferenceKey, next); } catch { /* Keep the in-memory preference. */ }
  void instance.changeLanguage(next);
}

if (typeof document !== 'undefined') {
  function updateDocumentLanguage(next: string) {
    document.documentElement.lang = next === 'zh-CN' ? 'zh-CN' : 'en-US';
  }
  updateDocumentLanguage(language);
  instance.on('languageChanged', updateDocumentLanguage);
  window.addEventListener('storage', event => {
    if (event.key === preferenceKey || event.key === null) void instance.changeLanguage(initialLanguage());
  });
}

export function t(english: string, params?: Record<string, string | number>): string {
  return instance.t(english, { ...params, defaultValue: english });
}

// Messages kept in component state stay in the source language until rendered.
export function translateMessage(text: string): string {
  if (currentLanguage() === 'en-US' || !text) return text;
  if (Object.hasOwn(chinese, text)) return t(text);
  const limit = /^Enter (-?\d+) or (more|less)\.$/.exec(text);
  if (limit) return t(limit[2] === 'more' ? 'Enter {{value}} or more.' : 'Enter {{value}} or less.', { value: limit[1] });
  return text;
}
