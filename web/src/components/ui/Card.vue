<script setup lang="ts">
defineProps<{ title?: string; step?: number; muted?: boolean }>()
</script>

<template>
  <section class="rounded-xl border border-border bg-card" :class="{ 'opacity-60': muted }">
    <header v-if="title || $slots.actions" class="flex items-center justify-between gap-4 border-b border-border px-5 py-3.5">
      <h2 class="flex items-center gap-2.5 text-sm font-semibold tracking-tight">
        <span v-if="step" class="grid size-5 place-items-center rounded-md bg-foreground font-mono text-[11px] text-background">{{ step }}</span>
        {{ title }}
      </h2>
      <div class="flex items-center gap-2"><slot name="actions" /></div>
    </header>
    <div class="p-5"><slot /></div>
  </section>
</template>
