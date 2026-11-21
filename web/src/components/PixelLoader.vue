<script setup lang="ts">
// A small pixel "thinking" animation shown while the AI works.
defineProps<{ label?: string }>()
</script>

<template>
  <span class="inline-flex items-center gap-2 text-sm text-muted-foreground">
    <span class="grid grid-cols-3 gap-[2px]" aria-hidden="true">
      <span v-for="i in 9" :key="i" class="pixel size-[5px] bg-st-sent" :style="{ animationDelay: `${(i * 137) % 900}ms` }" />
    </span>
    <span v-if="label">{{ label }}</span>
  </span>
</template>

<style scoped>
.pixel {
  animation: blink 900ms steps(2, jump-none) infinite;
}
@keyframes blink {
  0% { opacity: 0.15; }
  100% { opacity: 1; }
}
@media (prefers-reduced-motion: reduce) {
  .pixel { animation: none; opacity: 0.7; }
}
</style>
