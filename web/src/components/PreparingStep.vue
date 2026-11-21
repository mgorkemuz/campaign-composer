<script setup lang="ts">
import { computed } from 'vue'
import { Check } from 'lucide-vue-next'
import type { ApiErrorBody, Campaign } from '@/lib/types'
import Button from './ui/Button.vue'
import Problems from './ui/Problems.vue'
import PixelLoader from './PixelLoader.vue'
import Stepper from './Stepper.vue'

// Shown while the audience and messages are prepared automatically.
const props = defineProps<{
  campaign: Campaign
  streamingSMS: string
  error: ApiErrorBody | null
}>()
defineEmits<{ retry: [] }>()

const audienceDone = computed(() => props.campaign.rule !== null)
</script>

<template>
  <div class="mx-auto max-w-2xl space-y-10 pt-6">
    <Stepper :current="audienceDone ? 2 : 1" />
    <div class="space-y-6">
      <h1 class="text-4xl font-semibold tracking-tight sm:text-5xl">
        {{ audienceDone ? 'Writing your messages' : 'Finding your audience' }}
      </h1>
      <p class="text-muted-foreground">{{ campaign.brief }}</p>

      <ul class="space-y-3 text-sm">
        <li class="flex items-center gap-3">
          <Check v-if="audienceDone" class="size-4 text-st-delivered" />
          <PixelLoader v-else-if="!error" />
          <span v-if="audienceDone">
            {{ campaign.audience_summary || 'Audience ready' }}
            <span class="font-mono text-muted-foreground tabular">· {{ (campaign.audience_total ?? 0).toLocaleString() }} people</span>
          </span>
          <span v-else class="text-muted-foreground">Reading the brief…</span>
        </li>
        <li v-if="audienceDone" class="flex items-center gap-3">
          <PixelLoader v-if="!error" />
          <span class="text-muted-foreground">Writing the SMS and email for them…</span>
        </li>
      </ul>

      <div v-if="streamingSMS" class="ml-auto w-fit max-w-[85%] whitespace-pre-wrap rounded-2xl rounded-br-sm bg-st-sent px-4 py-2.5 text-white">
        {{ streamingSMS }}
      </div>

      <div v-if="error" class="space-y-3">
        <Problems :message="error.message" :problems="error.problems" />
        <div class="flex gap-2">
          <Button @click="$emit('retry')">Try again</Button>
          <RouterLink to="/campaigns/new" class="inline-flex h-9 items-center px-4 text-sm text-muted-foreground hover:underline">
            Write a new brief
          </RouterLink>
        </div>
      </div>
    </div>
  </div>
</template>
