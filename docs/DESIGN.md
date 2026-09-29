# Temporality — visual design specification

> Brand and UI design specification  
> Status: initial production baseline  
> Product: Temporality

## 1. Brand idea

Temporality is a system for observing how agent knowledge appears, changes, is confirmed, becomes invalid, and is reused in later operations.

The visual language should communicate:

- **time** — events and states have a temporal position;
- **knowledge** — information accumulates and changes state;
- **trajectory** — an agent is observed through a sequence of operations, not only by its final output;
- **reuse** — previously acquired knowledge can become useful again in a new context;
- **engineering precision** — this is an infrastructure/developer product, not a generic AI assistant.

### What the identity should avoid

Do not use:

- clocks, watch faces, hourglasses;
- brain / neural-network clichés;
- generic connected-node graphs;
- excessive gradients;
- neon cyan + violet "AI SaaS" palettes;
- excessive glassmorphism;
- decorative 3D;
- noisy icons containing many semantic elements.

The identity should feel **quiet, technical and distinctive**.

---

# 2. Logo

## Primary mark

The primary symbol consists of **three vertical, slightly curved temporal strands**.

The symbol intentionally has two simultaneous readings:

1. three time/world lines — a subtle reference to physics and temporal trajectories;
2. three bars — a subtle reference to measurements, state and observed data.

The mark must remain abstract. Do not add arrows, nodes, clocks or explicit timeline markers.

### Geometry

- Three parallel rounded strands.
- Equal visual weight.
- Small horizontal displacement between strands.
- Different vertical extents.
- Rounded caps.
- The middle/third strand may carry the accent color.
- No enclosing circle.
- No connecting nodes.
- No text inside the mark.

### Clear space

Minimum clear space around the mark:

`0.5 × mark width`

No UI element, text or border may enter this area.

### Minimum size

- Digital favicon: 16×16 — monochrome version preferred.
- App icon: 32×32 and above — full-color version.
- UI navigation: 20–24 px — monochrome or restrained two-tone version.
- Marketing: use the full wordmark whenever there is enough space.

## Wordmark

Use:

**Temporality**

Typography should be clean, neutral and slightly human rather than futuristic.

Recommended primary font:

- Inter

Fallback:

- system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif

Recommended wordmark weight:

`500`

Avoid:

- bold geometric display fonts;
- sci-fi fonts;
- all caps;
- excessive letter spacing.

---

# 3. Color system

The palette deliberately avoids the common electric-blue/purple AI aesthetic.

## Core colors

| Token | Hex | Role |
|---|---|---|
| `--tm-ink` | `#0F1F1D` | primary dark / logo |
| `--tm-forest` | `#1F3B37` | deep green surfaces |
| `--tm-teal` | `#2A6B63` | secondary brand accent |
| `--tm-amber` | `#E09B5A` | primary semantic accent |
| `--tm-cream` | `#EDE8DF` | warm light background |
| `--tm-white` | `#F7F5F0` | primary light surface |
| `--tm-muted` | `#8A96A3` | secondary text / inactive |
| `--tm-dark` | `#121B20` | dark application background |

### Brand principle

**Green carries the system. Amber carries the event.**

Green is structural:

- navigation;
- persistent states;
- knowledge;
- secondary controls;
- active system surfaces.

Amber is temporal/salient:

- newly observed event;
- transition;
- attention;
- important state change;
- selected temporal point.

Amber should be used sparingly.

---

# 4. Semantic colors

Brand colors and semantic colors are separate concepts.

```css
:root {
  --tm-success: #5E887A;
  --tm-warning: #E09B5A;
  --tm-danger: #B96B61;
  --tm-info: #5C7F7A;
  --tm-neutral: #8A96A3;
}
```

Do not make the whole interface green/orange merely because they are brand colors.

Semantic state should remain visually subordinate to the content.

---

# 5. Surfaces

## Light theme

```css
--tm-bg:       #F7F5F0;
--tm-surface:  #EDE8DF;
--tm-elevated: #FFFFFF;
--tm-border:   #D8D5CD;
--tm-text:     #0F1F1D;
--tm-text-2:   #56635F;
--tm-text-3:   #8A928F;
```

## Dark theme

```css
--tm-bg:       #0F1F1D;
--tm-surface:  #162825;
--tm-elevated: #1C302C;
--tm-border:   #2A403B;
--tm-text:     #F1EEE7;
--tm-text-2:   #AAB5B0;
--tm-text-3:   #71817B;
```

Dark mode should not be pure black.

---

# 6. Typography

## Font stack

```css
font-family:
  Inter,
  ui-sans-serif,
  system-ui,
  -apple-system,
  BlinkMacSystemFont,
  "Segoe UI",
  sans-serif;
```

## Scale

```css
--tm-text-xs:   11px;
--tm-text-sm:   13px;
--tm-text-md:   14px;
--tm-text-lg:   16px;
--tm-text-xl:   20px;
--tm-text-2xl:  24px;
--tm-text-3xl:  32px;
--tm-text-4xl:  40px;
```

Recommended weights:

```css
--tm-weight-regular: 400;
--tm-weight-medium: 500;
--tm-weight-semibold: 600;
```

Avoid 700+ except for very rare marketing headings.

---

# 7. Layout

Temporality should feel like an engineering instrument.

Use:

- generous whitespace;
- strong alignment;
- restrained borders;
- compact data density inside operational screens;
- larger whitespace in explanatory/marketing surfaces.

Base spacing unit:

`4px`

```css
--tm-space-1: 4px;
--tm-space-2: 8px;
--tm-space-3: 12px;
--tm-space-4: 16px;
--tm-space-5: 20px;
--tm-space-6: 24px;
--tm-space-8: 32px;
--tm-space-10: 40px;
--tm-space-12: 48px;
--tm-space-16: 64px;
```

Recommended radius:

```css
--tm-radius-sm: 6px;
--tm-radius-md: 10px;
--tm-radius-lg: 14px;
```

Avoid excessive rounded cards. A card should communicate grouping, not decoration.

---

# 8. Iconography

Icons should be:

- linear;
- geometric;
- 1.5–2 px stroke;
- rounded caps;
- minimal;
- visually quiet.

The logo is **not** an icon template.

For product concepts use simple primitives:

- time → line / point / vertical progression;
- knowledge → layered planes;
- trajectory → curved path;
- reuse → returning path;
- operation → small event marker.

Do not turn every concept into a literal pictogram.

---

# 9. Timeline visual language

The timeline is the strongest product-specific visual language.

Use:

- a thin vertical temporal axis;
- sparse event points;
- short horizontal branches;
- compact event cards;
- state transitions represented by position and restrained color.

Example:

```text
       ●  knowledge observed
       │
       │
       ●  knowledge confirmed
       │
       ├──── operation reused knowledge
       │
       ●  knowledge invalidated
```

The temporal axis should remain visually dominant over individual cards.

---

# 10. Knowledge states

Suggested visual treatment:

| State | Visual treatment |
|---|---|
| proposed | muted outline |
| confirmed | green accent |
| used | amber event marker |
| invalidated | muted / crossed or faded |
| superseded | muted with continuation |
| unknown | neutral gray |

Do not rely on color alone. Every state must also have a textual/iconographic representation.

---

# 11. Motion

Animation should reinforce temporal meaning.

Preferred:

- short fade/translate;
- timeline point appearing along an axis;
- state transition;
- subtle path movement.

Avoid:

- bouncing;
- elastic animations;
- perpetual ambient motion;
- animated gradients;
- decorative particle systems.

Suggested durations:

```css
--tm-duration-fast: 120ms;
--tm-duration-normal: 180ms;
--tm-duration-slow: 280ms;
```

Use `ease-out` for entering content and `ease-in-out` for state transitions.

---

# 12. Buttons and controls

Primary button:

- dark green / forest background;
- warm light text;
- 8–10 px radius;
- medium weight.

Accent action:

- amber;
- only for genuinely salient temporal actions.

Do not make every CTA amber.

Secondary controls:

- transparent;
- subtle border;
- neutral text.

---

# 13. Cards

Cards should be closer to **instrument panels** than marketing tiles.

Preferred:

```css
background: var(--tm-elevated);
border: 1px solid var(--tm-border);
border-radius: var(--tm-radius-md);
```

Avoid:

```css
box-shadow: 0 20px 60px ...;
backdrop-filter: blur(...);
```

unless there is a specific interaction reason.

---

# 14. Data visualization

Charts should use the brand palette sparingly.

Recommended hierarchy:

1. dark ink / forest — baseline;
2. muted teal — supporting series;
3. amber — selected event or significant transition;
4. muted gray — historical/inactive information.

Do not use rainbow categorical palettes.

For temporal charts, the x-axis should remain visually important. Prefer a continuous temporal line over decorative chart effects.

---

# 15. Product shell

A typical Temporality screen should feel like:

```text
┌──────────────────────────────────────────────────────────────┐
│  logo   Temporality                         search   profile │
├───────────────┬──────────────────────────────────────────────┤
│               │                                              │
│  Timeline     │  Operation / trajectory                      │
│  Knowledge    │                                              │
│  Agents       │       ●─────── knowledge                     │
│  Analytics    │       │                                      │
│               │       ●─────── confirmed                     │
│               │       │                                      │
│               │       ├─────── reused                        │
│               │       │                                      │
│               │       ●─────── invalidated                   │
│               │                                              │
└───────────────┴──────────────────────────────────────────────┘
```

Navigation should be calm and secondary to the temporal content.

---

# 16. Logo SVG usage

The canonical source is:

`temporality-mark.svg`

The SVG is deliberately self-contained and uses no external fonts, images or CSS.

For monochrome use, replace the fills with:

```css
currentColor
```

For small sizes, use the simplified monochrome form.

---

# 17. CSS design tokens

```css
:root {
  /* Brand */
  --tm-ink: #0F1F1D;
  --tm-forest: #1F3B37;
  --tm-teal: #2A6B63;
  --tm-amber: #E09B5A;
  --tm-cream: #EDE8DF;
  --tm-white: #F7F5F0;
  --tm-muted: #8A96A3;
  --tm-dark: #121B20;

  /* Light surfaces */
  --tm-bg: #F7F5F0;
  --tm-surface: #EDE8DF;
  --tm-elevated: #FFFFFF;
  --tm-border: #D8D5CD;
  --tm-text: #0F1F1D;
  --tm-text-2: #56635F;
  --tm-text-3: #8A928F;

  /* Semantic */
  --tm-success: #5E887A;
  --tm-warning: #E09B5A;
  --tm-danger: #B96B61;
  --tm-info: #5C7F7A;
  --tm-neutral: #8A96A3;

  /* Spacing */
  --tm-space-1: 4px;
  --tm-space-2: 8px;
  --tm-space-3: 12px;
  --tm-space-4: 16px;
  --tm-space-5: 20px;
  --tm-space-6: 24px;
  --tm-space-8: 32px;
  --tm-space-10: 40px;
  --tm-space-12: 48px;
  --tm-space-16: 64px;

  /* Radius */
  --tm-radius-sm: 6px;
  --tm-radius-md: 10px;
  --tm-radius-lg: 14px;

  /* Typography */
  --tm-font:
    Inter,
    ui-sans-serif,
    system-ui,
    -apple-system,
    BlinkMacSystemFont,
    "Segoe UI",
    sans-serif;

  --tm-text-xs: 11px;
  --tm-text-sm: 13px;
  --tm-text-md: 14px;
  --tm-text-lg: 16px;
  --tm-text-xl: 20px;
  --tm-text-2xl: 24px;
  --tm-text-3xl: 32px;
  --tm-text-4xl: 40px;

  --tm-weight-regular: 400;
  --tm-weight-medium: 500;
  --tm-weight-semibold: 600;

  /* Motion */
  --tm-duration-fast: 120ms;
  --tm-duration-normal: 180ms;
  --tm-duration-slow: 280ms;
}
```

---

# 18. Brand usage summary

### Do

- use the three-strand mark consistently;
- preserve generous clear space;
- use forest/teal as structural colors;
- reserve amber for temporal salience;
- keep visualizations restrained;
- let timelines and state transitions become part of the identity;
- use the mark without explanatory symbolism.

### Don't

- redraw the mark as a graph;
- add nodes or arrows;
- put the mark inside a clock;
- use cyan/violet neon gradients;
- add glow effects;
- use multiple accent colors at once;
- make every UI element rounded and floating.

---

# 19. Brand character

**Quietly technical.**

Temporality should look like a serious system that happens to have a distinctive visual identity — not like a marketing layer placed on top of an AI product.

The visual metaphor is:

> **time leaves structure behind.**
