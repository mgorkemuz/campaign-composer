<script setup lang="ts">
// An error message with the list of problems the API returned.
defineProps<{ message: string; problems?: string[] }>()
</script>

<template>
  <div role="alert" class="rounded-lg border border-destructive/30 bg-destructive/5 px-4 py-3 text-sm">
    <p class="font-medium text-destructive">{{ message }}</p>
    <ul v-if="problems?.length" class="mt-1.5 list-disc space-y-0.5 pl-5 text-muted-foreground">
      <li v-for="p in problems" :key="p" class="font-mono text-xs">{{ p }}</li>
    </ul>
  </div>
</template>
