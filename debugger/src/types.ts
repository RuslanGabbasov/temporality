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
  identity?: Json
  mode?: string
  revision?: Json
  budget?: { tokens?: number }
  [key: string]: unknown
}

export type FrameSection = 'focus' | 'map' | 'periphery' | 'working_set' | 'procedures' | 'recent' | 'attention_health' | 'memory_health' | 'identity_health'

export interface Execution {
  id?: string
  execution_id?: string
  status?: string
  [key: string]: unknown
}

export interface RenderSection {
  kind: FrameSection | 'identity' | 'objective' | string
  attention?: 'ambient' | 'deliberate' | string
  items: Json[]
  [key: string]: unknown
}

export interface TokenUsage {
  estimated?: number
  budget?: number
}

export interface RenderPacket {
  render_id: string
  frame_id: string
  memory_version?: string
  renderer_version?: string
  sections: RenderSection[]
  outside_frame?: Json
  provenance?: Json
  token_usage?: TokenUsage
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

export interface TransitionFrameRequest {
  transition: { operations: [{ op: 'attend'; focus: { type: 'query'; query: string } }] }
  event: { payload: {}; provenance: { source: 'debugger'; kind: 'user_follow_up' } }
}

export interface TransitionFrameResponse {
  frame: Frame
  event: FrpEvent
}

export interface ModelStepRequest {
  frame_id: string
  objective_id: string
  budget_tokens: number
  definitions: []
}

export interface ModelStepResponse {
  render_packet?: RenderPacket
  emission?: unknown
  step?: { frame?: Frame; events?: FrpEvent[] }
  model_provenance?: Json
  debugger_summary?: Json
  [key: string]: unknown
}

export interface ModelConfig {
  configured: boolean
  provenance?: { model?: string; provider?: string; base_url?: string; timeout_ms?: number; timeout_seconds?: number; max_output_tokens?: number; [key: string]: Json | undefined }
  model?: string
  provider?: string
  base_url?: string
  timeout_ms?: number
  timeout_seconds?: number
  max_output_tokens?: number
}

export interface CreateFrameResponse { frame: Frame }
export interface CreateObjectiveResponse { objective: Objective }
