<script setup lang="ts">
// The home page band: a pipeline that flows right to left. Squares enter at the
// right edge grey (queued), turn yellow (sending), then blue (sent), and settle
// on a final status as they travel: navy delivered, red not delivered. The
// final colours cycle through the app's real latest messages, so the mix is
// genuine; with no messages yet, an example mix is used.
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'

const props = withDefaults(defineProps<{ grid: string | null; rows?: number; cell?: number; gap?: number }>(),
  { rows: 14, cell: 10, gap: 2 })
const canvas = ref<HTMLCanvasElement>()
const wrap = ref<HTMLDivElement>()

const SPEED = 26 // pixels per second
// How many columns (from the right edge) a square spends in each stage, before
// a per-square jitter that keeps the boundaries ragged.
const QUEUED = 1.5, SENDING = 7, SENT = 14, JITTER = 7
const EXAMPLE = 'dddddddddudddddddddddddfdddddddddddddddduddddddddd'

const reduced = typeof window !== 'undefined' && window.matchMedia('(prefers-reduced-motion: reduce)').matches
let colours: Record<string, string> = {}
let codes = EXAMPLE
let width = 0
let raf = 0
const t0 = performance.now()

// Smooth 2D value noise, for the dripping silhouette.
function hash(x: number, y: number): number {
  let h = (x * 374761393 + y * 668265263) | 0
  h = Math.imul(h ^ (h >>> 13), 1274126177)
  return ((h ^ (h >>> 16)) >>> 0) / 4294967295
}
function noise(x: number, y: number): number {
  const xi = Math.floor(x), yi = Math.floor(y), xf = x - xi, yf = y - yi
  const u = xf * xf * (3 - 2 * xf), v = yf * yf * (3 - 2 * yf)
  const a = hash(xi, yi), b = hash(xi + 1, yi), c = hash(xi, yi + 1), d = hash(xi + 1, yi + 1)
  return a + (b - a) * u + (c - a) * v + (a - b - c + d) * u * v
}
const fbm = (x: number, y: number) => noise(x, y) * 0.6 + noise(x * 2.1, y * 2.1) * 0.3 + noise(x * 4.3, y * 4.3) * 0.1

// Solid at the top, breaking into drips towards the bottom. Tied to the global
// column index, so the shape travels with the squares.
function keep(col: number, y: number): boolean {
  const depth = y / props.rows
  return fbm(col * 0.07, y * 0.16) >= 0.12 + Math.pow(depth, 1.8) * (0.9 - noise(col * 0.35, 3.7) * 0.35)
}

function finalColour(col: number, y: number): string {
  const n = codes.length
  const c = codes[(((col * props.rows + y) % n) + n) % n]
  return ({ d: colours.delivered, t: colours.sent, s: colours.active, r: colours.active, q: colours.queued })[c] ?? colours.failed
}

function resize() {
  const el = canvas.value!, box = wrap.value!
  width = box.clientWidth
  const height = props.rows * (props.cell + props.gap)
  const dpr = window.devicePixelRatio || 1
  el.width = Math.round(width * dpr)
  el.height = Math.round(height * dpr)
  el.style.height = `${height}px`
  el.getContext('2d')!.setTransform(dpr, 0, 0, dpr, 0, 0)
}

function draw(now: number) {
  const el = canvas.value
  if (!el) return
  const ctx = el.getContext('2d')!
  const step = props.cell + props.gap
  const height = props.rows * step
  ctx.clearRect(0, 0, width, height)

  const scroll = reduced ? 0 : ((now - t0) / 1000) * SPEED
  const first = Math.floor(scroll / step) // global index of the leftmost column
  const offset = scroll - first * step
  const visible = Math.ceil(width / step) + 1
  const head = first + visible - 1 // the newest column, at the right edge

  for (let i = 0; i < visible; i++) {
    const col = first + i
    const x = i * step - offset
    const age = head - col
    for (let y = 0; y < props.rows; y++) {
      if (!keep(col, y)) continue
      const a = age - hash(col, y + 101) * JITTER
      ctx.fillStyle = a < QUEUED ? colours.queued : a < SENDING ? colours.active : a < SENT ? colours.sent : finalColour(col, y)
      ctx.fillRect(x, y * step, props.cell, props.cell)
    }
  }
  if (!reduced) raf = requestAnimationFrame(draw)
}

watch(() => props.grid, (g) => { codes = g ? g : EXAMPLE })

let observer: ResizeObserver | undefined
onMounted(() => {
  const css = getComputedStyle(document.documentElement)
  for (const k of ['queued', 'active', 'sent', 'delivered', 'failed']) colours[k] = css.getPropertyValue(`--st-${k}`).trim()
  if (props.grid) codes = props.grid
  resize()
  observer = new ResizeObserver(() => { resize(); if (reduced) draw(performance.now()) })
  observer.observe(wrap.value!)
  raf = requestAnimationFrame(draw) // rAF pauses by itself in background tabs
})
onBeforeUnmount(() => {
  cancelAnimationFrame(raf)
  observer?.disconnect()
})
</script>

<template>
  <div ref="wrap" class="w-full" aria-hidden="true">
    <canvas ref="canvas" class="block w-full" />
  </div>
</template>
