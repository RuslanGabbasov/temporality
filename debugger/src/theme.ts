// Carbon Design System theme configuration for Temporality
// Based on Carbon's Gray 100 (dark) theme with custom accent colors

export const temporalityTheme = {
  // Custom colors for Temporality brand
  colors: {
    // Primary accent — cyan (matches existing design)
    interactive: '#57d7e8',
    // Secondary accent — violet
    highlight: '#9e8cff',
    // Success
    success: '#93e8c0',
    // Warning  
    warning: '#e6b85c',
    // Error
    error: '#ff7285',
    
    // Background layers
    background: '#080b10',
    layer01: '#0d1118',
    layer02: '#121823',
    layer03: '#172130',
    
    // Text
    textPrimary: '#e5e9f0',
    textSecondary: '#7e8a9c',
    textOnColor: '#071014',
    
    // Borders
    borderSubtle: '#202a38',
    borderStrong: '#344258',
  },
  
  // Spacing scale (Carbon 16px base)
  spacing: {
    xs: '0.25rem',   // 4px
    sm: '0.5rem',    // 8px
    md: '1rem',      // 16px
    lg: '1.5rem',    // 24px
    xl: '2rem',      // 32px
    '2xl': '3rem',   // 48px
  },
  
  // Typography scale
  typography: {
    fontFamily: "'IBM Plex Sans', -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif",
    fontMono: "'IBM Plex Mono', 'SFMono-Regular', Consolas, monospace",
    
    // Carbon type tokens
    heading01: {
      fontSize: '0.875rem',
      fontWeight: 600,
      lineHeight: '1.25rem',
      letterSpacing: '0.01em',
    },
    heading02: {
      fontSize: '1rem',
      fontWeight: 600,
      lineHeight: '1.375rem',
      letterSpacing: '0',
    },
    heading03: {
      fontSize: '1.25rem',
      fontWeight: 600,
      lineHeight: '1.75rem',
      letterSpacing: '0',
    },
    body01: {
      fontSize: '0.875rem',
      fontWeight: 400,
      lineHeight: '1.25rem',
      letterSpacing: '0.01em',
    },
    body02: {
      fontSize: '1rem',
      fontWeight: 400,
      lineHeight: '1.5rem',
      letterSpacing: '0',
    },
    code01: {
      fontSize: '0.75rem',
      fontWeight: 400,
      lineHeight: '1rem',
      fontFamily: "'IBM Plex Mono', 'SFMono-Regular', Consolas, monospace",
    },
  },
  
  // Layout
  layout: {
    sidebar: {
      width: '256px',
      collapsed: '48px',
    },
    header: {
      height: '48px',
    },
    panel: {
      minWidth: '320px',
    },
  },
}

export type TemporalityTheme = typeof temporalityTheme