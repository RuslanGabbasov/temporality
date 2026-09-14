import { describe, expect, it, vi } from 'vitest'
import { continueWithModel, EpisodeWorkflowError, FollowUpWorkflowError, renderRequest, runEpisodeWorkflow, runFollowUpWorkflow, twoBranchForkRequest, type EpisodeDraft, type EpisodeIds, type FollowUpApi, type WorkflowApi } from './workflow'

const ids: EpisodeIds = { episodeId: 'episode', objectiveId: 'objective', agentId: 'agent', branchId: 'branch', frameId: 'parent' }
const draft: EpisodeDraft = { prompt: 'Investigate', successConditions: ['Done'], tokenBudget: 1200, maxCost: 2, mode: 'explore', trustMin: 0.6, rebuildRegions: true }

function mockApi(): WorkflowApi {
  return {
    createObjective: vi.fn().mockResolvedValue({}),
    createFrame: vi.fn().mockResolvedValue({ frame: { frame_id: 'parent' } }),
    rebuildRegions: vi.fn().mockResolvedValue({}),
    modelStep: vi.fn().mockResolvedValue({ step: { frame: { frame_id: 'child' } } }),
  }
}

describe('episode workflow', () => {
  it('runs the successful sequence and returns the child', async () => {
    const client = mockApi(); const stages: string[] = []
    const result = await runEpisodeWorkflow(client, draft, { configured: true }, (stage) => stages.push(stage), ids)
    expect(stages).toEqual(['Creating objective', 'Creating frame', 'Rebuilding regions', 'Calling model'])
    expect(result.childFrameId).toBe('child')
    expect(client.modelStep).toHaveBeenCalledWith({ frame_id: 'parent', objective_id: 'objective', budget_tokens: 1200, definitions: [] }, undefined)
  })

  it('creates persistent records but does not call an unconfigured model', async () => {
    const client = mockApi()
    const result = await runEpisodeWorkflow(client, draft, { configured: false }, undefined, ids)
    expect(result.needsModel).toBe(true)
    expect(client.createObjective).toHaveBeenCalledOnce()
    expect(client.createFrame).toHaveBeenCalledOnce()
    expect(client.modelStep).not.toHaveBeenCalled()
  })

  it('preserves created IDs when the model fails so it can be retried', async () => {
    const client = mockApi()
    vi.mocked(client.modelStep).mockRejectedValue(new Error('502 Bad Gateway — provider failed'))
    await expect(runEpisodeWorkflow(client, draft, { configured: true }, undefined, ids)).rejects.toMatchObject({
      message: '502 Bad Gateway — provider failed', result: { ids, parentFrameId: 'parent', needsModel: false },
    } satisfies Partial<EpisodeWorkflowError>)
  })

  it('preserves resumable IDs when model cancellation aborts the workflow', async () => {
    const client = mockApi(); const controller = new AbortController()
    vi.mocked(client.modelStep).mockImplementation((_body, signal) => new Promise((_resolve, reject) => {
      if (signal?.aborted) reject(new DOMException('Aborted', 'AbortError'))
      else signal?.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')), { once: true })
    }))
    const running = runEpisodeWorkflow(client, draft, { configured: true }, undefined, ids, controller.signal)
    controller.abort()
    await expect(running).rejects.toMatchObject({
      result: { ids, parentFrameId: 'parent', needsModel: false },
      cause: { name: 'AbortError' },
    } satisfies Partial<EpisodeWorkflowError>)
  })

  it('continues from the selected frame and its objective', async () => {
    const client = mockApi()
    await continueWithModel(client, 'selected-frame', 'selected-objective', 777)
    expect(client.modelStep).toHaveBeenCalledWith({ frame_id: 'selected-frame', objective_id: 'selected-objective', budget_tokens: 777, definitions: [] }, undefined)
  })

  it('runs follow-up transition then model with the instruction Frame', async () => {
    const calls: string[] = []
    const client: FollowUpApi = {
      transitionFrame: vi.fn().mockImplementation(async () => { calls.push('transition'); return { frame: { frame_id: 'instruction' }, event: {} } }),
      modelStep: vi.fn().mockImplementation(async () => { calls.push('model'); return { step: { frame: { frame_id: 'child' } } } }),
    }
    const stages: string[] = []
    const result = await runFollowUpWorkflow(client, { sourceFrameId: 'selected', text: '  clarify this  ', objectiveId: 'objective', budgetTokens: 777 }, (stage) => stages.push(stage))
    expect(calls).toEqual(['transition', 'model'])
    expect(stages).toEqual(['Saving follow-up', 'Calling model'])
    expect(result).toMatchObject({ instructionFrameId: 'instruction', text: 'clarify this', modelResult: { step: { frame: { frame_id: 'child' } } } })
    expect(client.modelStep).toHaveBeenCalledWith({ frame_id: 'instruction', objective_id: 'objective', budget_tokens: 777, definitions: [] }, undefined)
  })

  it('does not call model when follow-up transition fails', async () => {
    const client: FollowUpApi = { transitionFrame: vi.fn().mockRejectedValue(new Error('transition failed')), modelStep: vi.fn() }
    await expect(runFollowUpWorkflow(client, { sourceFrameId: 'selected', text: 'question', objectiveId: 'objective', budgetTokens: 777 })).rejects.toMatchObject({ result: undefined } satisfies Partial<FollowUpWorkflowError>)
    expect(client.modelStep).not.toHaveBeenCalled()
  })

  it('preserves instruction Frame and retries model without a duplicate transition', async () => {
    const client: FollowUpApi = {
      transitionFrame: vi.fn().mockResolvedValue({ frame: { frame_id: 'instruction' }, event: {} }),
      modelStep: vi.fn().mockRejectedValueOnce(new Error('model failed')).mockResolvedValueOnce({ step: { frame: { frame_id: 'child' } } }),
    }
    const input = { sourceFrameId: 'selected', text: 'question', objectiveId: 'objective', budgetTokens: 777 }
    let saved
    try { await runFollowUpWorkflow(client, input) } catch (error) { saved = (error as FollowUpWorkflowError).result }
    expect(saved).toMatchObject({ instructionFrameId: 'instruction', text: 'question' })
    await runFollowUpWorkflow(client, input, undefined, saved)
    expect(client.transitionFrame).toHaveBeenCalledOnce()
    expect(client.modelStep).toHaveBeenCalledTimes(2)
  })

  it('builds render input only from the selected Frame and falls back to 4000 tokens', () => {
    expect(renderRequest({ frame_id: 'selected', objective_id: 'objective', budget: { tokens: 777 } }, 'ignored')).toEqual({ frame_id: 'selected', objective_id: 'objective', budget_tokens: 777 })
    expect(renderRequest({ objective_id: 'objective' }, 'selected-fallback')).toEqual({ frame_id: 'selected-fallback', objective_id: 'objective', budget_tokens: 4000 })
    expect(renderRequest({ frame_id: 'selected' }, 'ignored')).toBeUndefined()
  })

  it('builds one atomic two-branch fork request with model configs', () => {
    const values = ['branch-a', 'branch-b']; let index = 0
    expect(twoBranchForkRequest('source', () => values[index++])).toEqual({
      source_frame_id: 'source',
      branches: [
        { branch_id: 'branch-a', label: 'counterfactual-a', model_config: {} },
        { branch_id: 'branch-b', label: 'counterfactual-b', model_config: {} },
      ],
    })
  })
})
