import { createContext, useContext, useState, useCallback, type ReactNode } from 'react'
import { DEFAULT_LOCALE, LOCALES, isLocale, localeFromTag, type Locale } from './locales'

interface I18nContextValue {
  locale: Locale
  setLocale: (l: Locale) => void
  t: (key: string, vars?: Record<string, string>) => string
}

const LOCALE_STORAGE_KEY = 'temporality_locale'

function getInitialLocale(): Locale {
  const stored = localStorage.getItem(LOCALE_STORAGE_KEY)
  if (isLocale(stored)) return stored
  return localeFromTag(navigator.language)
}

const I18nContext = createContext<I18nContextValue>({
  locale: DEFAULT_LOCALE,
  setLocale: () => {},
  t: (key) => key,
})

export function I18nProvider({ children }: { children: ReactNode }) {
  const [locale, setLocaleState] = useState<Locale>(getInitialLocale)

  const setLocale = useCallback((l: Locale) => {
    setLocaleState(l)
    localStorage.setItem(LOCALE_STORAGE_KEY, l)
  }, [])

  const t = useCallback((key: string, vars?: Record<string, string>) => {
    let text = LOCALES[locale].messages[key] ?? LOCALES[DEFAULT_LOCALE].messages[key] ?? key
    if (vars) {
      Object.entries(vars).forEach(([k, v]) => {
        text = text.replace(`{{${k}}}`, v)
      })
    }
    return text
  }, [locale])

  return <I18nContext.Provider value={{ locale, setLocale, t }}>{children}</I18nContext.Provider>
}

export function useI18n() {
  return useContext(I18nContext)
}

export function useT() {
  return useI18n().t
}

/** Pick a plural form for the locale: English one/other, Russian
 * one/few/many. Keys follow the {{key}}_one/_few/_other convention. */
export function pluralT(t: (key: string, vars?: Record<string, string>) => string, locale: string, key: string, count: number, vars?: Record<string, string>): string {
  const n = Math.abs(count)
  let suffix: 'one' | 'few' | 'other'
  if (locale === 'ru') {
    const mod10 = n % 10, mod100 = n % 100
    suffix = (mod10 === 1 && mod100 !== 11) ? 'one'
      : (mod10 >= 2 && mod10 <= 4 && (mod100 < 10 || mod100 >= 20)) ? 'few' : 'other'
  } else {
    suffix = n === 1 ? 'one' : 'other'
  }
  return t(`${key}_${suffix}`, { ...vars, count: String(count) })
}

export { LOCALES } from './locales'
export type { Locale }
