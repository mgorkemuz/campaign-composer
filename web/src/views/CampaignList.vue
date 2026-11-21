<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { api } from '@/lib/api'
import type { Campaign } from '@/lib/types'
import Hero from '@/components/Hero.vue'
import StatusBadge from '@/components/StatusBadge.vue'

const campaigns = ref<Campaign[] | null>(null)
const error = ref('')

onMounted(async () => {
  try {
    campaigns.value = await api.listCampaigns()
  } catch (e) {
    error.value = (e as Error).message
  }
})

const fmt = (iso: string) => new Date(iso).toLocaleString([], { dateStyle: 'medium', timeStyle: 'short' })
</script>

<template>
  <div class="-mt-8 space-y-10">
    <Hero />

    <section class="space-y-4">
      <h2 class="text-xs font-medium uppercase tracking-wide text-muted-foreground">Your campaigns</h2>
      <p v-if="error" class="text-sm text-destructive">{{ error }}</p>
      <p v-else-if="campaigns === null" class="text-sm text-muted-foreground">Loading…</p>
      <p v-else-if="campaigns.length === 0" class="text-sm text-muted-foreground">None yet. Start one above.</p>
      <ul v-else class="divide-y divide-border overflow-hidden rounded-xl border border-border bg-card">
        <li v-for="c in campaigns" :key="c.id">
          <RouterLink :to="`/campaigns/${c.id}`" class="flex items-center gap-4 px-5 py-4 hover:bg-accent/60">
            <div class="min-w-0 flex-1">
              <p class="truncate font-medium">{{ c.brief }}</p>
              <p class="mt-0.5 text-xs text-muted-foreground">
                {{ fmt(c.created_at) }}<template v-if="c.audience_total !== null"> · {{ c.audience_total.toLocaleString() }} people</template>
              </p>
            </div>
            <StatusBadge :status="c.status" />
          </RouterLink>
        </li>
      </ul>
    </section>
  </div>
</template>
