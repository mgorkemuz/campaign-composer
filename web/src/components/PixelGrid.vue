<script setup lang="ts">
// One square per delivery, coloured by status. Drawn on a canvas: thousands of
// squares redrawn twice a second would be slow as DOM elements.
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'

const props = defineProps<{ grid: string }>()
const canvas = ref<HTMLCanvasElement>()
const wrap = ref<HTMLDivElement>()
const hover = ref<{ index: number; status: string; x: number; y: number } | null>(null)

const CELL = 8
const GAP = 2
const statusName: Record<string, string> = {
  q: 'queued', s: 'sending', r: 'retrying', t: 'sent, waiting for receipt', d: 'delivered', u: 'undelivered', f: 'failed',
}
const colourVar: Record<string, string> = {
  q: '--st-queued', s: '--st-active', r: '--st-active', t: '--st-sent', d: '--st-delivered', u: '--st-failed', f: '--st-failed',
}

let columns = 1
let observer: ResizeObserver | undefined

function draw() {
  const el = canvas.value
  const box = wrap.value
  if (!el || !box) return
  const width = box.clientWidth
  columns = Math.max(1, Math.floor((width + GAP) / (CELL + GAP)))
  const rows = Math.max(1, Math.ceil(props.grid.length / columns))
  const dpr = window.devicePixelRatio || 1
  el.width = width * dpr
  el.height = (rows * (CELL + GAP) - GAP) * dpr
  el.style.height = `${rows * (CELL + GAP) - GAP}px`
  const ctx = el.getContext('2d')!
  ctx.scale(dpr, dpr)
  const css = getComputedStyle(document.documentElement)
  const colours: Record<string, string> = {}
  for (const [code, v] of Object.entries(colourVar)) colours[code] = css.getPropertyValue(v).trim()
  for (let i = 0; i < props.grid.length; i++) {
    ctx.fillStyle = colours[props.grid[i]] ?? colours.q
    ctx.fillRect((i % columns) * (CELL + GAP), Math.floor(i / columns) * (CELL + GAP), CELL, CELL)
  }
}

function onMove(e: MouseEvent) {
  const r = canvas.value!.getBoundingClientRect()
  const col = Math.floor((e.clientX - r.left) / (CELL + GAP))
  const row = Math.floor((e.clientY - r.top) / (CELL + GAP))
  const index = row * columns + col
  hover.value = col < columns && index < props.grid.length
    ? { index, status: statusName[props.grid[index]], x: e.clientX - r.left, y: e.clientY - r.top }
    : null
}

watch(() => props.grid, draw)
onMounted(() => {
  draw()
  observer = new ResizeObserver(draw)
  observer.observe(wrap.value!)
})
onBeforeUnmount(() => observer?.disconnect())
</script>

<template>
  <div ref="wrap" class="relative w-full">
    <canvas ref="canvas" class="block w-full" role="img"
      :aria-label="`${grid.length} deliveries`" @mousemove="onMove" @mouseleave="hover = null" />
    <div v-if="hover" class="pointer-events-none absolute z-10 rounded-md bg-foreground px-2 py-1 text-xs text-background"
      :style="{ left: `${hover.x + 10}px`, top: `${hover.y + 10}px` }">
      #{{ hover.index + 1 }} · {{ hover.status }}
    </div>
  </div>
</template>
