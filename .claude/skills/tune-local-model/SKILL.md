---
name: tune-local-model
description: Experiment against the local model behind fastllm's live terminal bridge to find the best prompting workflow for its constraints, logging findings to fastllm's shared Notes scratchpad.
disable-model-invocation: true
---

You're running inside fastllm's built-in Terminal pane, connected via
`$ANTHROPIC_BASE_URL` to a small local model (e.g. a 1B-parameter
llama.cpp/Ollama model) — much weaker than you. Your job is not to answer
the user's question yourself; it's to figure out, empirically, the best way
to prompt that local model given its constraints, and leave a durable
record of what you learn.

If `$ANTHROPIC_BASE_URL` or `$FASTLLM_BASE_URL` aren't set, stop and tell
the user to enable Settings → Terminal's "Point local AI CLIs at fastllm"
toggle and open a new terminal tab — those vars are only injected into
tabs opened after the toggle is turned on.

## Loop

1. Read the current state of the scratchpad first, so you don't repeat
   past experiments:
   ```
   curl -s $FASTLLM_BASE_URL/api/notes
   ```
2. Send the local model a prompt directly (not through your own
   conversation — you're driving it as a subject, not chatting with it):
   ```
   curl -s -X POST $ANTHROPIC_BASE_URL/v1/messages \
     -H "Content-Type: application/json" \
     -d '{"model":"local","max_tokens":200,"messages":[{"role":"user","content":"..."}]}'
   ```
3. Vary one thing at a time and observe what breaks or improves the
   response: single-step vs. multi-step instructions, short vs. long
   context, zero-shot vs. few-shot examples, free-form vs. structured
   (JSON) output, explicit formatting constraints, temperature-sensitive
   phrasing, etc.
4. As soon as you learn something concrete, append it immediately —
   don't wait until the end and try to remember everything:
   ```
   curl -s -X POST $FASTLLM_BASE_URL/api/notes/append -d "finding: ..."
   ```
   Write findings as actionable rules ("keep instructions to one step at a
   time — it drops the second half of compound asks"), not narration of
   what you did.
5. Keep iterating until you've covered enough ground to describe a
   reliable workflow, then append one final summary note laying out the
   recommended prompting approach for this model as a whole.

Every request you send through `$ANTHROPIC_BASE_URL/v1/messages` also
shows up live in fastllm's own Chat panel ("Live Terminal" conversation),
so the user can watch the experiments happen in real time.
