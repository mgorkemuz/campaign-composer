<script setup lang="ts">
import { useRoute } from 'vue-router'
import PixelMark from './components/PixelMark.vue'

// The home page has its own "Start a campaign" button in the hero.
const route = useRoute()
</script>

<template>
  <div class="min-h-screen">
    <header class="sticky top-0 z-10 border-b border-border bg-background/90 backdrop-blur">
      <div class="mx-auto flex h-14 max-w-6xl items-center justify-between px-4">
        <RouterLink to="/" class="flex items-center gap-2.5">
          <PixelMark />
          <span class="text-sm font-semibold tracking-tight">CAMPAIGN COMPOSER</span>
        </RouterLink>
        <RouterLink v-if="route.path !== '/'" to="/campaigns/new"
          class="rounded-lg bg-primary px-3 py-1.5 text-sm font-medium text-primary-foreground hover:opacity-90">
          New campaign
        </RouterLink>
      </div>
    </header>
    <main class="mx-auto max-w-6xl px-4 py-8">
      <RouterView />
    </main>
  </div>
</template>
