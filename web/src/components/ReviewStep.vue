<script setup lang="ts">
import { computed, ref } from 'vue'
import { Pencil, RefreshCw, Send } from 'lucide-vue-next'
import { api, ApiError } from '@/lib/api'
import type { AudienceResult, Campaign, Field, Preview, Rule } from '@/lib/types'
import Button from './ui/Button.vue'
import Problems from './ui/Problems.vue'
import Stepper from './Stepper.vue'
import RuleChips from './RuleChips.vue'
import MessagePreview from './MessagePreview.vue'
import MessageEditor from './MessageEditor.vue'

// One page to check everything and send. Editing is optional and tucked away.
const props = defineProps<{ campaign: Campaign; fields: Field[]; preview: Preview | null }>()
const emit = defineEmits<{ campaign: [c: Campaign]; audience: [r: AudienceResult]; rewrite: [language: string]; sent: [c: Campaign] }>()

const editAudience = ref(false)
const editMessages = ref(false)
const instruction = ref('')
const busy = ref<'' | 'audience' | 'send'>('')
const error = ref<ApiError | null>(null)
const sendError = ref<ApiError | null>(null)
const confirming = ref(false)
const language = ref(props.campaign.language ?? '')
const recipient = ref(0)

const ready = computed(() => props.campaign.status === 'content_ready' && props.campaign.content_state === 'ready')
const total = computed(() => (props.campaign.sms_reachable ?? 0) + (props.campaign.email_reachable ?? 0))
const needsRewrite = computed(() => props.campaign.content_state === 'invalid' || props.campaign.content_state === 'failed')

async function audience(call: () => Promise<AudienceResult>) {
  busy.value = 'audience'
  error.value = null
  try {
    emit('audience', await call())
    instruction.value = ''
  } catch (e) {
    error.value = e instanceof ApiError ? e : new ApiError(0, 'error', 'Something went wrong.')
  } finally {
    busy.value = ''
  }
}
const refine = () => audience(() => api.refineAudience(props.campaign.id, instruction.value))
const setRule = (rule: Rule) => audience(() => api.setRule(props.campaign.id, rule))

async function send() {
  if (busy.value === 'send') return
  busy.value = 'send'
  sendError.value = null
  try {
    const res = await api.approve(props.campaign.id)
    // Hand the approved campaign straight to the page so it switches to the live
    // view at once. The button stays busy until then: it must never look idle
    // (or be clickable) after a send went through.
    emit('sent', res.campaign)
  } catch (e) {
    sendError.value = e instanceof ApiError ? e : new ApiError(0, 'error', 'Could not send.')
    confirming.value = false
    busy.value = ''
  }
}
</script>

<template>
  <div class="mx-auto max-w-3xl space-y-8 pb-28 pt-6">
    <Stepper :current="3" />
    <h1 class="text-4xl font-semibold tracking-tight sm:text-5xl">Ready to send</h1>

    <!-- Audience -->
    <section class="space-y-3">
      <div class="flex items-center justify-between">
        <h2 class="text-xs font-medium uppercase tracking-wide text-muted-foreground">Audience</h2>
        <Button variant="ghost" size="sm" @click="editAudience = !editAudience"><Pencil class="size-3.5" /> {{ editAudience ? 'Done' : 'Edit' }}</Button>
      </div>
      <p class="text-lg">{{ campaign.audience_summary || 'Your audience' }}</p>
      <p class="font-mono text-sm text-muted-foreground tabular">
        {{ (campaign.audience_total ?? 0).toLocaleString() }} people ·
        {{ (campaign.sms_reachable ?? 0).toLocaleString() }} by SMS ·
        {{ (campaign.email_reachable ?? 0).toLocaleString() }} by email
      </p>
      <div v-if="editAudience" class="space-y-3 rounded-xl border border-border bg-card p-4">
        <RuleChips v-if="campaign.rule" :rule="campaign.rule" :fields="fields" :editable="busy === ''" @change="setRule" />
        <form class="flex gap-2" @submit.prevent="refine">
          <input v-model="instruction" maxlength="500" placeholder="Or say what to change: only English speakers, 14 days…"
            class="h-9 flex-1 rounded-lg border border-input bg-background px-3 text-sm outline-none focus:ring-2 focus:ring-ring" />
          <Button type="submit" variant="outline" :loading="busy === 'audience'" :disabled="!instruction.trim()">Change</Button>
        </form>
        <ul v-if="campaign.audience_assumptions.length" class="list-disc space-y-1 pl-5 text-xs text-muted-foreground">
          <li v-for="a in campaign.audience_assumptions" :key="a">{{ a }}</li>
        </ul>
      </div>
    </section>

    <!-- Messages -->
    <section class="space-y-3">
      <div class="flex items-center justify-between">
        <h2 class="text-xs font-medium uppercase tracking-wide text-muted-foreground">Messages</h2>
        <div class="flex items-center gap-1">
          <select v-if="preview?.sample.length && !editMessages" v-model="recipient" class="h-8 rounded-md bg-transparent px-1 text-xs text-muted-foreground">
            <option v-for="(u, i) in preview.sample" :key="u.id" :value="i">as {{ u.first_name }}</option>
          </select>
          <Button variant="ghost" size="sm" @click="editMessages = !editMessages"><Pencil class="size-3.5" /> {{ editMessages ? 'Close' : 'Edit' }}</Button>
        </div>
      </div>

      <div v-if="needsRewrite" class="space-y-3">
        <Problems :message="campaign.content_error?.message ?? 'The messages need rewriting.'" :problems="campaign.content_error?.problems" />
      </div>

      <MessageEditor v-if="editMessages" :campaign="campaign" @saved="(c) => { emit('campaign', c); editMessages = false }" @cancel="editMessages = false" />
      <MessagePreview v-else :sms="campaign.sms" :subject="campaign.email_subject" :body="campaign.email_body"
        :recipient="preview?.sample[recipient] ?? null" />

      <div class="flex items-center gap-2 text-sm">
        <select v-model="language" class="h-8 rounded-md border border-input bg-background px-2 text-xs" aria-label="Language">
          <option value="">Brief's language</option>
          <option value="tr">Türkçe</option>
          <option value="en">English</option>
        </select>
        <Button variant="outline" size="sm" @click="emit('rewrite', language)"><RefreshCw class="size-3.5" /> Rewrite</Button>
      </div>
    </section>

    <Problems v-if="error" :message="error.message" :problems="error.problems" />

    <!-- The only decision: send. -->
    <div class="fixed inset-x-0 bottom-0 border-t border-border bg-background/95 backdrop-blur">
      <div class="mx-auto flex max-w-3xl items-center justify-between gap-4 px-4 py-4">
        <p v-if="sendError" role="alert" class="text-sm text-destructive">
          {{ sendError.message }}<template v-if="sendError.problems.length">: {{ sendError.problems.join('; ') }}</template>
        </p>
        <p v-else class="text-sm text-muted-foreground">
          <template v-if="ready">Only people who consented to each channel.</template>
          <template v-else>Fix the messages before sending.</template>
        </p>
        <div class="flex gap-2">
          <Button v-if="confirming" variant="ghost" :disabled="busy === 'send'" @click="confirming = false">Cancel</Button>
          <Button v-if="confirming" :loading="busy === 'send'" @click="send"><Send class="size-4" /> Yes, send {{ total.toLocaleString() }}</Button>
          <Button v-else :disabled="!ready" @click="confirming = true"><Send class="size-4" /> Send to {{ total.toLocaleString() }}</Button>
        </div>
      </div>
    </div>
  </div>
</template>
