import { en } from './en'
import { ru } from './ru'

export interface LocaleMeta {
  /** Endonym shown in the language picker. */
  name: string
  messages: Record<string, string>
}

const DEFINITIONS = {
  en: { name: 'English', messages: en },
  ru: { name: 'Русский', messages: ru },
} satisfies Record<string, LocaleMeta>

/** Locale registry: adding a language = drop a messages file + one entry here. */
export const LOCALES: Record<keyof typeof DEFINITIONS, LocaleMeta> = DEFINITIONS

export type Locale = keyof typeof DEFINITIONS

export const DEFAULT_LOCALE: Locale = 'en'

export function isLocale(value: string | null | undefined): value is Locale {
  return !!value && value in LOCALES
}

/** Map a BCP-47 tag (navigator.language) to a supported locale. */
export function localeFromTag(tag: string): Locale {
  const primary = tag.split('-')[0].toLowerCase()
  return isLocale(primary) ? primary : DEFAULT_LOCALE
}
