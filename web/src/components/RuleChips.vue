<script setup lang="ts">
import { ref } from 'vue'
import { X } from 'lucide-vue-next'
import type { Field, Leaf, Rule } from '@/lib/types'
import { describeLeaf, isGroup, removeCondition, setLeafValue } from '@/lib/rules'

const props = defineProps<{ rule: Rule; fields: Field[]; editable: boolean }>()
const emit = defineEmits<{ change: [rule: Rule] }>()

// The chip whose value is being edited, as "top" or "top.inner".
const editing = ref<string | null>(null)
const draft = ref<string>('')

const key = (top: number, inner?: number) => (inner === undefined ? `${top}` : `${top}.${inner}`)
const fieldOf = (c: Leaf) => props.fields.find((f) => f.name === c.field)

function startEdit(c: Leaf, top: number, inner?: number) {
  if (!props.editable || c.op === 'ever' || c.op === 'never') return
  editing.value = key(top, inner)
  draft.value = Array.isArray(c.value) ? (c.value as unknown[]).join(', ') : String(c.value)
}

function commit(c: Leaf, top: number, inner?: number) {
  const f = fieldOf(c)
  let value: unknown = draft.value.trim()
  if (c.op === 'in' || c.op === 'not_in') value = draft.value.split(',').map((s) => s.trim()).filter(Boolean)
  else if (c.op === 'between') value = draft.value.split(/[,–-]/).map((s) => Number(s.trim()))
  else if (f?.kind !== 'text') value = Number(draft.value)
  editing.value = null
  emit('change', setLeafValue(props.rule, top, inner, value))
}
</script>

<template>
  <div class="space-y-2">
    <p class="text-xs font-medium uppercase tracking-wide text-muted-foreground">
      {{ rule.match === 'all' ? 'Everyone matching all of these' : 'Everyone matching any of these' }}
    </p>
    <p v-if="!rule.conditions.length" class="text-sm text-muted-foreground">No conditions: every user.</p>
    <div class="flex flex-wrap gap-2">
      <template v-for="(c, top) in rule.conditions" :key="top">
        <div v-if="isGroup(c)" class="flex flex-wrap items-center gap-1.5 rounded-lg border border-dashed border-border p-1.5">
          <span class="px-1 text-[11px] font-medium uppercase text-muted-foreground">{{ c.match }} of</span>
          <template v-for="(leaf, inner) in c.conditions" :key="inner">
            <span class="chip">
              <span class="text-muted-foreground">{{ describeLeaf(leaf, fields).field }}</span>
              <span>{{ describeLeaf(leaf, fields).op }}</span>
              <input v-if="editing === key(top, inner)" v-model="draft" class="chip-input" autofocus
                @keydown.enter.prevent="commit(leaf, top, inner)" @keydown.esc="editing = null" @blur="commit(leaf, top, inner)" />
              <button v-else type="button" class="font-medium" :class="{ 'underline decoration-dotted': editable }"
                @click="startEdit(leaf, top, inner)">{{ describeLeaf(leaf, fields).value }}</button>
              <button v-if="editable" type="button" aria-label="Remove condition" class="text-muted-foreground hover:text-foreground"
                @click="emit('change', removeCondition(rule, top, inner))"><X class="size-3.5" /></button>
            </span>
          </template>
        </div>
        <span v-else class="chip">
          <span class="text-muted-foreground">{{ describeLeaf(c, fields).field }}</span>
          <span>{{ describeLeaf(c, fields).op }}</span>
          <template v-if="editing === key(top)">
            <select v-if="fieldOf(c)?.values && (c.op === 'eq' || c.op === 'neq')" v-model="draft" class="chip-input"
              @change="commit(c, top)" @blur="editing = null">
              <option v-for="v in fieldOf(c)!.values" :key="v" :value="v">{{ v }}</option>
            </select>
            <input v-else v-model="draft" class="chip-input" autofocus
              @keydown.enter.prevent="commit(c, top)" @keydown.esc="editing = null" @blur="commit(c, top)" />
          </template>
          <button v-else-if="describeLeaf(c, fields).value" type="button" class="font-medium"
            :class="{ 'underline decoration-dotted': editable }" @click="startEdit(c, top)">
            {{ describeLeaf(c, fields).value }}
          </button>
          <button v-if="editable" type="button" aria-label="Remove condition" class="text-muted-foreground hover:text-foreground"
            @click="emit('change', removeCondition(rule, top))"><X class="size-3.5" /></button>
        </span>
      </template>
    </div>
  </div>
</template>

<style scoped>
@reference "../style.css";
.chip {
  @apply inline-flex items-center gap-1.5 rounded-md border border-border bg-secondary px-2.5 py-1 text-sm;
}
.chip-input {
  @apply h-6 w-28 rounded border border-input bg-background px-1.5 text-sm outline-none focus:ring-2 focus:ring-ring;
}
</style>
