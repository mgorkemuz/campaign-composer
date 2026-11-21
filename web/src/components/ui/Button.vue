<script setup lang="ts">
import { computed } from 'vue'

const props = withDefaults(defineProps<{
  variant?: 'default' | 'outline' | 'ghost' | 'destructive'
  size?: 'sm' | 'md'
  loading?: boolean
  disabled?: boolean
  type?: 'button' | 'submit'
}>(), { variant: 'default', size: 'md', type: 'button' })

const classes = computed(() => [
  'inline-flex items-center justify-center gap-2 rounded-lg font-medium transition-colors',
  'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:pointer-events-none disabled:opacity-50',
  props.size === 'sm' ? 'h-8 px-3 text-xs' : 'h-9 px-4 text-sm',
  {
    default: 'bg-primary text-primary-foreground hover:opacity-90',
    outline: 'border border-border bg-background hover:bg-accent',
    ghost: 'hover:bg-accent',
    destructive: 'bg-destructive text-white hover:opacity-90',
  }[props.variant],
])
</script>

<template>
  <button :type="type" :class="classes" :disabled="disabled || loading">
    <span v-if="loading" class="size-3.5 animate-spin rounded-full border-2 border-current border-r-transparent" />
    <slot />
  </button>
</template>
