---
name: tune-local-model
description: Experiment against the local model behind fastllm's live terminal bridge to find the best prompting workflow for its constraints, logging findings to fastllm's shared Notes scratchpad.
disable-model-invocation: true
---

You're running inside fastllm's built-in Terminal pane, with access to a
small local model (e.g. a 1B-parameter llama.cpp/Ollama model) — much
weaker than you. Your job is not to answer the user's question yourself;
it's to figure out, empirically, the best way to prompt that local model
**for programming tasks specifically** — reading/explaining code,
generating snippets, fixing bugs, following multi-step coding
instructions, formatting code output correctly — given its constraints,
and leave a durable record of what you learn. Don't spend experiments on
general trivia (geography, history, etc.) — every prompt you send it
should resemble something a programming assistant would actually be asked.

**Important — do not use `$ANTHROPIC_BASE_URL` for this.** If it's set,
ignore it: that variable is how *your own* inference gets routed, and if
it points at fastllm you'd be replacing yourself with the local model
instead of investigating it as a subject. All experiment calls in this
skill go through `$FASTLLM_BASE_URL` instead — fastllm's own API, which
your own reasoning never reads, so using it can't reroute you.

If `$FASTLLM_BASE_URL` isn't set at all, stop and tell the user to enable
Settings → Terminal's "Enable terminal" toggle (the env var is injected
whenever the terminal feature is on, independent of the separate "Point
local AI CLIs at fastllm" toggle — leave that one off for this skill) and
open a new terminal tab, since it's only injected into tabs opened after
that.

## Loop

1. Read the current state of the scratchpad first, so you don't repeat
   past experiments:
   ```
   curl -s $FASTLLM_BASE_URL/api/notes
   ```
2. Send the local model a prompt directly (not through your own
   conversation — you're driving it as a subject, not chatting with it):
   ```
   curl -s -X POST $FASTLLM_BASE_URL/v1/messages \
     -H "Content-Type: application/json" \
     -d '{"model":"local","max_tokens":200,"messages":[{"role":"user","content":"..."}]}'
   ```
3. Vary one thing at a time and observe what breaks or improves the
   response, using programming-flavored prompts throughout — e.g. "write
   a function that...", "explain what this snippet does...", "fix the bug
   in this code...", "here's a 3-step refactor, do it in order...". Try
   axes like: single-step vs. multi-step coding instructions, short vs.
   long code context, zero-shot vs. few-shot examples, free-form vs.
   structured (JSON/diff/fenced-code-block) output, explicit formatting
   constraints, which languages it handles better or worse, how much
   surrounding code it can hold onto without losing track.
4. As soon as you learn something concrete, append it immediately —
   don't wait until the end and try to remember everything:
   ```
   curl -s -X POST $FASTLLM_BASE_URL/api/notes/append -d "finding: ..."
   ```
   Write findings as actionable rules ("keep instructions to one step at a
   time — it drops the second half of compound asks"), not narration of
   what you did.
5. **Run at most 10 experiments total** (step 2 above, one prompt-and-
   observe cycle each) — this is a hard cap, not a target to aim for if
   you reach a clear picture sooner. Stop early once you can already
   describe a reliable workflow. Either way, end by appending one final
   summary note laying out the recommended prompting approach for using
   this model on programming tasks.

Every request you send through `$FASTLLM_BASE_URL/v1/messages` also shows
up live in fastllm's own Chat panel ("Live Terminal" conversation), so the
user can watch the experiments happen in real time.
