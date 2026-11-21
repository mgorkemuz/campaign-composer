<script setup lang="ts">
import { computed, ref } from 'vue'
import { api, ApiError } from '@/lib/api'
import type { Campaign } from '@/lib/types'
import { render, smsSegments, SMS_MAX, SUBJECT_MAX, unknownVariables, worstCaseLength } from '@/lib/sms'
import Button from './ui/Button.vue'
import Problems from './ui/Problems.vue'

const props = defineProps<{ campaign: Campaign }>()
const emit = defineEmits<{ saved: [c: Campaign]; cancel: [] }>()

const draft = ref({ sms: props.campaign.sms, email_subject: props.campaign.email_subject, email_body: props.campaign.email_body })
const busy = ref(false)
const error = ref<ApiError | null>(null)

const longest = computed(() => props.campaign.audience_longest ?? { first_name: 0, last_name: 0, city: 0 })
const smsLen = computed(() => worstCaseLength(draft.value.sms, longest.value))
const subjectLen = computed(() => worstCaseLength(draft.value.email_subject, longest.value))
const parts = computed(() => smsSegments(render(draft.value.sms, {
  first_name: 'x'.repeat(longest.value.first_name), last_name: 'x'.repeat(longest.value.last_name), city: 'x'.repeat(longest.value.city),
})))
const unknown = computed(() => [...new Set(Object.values(draft.value).flatMap(unknownVariables))].map((u) => '{{' + u + '}}').join(', '))

async function save() {
  busy.value = true
  error.value = null
  try {
    emit('saved', await api.setContent(props.campaign.id, { language: props.campaign.language ?? 'tr', ...draft.value }))
  } catch (e) {
    error.value = e instanceof ApiError ? e : new ApiError(0, 'error', 'Could not save.')
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="space-y-4">
    <label class="block space-y-1.5">
      <span class="flex justify-between text-xs font-medium">SMS
        <span class="font-mono tabular" :class="smsLen > SMS_MAX ? 'text-destructive' : 'text-muted-foreground'">
          {{ smsLen }}/{{ SMS_MAX }} · {{ parts.segments }} part{{ parts.segments === 1 ? '' : 's' }}
        </span>
      </span>
      <textarea v-model="draft.sms" rows="3" class="field" />
      <span v-if="parts.encoding === 'UCS-2' && parts.segments > 1" class="block text-xs text-muted-foreground">
        Turkish letters fit 70 per part, so this goes out as {{ parts.segments }} parts.
      </span>
    </label>
    <label class="block space-y-1.5">
      <span class="flex justify-between text-xs font-medium">Subject
        <span class="font-mono tabular" :class="subjectLen > SUBJECT_MAX ? 'text-destructive' : 'text-muted-foreground'">{{ subjectLen }}/{{ SUBJECT_MAX }}</span>
      </span>
      <input v-model="draft.email_subject" class="field h-9" />
    </label>
    <label class="block space-y-1.5">
      <span class="text-xs font-medium">Email</span>
      <textarea v-model="draft.email_body" rows="8" class="field" />
    </label>
    <p class="text-xs text-muted-foreground">
      Use <code v-pre>{{first_name}}</code>, <code v-pre>{{last_name}}</code> or <code v-pre>{{city}}</code>.
      <span v-if="unknown" class="text-destructive"> Unknown: {{ unknown }}</span>
    </p>
    <Problems v-if="error" :message="error.message" :problems="error.problems" />
    <div class="flex gap-2">
      <Button size="sm" :loading="busy" @click="save">Save</Button>
      <Button size="sm" variant="ghost" @click="$emit('cancel')">Cancel</Button>
    </div>
  </div>
</template>

<style scoped>
@reference "../style.css";
.field {
  @apply w-full rounded-lg border border-input bg-background p-3 text-sm outline-none focus:ring-2 focus:ring-ring;
}
</style>
