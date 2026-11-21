<script setup lang="ts">
import { computed } from 'vue'
import type { SampleUser } from '@/lib/types'
import { render } from '@/lib/sms'

const props = defineProps<{ sms: string; subject: string; body: string; recipient: SampleUser | null }>()
const r = computed(() => props.recipient ?? { first_name: 'Ayşe', last_name: 'Yılmaz', city: 'İstanbul' })
const p = (t: string) => render(t, r.value)
</script>

<template>
  <div class="grid gap-5 md:grid-cols-[260px_1fr]">
    <div class="rounded-[2rem] border-[6px] border-foreground bg-secondary p-4">
      <div class="mb-3 text-center text-[10px] text-muted-foreground">SMS</div>
      <div class="ml-auto w-fit max-w-[92%] whitespace-pre-wrap rounded-2xl rounded-br-sm bg-st-sent px-3.5 py-2 text-sm text-white">
        {{ p(sms) || '…' }}
      </div>
    </div>
    <div class="overflow-hidden rounded-lg border border-border bg-background">
      <div class="border-b border-border bg-secondary px-4 py-2 text-sm font-semibold">{{ p(subject) || '…' }}</div>
      <div class="whitespace-pre-wrap px-4 py-3 text-sm leading-relaxed">{{ p(body) }}</div>
    </div>
  </div>
</template>
