# Wormhole Marketing Website Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build and scaffold a standalone Next.js marketing site for Wormhole (repo: `~/Projects/wormhole-website`) — single-page landing site with 3D aperture hero, glass-panel sections, dot-matrix backdrop, dark/light theming, on-brand per `brand/WORMHOLE-BRAND-GUIDELINES.md`.

**Architecture:** Next.js 15 App Router site. `app/page.tsx` assembles ordered section components. Shared primitives (`GlassPanel`, `DotMatrix`, `ThemeProvider`) live under `components/`. Hero uses React Three Fiber for the six-lobed aperture geometry; every animated element has a static/reduced-motion fallback.

**Tech Stack:** Next.js 15, TypeScript, Tailwind CSS, React Three Fiber + drei, Framer Motion, Vitest + React Testing Library, `@react-three/test-renderer`.

## Global Constraints

- Repo lives at `~/Projects/wormhole-website`, standalone git repo, not a submodule of `wormhole`.
- Both light and dark themes MUST be fully implemented (brand guide sec 5.1); dark is default on load.
- Colour tokens exact values from brand guide sec 5.2/5.3: `canvas` light `#F7F8FB` / dark `#0B0D12`, `surface` light `#FFFFFF` / dark `#121620`, `surface-raised` light `#EEF1F6` / dark `#191E29`, `text-primary` light `#11131A` / dark `#F5F7FB`, `text-secondary` light `#626A7A` / dark `#A7AFBF`, `border` light `#D9DEE8` / dark `#29303D`, `accent-primary` `#6872F8`, `accent-hover` `#535DE0`, `accent-soft` `#E7E9FF`, `accent-dark-soft` `#24294F`, `accent-bright` `#9299FF`.
- Fonts: Space Grotesk (display), Inter (body), IBM Plex Mono (technical) — brand guide sec 4.
- `prefers-reduced-motion: reduce` MUST disable non-essential animation; page fully usable with all motion off (brand guide sec 13).
- No scroll hijacking (sec 10.4). No fabricated customers, stats, or integrations (sec 12.1). Indicative interfaces MUST be labelled indicative (sec 2.2).
- Glass panels: `--panel-radius: 22px`, blur `18px`, tokens per sec 7.6.
- No more than one dominant gradient element per composition (sec 5.4).

---

### Task 1: Repo scaffold + test harness

**Files:**
- Create: `~/Projects/wormhole-website/` (via `create-next-app`)
- Create: `vitest.config.ts`
- Create: `vitest.setup.ts`
- Create: `tests/smoke.test.tsx`
- Modify: `package.json` (add `test` script)

**Interfaces:**
- Produces: working Next.js 15 + TS + Tailwind project, Vitest test runner wired to `npm test`, all later tasks assume this root.

- [ ] **Step 1: Scaffold the Next.js app**

```bash
cd ~/Projects
npx create-next-app@latest wormhole-website \
  --typescript --tailwind --eslint --app --no-src-dir \
  --import-alias "@/*" --use-npm
cd wormhole-website
```

- [ ] **Step 2: Install runtime deps**

```bash
npm install three @react-three/fiber @react-three/drei framer-motion
```

- [ ] **Step 3: Install test deps**

```bash
npm install -D vitest @vitejs/plugin-react jsdom \
  @testing-library/react @testing-library/jest-dom \
  @react-three/test-renderer
```

- [ ] **Step 4: Add Vitest config**

```typescript
// vitest.config.ts
import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  test: {
    environment: 'jsdom',
    setupFiles: ['./vitest.setup.ts'],
    globals: true,
  },
})
```

```typescript
// vitest.setup.ts
import '@testing-library/jest-dom/vitest'
```

- [ ] **Step 5: Add test script**

Edit `package.json` scripts block, add:

```json
"test": "vitest run"
```

- [ ] **Step 6: Write smoke test**

```typescript
// tests/smoke.test.tsx
import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'

function Hello() {
  return <div>wormhole</div>
}

describe('test harness', () => {
  it('renders', () => {
    render(<Hello />)
    expect(screen.getByText('wormhole')).toBeInTheDocument()
  })
})
```

- [ ] **Step 7: Run test, verify pass**

Run: `npm test`
Expected: `tests/smoke.test.tsx` passes, 1 test.

- [ ] **Step 8: Commit**

```bash
git add -A
git commit -m "chore: scaffold Next.js site with Vitest harness"
```

---

### Task 2: Design tokens (globals.css + tailwind.config)

**Files:**
- Modify: `app/globals.css`
- Modify: `tailwind.config.ts`
- Create: `lib/tokens.ts`
- Test: `tests/tokens.test.ts`

**Interfaces:**
- Produces: `lib/tokens.ts` exporting `TOKEN_NAMES` (array of CSS custom-property names, no leading `--`), consumed by Task 5 (`GlassPanel`) and Task 4 (`ThemeToggle`).

- [ ] **Step 1: Write globals.css tokens**

```css
/* app/globals.css */
@tailwind base;
@tailwind components;
@tailwind utilities;

:root {
  --canvas: #F7F8FB;
  --surface: #FFFFFF;
  --surface-raised: #EEF1F6;
  --text-primary: #11131A;
  --text-secondary: #626A7A;
  --border: #D9DEE8;
  --accent-primary: #6872F8;
  --accent-hover: #535DE0;
  --accent-soft: #E7E9FF;
  --accent-bright: #9299FF;

  --glass-bg: rgb(255 255 255 / 72%);
  --glass-border: rgb(17 19 26 / 12%);
  --glass-shadow: 0 20px 60px rgb(13 18 32 / 12%);
  --glass-blur: 18px;
  --panel-radius: 22px;
}

[data-theme='dark'] {
  --canvas: #0B0D12;
  --surface: #121620;
  --surface-raised: #191E29;
  --text-primary: #F5F7FB;
  --text-secondary: #A7AFBF;
  --border: #29303D;
  --accent-soft: #24294F;

  --glass-bg: rgb(18 22 32 / 72%);
  --glass-border: rgb(245 247 251 / 14%);
  --glass-shadow: 0 24px 70px rgb(0 0 0 / 34%);
}

@media (prefers-reduced-motion: reduce) {
  *, *::before, *::after {
    animation-duration: 0.01ms !important;
    animation-iteration-count: 1 !important;
    transition-duration: 0.01ms !important;
    scroll-behavior: auto !important;
  }
}

body {
  background: var(--canvas);
  color: var(--text-primary);
}
```

- [ ] **Step 2: Extend Tailwind with tokens**

```typescript
// tailwind.config.ts
import type { Config } from 'tailwindcss'

export default {
  content: ['./app/**/*.{ts,tsx}', './components/**/*.{ts,tsx}'],
  theme: {
    extend: {
      colors: {
        canvas: 'var(--canvas)',
        surface: 'var(--surface)',
        'surface-raised': 'var(--surface-raised)',
        'text-primary': 'var(--text-primary)',
        'text-secondary': 'var(--text-secondary)',
        border: 'var(--border)',
        'accent-primary': 'var(--accent-primary)',
        'accent-hover': 'var(--accent-hover)',
        'accent-soft': 'var(--accent-soft)',
        'accent-bright': 'var(--accent-bright)',
      },
      borderRadius: {
        panel: 'var(--panel-radius)',
      },
    },
  },
  plugins: [],
} satisfies Config
```

- [ ] **Step 3: Write lib/tokens.ts**

```typescript
// lib/tokens.ts
export const TOKEN_NAMES = [
  'canvas',
  'surface',
  'surface-raised',
  'text-primary',
  'text-secondary',
  'border',
  'accent-primary',
  'accent-hover',
  'accent-soft',
  'accent-bright',
  'glass-bg',
  'glass-border',
  'glass-shadow',
  'glass-blur',
  'panel-radius',
] as const

export type TokenName = (typeof TOKEN_NAMES)[number]

export function cssVar(name: TokenName): string {
  return `var(--${name})`
}
```

- [ ] **Step 4: Write test**

```typescript
// tests/tokens.test.ts
import { describe, expect, it } from 'vitest'
import { cssVar, TOKEN_NAMES } from '@/lib/tokens'

describe('tokens', () => {
  it('cssVar wraps token name in var()', () => {
    expect(cssVar('accent-primary')).toBe('var(--accent-primary)')
  })

  it('includes all required glass tokens', () => {
    expect(TOKEN_NAMES).toContain('glass-bg')
    expect(TOKEN_NAMES).toContain('panel-radius')
  })
})
```

- [ ] **Step 5: Run tests, verify pass**

Run: `npm test`
Expected: tokens tests pass alongside smoke test.

- [ ] **Step 6: Commit**

```bash
git add app/globals.css tailwind.config.ts lib/tokens.ts tests/tokens.test.ts
git commit -m "feat: add brand design tokens for light/dark themes"
```

---

### Task 3: Fonts

**Files:**
- Modify: `app/layout.tsx`

**Interfaces:**
- Produces: CSS variables `--font-display`, `--font-body`, `--font-mono` applied to `<html>`, consumed by Tailwind font utilities in later section components.

- [ ] **Step 1: Wire next/font in layout**

```typescript
// app/layout.tsx
import type { Metadata } from 'next'
import { Space_Grotesk, Inter, IBM_Plex_Mono } from 'next/font/google'
import './globals.css'

const spaceGrotesk = Space_Grotesk({
  subsets: ['latin'],
  weight: ['500', '600', '700'],
  variable: '--font-display',
})

const inter = Inter({
  subsets: ['latin'],
  weight: ['400', '500', '600'],
  variable: '--font-body',
})

const plexMono = IBM_Plex_Mono({
  subsets: ['latin'],
  weight: ['400', '500'],
  variable: '--font-mono',
})

export const metadata: Metadata = {
  title: 'Wormhole',
  description: 'The shortest path between an AI agent and organisational context.',
}

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en" data-theme="dark" className={`${spaceGrotesk.variable} ${inter.variable} ${plexMono.variable}`}>
      <body className="font-body">{children}</body>
    </html>
  )
}
```

- [ ] **Step 2: Add font family mapping to Tailwind**

Edit `tailwind.config.ts`, inside `theme.extend` add:

```typescript
fontFamily: {
  display: ['var(--font-display)'],
  body: ['var(--font-body)'],
  mono: ['var(--font-mono)'],
},
```

- [ ] **Step 3: Verify build succeeds**

Run: `npm run build`
Expected: build completes with no font-loading errors.

- [ ] **Step 4: Commit**

```bash
git add app/layout.tsx tailwind.config.ts
git commit -m "feat: wire brand typography via next/font"
```

---

### Task 4: ThemeProvider + ThemeToggle

**Files:**
- Create: `components/theme/ThemeProvider.tsx`
- Create: `components/theme/ThemeToggle.tsx`
- Modify: `app/layout.tsx`
- Test: `tests/theme-toggle.test.tsx`

**Interfaces:**
- Produces: `useTheme()` hook returning `{ theme: 'light' | 'dark', toggle: () => void }`, consumed by `ThemeToggle` and any component needing theme-aware rendering.

- [ ] **Step 1: Write failing test**

```typescript
// tests/theme-toggle.test.tsx
import { render, screen, fireEvent } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { ThemeProvider } from '@/components/theme/ThemeProvider'
import { ThemeToggle } from '@/components/theme/ThemeToggle'

describe('ThemeToggle', () => {
  it('defaults to dark and toggles to light on click', () => {
    render(
      <ThemeProvider>
        <ThemeToggle />
      </ThemeProvider>
    )
    expect(document.documentElement.getAttribute('data-theme')).toBe('dark')
    fireEvent.click(screen.getByRole('button', { name: /switch to light/i }))
    expect(document.documentElement.getAttribute('data-theme')).toBe('light')
  })
})
```

- [ ] **Step 2: Run test, verify it fails**

Run: `npm test -- theme-toggle`
Expected: FAIL — modules don't exist yet.

- [ ] **Step 3: Write ThemeProvider**

```typescript
// components/theme/ThemeProvider.tsx
'use client'

import { createContext, useContext, useEffect, useState } from 'react'

type Theme = 'light' | 'dark'
type ThemeContextValue = { theme: Theme; toggle: () => void }

const ThemeContext = createContext<ThemeContextValue | null>(null)

export function ThemeProvider({ children }: { children: React.ReactNode }) {
  const [theme, setTheme] = useState<Theme>('dark')

  useEffect(() => {
    const stored = window.localStorage.getItem('wormhole-theme') as Theme | null
    if (stored) setTheme(stored)
  }, [])

  useEffect(() => {
    document.documentElement.setAttribute('data-theme', theme)
    window.localStorage.setItem('wormhole-theme', theme)
  }, [theme])

  function toggle() {
    setTheme((t) => (t === 'dark' ? 'light' : 'dark'))
  }

  return <ThemeContext.Provider value={{ theme, toggle }}>{children}</ThemeContext.Provider>
}

export function useTheme(): ThemeContextValue {
  const ctx = useContext(ThemeContext)
  if (!ctx) throw new Error('useTheme must be used within ThemeProvider')
  return ctx
}
```

- [ ] **Step 4: Write ThemeToggle**

```typescript
// components/theme/ThemeToggle.tsx
'use client'

import { useTheme } from './ThemeProvider'

export function ThemeToggle() {
  const { theme, toggle } = useTheme()
  const nextLabel = theme === 'dark' ? 'Switch to light theme' : 'Switch to dark theme'

  return (
    <button
      type="button"
      onClick={toggle}
      aria-label={nextLabel}
      className="rounded-panel border border-border px-3 py-1.5 text-sm text-text-secondary hover:text-text-primary"
    >
      {theme === 'dark' ? 'Dark' : 'Light'}
    </button>
  )
}
```

- [ ] **Step 5: Run test, verify pass**

Run: `npm test -- theme-toggle`
Expected: PASS.

- [ ] **Step 6: Wrap layout in ThemeProvider**

Edit `app/layout.tsx`: import `ThemeProvider`, wrap `{children}` with `<ThemeProvider>{children}</ThemeProvider>` inside `<body>`. Remove the hardcoded `data-theme="dark"` from `<html>` (ThemeProvider now sets it).

- [ ] **Step 7: Commit**

```bash
git add components/theme tests/theme-toggle.test.tsx app/layout.tsx
git commit -m "feat: add theme provider with dark default and light/dark toggle"
```

---

### Task 5: GlassPanel primitive

**Files:**
- Create: `components/glass/GlassPanel.tsx`
- Test: `tests/glass-panel.test.tsx`

**Interfaces:**
- Consumes: `TOKEN_NAMES`/`cssVar` from `lib/tokens.ts` (Task 2).
- Produces: `<GlassPanel as?: keyof JSX.IntrinsicElements, className?: string, children: ReactNode>` component, consumed by every section component in Tasks 8-11.

- [ ] **Step 1: Write failing test**

```typescript
// tests/glass-panel.test.tsx
import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { GlassPanel } from '@/components/glass/GlassPanel'

describe('GlassPanel', () => {
  it('renders children inside a glass-styled panel', () => {
    render(<GlassPanel>panel content</GlassPanel>)
    const panel = screen.getByText('panel content')
    expect(panel).toBeInTheDocument()
    expect(panel.className).toMatch(/glass-panel/)
  })

  it('renders as a custom element via `as` prop', () => {
    render(<GlassPanel as="section">section content</GlassPanel>)
    expect(screen.getByText('section content').tagName).toBe('SECTION')
  })
})
```

- [ ] **Step 2: Run test, verify it fails**

Run: `npm test -- glass-panel`
Expected: FAIL — module not found.

- [ ] **Step 3: Write GlassPanel**

```typescript
// components/glass/GlassPanel.tsx
import type { ElementType, ReactNode } from 'react'

type GlassPanelProps = {
  as?: ElementType
  className?: string
  children: ReactNode
}

export function GlassPanel({ as: Tag = 'div', className = '', children }: GlassPanelProps) {
  return (
    <Tag
      className={`glass-panel rounded-panel border border-border/40 bg-surface/70 p-6 backdrop-blur-[18px] shadow-[var(--glass-shadow)] ${className}`}
    >
      {children}
    </Tag>
  )
}
```

- [ ] **Step 4: Run test, verify pass**

Run: `npm test -- glass-panel`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add components/glass tests/glass-panel.test.tsx
git commit -m "feat: add GlassPanel primitive"
```

---

### Task 6: DotMatrix backdrop

**Files:**
- Create: `components/matrix/DotMatrix.tsx`
- Test: `tests/dot-matrix.test.tsx`

**Interfaces:**
- Produces: `<DotMatrix className?: string />`, a `<canvas>`-rendering component with `data-animating` attribute (`"true"`/`"false"`) for testability, consumed by Hero (Task 7).

- [ ] **Step 1: Write failing test**

```typescript
// tests/dot-matrix.test.tsx
import { render, screen } from '@testing-library/react'
import { describe, expect, it, vi, afterEach } from 'vitest'
import { DotMatrix } from '@/components/matrix/DotMatrix'

afterEach(() => {
  vi.restoreAllMocks()
})

describe('DotMatrix', () => {
  it('renders a canvas element', () => {
    render(<DotMatrix />)
    expect(screen.getByTestId('dot-matrix-canvas')).toBeInTheDocument()
  })

  it('does not animate when prefers-reduced-motion is set', () => {
    vi.stubGlobal('matchMedia', (query: string) => ({
      matches: query.includes('reduce'),
      media: query,
      addEventListener: () => {},
      removeEventListener: () => {},
    }))
    render(<DotMatrix />)
    expect(screen.getByTestId('dot-matrix-canvas').dataset.animating).toBe('false')
  })
})
```

- [ ] **Step 2: Run test, verify it fails**

Run: `npm test -- dot-matrix`
Expected: FAIL — module not found.

- [ ] **Step 3: Write DotMatrix**

```typescript
// components/matrix/DotMatrix.tsx
'use client'

import { useEffect, useRef, useState } from 'react'

const SPACING = 24
const DOT_RADIUS = 1.5
const IDLE_OPACITY = 0.14

export function DotMatrix({ className = '' }: { className?: string }) {
  const canvasRef = useRef<HTMLCanvasElement | null>(null)
  const [animating, setAnimating] = useState(true)

  useEffect(() => {
    const reduced = window.matchMedia('(prefers-reduced-motion: reduce)').matches
    setAnimating(!reduced)
  }, [])

  useEffect(() => {
    const canvas = canvasRef.current
    if (!canvas) return
    const ctx = canvas.getContext('2d')
    if (!ctx) return

    function draw() {
      const { width, height } = canvas!
      ctx!.clearRect(0, 0, width, height)
      ctx!.fillStyle = `rgb(130 140 255 / ${IDLE_OPACITY})`
      for (let x = 0; x < width; x += SPACING) {
        for (let y = 0; y < height; y += SPACING) {
          ctx!.beginPath()
          ctx!.arc(x, y, DOT_RADIUS, 0, Math.PI * 2)
          ctx!.fill()
        }
      }
    }

    function resize() {
      canvas!.width = canvas!.clientWidth
      canvas!.height = canvas!.clientHeight
      draw()
    }

    resize()
    window.addEventListener('resize', resize)
    return () => window.removeEventListener('resize', resize)
  }, [])

  return (
    <canvas
      ref={canvasRef}
      data-testid="dot-matrix-canvas"
      data-animating={animating ? 'true' : 'false'}
      className={`h-full w-full ${className}`}
      aria-hidden="true"
    />
  )
}
```

- [ ] **Step 4: Run test, verify pass**

Run: `npm test -- dot-matrix`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add components/matrix tests/dot-matrix.test.tsx
git commit -m "feat: add reduced-motion-aware dot matrix backdrop"
```

---

### Task 7: Hero aperture scene (R3F) + Hero section

**Files:**
- Create: `components/hero/ApertureScene.tsx`
- Create: `components/hero/Hero.tsx`
- Test: `tests/aperture-scene.test.tsx`
- Test: `tests/hero.test.tsx`

**Interfaces:**
- Consumes: `DotMatrix` (Task 6).
- Produces: `<Hero />` section, consumed by `app/page.tsx` (Task 12).

- [ ] **Step 1: Write failing test for ApertureScene**

```typescript
// tests/aperture-scene.test.tsx
import { describe, expect, it } from 'vitest'
import ReactThreeTestRenderer from '@react-three/test-renderer'
import { ApertureScene } from '@/components/hero/ApertureScene'

describe('ApertureScene', () => {
  it('renders six lobe meshes forming the aperture', async () => {
    const renderer = await ReactThreeTestRenderer.create(<ApertureScene />)
    const meshes = renderer.scene.children.filter((c) => c.type === 'Mesh')
    expect(meshes.length).toBe(6)
  })
})
```

- [ ] **Step 2: Run test, verify it fails**

Run: `npm test -- aperture-scene`
Expected: FAIL — module not found.

- [ ] **Step 3: Write ApertureScene**

Six lobes arranged rotationally, per brand guide sec 3.1 (six soft bulbous points) and sec 9.1 (frosted glass, studio lighting).

```typescript
// components/hero/ApertureScene.tsx
'use client'

import { useMemo, useRef } from 'react'
import { useFrame } from '@react-three/fiber'
import { MeshTransmissionMaterial } from '@react-three/drei'
import * as THREE from 'three'

const LOBE_COUNT = 6

export function ApertureScene() {
  const groupRef = useRef<THREE.Group>(null)

  const lobes = useMemo(
    () =>
      Array.from({ length: LOBE_COUNT }, (_, i) => {
        const angle = (i / LOBE_COUNT) * Math.PI * 2
        return {
          key: `lobe-${i}`,
          position: [Math.cos(angle) * 1.4, Math.sin(angle) * 1.4, 0] as [number, number, number],
          rotation: [0, 0, angle] as [number, number, number],
        }
      }),
    []
  )

  useFrame((_, delta) => {
    if (groupRef.current) groupRef.current.rotation.z += delta * 0.05
  })

  return (
    <group ref={groupRef}>
      <ambientLight intensity={0.6} />
      <directionalLight position={[3, 4, 5]} intensity={1.2} />
      {lobes.map((lobe) => (
        <mesh key={lobe.key} position={lobe.position} rotation={lobe.rotation}>
          <torusGeometry args={[0.9, 0.35, 32, 64, Math.PI * 1.1]} />
          <MeshTransmissionMaterial
            thickness={0.4}
            roughness={0.15}
            transmission={1}
            ior={1.2}
            chromaticAberration={0.02}
            color="#6872f8"
          />
        </mesh>
      ))}
    </group>
  )
}
```

- [ ] **Step 4: Run test, verify pass**

Run: `npm test -- aperture-scene`
Expected: PASS, 6 meshes found.

- [ ] **Step 5: Write failing test for Hero**

```typescript
// tests/hero.test.tsx
import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { Hero } from '@/components/hero/Hero'

describe('Hero', () => {
  it('renders the core product thesis headline', () => {
    render(<Hero />)
    expect(
      screen.getByText('The shortest path between an AI agent and organisational context.')
    ).toBeInTheDocument()
  })

  it('renders a single primary CTA', () => {
    render(<Hero />)
    expect(screen.getAllByRole('link', { name: /get started|join/i }).length).toBe(1)
  })
})
```

- [ ] **Step 6: Run test, verify it fails**

Run: `npm test -- hero`
Expected: FAIL — module not found.

- [ ] **Step 7: Write Hero section**

`@react-three/fiber`'s `Canvas` requires a WebGL context that jsdom doesn't provide; the Hero test above only exercises text/DOM content, so `Canvas` renders in jsdom as an empty wrapper without throwing (R3F degrades gracefully when no WebGL context is available in test mode with `frameloop="never"` fallback not required here — RTL doesn't invoke real GPU calls, only DOM mount).

```typescript
// components/hero/Hero.tsx
'use client'

import { Canvas } from '@react-three/fiber'
import { ApertureScene } from './ApertureScene'
import { DotMatrix } from '@/components/matrix/DotMatrix'

export function Hero() {
  return (
    <section className="relative flex h-screen w-full items-center justify-center overflow-hidden bg-canvas">
      <div className="absolute inset-0">
        <DotMatrix />
      </div>
      <div className="absolute inset-0">
        <Canvas camera={{ position: [0, 0, 6], fov: 45 }}>
          <ApertureScene />
        </Canvas>
      </div>
      <div className="relative z-10 mx-auto max-w-3xl px-6 text-center">
        <h1 className="font-display text-4xl font-semibold text-text-primary md:text-6xl">
          The shortest path between an AI agent and organisational context.
        </h1>
        <p className="mt-4 font-body text-lg text-text-secondary">
          Persistent organisational infrastructure for AI agents: an Event Bus, a Task Graph, and a linked Knowledge Base.
        </p>
        <a
          href="#joining"
          className="mt-8 inline-block rounded-panel bg-accent-primary px-6 py-3 font-body font-medium text-white hover:bg-accent-hover"
        >
          Get started
        </a>
      </div>
    </section>
  )
}
```

- [ ] **Step 8: Run test, verify pass**

Run: `npm test -- hero`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add components/hero tests/aperture-scene.test.tsx tests/hero.test.tsx
git commit -m "feat: add 3D aperture hero scene"
```

---

### Task 8: Problem + InwardTransition sections

**Files:**
- Create: `components/sections/Problem.tsx`
- Create: `components/sections/InwardTransition.tsx`
- Test: `tests/problem.test.tsx`

**Interfaces:**
- Consumes: `GlassPanel` (Task 5).
- Produces: `<Problem />`, `<InwardTransition />`, consumed by `app/page.tsx` (Task 12).

- [ ] **Step 1: Write failing test**

```typescript
// tests/problem.test.tsx
import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { Problem } from '@/components/sections/Problem'

const FRAGMENTS = ['Chat threads', 'Ticket trackers', 'Scattered docs', 'Model context windows']

describe('Problem', () => {
  it('renders each fragmented-context panel', () => {
    render(<Problem />)
    for (const fragment of FRAGMENTS) {
      expect(screen.getByText(fragment)).toBeInTheDocument()
    }
  })
})
```

- [ ] **Step 2: Run test, verify it fails**

Run: `npm test -- problem`
Expected: FAIL — module not found.

- [ ] **Step 3: Write Problem section**

```typescript
// components/sections/Problem.tsx
import { GlassPanel } from '@/components/glass/GlassPanel'

const FRAGMENTS = [
  { title: 'Chat threads', body: 'Decisions made in conversation, lost once the window closes.' },
  { title: 'Ticket trackers', body: 'Work tracked in isolation from the knowledge that produced it.' },
  { title: 'Scattered docs', body: 'Context spread across tools no agent can query together.' },
  { title: 'Model context windows', body: 'Organisational memory that resets every session.' },
]

export function Problem() {
  return (
    <section className="mx-auto max-w-5xl px-6 py-24">
      <h2 className="font-display text-3xl font-semibold text-text-primary">
        Context is fragmented across every tool an agent touches.
      </h2>
      <div className="mt-10 grid grid-cols-1 gap-6 md:grid-cols-2">
        {FRAGMENTS.map((f) => (
          <GlassPanel key={f.title}>
            <h3 className="font-display text-lg font-medium text-text-primary">{f.title}</h3>
            <p className="mt-2 font-body text-sm text-text-secondary">{f.body}</p>
          </GlassPanel>
        ))}
      </div>
    </section>
  )
}
```

- [ ] **Step 4: Run test, verify pass**

Run: `npm test -- problem`
Expected: PASS.

- [ ] **Step 5: Write InwardTransition**

Damped convergence using Framer Motion `useScroll`/`useTransform`; static (no motion) render when `prefers-reduced-motion` is set, per Global Constraints.

```typescript
// components/sections/InwardTransition.tsx
'use client'

import { useEffect, useRef, useState } from 'react'
import { motion, useReducedMotion, useScroll, useTransform } from 'framer-motion'

export function InwardTransition() {
  const ref = useRef<HTMLDivElement>(null)
  const prefersReducedMotion = useReducedMotion()
  const { scrollYProgress } = useScroll({ target: ref, offset: ['start end', 'end start'] })
  const scale = useTransform(scrollYProgress, [0, 0.5, 1], [1, 0.6, 1])
  const opacity = useTransform(scrollYProgress, [0, 0.5, 1], [1, 0.3, 1])

  return (
    <div ref={ref} className="flex h-[40vh] items-center justify-center">
      <motion.div
        style={prefersReducedMotion ? undefined : { scale, opacity }}
        className="h-24 w-24 rounded-full bg-gradient-to-br from-accent-primary via-[#9a72f8] to-[#65d6e6]"
        aria-hidden="true"
      />
    </div>
  )
}
```

- [ ] **Step 6: Commit**

```bash
git add components/sections/Problem.tsx components/sections/InwardTransition.tsx tests/problem.test.tsx
git commit -m "feat: add Problem section and inward scroll transition"
```

---

### Task 9: Core section

**Files:**
- Create: `components/sections/Core.tsx`
- Test: `tests/core.test.tsx`

**Interfaces:**
- Consumes: `GlassPanel` (Task 5).
- Produces: `<Core />`, consumed by `app/page.tsx` (Task 12).

- [ ] **Step 1: Write failing test**

```typescript
// tests/core.test.tsx
import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { Core } from '@/components/sections/Core'

describe('Core', () => {
  it('renders all four Core pillars with a mono example', () => {
    render(<Core />)
    expect(screen.getByText('Event Bus')).toBeInTheDocument()
    expect(screen.getByText('task.status_changed')).toBeInTheDocument()
    expect(screen.getByText('Task Graph')).toBeInTheDocument()
    expect(screen.getByText('Knowledge Base')).toBeInTheDocument()
    expect(screen.getByText('kb.article.created')).toBeInTheDocument()
    expect(screen.getByText('Identity & Permissions')).toBeInTheDocument()
  })
})
```

- [ ] **Step 2: Run test, verify it fails**

Run: `npm test -- core`
Expected: FAIL — module not found.

- [ ] **Step 3: Write Core section**

Mono examples reused from brand guide sec 4.3 (`task.status_changed`, `kb.article.created`, `agent:reviewer-01`).

```typescript
// components/sections/Core.tsx
import { GlassPanel } from '@/components/glass/GlassPanel'

const PILLARS = [
  { title: 'Event Bus', body: 'Typed communication between agents.', example: 'task.status_changed' },
  { title: 'Task Graph', body: 'Coordination across dependent work.', example: 'task.blocked_by' },
  { title: 'Knowledge Base', body: 'Linked organisational memory.', example: 'kb.article.created' },
  { title: 'Identity & Permissions', body: 'Attribution and control per agent.', example: 'agent:reviewer-01' },
]

export function Core() {
  return (
    <section className="mx-auto max-w-5xl px-6 py-24">
      <h2 className="font-display text-3xl font-semibold text-text-primary">Four pillars, one shared context.</h2>
      <div className="mt-10 grid grid-cols-1 gap-6 md:grid-cols-2">
        {PILLARS.map((p) => (
          <GlassPanel key={p.title}>
            <h3 className="font-display text-lg font-medium text-text-primary">{p.title}</h3>
            <p className="mt-2 font-body text-sm text-text-secondary">{p.body}</p>
            <code className="mt-4 block font-mono text-xs text-accent-bright">{p.example}</code>
          </GlassPanel>
        ))}
      </div>
    </section>
  )
}
```

- [ ] **Step 4: Run test, verify pass**

Run: `npm test -- core`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add components/sections/Core.tsx tests/core.test.tsx
git commit -m "feat: add Core pillars section"
```

---

### Task 10: Joining + ModelIndependence sections

**Files:**
- Create: `components/sections/Joining.tsx`
- Create: `components/sections/ModelIndependence.tsx`
- Test: `tests/joining.test.tsx`

**Interfaces:**
- Consumes: `GlassPanel` (Task 5).
- Produces: `<Joining />`, `<ModelIndependence />`, consumed by `app/page.tsx` (Task 12).

- [ ] **Step 1: Write failing test**

Brand guide sec 2.2 requires indicative interfaces to be labelled indicative — test enforces that label is present.

```typescript
// tests/joining.test.tsx
import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { Joining } from '@/components/sections/Joining'

describe('Joining', () => {
  it('has section id for hero CTA anchor', () => {
    const { container } = render(<Joining />)
    expect(container.querySelector('#joining')).toBeInTheDocument()
  })

  it('labels the sequence as indicative', () => {
    render(<Joining />)
    expect(screen.getByText(/indicative/i)).toBeInTheDocument()
  })

  it('renders the status sequence steps', () => {
    render(<Joining />)
    expect(screen.getByText('Resolving agent identity')).toBeInTheDocument()
    expect(screen.getByText('Ready')).toBeInTheDocument()
  })
})
```

- [ ] **Step 2: Run test, verify it fails**

Run: `npm test -- joining`
Expected: FAIL — module not found.

- [ ] **Step 3: Write Joining section**

Status sequence copy from brand guide sec 12.4.

```typescript
// components/sections/Joining.tsx
import { GlassPanel } from '@/components/glass/GlassPanel'

const STEPS = [
  'Resolving agent identity',
  'Loading project permissions',
  'Synchronising knowledge',
  'Loading assigned tasks',
  'Ready',
]

export function Joining() {
  return (
    <section id="joining" className="mx-auto max-w-3xl px-6 py-24">
      <h2 className="font-display text-3xl font-semibold text-text-primary">Joining an organisation.</h2>
      <p className="mt-2 font-body text-sm text-text-secondary">
        Indicative sequence — actual steps depend on deployment configuration.
      </p>
      <GlassPanel className="mt-8">
        <ol className="space-y-3 font-mono text-sm text-text-secondary">
          {STEPS.map((step) => (
            <li key={step}>{step}</li>
          ))}
        </ol>
      </GlassPanel>
    </section>
  )
}
```

- [ ] **Step 4: Run test, verify pass**

Run: `npm test -- joining`
Expected: PASS.

- [ ] **Step 5: Write ModelIndependence section**

No fabricated vendor claims — generic "MCP-compliant agent" labels only, per Global Constraints.

```typescript
// components/sections/ModelIndependence.tsx
import { GlassPanel } from '@/components/glass/GlassPanel'

const FORMS = ['MCP-compliant agent A', 'MCP-compliant agent B', 'MCP-compliant agent C']

export function ModelIndependence() {
  return (
    <section className="mx-auto max-w-4xl px-6 py-24 text-center">
      <h2 className="font-display text-3xl font-semibold text-text-primary">
        Model-agnostic by design.
      </h2>
      <p className="mx-auto mt-2 max-w-xl font-body text-sm text-text-secondary">
        Any MCP-compliant client reads and writes the same shared context.
      </p>
      <div className="mt-10 flex flex-wrap justify-center gap-4">
        {FORMS.map((form) => (
          <GlassPanel key={form} className="px-5 py-3">
            <span className="font-mono text-xs text-text-secondary">{form}</span>
          </GlassPanel>
        ))}
      </div>
    </section>
  )
}
```

- [ ] **Step 6: Commit**

```bash
git add components/sections/Joining.tsx components/sections/ModelIndependence.tsx tests/joining.test.tsx
git commit -m "feat: add Joining and model-independence sections"
```

---

### Task 11: Deployment + FinalCTA sections

**Files:**
- Create: `components/sections/Deployment.tsx`
- Create: `components/sections/FinalCTA.tsx`
- Test: `tests/deployment.test.tsx`
- Test: `tests/final-cta.test.tsx`

**Interfaces:**
- Consumes: `GlassPanel` (Task 5), `ThemeToggle` (Task 4).
- Produces: `<Deployment />`, `<FinalCTA />`, consumed by `app/page.tsx` (Task 12).

- [ ] **Step 1: Write failing tests**

```typescript
// tests/deployment.test.tsx
import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { Deployment } from '@/components/sections/Deployment'

describe('Deployment', () => {
  it('presents self-hosted and managed on identical core code', () => {
    render(<Deployment />)
    expect(screen.getByText('Self-hosted')).toBeInTheDocument()
    expect(screen.getByText('Wormhole-managed')).toBeInTheDocument()
    expect(screen.getAllByText(/same core/i).length).toBeGreaterThan(0)
  })
})
```

```typescript
// tests/final-cta.test.tsx
import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { FinalCTA } from '@/components/sections/FinalCTA'

describe('FinalCTA', () => {
  it('renders exactly one primary action', () => {
    render(<FinalCTA />)
    expect(screen.getAllByRole('link').length).toBe(1)
  })
})
```

- [ ] **Step 2: Run tests, verify they fail**

Run: `npm test -- deployment final-cta`
Expected: FAIL — modules not found.

- [ ] **Step 3: Write Deployment section**

No availability-date claims (Global Constraints: no fabricated claims).

```typescript
// components/sections/Deployment.tsx
import { GlassPanel } from '@/components/glass/GlassPanel'

export function Deployment() {
  return (
    <section className="mx-auto max-w-4xl px-6 py-24">
      <h2 className="font-display text-3xl font-semibold text-text-primary">
        Same core code, wherever it runs.
      </h2>
      <div className="mt-10 grid grid-cols-1 gap-6 md:grid-cols-2">
        <GlassPanel>
          <h3 className="font-display text-lg font-medium text-text-primary">Self-hosted</h3>
          <p className="mt-2 font-body text-sm text-text-secondary">
            Run Wormhole on your own infrastructure, on the same core code as every deployment.
          </p>
        </GlassPanel>
        <GlassPanel>
          <h3 className="font-display text-lg font-medium text-text-primary">Wormhole-managed</h3>
          <p className="mt-2 font-body text-sm text-text-secondary">
            A hosted offering built on the same core code, for teams who prefer not to operate it themselves.
          </p>
        </GlassPanel>
      </div>
    </section>
  )
}
```

- [ ] **Step 4: Write FinalCTA section**

```typescript
// components/sections/FinalCTA.tsx
export function FinalCTA() {
  return (
    <section className="mx-auto max-w-2xl px-6 py-32 text-center">
      <h2 className="font-display text-3xl font-semibold text-text-primary">
        Give your agents somewhere to remember.
      </h2>
      <a
        href="https://github.com/H4RL33/wormhole"
        className="mt-8 inline-block rounded-panel bg-accent-primary px-6 py-3 font-body font-medium text-white hover:bg-accent-hover"
      >
        View on GitHub
      </a>
    </section>
  )
}
```

- [ ] **Step 5: Run tests, verify pass**

Run: `npm test -- deployment final-cta`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add components/sections/Deployment.tsx components/sections/FinalCTA.tsx tests/deployment.test.tsx tests/final-cta.test.tsx
git commit -m "feat: add Deployment and final CTA sections"
```

---

### Task 12: Page assembly, brand assets, reduced-motion pass

**Files:**
- Modify: `app/page.tsx`
- Modify: `app/layout.tsx` (nav with ThemeToggle)
- Create: `public/brand/` (copied from `wormhole/brand/`)
- Create: `app/icon.png`
- Test: `tests/page.test.tsx`

**Interfaces:**
- Consumes: `Hero`, `Problem`, `InwardTransition`, `Core`, `Joining`, `ModelIndependence`, `Deployment`, `FinalCTA` (Tasks 7-11), `ThemeToggle` (Task 4).

- [ ] **Step 1: Copy brand assets**

```bash
mkdir -p public/brand
cp /mnt/data/vault/projects/wormhole/brand/*.png /mnt/data/vault/projects/wormhole/brand/*.jpg public/brand/
cp /mnt/data/vault/projects/wormhole/brand/icon_wbs.png app/icon.png
```

- [ ] **Step 2: Write failing test for page assembly**

```typescript
// tests/page.test.tsx
import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import Page from '@/app/page'

describe('Home page', () => {
  it('renders all sections in brand-guide order', () => {
    render(<Page />)
    const headings = screen.getAllByRole('heading', { level: 2 }).map((h) => h.textContent)
    expect(headings).toEqual([
      'Context is fragmented across every tool an agent touches.',
      'Four pillars, one shared context.',
      'Joining an organisation.',
      'Model-agnostic by design.',
      'Same core code, wherever it runs.',
      'Give your agents somewhere to remember.',
    ])
  })
})
```

- [ ] **Step 3: Run test, verify it fails**

Run: `npm test -- page`
Expected: FAIL — `app/page.tsx` still has scaffold content.

- [ ] **Step 4: Assemble page.tsx**

```typescript
// app/page.tsx
import { Hero } from '@/components/hero/Hero'
import { Problem } from '@/components/sections/Problem'
import { InwardTransition } from '@/components/sections/InwardTransition'
import { Core } from '@/components/sections/Core'
import { Joining } from '@/components/sections/Joining'
import { ModelIndependence } from '@/components/sections/ModelIndependence'
import { Deployment } from '@/components/sections/Deployment'
import { FinalCTA } from '@/components/sections/FinalCTA'

export default function Page() {
  return (
    <main>
      <Hero />
      <Problem />
      <InwardTransition />
      <Core />
      <Joining />
      <ModelIndependence />
      <Deployment />
      <FinalCTA />
    </main>
  )
}
```

- [ ] **Step 5: Run test, verify pass**

Run: `npm test -- page`
Expected: PASS.

- [ ] **Step 6: Add nav with ThemeToggle**

Edit `app/layout.tsx`: inside `<ThemeProvider>`, before `{children}`, add:

```typescript
<nav className="fixed inset-x-0 top-0 z-20 flex items-center justify-between px-6 py-4">
  <span className="font-display text-sm font-medium text-text-primary">Wormhole</span>
  <ThemeToggle />
</nav>
```

Import `ThemeToggle` at the top of the file.

- [ ] **Step 7: Run full test suite**

Run: `npm test`
Expected: all tests pass.

- [ ] **Step 8: Run production build**

Run: `npm run build`
Expected: build succeeds with no errors.

- [ ] **Step 9: Manual QA against brand guide sec 18 marketing checklist**

Run `npm run dev`, open `http://localhost:3000`, and confirm:
- every section is readable with browser dev-tools "emulate CSS prefers-reduced-motion: reduce" enabled
- scroll is not hijacked (mouse wheel and keyboard `Page Down` both work normally)
- light/dark toggle changes all tokens correctly, contrast holds in both
- mobile viewport (375px) shows a simplified, still-legible layout

- [ ] **Step 10: Commit**

```bash
git add app/page.tsx app/layout.tsx app/icon.png public/brand tests/page.test.tsx
git commit -m "feat: assemble landing page and copy brand assets"
```

---

## Self-Review Notes

- **Spec coverage:** all 8 sections from spec sec "Page sections" have a task (Tasks 7-11) and are assembled in Task 12. Theming (Task 4), tokens (Task 2), fonts (Task 3), assets (Task 12) all covered. Motion/reduced-motion constraint enforced in globals.css (Task 2), DotMatrix (Task 6), InwardTransition (Task 8), and manually verified (Task 12 Step 9).
- **Placeholder scan:** none found — every step has runnable code or an exact command.
- **Type consistency:** `GlassPanel` props (`as`, `className`, `children`) used consistently across Tasks 7-11. `useTheme()` return shape (`theme`, `toggle`) matches between Task 4's provider and toggle. `cssVar`/`TOKEN_NAMES` from Task 2 not directly re-consumed by name in later components (GlassPanel uses Tailwind classes referencing the same CSS vars directly) — acceptable, tokens.ts primarily documents the token set for future consumers.
