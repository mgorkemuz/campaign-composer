<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { api } from '@/lib/api'
import type { Campaign, Channel, DeliveryRow, Progress } from '@/lib/types'
import { CalendarClock, RotateCcw } from 'lucide-vue-next'
import Button from './ui/Button.vue'
import Card from './ui/Card.vue'
import PixelGrid from './PixelGrid.vue'
import StatTile from './StatTile.vue'

const props = defineProps<{ campaign: Campaign; progress: Progress | null }>()
const emit = defineEmits<{ resent: []; unscheduled: [c: Campaign] }>()

// Scheduled: show when it will go out, with a countdown, and allow cancelling.
const now = ref(Date.now())
const tick = window.setInterval(() => (now.value = Date.now()), 15000)
onBeforeUnmount(() => clearInterval(tick))
const scheduledLabel = computed(() => props.campaign.scheduled_at
  ? new Date(props.campaign.scheduled_at).toLocaleString([], { dateStyle: 'medium', timeStyle: 'short' }) : '')
const countdown = computed(() => {
  if (!props.campaign.scheduled_at) return ''
  const mins = Math.max(0, Math.round((new Date(props.campaign.scheduled_at).getTime() - now.value) / 60000))
  return mins < 60 ? `in ${mins} min` : `in ${Math.floor(mins / 60)} h ${mins % 60} min`
})
const cancelling = ref(false)
const cancelError = ref('')
async function cancelSchedule() {
  cancelling.value = true
  cancelError.value = ''
  try {
    emit('unscheduled', await api.unschedule(props.campaign.id))
  } catch (e) {
    cancelError.value = (e as Error).message
    cancelling.value = false
  }
}

// Resend what did not arrive, once sending has finished.
const confirming = ref(false)
const resending = ref(false)
const resendError = ref('')
// Hidden as soon as a resend is accepted, without waiting for the next progress event.
const resentFor = ref<string | null>(null)
const canResend = computed(() => props.progress?.status === 'completed' && (props.progress?.resendable ?? 0) > 0 &&
  resentFor.value !== props.progress?.completed_at)
async function resend() {
  if (resending.value) return
  resending.value = true
  resendError.value = ''
  try {
    await api.resend(props.campaign.id)
    resentFor.value = props.progress?.completed_at ?? null
    confirming.value = false
    emit('resent')
  } catch (e) {
    resendError.value = (e as Error).message
  } finally {
    resending.value = false
  }
}

const channels: { key: Channel; label: string }[] = [{ key: 'sms', label: 'SMS' }, { key: 'email', label: 'Email' }]
const legend = [
  { label: 'Queued', c: 'bg-st-queued' }, { label: 'Sending / retrying', c: 'bg-st-active' },
  { label: 'Sent', c: 'bg-st-sent' }, { label: 'Delivered', c: 'bg-st-delivered' }, { label: 'Undelivered / failed', c: 'bg-st-failed' },
]
// Bar segments in pipeline order, with their colours.
const segments = [
  { keys: ['delivered'], c: 'bg-st-delivered' }, { keys: ['sent'], c: 'bg-st-sent' },
  { keys: ['sending', 'retrying'], c: 'bg-st-active' }, { keys: ['undelivered', 'failed'], c: 'bg-st-failed' },
]

const sum = (ch: Channel, keys: string[]) =>
  keys.reduce((n, k) => n + (props.progress?.counts[ch][k as keyof Progress['counts'][Channel]] ?? 0), 0)
const total = (ch: Channel) => props.progress?.grid[ch].length ?? 0
const all = (keys: string[]) => sum('sms', keys) + sum('email', keys)
const done = computed(() => all(['delivered', 'undelivered', 'failed']))
const grand = computed(() => total('sms') + total('email'))
const throughput = computed(() => (props.progress ? props.progress.throughput.sms + props.progress.throughput.email : 0))

// Failures: refreshed at most every 3 s while progress arrives.
const failures = ref<DeliveryRow[]>([])
let lastFetch = 0
watch(() => props.progress, async () => {
  if (Date.now() - lastFetch < 3000 && props.progress?.status !== 'completed') return
  lastFetch = Date.now()
  const [failed, undelivered] = await Promise.all([
    api.deliveries(props.campaign.id, 'failed', 20), api.deliveries(props.campaign.id, 'undelivered', 20),
  ])
  failures.value = [...failed, ...undelivered].slice(0, 20)
}, { immediate: true })
</script>

<template>
  <div class="space-y-6">
    <div v-if="campaign.status === 'scheduled'" class="flex flex-wrap items-center justify-between gap-3 rounded-xl border border-border bg-card px-5 py-4">
      <p class="flex items-center gap-2 text-sm">
        <CalendarClock class="size-4 text-st-sent" />
        Scheduled for <span class="font-medium">{{ scheduledLabel }}</span>
        <span class="text-muted-foreground">({{ countdown }})</span>
      </p>
      <Button variant="outline" :loading="cancelling" @click="cancelSchedule">Cancel schedule</Button>
      <p v-if="cancelError" class="w-full text-sm text-destructive">{{ cancelError }}</p>
    </div>

    <div class="grid grid-cols-2 gap-3 md:grid-cols-5">
      <StatTile label="Done" :value="`${done.toLocaleString()} / ${grand.toLocaleString()}`" />
      <StatTile label="Delivered" :value="all(['delivered'])" swatch="bg-st-delivered" />
      <StatTile label="Undelivered or failed" :value="all(['undelivered', 'failed'])" swatch="bg-st-failed" />
      <StatTile label="Throughput" :value="`${throughput.toFixed(0)}/s`" hint="last 5 s, both channels" />
      <StatTile label="Retries" :value="progress?.retries ?? 0" :hint="`${progress?.pending_receipts ?? 0} receipts pending`" />
    </div>

    <Card v-for="ch in channels" :key="ch.key" :title="`${ch.label} · ${total(ch.key).toLocaleString()} messages`">
      <template #actions>
        <span class="font-mono text-xs text-muted-foreground tabular">{{ (progress?.throughput[ch.key] ?? 0).toFixed(0) }}/s</span>
      </template>
      <div class="mb-4 flex h-2 overflow-hidden rounded-full bg-st-queued">
        <div v-for="s in segments" :key="s.c" :class="s.c" class="transition-[width] duration-500"
          :style="{ width: total(ch.key) ? `${(sum(ch.key, s.keys) / total(ch.key)) * 100}%` : '0%' }" />
      </div>
      <PixelGrid v-if="progress" :grid="progress.grid[ch.key]" />
      <p v-else class="text-sm text-muted-foreground">Waiting for the first update…</p>
    </Card>

    <div class="flex flex-wrap gap-4 text-xs text-muted-foreground">
      <span v-for="l in legend" :key="l.label" class="flex items-center gap-1.5"><span class="size-2.5" :class="l.c" />{{ l.label }}</span>
    </div>

    <div v-if="canResend" class="flex flex-wrap items-center justify-between gap-3 rounded-xl border border-border bg-card px-5 py-4">
      <p class="text-sm">
        <span class="font-mono tabular">{{ progress!.resendable }}</span> message{{ progress!.resendable === 1 ? '' : 's' }} did not arrive.
        <span class="text-muted-foreground">Invalid numbers and emails are skipped.</span>
      </p>
      <div class="flex gap-2">
        <Button v-if="confirming" variant="ghost" @click="confirming = false">Cancel</Button>
        <Button v-if="confirming" :loading="resending" @click="resend"><RotateCcw class="size-4" /> Yes, resend {{ progress!.resendable }}</Button>
        <Button v-else variant="outline" @click="confirming = true"><RotateCcw class="size-4" /> Resend</Button>
      </div>
      <p v-if="resendError" class="w-full text-sm text-destructive">{{ resendError }}</p>
    </div>

    <Card v-if="failures.length" title="Not delivered">
      <table class="w-full text-sm">
        <thead class="text-left text-xs text-muted-foreground">
          <tr><th class="pb-2 font-medium">Recipient</th><th class="pb-2 font-medium">Channel</th><th class="pb-2 font-medium">Status</th><th class="pb-2 font-medium">Attempts</th><th class="pb-2 font-medium">Reason</th></tr>
        </thead>
        <tbody class="divide-y divide-border">
          <tr v-for="f in failures" :key="f.id">
            <td class="py-1.5">{{ f.first_name }} <span class="text-muted-foreground">({{ f.city }})</span></td>
            <td class="py-1.5 uppercase">{{ f.channel }}</td>
            <td class="py-1.5">{{ f.status }}</td>
            <td class="py-1.5 font-mono tabular">{{ f.attempts }}</td>
            <td class="py-1.5 font-mono text-xs text-muted-foreground">
              {{ f.last_error || 'receipt: undelivered' }}
              <span v-if="f.permanent" class="ml-1 rounded bg-secondary px-1.5 py-0.5 font-sans">won't resend</span>
            </td>
          </tr>
        </tbody>
      </table>
    </Card>
  </div>
</template>
