---
order: 23
section: Working with the agent
title: Files
description: How the agent hands files to a human — into the chat, into your messenger — and where to pick them up.
---

# Files

Agents produce files — reports, decks, archives, previews — and hand them over with `send_file`. The file always stays in the run workspace; delivery goes wherever it is convenient for you.

## In the chat

After `send_file`, a **file chip** appears under the answer — «📎 preview.png». Clicking it downloads the file from the run workspace. The chip appears the moment the send starts, even if the model's reply forgets to mention the file.

![File in the chat](../assets/guide/chat-file-chip.png)

If the agent works with a messenger, the file is delivered there as an attachment: a document in Matrix, a file with a caption in Telegram.

## In messengers

![File in Telegram](../assets/guide/telegram-sendfile.png)

Delivery goes to every connected channel of the recipient that can carry attachments. If no channel is configured, the file stays in the run workspace — and the agent is instructed to mention the path in its reply, which the chat turns into a download link.

## Limits

- Attachment size — up to 50 MB (messenger storage caps).
- The file never leaves the system until delivered: «sent» means «in the run workspace and/or delivered to a channel» — the tool result always shows which.
