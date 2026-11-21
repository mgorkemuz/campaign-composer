<script setup lang="ts">
// The logo: a few squares in the delivery-status colours.
const cells = [
  { x: 0, y: 0, c: 'bg-foreground' }, { x: 1, y: 0, c: 'bg-st-sent' }, { x: 2, y: 0, c: 'bg-foreground' },
  { x: 0, y: 1, c: 'bg-st-active' }, { x: 2, y: 1, c: 'bg-st-delivered' },
]
</script>

<template>
  <span class="relative inline-block h-[14px] w-[22px]" aria-hidden="true">
    <span v-for="(cell, i) in cells" :key="i" :class="cell.c" class="absolute size-[6px]"
      :style="{ left: `${cell.x * 8}px`, top: `${cell.y * 8}px` }" />
  </span>
</template>
