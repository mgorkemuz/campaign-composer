import { describe, expect, it } from 'vitest'
import { render, smsSegments, unknownVariables, worstCaseLength } from './sms'
import { describeLeaf, removeCondition, setLeafValue } from './rules'
import type { Field, Rule } from './types'

const longest = { first_name: 11, last_name: 9, city: 9 }

describe('sms helpers (mirror internal/message)', () => {
  it('measures the worst case after personalization', () => {
    expect(worstCaseLength('Hi {{first_name}}!', longest)).toBe(4 + 11)
    expect(worstCaseLength('Şehir: {{ city }}', longest)).toBe(7 + 9) // characters, not bytes
  })
  it('counts SMS parts like the server', () => {
    expect(smsSegments('a'.repeat(160))).toEqual({ encoding: 'GSM-7', segments: 1 })
    expect(smsSegments('a'.repeat(161))).toEqual({ encoding: 'GSM-7', segments: 2 })
    expect(smsSegments('€'.repeat(81))).toEqual({ encoding: 'GSM-7', segments: 2 })
    expect(smsSegments('ş'.repeat(70))).toEqual({ encoding: 'UCS-2', segments: 1 })
    expect(smsSegments('ş'.repeat(71))).toEqual({ encoding: 'UCS-2', segments: 2 })
    expect(smsSegments('Çok güzel üzüm')).toEqual({ encoding: 'GSM-7', segments: 1 })
  })
  it('renders and flags unknown variables', () => {
    expect(render('Merhaba {{first_name}}, {{city}}', { first_name: 'Ayşe', last_name: 'Y', city: 'İzmir' })).toBe(
      'Merhaba Ayşe, İzmir',
    )
    expect(unknownVariables('Hi {{nickname}} {{first_name}}')).toEqual(['nickname'])
  })
})

const fields: Field[] = [
  { name: 'city', label: 'City', kind: 'text', ops: ['eq'], values: ['İstanbul', 'Ankara'] },
  { name: 'last_cart_at', label: 'Last added to cart', kind: 'date', ops: ['within_days'] },
]

const rule: Rule = {
  match: 'all',
  conditions: [
    { field: 'city', op: 'eq', value: 'İstanbul' },
    { match: 'any', conditions: [{ field: 'last_cart_at', op: 'within_days', value: 30 }, { field: 'city', op: 'eq', value: 'Ankara' }] },
  ],
}

describe('rule helpers', () => {
  it('describes conditions for chips', () => {
    expect(describeLeaf({ field: 'last_cart_at', op: 'not_within_days', value: 30 }, fields)).toEqual({
      field: 'Last added to cart', op: 'not in the last', value: '30 days',
    })
    expect(describeLeaf({ field: 'last_purchase_at', op: 'never', value: null }, fields).value).toBe('')
  })
  it('removes a condition, dropping groups that become empty', () => {
    expect(removeCondition(rule, 0).conditions).toHaveLength(1)
    const one = removeCondition(rule, 1, 0)
    expect((one.conditions[1] as { conditions: unknown[] }).conditions).toHaveLength(1)
    expect(removeCondition(removeCondition(rule, 1, 0), 1, 0).conditions).toHaveLength(1)
    expect(rule.conditions).toHaveLength(2) // the original is not changed
  })
  it('sets a leaf value inside a group', () => {
    const r = setLeafValue(rule, 1, 0, 14)
    expect((r.conditions[1] as { conditions: { value: unknown }[] }).conditions[0].value).toBe(14)
  })
})
