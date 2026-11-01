import type { CSSProperties } from 'react'

/** Skeleton primitives for slow-loading surfaces. A skeleton keeps the
 *  page's shape while data is in flight — an empty block (or a bare
 *  «запусков пока нет» shown before the first fetch lands) reads as broken,
 *  while a shimmering placeholder reads as «working on it». The styling
 *  lives in carbon.scss (.tm-skeleton) and rides the theme variables, so
 *  both the dark and the light theme shimmer in their own palette. */

export function SkeletonBox({ width, height, radius = 6, style }: {
  width?: number | string
  height?: number | string
  radius?: number | string
  style?: CSSProperties
}) {
  return <div className="tm-skeleton" style={{ width, height, borderRadius: radius, ...style }} />
}

/** A run of text-like lines, widths tapering toward the bottom — the shape
 *  of a paragraph or a card body. */
export function SkeletonLines({ lines = 3, height = '0.65rem', gap = '0.45rem', style }: {
  lines?: number
  height?: number | string
  gap?: number | string
  style?: CSSProperties
}) {
  return (
    <div style={{ display: 'grid', gap, ...style }}>
      {Array.from({ length: lines }, (_, i) => (
        <SkeletonBox key={i} height={height} width={`${100 - Math.min(i, 3) * 14}%`} radius={4} />
      ))}
    </div>
  )
}

/** Tile-shaped rows for lists (the runs list, saga tables): the same
 *  footprint the real rows will occupy, so nothing jumps on arrival. */
export function SkeletonTiles({ rows = 5, height = '2.6rem', gap = '0.35rem', style }: {
  rows?: number
  height?: number | string
  gap?: number | string
  style?: CSSProperties
}) {
  return (
    <div style={{ display: 'grid', gap, ...style }}>
      {Array.from({ length: rows }, (_, i) => (
        <SkeletonBox key={i} height={height} />
      ))}
    </div>
  )
}
