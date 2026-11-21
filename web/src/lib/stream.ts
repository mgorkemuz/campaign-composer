import type { Progress } from './types'

export interface ContentDelta {
  attempt: number
  field: 'sms' | 'email_subject' | 'email_body'
  text: string
}

export interface StreamHandlers {
  /** A stored event: created, audience_ready, content_started, content_ready, approved, completed, ... */
  onEvent?: (type: string, data: unknown) => void
  /** Message text as the AI writes it. */
  onDelta?: (d: ContentDelta) => void
  /** Delivery progress, at most every 500 ms. */
  onProgress?: (p: Progress) => void
  /** The server said nothing more will change. */
  onEnd?: () => void
}

const storedEvents = [
  'created', 'audience_ready', 'content_started', 'content_ready', 'content_failed', 'approved', 'completed', 'resend',
]

/**
 * Opens a campaign's event stream. EventSource reconnects by itself and sends
 * Last-Event-ID, so the server replays what was missed. Returns a close function.
 */
export function openCampaignStream(id: string, h: StreamHandlers): () => void {
  const es = new EventSource(`/api/campaigns/${id}/events`)
  for (const type of storedEvents) {
    es.addEventListener(type, (e) => {
      const ev = JSON.parse((e as MessageEvent).data)
      h.onEvent?.(type, ev.data)
    })
  }
  es.addEventListener('delta', (e) => {
    const d = JSON.parse((e as MessageEvent).data)
    if (d.field) h.onDelta?.(d as ContentDelta)
  })
  es.addEventListener('progress', (e) => h.onProgress?.(JSON.parse((e as MessageEvent).data)))
  es.addEventListener('end', () => {
    es.close()
    h.onEnd?.()
  })
  return () => es.close()
}
