// Mirrors internal/message for instant feedback while typing. The server's
// check is the one that counts; this only keeps the counter live.

export const SMS_MAX = 160
export const SUBJECT_MAX = 80
export const VARIABLES = ['first_name', 'last_name', 'city'] as const

export type Longest = Record<(typeof VARIABLES)[number], number>

const varRe = /\{\{\s*([^{}]*?)\s*\}\}/g

const gsmBasic =
  '@£$¥èéùìòÇ\nØø\rÅåΔ_ΦΓΛΩΠΨΣΘΞÆæßÉ !"#¤%&\'()*+,-./0123456789:;<=>?' +
  '¡ABCDEFGHIJKLMNOPQRSTUVWXYZÄÖÑÜ§¿abcdefghijklmnopqrstuvwxyzäöñüà'
const gsmExtension = '^{}\\[~]|€\f'

/** Characters once personalized for the recipient with the longest values. */
export function worstCaseLength(text: string, longest: Longest): number {
  let n = [...text.replace(varRe, '')].length
  for (const m of text.matchAll(varRe)) n += longest[m[1] as keyof Longest] ?? 0
  return n
}

export function unknownVariables(text: string): string[] {
  return [...text.matchAll(varRe)].map((m) => m[1]).filter((v) => !(VARIABLES as readonly string[]).includes(v))
}

/** The encoding an SMS needs and how many parts it is sent as. */
export function smsSegments(text: string): { encoding: 'GSM-7' | 'UCS-2'; segments: number } {
  let units = 0
  for (const ch of text) {
    if (gsmBasic.includes(ch)) units++
    else if (gsmExtension.includes(ch)) units += 2
    else {
      let utf16 = 0
      for (const c of text) utf16 += c.length // code units: 2 for characters beyond the BMP
      return { encoding: 'UCS-2', segments: parts(utf16, 70, 67) }
    }
  }
  return { encoding: 'GSM-7', segments: parts(units, 160, 153) }
}

function parts(units: number, single: number, multi: number): number {
  if (units === 0) return 0
  return units <= single ? 1 : Math.ceil(units / multi)
}

export interface Recipient {
  first_name: string
  last_name: string
  city: string
}

export function render(text: string, r: Recipient): string {
  return text.replace(varRe, (whole, name: string) =>
    (VARIABLES as readonly string[]).includes(name) ? r[name as keyof Recipient] : whole,
  )
}
