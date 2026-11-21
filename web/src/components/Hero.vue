<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { ArrowRight } from 'lucide-vue-next'
import { api } from '@/lib/api'
import PixelBand from './PixelBand.vue'
import PixelMark from './PixelMark.vue'

// The band shows the latest real messages; refreshed while the page is open so
// a running campaign appears live.
const grid = ref<string | null>(null)
let timer: number | undefined

async function load() {
  try {
    grid.value = (await api.recentGrid()).grid
  } catch {
    if (grid.value === null) grid.value = ''
  }
}
onMounted(() => {
  load()
  timer = window.setInterval(() => { if (!document.hidden) load() }, 4000)
})
onBeforeUnmount(() => clearInterval(timer))

const legend = [
  { label: 'Queued', c: 'bg-st-queued' }, { label: 'Sending', c: 'bg-st-active' }, { label: 'Sent', c: 'bg-st-sent' },
  { label: 'Delivered', c: 'bg-st-delivered' }, { label: 'Not delivered', c: 'bg-st-failed' },
]
</script>

<template>
  <!-- Full-bleed, on a plain background (no grid behind the hero). -->
  <section class="relative ml-[calc(50%-50vw)] w-screen border-b border-border bg-background">
    <div class="mx-auto grid max-w-6xl md:grid-cols-[1fr_380px]">
      <div class="px-4 pb-10 pt-10 md:pb-14 md:pt-16">
        <h1 class="text-[13vw] font-medium uppercase leading-[0.9] tracking-[-0.04em] md:text-[96px] lg:text-[112px]">
          Brief in.<br />Delivered.
        </h1>
      </div>
      <div class="flex flex-col justify-between gap-8 border-border px-4 pb-10 pt-10 md:border-l md:px-8 md:pb-14 md:pt-16">
        <p class="hero-tagline text-xl uppercase leading-tight tracking-tight">
          AI campaigns from one sentence to every inbox
        </p>
        <div class="space-y-5">
          <div class="flex items-start justify-between gap-6">
            <PixelMark />
            <p class="max-w-[240px] text-right text-sm leading-relaxed text-muted-foreground">
              Say who and why. Claude finds the audience and writes the SMS and email; you watch every message land.
            </p>
          </div>
          <RouterLink to="/campaigns/new"
            class="flex h-11 items-center justify-between rounded-lg bg-primary px-4 text-sm font-medium text-primary-foreground hover:opacity-90">
            Start a campaign <ArrowRight class="size-4" />
          </RouterLink>
        </div>
      </div>
    </div>

    <PixelBand :grid="grid" />

    <div class="mx-auto flex max-w-6xl flex-wrap items-center justify-between gap-3 px-4 py-3 text-xs text-muted-foreground">
      <span v-if="grid">Your latest {{ grid.length.toLocaleString() }} messages, flowing through the pipeline.</span>
      <span v-else-if="grid === ''">An example flow: your messages will show up here.</span>
      <span class="flex flex-wrap gap-3">
        <span v-for="l in legend" :key="l.label" class="flex items-center gap-1.5"><span class="size-2" :class="l.c" />{{ l.label }}</span>
      </span>
    </div>
  </section>
</template>

<style scoped>
/* Justified edge to edge, last line included, like a printed masthead. Only
   where the column is wide enough; on phones it would stretch a short line. */
@media (min-width: 768px) {
  .hero-tagline {
    text-align: justify;
    text-align-last: justify;
  }
}
</style>
