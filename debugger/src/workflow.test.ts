import { describe, expect, it, vi } from 'vitest'
import { continueWithModel, EpisodeWorkflowError, renderRequest, runEpisodeWorkflow, twoBranchForkRequest, type EpisodeDraft, type EpisodeIds, type WorkflowApi } from './workflow'

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
    expect(client.modelStep).toHaveBeenCalledWith({ frame_id: 'parent', objective_id: 'objective', budget_tokens: 1200, definitions: [] })
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

  it('continues from the selected frame and its objective', async () => {
    const client = mockApi()
    await continueWithModel(client, 'selected-frame', 'selected-objective', 777)
    expect(client.modelStep).toHaveBeenCalledWith({ frame_id: 'selected-frame', objective_id: 'selected-objective', budget_tokens: 777, definitions: [] })
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
