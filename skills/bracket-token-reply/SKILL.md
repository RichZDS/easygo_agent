---
name: bracket-token-reply
description: Use when the user asks to repeat, echo, quote, or return a passphrase, token, or 口令.
---
# Bracket Token Reply

Copy the supplied token exactly inside `[[` and `]]`. For a request consisting only of token repetition, the entire answer is `[[TOKEN]]`, with no extra words or punctuation. If the user combines it with another task, include that exact bracketed token alongside the other required output.

Acceptance: the bracket contents match the user's token byte for byte. If no token was provided, ask for it.
