// Shapes returned by the Go API (see internal/campaign and internal/segment).

export type Kind = 'text' | 'number' | 'date'

export interface Field {
  name: string
  label: string
  kind: Kind
  ops: string[]
  values?: string[]
}

export interface Leaf {
  field: string
  op: string
  value: unknown
}

export interface Group {
  match: 'all' | 'any'
  conditions: Leaf[]
}

export type Condition = Leaf | Group

export interface Rule {
  match: 'all' | 'any'
  conditions: Condition[]
}

export interface SampleUser {
  id: number
  first_name: string
  last_name: string
  city: string
  language: string
}

export interface Preview {
  total: number
  sms_reachable: number
  email_reachable: number
  sample: SampleUser[]
}

export interface ContentCheck {
  sms_length: number
  subject_length: number
  sms_encoding: 'GSM-7' | 'UCS-2'
  sms_segments: number
}

export interface ApiErrorBody {
  code: string
  message: string
  problems?: string[]
}

export type CampaignStatus =
  | 'draft' | 'audience_ready' | 'content_ready' | 'approved' | 'scheduled' | 'sending' | 'completed' | 'failed'

export type ContentState = 'none' | 'generating' | 'ready' | 'failed' | 'invalid'

export interface Campaign {
  id: string
  brief: string
  status: CampaignStatus
  rule: Rule | null
  audience_summary: string
  audience_assumptions: string[]
  audience_total: number | null
  sms_reachable: number | null
  email_reachable: number | null
  audience_longest: { first_name: number; last_name: number; city: number } | null
  llm_model: string
  language: 'tr' | 'en' | null
  sms: string
  email_subject: string
  email_body: string
  content_state: ContentState
  content_error: ApiErrorBody | null
  content_check: ContentCheck | null
  translations: Translation[]
  approved_at: string | null
  scheduled_at: string | null
  completed_at: string | null
  created_at: string
  updated_at: string
}

export interface Translation {
  language: string
  state: 'translating' | 'ready' | 'failed'
  recipients: number
  sms: string
  email_subject: string
  email_body: string
  content_check: ContentCheck | null
  error: ApiErrorBody | null
}

export interface AudienceResult {
  campaign: Campaign
  preview: Preview
}

export type Channel = 'sms' | 'email'
export type DeliveryStatus = 'queued' | 'sending' | 'retrying' | 'sent' | 'delivered' | 'undelivered' | 'failed'

export interface Progress {
  status: CampaignStatus
  counts: Record<Channel, Partial<Record<DeliveryStatus, number>>>
  grid: Record<Channel, string>
  throughput: Record<Channel, number>
  retries: number
  pending_receipts: number
  resendable: number
  completed_at: string | null
}

export interface DeliveryRow {
  id: number
  channel: Channel
  status: DeliveryStatus
  attempts: number
  last_error: string
  first_name: string
  city: string
  permanent: boolean
  round: number
}
