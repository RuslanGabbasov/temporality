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

export interface RenderRequest {
  frame_id: string
  objective_id: string
  budget_tokens: number
}

export interface BlameRequest {
  root_id: string
  max_depth: number
}

export interface ForkBranchRequest {
  branch_id: string
  label: string
  model_config: Record<string, Json>
}

export interface ForkRequest {
  source_frame_id: string
  branches: ForkBranchRequest[]
}

export interface ForkBranch extends Record<string, unknown> {
  branch_id: string
  label?: string
}

export interface ForkGroup extends Record<string, unknown> {
  fork_group_id: string
  source_frame_id: string
  branches: ForkBranch[]
}

export interface Objective {
  objective_id: string
  episode_id: string
  text: string
  success_conditions: string[]
  constraints: { max_cost?: number }
}

export interface CreateObjectiveRequest {
  objective: Objective
  event: { payload: Record<string, Json>; provenance: Record<string, Json> }
}

export interface CreateFrameRequest {
  frame: Frame & {
    frame_id: string
    agent_id: string
    episode_id: string
    branch_id: string
    objective_id: string
    focus: { type: 'query'; query: string }
    mode: string
    attention: { policy: string; deliberate: boolean; ambient: boolean; max_candidates: number }
    filters: { trust_min: number }
    budget: { tokens: number }
  }
  event: { payload: Record<string, Json>; provenance: Record<string, Json> }
}

export interface ModelStepRequest {
  frame_id: string
  objective_id: string
  budget_tokens: number
  definitions: []
}

export interface ModelStepResponse {
  step?: { frame?: Frame; events?: FrpEvent[] }
  model_provenance?: Json
  [key: string]: unknown
}

export interface ModelConfig {
  configured: boolean
  provenance?: { model?: string; provider?: string; base_url?: string; timeout_ms?: number; timeout_seconds?: number; [key: string]: Json | undefined }
  model?: string
  provider?: string
  base_url?: string
  timeout_ms?: number
  timeout_seconds?: number
}

export interface CreateFrameResponse { frame: Frame }
export interface CreateObjectiveResponse { objective: Objective }
