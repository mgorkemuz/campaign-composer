import type { AudienceResult, Campaign, DeliveryRow, Field, Preview, Rule } from './types'

/** An error answer from the API, with its code and any listed problems. */
export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
    public problems: string[] = [],
  ) {
    super(message)
  }
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    headers: body === undefined ? {} : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  const data = await res.json().catch(() => ({}))
  if (!res.ok) {
    const e = data?.error ?? {}
    throw new ApiError(res.status, e.code ?? 'error', e.message ?? res.statusText, e.problems ?? [])
  }
  return data as T
}

export const api = {
  fields: () => request<{ fields: Field[] }>('GET', '/api/segments/fields').then((r) => r.fields),
  preview: (rule: Rule) => request<{ rule: Rule; preview: Preview }>('POST', '/api/segments/preview', { rule }),

  listCampaigns: () => request<{ campaigns: Campaign[] }>('GET', '/api/campaigns').then((r) => r.campaigns),
  getCampaign: (id: string) => request<Campaign>('GET', `/api/campaigns/${id}`),
  createCampaign: (brief: string) => request<Campaign>('POST', '/api/campaigns', { brief }),

  generateAudience: (id: string) => request<AudienceResult>('POST', `/api/campaigns/${id}/audience`),
  refineAudience: (id: string, instruction: string) =>
    request<AudienceResult>('POST', `/api/campaigns/${id}/audience/refine`, { instruction }),
  setRule: (id: string, rule: Rule) => request<AudienceResult>('PUT', `/api/campaigns/${id}/audience`, { rule }),

  generateContent: (id: string, language: string, instruction = '') =>
    request<Campaign>('POST', `/api/campaigns/${id}/content`, { language, instruction }),
  setContent: (id: string, c: { language: string; sms: string; email_subject: string; email_body: string }) =>
    request<Campaign>('PUT', `/api/campaigns/${id}/content`, c),

  approve: (id: string, sendAt?: Date) =>
    request<{ campaign: Campaign; sms: number; email: number }>('POST', `/api/campaigns/${id}/approve`,
      sendAt ? { send_at: sendAt.toISOString() } : undefined),
  unschedule: (id: string) => request<Campaign>('POST', `/api/campaigns/${id}/unschedule`),
  recentGrid: () => request<{ grid: string }>('GET', '/api/deliveries/recent?limit=2000'),
  resend: (id: string) => request<{ resent: number }>('POST', `/api/campaigns/${id}/resend`),
  deliveries: (id: string, status = '', limit = 50) =>
    request<{ deliveries: DeliveryRow[] }>('GET', `/api/campaigns/${id}/deliveries?status=${status}&limit=${limit}`)
      .then((r) => r.deliveries),
}
