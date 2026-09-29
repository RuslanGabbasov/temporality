import { createContext, useContext, useState, useCallback, useEffect, type ReactNode } from 'react'

type Theme = 'dark' | 'light' | 'system'

interface ThemeContextValue {
  theme: Theme
  setTheme: (t: Theme) => void
  resolved: 'dark' | 'light'
}

const THEME_STORAGE_KEY = 'temporality_theme'

function getInitialTheme(): Theme {
  const stored = localStorage.getItem(THEME_STORAGE_KEY) as Theme | null
  if (stored === 'dark' || stored === 'light') return stored
  return 'system' as Theme // will resolve below
}

function getSystemTheme(): 'dark' | 'light' {
  return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
}

function resolveTheme(t: Theme): 'dark' | 'light' {
  return t === 'system' ? getSystemTheme() : t
}

const ThemePrefContext = createContext<ThemeContextValue>({
  theme: 'dark',
  setTheme: () => {},
  resolved: 'dark',
})

export function ThemePrefProvider({ children }: { children: ReactNode }) {
  const [theme, setThemeState] = useState<Theme>(getInitialTheme)
  const [systemTheme, setSystemTheme] = useState<'dark' | 'light'>(getSystemTheme)

  useEffect(() => {
    const mq = window.matchMedia('(prefers-color-scheme: dark)')
    const handler = (e: MediaQueryListEvent) => setSystemTheme(e.matches ? 'dark' : 'light')
    mq.addEventListener('change', handler)
    return () => mq.removeEventListener('change', handler)
  }, [])

  const resolved = theme === 'system' ? systemTheme : theme

  useEffect(() => {
    document.documentElement.setAttribute('data-theme', resolved)
  }, [resolved])

  const setTheme = useCallback((t: Theme) => {
    setThemeState(t)
    if (t === 'system') {
      localStorage.removeItem(THEME_STORAGE_KEY)
    } else {
      localStorage.setItem(THEME_STORAGE_KEY, t)
    }
  }, [])

  return (
    <ThemePrefContext.Provider value={{ theme, setTheme, resolved }}>
      {children}
    </ThemePrefContext.Provider>
  )
}

export function useTheme() {
  return useContext(ThemePrefContext)
}

export type { Theme }