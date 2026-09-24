# FRP Cognitive Debugger

Standalone React + TypeScript + Vite frontend for inspecting FRP episodes and frames. It only communicates with the public FRP HTTP API; it has no direct substrate access.

## Features

- Episode timeline with frame and execution navigation
- Frame sections: focus, map, periphery, procedures, and recent
- Boundary, provenance, and token-usage inspection
- Render, time-travel replay, blame analysis, and two-branch counterfactual fork
- `/agents` — Agent Kernel run history with results and approvals
- `/observability` — knowledge observability (lifecycle, hints, invalidation)
- `/experience` — Experience Timeline: runs, model/tool trajectory and experience clusters (appeared → recalled → injected → reused → validated → contradicted → weakened → archived) over one time axis, with lens toggles, scope/role/lifecycle filters, zoom/pan and activation links
- Loading/error/empty states, keyboard focus, reduced-motion support, and responsive layout

## Run locally

```sh
npm install
npm run dev
```

By default requests use `/api`. The Vite development proxy forwards that prefix to `http://localhost:8080` and strips `/api`, so `/api/v1/events` reaches `http://localhost:8080/v1/events`.

To call a different API directly, create `.env.local`:

```dotenv
VITE_FRP_API_URL=https://frp.example.test
```

The API must allow the frontend origin when a direct cross-origin URL is used.

## Validation

```sh
npm test
npm run build
```

## API assumptions

- `GET /v1/events?episode_id=…&limit=…` returns either an event array or `{ "events": [...] }`.
- A frame event carries `payload.frame_id`; an execution ID can be top-level or in `payload.execution_id`.
- `GET /v1/frames/:frame_id` and `GET /v1/executions/:execution_id` return JSON objects.
- `POST /v1/render`, `/v1/replay`, `/v1/blame`, and `/v1/fork` accept JSON bodies shown in `src/api.ts`.

If a backend deployment uses different singular resource routes or envelope shapes, adapt only `src/api.ts` and the relevant type in `src/types.ts`.
