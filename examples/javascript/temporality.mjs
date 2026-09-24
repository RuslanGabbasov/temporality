/** Dependency-free client for the universal Temporality HTTP API. */
export function createTemporalityClient(baseURL = 'http://localhost:8080') {
  const base = baseURL.replace(/\/$/, '')

  async function post(path, body) {
    const response = await fetch(`${base}${path}`, {
      method: 'POST',
      headers: { 'content-type': 'application/json', accept: 'application/json' },
      body: JSON.stringify(body),
    })
    const payload = await response.json()
    if (!response.ok) throw new Error(`Temporality HTTP ${response.status}: ${JSON.stringify(payload)}`)
    return payload
  }

  return {
    record: (...events) => post('/v1/observations/events', { events }),
    async hintsAfterTool({ project, task, tool, toolResult, run, actor, entities, topics, limit = 8 }) {
      const response = await post('/v1/observations/hints', {
        project, task, tool, tool_result: toolResult, run, actor, entities, topics, limit,
      })
      // Preserve the tool result. The harness may pass context_block separately
      // to the model as supplemental memory.
      return { toolResult, temporalityContext: response.context_block, hints: response.hints }
    },
  }
}

// Example: call this after a tool callback in any JavaScript harness.
// const temporality = createTemporalityClient(process.env.TEMPORALITY_URL)
// const activation = await temporality.hintsAfterTool({
//   project: 'repo-a', task: 'fix failing tests', tool: 'test-runner', toolResult,
// })
// continueAgent({ toolResult: activation.toolResult, additionalContext: activation.temporalityContext })
