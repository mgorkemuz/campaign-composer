import type { Condition, Field, Group, Leaf, Rule } from './types'

export function isGroup(c: Condition): c is Group {
  return (c as Group).conditions !== undefined
}

const opText: Record<string, string> = {
  eq: 'is',
  neq: 'is not',
  in: 'is one of',
  not_in: 'is none of',
  gt: '>',
  gte: '≥',
  lt: '<',
  lte: '≤',
  between: 'between',
  within_days: 'in the last',
  not_within_days: 'not in the last',
  ever: 'ever',
  never: 'never',
}

/** A short human label for one condition, e.g. "Last added to cart · in the last 30 days". */
export function describeLeaf(c: Leaf, fields: Field[]): { field: string; op: string; value: string } {
  const label = fields.find((f) => f.name === c.field)?.label ?? c.field
  const op = opText[c.op] ?? c.op
  let value = ''
  switch (c.op) {
    case 'ever':
    case 'never':
      break
    case 'within_days':
    case 'not_within_days':
      value = `${c.value} days`
      break
    case 'between':
      value = (c.value as number[]).join(' – ')
      break
    case 'in':
    case 'not_in':
      value = (c.value as string[]).join(', ')
      break
    default:
      value = String(c.value)
  }
  return { field: label, op, value }
}

/** Returns a copy of the rule without the condition at path (top index, then inner index). */
export function removeCondition(rule: Rule, top: number, inner?: number): Rule {
  const conditions = rule.conditions.flatMap((c, i): Condition[] => {
    if (i !== top) return [c]
    if (inner === undefined || !isGroup(c)) return []
    const rest = c.conditions.filter((_, j) => j !== inner)
    return rest.length ? [{ ...c, conditions: rest }] : []
  })
  return { ...rule, conditions }
}

/** Returns a copy of the rule with one leaf's value replaced. */
export function setLeafValue(rule: Rule, top: number, inner: number | undefined, value: unknown): Rule {
  const conditions = rule.conditions.map((c, i): Condition => {
    if (i !== top) return c
    if (inner === undefined) return { ...(c as Leaf), value }
    const g = c as Group
    return { ...g, conditions: g.conditions.map((l, j) => (j === inner ? { ...l, value } : l)) }
  })
  return { ...rule, conditions }
}
