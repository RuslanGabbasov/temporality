export type Json = null | boolean | number | string | Json[] | { [key: string]: Json }

export interface FrpEvent {
  id?: string
  event_id?: string
  type?: string
  event_type?: string
  timestamp?: string
  created_at?: string
  execution_id?: string
  payload?: Record<string, Json>
  [key: string]: unknown
}

export interface Frame {
  id?: string
  frame_id?: string
  objective_id?: string
  episode_id?: string
  focus?: Json
  map?: Json
  periphery?: Json
  procedures?: Json
  recent?: Json
  sections?: Partial<Record<FrameSection, Json>>
  outside_frame?: Json
  provenance?: Json
  token_usage?: Json
  usage?: Json
  [key: string]: unknown
}

export type FrameSection = 'focus' | 'map' | 'periphery' | 'procedures' | 'recent'

export interface Execution {
  id?: string
  execution_id?: string
  status?: string
  [key: string]: unknown
}

export interface RenderResponse {
  rendered?: string
  output?: string
  content?: string
  [key: string]: unknown
}

export interface Branch {
  branch_id: string
  label: string
  response: unknown
}
