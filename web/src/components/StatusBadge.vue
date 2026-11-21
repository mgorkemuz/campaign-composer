<script setup lang="ts">
import { computed } from 'vue'
import type { CampaignStatus } from '@/lib/types'

const props = defineProps<{ status: CampaignStatus }>()
const look = computed(() => ({
  draft: ['Draft', 'bg-st-queued'],
  audience_ready: ['Audience ready', 'bg-st-active'],
  content_ready: ['Ready to approve', 'bg-st-sent'],
  approved: ['Approved', 'bg-st-sent'],
  scheduled: ['Scheduled', 'bg-st-sent'],
  sending: ['Sending', 'bg-st-active'],
  completed: ['Completed', 'bg-st-delivered'],
  failed: ['Failed', 'bg-st-failed'],
}[props.status]))
</script>

<template>
  <span class="inline-flex items-center gap-1.5 rounded-md border border-border px-2 py-0.5 text-xs font-medium">
    <span class="size-2" :class="look[1]" />{{ look[0] }}
  </span>
</template>
