import { renderToStaticMarkup } from 'react-dom/server'
import { afterEach, describe, expect, it, vi } from 'vitest'
import Sandbox from './Sandbox'
import { I18nProvider } from './i18n'
import { en } from './locales/en'
import { ru } from './locales/ru'

function stubLocalStorage() {
  vi.stubGlobal('localStorage', {
    getItem: () => null,
    setItem: () => {},
    removeItem: () => {},
  })
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('Sandbox profiles page', () => {
  it('renders the registry shell without crashing', () => {
    stubLocalStorage()
    const html = renderToStaticMarkup(
      <I18nProvider>
        <Sandbox />
      </I18nProvider>,
    )
    expect(html).toContain('Sandbox profiles')
    expect(html).toContain('Custom profile')
  })
})

describe('recipe-mode locale keys (AC-8)', () => {
  it('define every new sandbox recipe key in both locales', () => {
    const keys = [
      'sandbox.recipe_mode_legend',
      'sandbox.mode_base',
      'sandbox.mode_dockerfile',
      'sandbox.field_dockerfile',
      'sandbox.dockerfile_hint',
      'sandbox.dockerfile_checking',
      'sandbox.dockerfile_valid',
      'sandbox.dockerfile_invalid',
      'sandbox.dockerfile_save_hint',
      'sandbox.override_tag',
    ]
    for (const key of keys) {
      expect(ru[key], `ru ${key}`).toBeTruthy()
      expect(en[key], `en ${key}`).toBeTruthy()
    }
    expect(ru['sandbox.mode_base']).toBe('Собирать из базы')
    expect(ru['sandbox.mode_dockerfile']).toBe('Свой Dockerfile')
  })
})
