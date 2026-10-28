---
order: 22
section: Working with the agent
title: Forms & approvals
description: How the agent asks a human structured questions — cards in the chat, a button in your messenger, a browser form behind a one-time link — with server-side validation.
---

# Forms & approvals

When the agent lacks data or needs a consequential action confirmed, it asks via `ask_human`. The question reaches you where you are: as a card in the chat, a button in your messenger, or a text message with a link.

## Where forms come from

The agent describes the question declaratively — a JSON schema of fields:

```json
{
  "question": "Release parameters for billing-service?",
  "schema": {
    "type": "object",
    "properties": {
      "channel": { "type": "string", "enum": ["canary", "stable"], "title": "Channel" },
      "version": { "type": "string", "minLength": 5, "title": "Version" },
      "confirm": { "type": "boolean", "title": "Confirm the rollout" }
    },
    "required": ["channel", "version", "confirm"]
  }
}
```

Supported: strings (with choices, lengths, regex patterns), integers and numbers with bounds, booleans, arrays (multi-select and date ranges), date/time/email/URL formats and masked input. Limits: 20 fields, 8 KB per schema — a form is a question to a human, not a database.

The server validates the answer against the same schema **before** resuming the run: an invalid value does not resume anything, it returns to the agent as an error.

## Where you see the form

**In the built-in chat** — a card right in the conversation: choices as buttons, free-form answer, and full field sets for forms. It appears the moment the question is asked and closes itself when answered.

![Form in the chat](../assets/chat-run.webp)

**In Matrix** — an HTML card: a field table with formats and required marks; answer in the room as `field: value` lines, or tap «fill the form».

**In Telegram** — the question plus an inline «Open form» button; a tap opens the form in your browser.

**The universal path** — from any messenger, the button opens a **hosted form**: a standalone minimalist page behind a one-time scoped link. No harness account needed — the link is the access, to exactly one question, for a limited time. Fill it in, submit, close the tab.

<!-- Screenshot: hosted-form-desktop.webp — add once captured (the /kernel-api/forms/<token> page at desktop width) -->

Links expire and die with the question: answered, cancelled or expired — the form stops accepting. One question, one submission.

## What happens after you answer

The server validates the answer, folds it into canonical JSON and hands it to the agent as the tool result. The run continues with your data. The whole chain — question, answer, who answered and when — stays in the journal.

For consequential questions (rollouts, external letters) the agent must request a separate human approval — same mechanics.

## If the evaluation suite is not ready

Applying an agent's evolution proposal is guarded by the evaluation gate (see [Evolution & quality](evolution.html)). If the agent has no evaluation cases yet, you can still apply with a written justification — the bypass is recorded in the audit log. A deliberate balance: you have read the proposal and take the decision; the system only requires the trail.
