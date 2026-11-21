<script setup lang="ts">
import { Check } from 'lucide-vue-next'

// The four steps of a campaign; `current` is the one in progress.
defineProps<{ current: number }>()
const steps = ['Brief', 'Audience', 'Messages', 'Send']
</script>

<template>
  <ol class="flex items-center gap-2 text-xs">
    <li v-for="(s, i) in steps" :key="s" class="flex items-center gap-2">
      <span class="flex items-center gap-1.5" :class="i <= current ? 'text-foreground' : 'text-muted-foreground'">
        <span class="grid size-5 place-items-center rounded-md font-mono text-[11px]"
          :class="i < current ? 'bg-st-delivered text-white' : i === current ? 'bg-foreground text-background' : 'border border-border'">
          <Check v-if="i < current" class="size-3" /><template v-else>{{ i + 1 }}</template>
        </span>
        <span class="hidden sm:inline" :class="{ 'font-medium': i === current }">{{ s }}</span>
      </span>
      <span v-if="i < steps.length - 1" class="h-px w-6 bg-border sm:w-10" />
    </li>
  </ol>
</template>
