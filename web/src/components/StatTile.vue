<script setup lang="ts">
defineProps<{ label: string; value: string | number; hint?: string; swatch?: string }>()
</script>

<template>
  <div class="rounded-lg border border-border bg-card px-4 py-3">
    <p class="flex items-center gap-1.5 text-xs text-muted-foreground">
      <span v-if="swatch" class="size-2" :class="swatch" />{{ label }}
    </p>
    <p class="mt-1 font-mono text-2xl font-medium tabular">{{ typeof value === 'number' ? value.toLocaleString() : value }}</p>
    <p v-if="hint" class="text-xs text-muted-foreground">{{ hint }}</p>
  </div>
</template>
