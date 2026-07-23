# Wormhole Marketing Website — Design

**Status:** Approved
**Date:** 2026-07-23
**Repo:** new standalone repo, `~/Projects/wormhole-website` (GitHub: `wormhole-website`)

## Purpose

Standalone marketing/landing site for Wormhole. Not part of the `wormhole` monorepo. Communicates product thesis, drives Joining/signup interest. Built per `brand/WORMHOLE-BRAND-GUIDELINES.md`.

## Stack

- Next.js 15, App Router, TypeScript
- React Three Fiber + drei — 3D aperture hero scene
- Framer Motion — panel/scroll transitions
- Tailwind CSS — tokens sourced from brand guide sec 5 (colour) and sec 15 (token groups)
- Fonts via `next/font`, self-hosted: Space Grotesk (display), Inter (body), IBM Plex Mono (technical)
- Deploy target: Vercel

## Scope (v1)

Single scrollable landing page only. No pricing/docs/about/contact pages, no CMS, no real Joining flow, no custom cursor replacement (brand guide marks cursor effects optional — deferred).

## Page sections (brand guide sec 12.1 rhythm)

1. **Hero** — 3D aperture scene (R3F), product thesis copy, dot-matrix backdrop with cursor-local lensing
2. **Problem** — fragmented context shown as separated glass panels
3. **Inward transition** — panels converge toward aperture (brand guide sec 10.4 inward-scroll pattern)
4. **Core** — four glass panels: Event Bus, Task Graph, Knowledge Base, Identity/Permissions
5. **Joining** — structured onboarding sequence, teaser only (no live flow)
6. **Model independence** — different agent forms entering one shared system
7. **Deployment** — self-hosted vs managed, identical core code, no false claims of hosted GA
8. **Final CTA** — quiet, stable composition, single primary action

Each section: one message, one dominant spatial idea, one primary action, per brand guide sec 12.1.

## Theming

Both light and dark implemented as equal systems (brand guide sec 5.1), dark is default on load. Toggle available. Tokens: `canvas`, `surface`, `surface-raised`, `text-primary`, `text-secondary`, `border`, `accent-primary/hover/soft/dark-soft/bright` — values per brand guide sec 5.2/5.3. Portal spectrum (violet/cyan) reserved for expressive gradient elements, one dominant gradient per composition (sec 5.4).

## Motion

- Damped parallax on environmental layers only (dot matrix, ambient shapes, floating panels) — never on body text/reading surfaces (sec 11.4)
- `prefers-reduced-motion` MUST disable non-essential animation; page must be fully readable/functional with all motion off (sec 13)
- No scroll hijacking; inward-scroll transitions alternate with stable reading planes (sec 10.4)
- Timing scale per sec 10.3 (120ms micro-state through 600–1200ms major spatial transitions)

## Assets

Raster brand masters (`brand/*.png`, `*.jpg`) copied into `public/brand/`. No SVG masters exist yet (brand guide sec 3.7/19 flags this as a pre-launch gap) — raster used as-is for v1, filled lockup only, correct polarity per surface. Favicon cropped from filled symbol.

## Structure

```
wormhole-website/
├── app/
│   ├── layout.tsx
│   ├── page.tsx
│   └── globals.css        # design tokens, light+dark
├── components/
│   ├── hero/               # R3F aperture scene
│   ├── sections/           # Problem, Core, Joining, ModelIndependence, Deployment, CTA
│   ├── glass/               # GlassPanel primitive (sec 7)
│   └── matrix/               # dot-matrix canvas (sec 8)
├── lib/
│   └── tokens.ts
├── public/brand/
└── next.config.ts
```

## Out of scope (v1)

Pricing/docs/about pages, CMS, real Joining flow, custom cursor replacement, invented customers/stats/integrations (brand guide sec 12.1 explicit prohibition).
