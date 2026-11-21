<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { ArrowRight } from 'lucide-vue-next'
import { api, ApiError } from '@/lib/api'
import Button from '@/components/ui/Button.vue'
import Stepper from '@/components/Stepper.vue'

const router = useRouter()
const brief = ref('')
const busy = ref(false)
const error = ref('')
const example = "Remind Istanbul users who added items to their cart but didn't buy in the last 30 days about a 30% discount, in a friendly, short tone."

async function next() {
  busy.value = true
  error.value = ''
  try {
    const c = await api.createCampaign(brief.value)
    router.push(`/campaigns/${c.id}`) // audience and messages are prepared there, automatically
  } catch (e) {
    error.value = e instanceof ApiError ? e.message : 'Could not create the campaign.'
    busy.value = false
  }
}
</script>

<template>
  <div class="mx-auto max-w-2xl space-y-10 pt-6">
    <Stepper :current="0" />
    <form class="space-y-5" @submit.prevent="next">
      <h1 class="text-4xl font-semibold tracking-tight sm:text-5xl">Who should hear from you, and why?</h1>
      <textarea v-model="brief" rows="4" maxlength="2000" required autofocus
        placeholder="Describe the audience and the message in your own words…"
        class="w-full resize-none rounded-xl border border-input bg-card p-4 text-lg outline-none focus:ring-2 focus:ring-ring"
        @keydown.meta.enter="next" />
      <div class="flex items-center justify-between">
        <button type="button" class="text-sm text-muted-foreground underline-offset-4 hover:underline" @click="brief = example">
          Try an example
        </button>
        <Button type="submit" :loading="busy" :disabled="!brief.trim()">Continue <ArrowRight class="size-4" /></Button>
      </div>
      <p v-if="error" class="text-sm text-destructive">{{ error }}</p>
    </form>
  </div>
</template>
