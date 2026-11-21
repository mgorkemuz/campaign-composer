<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { ArrowLeft } from 'lucide-vue-next'
import { api, ApiError } from '@/lib/api'
import { openCampaignStream } from '@/lib/stream'
import type { ApiErrorBody, AudienceResult, Campaign, Field, Preview, Progress } from '@/lib/types'
import PreparingStep from '@/components/PreparingStep.vue'
import ReviewStep from '@/components/ReviewStep.vue'
import Dashboard from '@/components/Dashboard.vue'
import StatusBadge from '@/components/StatusBadge.vue'

// One campaign, three phases: preparing (audience + messages, automatic),
// review & send (the only decision), and live.
const props = defineProps<{ id: string }>()

const campaign = ref<Campaign | null>(null)
const fields = ref<Field[]>([])
const preview = ref<Preview | null>(null)
const progress = ref<Progress | null>(null)
const streamingSMS = ref('')
const error = ref<ApiErrorBody | null>(null)
const loadError = ref('')
let attempt = 0
let preparing = false
let close: (() => void) | undefined

const phase = computed(() => {
  const c = campaign.value
  if (!c) return 'loading'
  if (c.approved_at) return 'live'
  const hasMessages = c.sms !== ''
  if (!c.rule || c.content_state === 'generating' || (!hasMessages && c.content_state !== 'invalid')) return 'preparing'
  return 'review'
})

async function reload() {
  campaign.value = await api.getCampaign(props.id)
}

function toError(e: unknown): ApiErrorBody {
  return e instanceof ApiError ? { code: e.code, message: e.message, problems: e.problems }
    : { code: 'error', message: 'Something went wrong.' }
}

// Moves the campaign forward without asking: audience first, then messages.
async function prepare() {
  if (preparing || !campaign.value) return
  preparing = true
  error.value = null
  try {
    if (!campaign.value.rule) {
      const r = await api.generateAudience(props.id)
      campaign.value = r.campaign
      preview.value = r.preview
    }
    const c = campaign.value
    if (c.sms === '' && c.content_state !== 'generating') {
      if (c.content_state === 'failed' && c.content_error) {
        error.value = c.content_error // show the failure; "Try again" starts over
      } else {
        campaign.value = await api.generateContent(props.id, '')
      }
    }
  } catch (e) {
    error.value = toError(e)
  } finally {
    preparing = false
  }
}

async function retry() {
  error.value = null
  if (!campaign.value?.rule) return prepare()
  try {
    campaign.value = await api.generateContent(props.id, '')
  } catch (e) {
    error.value = toError(e)
  }
}

async function rewrite(language: string) {
  streamingSMS.value = ''
  try {
    campaign.value = await api.generateContent(props.id, language)
  } catch (e) {
    error.value = toError(e)
  }
}

function onAudience(r: AudienceResult) {
  campaign.value = r.campaign
  preview.value = r.preview
}

onMounted(async () => {
  try {
    ;[campaign.value, fields.value] = await Promise.all([api.getCampaign(props.id), api.fields()])
  } catch (e) {
    loadError.value = (e as Error).message
    return
  }
  if (campaign.value.rule) api.preview(campaign.value.rule).then((r) => (preview.value = r.preview))
  connect()
  if (phase.value === 'preparing') prepare()
})

// The stream closes itself once a campaign has settled; a resend reopens it.
function connect() {
  close?.()
  close = openCampaignStream(props.id, {
    onEvent: async (type, data) => {
      if (type === 'content_started') {
        attempt = 0
        streamingSMS.value = ''
      }
      if (type === 'content_failed') error.value = data as ApiErrorBody
      if (type !== 'created') await reload()
      if (type === 'audience_ready' && campaign.value?.rule) {
        api.preview(campaign.value.rule).then((r) => (preview.value = r.preview))
      }
    },
    onDelta: (d) => {
      if (d.attempt !== attempt) {
        attempt = d.attempt
        streamingSMS.value = ''
      }
      if (d.field === 'sms') streamingSMS.value += d.text
    },
    onProgress: (p) => (progress.value = p),
    onEnd: () => reload(),
  })
}

async function onResent() {
  await reload()
  connect()
}
onBeforeUnmount(() => close?.())
</script>

<template>
  <RouterLink to="/" class="mb-6 inline-flex items-center gap-1.5 text-sm text-muted-foreground hover:text-foreground">
    <ArrowLeft class="size-4" /> All campaigns
  </RouterLink>
  <p v-if="loadError" class="text-sm text-destructive">{{ loadError }}</p>
  <p v-else-if="phase === 'loading'" class="text-sm text-muted-foreground">Loading…</p>

  <PreparingStep v-else-if="phase === 'preparing'" :campaign="campaign!" :streaming-s-m-s="streamingSMS" :error="error" @retry="retry" />

  <ReviewStep v-else-if="phase === 'review'" :campaign="campaign!" :fields="fields" :preview="preview"
    @campaign="(c) => (campaign = c)" @audience="onAudience" @rewrite="rewrite" @sent="(c) => (campaign = c)" />

  <div v-else class="space-y-6">
    <div class="space-y-2">
      <StatusBadge :status="campaign!.status" />
      <h1 class="text-2xl font-semibold leading-snug tracking-tight sm:text-3xl">{{ campaign!.brief }}</h1>
    </div>
    <Dashboard :campaign="campaign!" :progress="progress" @resent="onResent" />
  </div>
</template>
