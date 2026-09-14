import type { CreateFrameRequest, CreateFrameResponse, CreateObjectiveRequest, ForkRequest, Frame, ModelConfig, ModelStepRequest, ModelStepResponse, RenderRequest, TransitionFrameRequest, TransitionFrameResponse } from './types'

export type WorkflowStage = 'Creating objective' | 'Creating frame' | 'Rebuilding regions' | 'Calling model'

export interface EpisodeDraft {
  prompt: string
  successConditions: string[]
  tokenBudget: number
  maxCost?: number
  mode: string
  trustMin: number
  rebuildRegions: boolean
}

export interface EpisodeIds {
  episodeId: string
  objectiveId: string
  agentId: string
  branchId: string
  frameId: string
}

export interface EpisodeWorkflowResult {
  ids: EpisodeIds
  parentFrameId: string
  childFrameId?: string
  modelResult?: ModelStepResponse
  needsModel: boolean
}

export interface WorkflowApi {
  createObjective(body: CreateObjectiveRequest): Promise<unknown>
  createFrame(body: CreateFrameRequest): Promise<CreateFrameResponse>
  rebuildRegions(episodeId: string, branchId: string): Promise<unknown>
  modelStep(body: ModelStepRequest, signal?: AbortSignal): Promise<ModelStepResponse>
}

export interface FollowUpApi {
  transitionFrame(frameId: string, body: TransitionFrameRequest): Promise<TransitionFrameResponse>
  modelStep(body: ModelStepRequest, signal?: AbortSignal): Promise<ModelStepResponse>
}

export interface FollowUpResult {
  sourceFrameId: string
  instructionFrameId: string
  text: string
  objectiveId: string
  budgetTokens: number
  modelResult?: ModelStepResponse
}

export type FollowUpStage = 'Saving follow-up' | 'Calling model'

export class FollowUpWorkflowError extends Error {
  constructor(message: string, public readonly result?: FollowUpResult, options?: ErrorOptions) {
    super(message, options)
    this.name = 'FollowUpWorkflowError'
  }
}

export class EpisodeWorkflowError extends Error {
  constructor(message: string, public readonly result: EpisodeWorkflowResult, options?: ErrorOptions) {
    super(message, options)
    this.name = 'EpisodeWorkflowError'
  }
}

export function generateEpisodeIds(randomUUID = () => crypto.randomUUID()): EpisodeIds {
  return { episodeId: randomUUID(), objectiveId: randomUUID(), agentId: randomUUID(), branchId: randomUUID(), frameId: randomUUID() }
}

export function childFrameId(result: ModelStepResponse): string | undefined {
  const frame = result.step?.frame
  return frame?.frame_id ?? frame?.id
}

export function renderRequest(frame: Frame, selectedFrameId: string): RenderRequest | undefined {
  const frameId = frame.frame_id ?? frame.id ?? selectedFrameId
  if (!frameId || !frame.objective_id) return undefined
  const budget = frame.budget
  const budgetTokens = typeof budget === 'object' && budget && 'tokens' in budget && typeof budget.tokens === 'number' ? budget.tokens : 4000
  return { frame_id: frameId, objective_id: frame.objective_id, budget_tokens: budgetTokens }
}

export function twoBranchForkRequest(sourceFrameId: string, randomUUID: () => string = () => crypto.randomUUID()): ForkRequest {
  return {
    source_frame_id: sourceFrameId,
    branches: ['counterfactual-a', 'counterfactual-b'].map((label) => ({ branch_id: randomUUID(), label, model_config: {} })),
  }
}

export async function runEpisodeWorkflow(
  client: WorkflowApi,
  draft: EpisodeDraft,
  config: ModelConfig,
  onStage: (stage: WorkflowStage) => void = () => undefined,
  ids = generateEpisodeIds(),
  signal?: AbortSignal,
): Promise<EpisodeWorkflowResult> {
  const retained: EpisodeWorkflowResult = { ids, parentFrameId: ids.frameId, needsModel: !config.configured }
  try {
    onStage('Creating objective')
    await client.createObjective({
      objective: {
        objective_id: ids.objectiveId,
        episode_id: ids.episodeId,
        text: draft.prompt.trim(),
        success_conditions: draft.successConditions,
        constraints: draft.maxCost === undefined ? {} : { max_cost: draft.maxCost },
      },
      event: { payload: {}, provenance: { source: 'debugger' } },
    })
    onStage('Creating frame')
    const created = await client.createFrame({
      frame: {
        frame_id: ids.frameId,
        agent_id: ids.agentId,
        episode_id: ids.episodeId,
        branch_id: ids.branchId,
        objective_id: ids.objectiveId,
        focus: { type: 'query', query: draft.prompt.trim() },
        mode: draft.mode,
        attention: { policy: 'balanced', deliberate: true, ambient: true, max_candidates: 32 },
        zoom: 2,
        filters: { trust_min: draft.trustMin },
        budget: { tokens: draft.tokenBudget },
      },
      event: { payload: {}, provenance: { source: 'debugger' } },
    })
    retained.parentFrameId = created.frame.frame_id ?? created.frame.id ?? ids.frameId
    if (draft.rebuildRegions) {
      onStage('Rebuilding regions')
      await client.rebuildRegions(ids.episodeId, ids.branchId)
    }
    if (!config.configured) return retained
    onStage('Calling model')
    const modelResult = await continueWithModel(client, retained.parentFrameId, ids.objectiveId, draft.tokenBudget, signal)
    return { ...retained, needsModel: false, modelResult, childFrameId: childFrameId(modelResult) }
  } catch (cause) {
    const message = cause instanceof Error ? cause.message : 'Episode workflow failed'
    throw new EpisodeWorkflowError(message, retained, { cause })
  }
}

export function continueWithModel(client: Pick<WorkflowApi, 'modelStep'>, frameId: string, objectiveId: string, budgetTokens: number, signal?: AbortSignal) {
  return client.modelStep({ frame_id: frameId, objective_id: objectiveId, budget_tokens: budgetTokens, definitions: [] }, signal)
}

export async function runFollowUpWorkflow(
  client: FollowUpApi,
  input: Omit<FollowUpResult, 'instructionFrameId' | 'modelResult'>,
  onStage: (stage: FollowUpStage) => void = () => undefined,
  saved?: FollowUpResult,
  signal?: AbortSignal,
): Promise<FollowUpResult> {
  let retained = saved
  try {
    if (!retained) {
      const text = input.text.trim()
      if (!text) throw new Error('Follow-up is required')
      onStage('Saving follow-up')
      const transitioned = await client.transitionFrame(input.sourceFrameId, {
        transition: { operations: [{ op: 'attend', focus: { type: 'query', query: text } }] },
        event: { payload: {}, provenance: { source: 'debugger', kind: 'user_follow_up' } },
      })
      const instructionFrameId = transitioned.frame.frame_id ?? transitioned.frame.id
      if (!instructionFrameId) throw new Error('Transition response did not include a Frame ID')
      retained = { ...input, text, instructionFrameId }
    }
    onStage('Calling model')
    const modelResult = await continueWithModel(client, retained.instructionFrameId, retained.objectiveId, retained.budgetTokens, signal)
    return { ...retained, modelResult }
  } catch (cause) {
    const message = cause instanceof Error ? cause.message : 'Follow-up workflow failed'
    throw new FollowUpWorkflowError(message, retained, { cause })
  }
}
