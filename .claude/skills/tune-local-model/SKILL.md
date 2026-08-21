---
name: tune-local-model
description: Experiment against the local model behind fastllm's live terminal bridge to find the best prompting workflow for its constraints, logging findings to fastllm's shared Notes scratchpad.
disable-model-invocation: true
arguments: max_experiments
argument-hint: "[max_experiments]"
context: fork
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

**Experiment budget:** `$max_experiments` if given (e.g. `/tune-local-model
5` runs at most 5); otherwise default to 10. Treat it as a hard cap, not a
target — stop earlier if you've already got a clear picture.

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

## Step 0: pick one model and lock it for the whole run

This skill tunes *prompting technique* for a single model, not a model
bake-off — do not switch `"model"` between experiments once chosen.

1. Read the scratchpad for prior findings, so you don't repeat past
   experiments and don't re-pick a model a past run already found broken:
   ```
   curl -s $FASTLLM_BASE_URL/api/notes
   ```
2. List what's actually installed — the configured default (`llama3.1`)
   isn't installed on this setup, so you must pick an explicit tag rather
   than omitting `"model"`:
   ```
   curl -s $FASTLLM_BASE_URL/api/models
   ```
3. Pick the first/smallest reasonable tag from that list that the
   scratchpad hasn't already flagged as broken (looping, empty output,
   OOM on load) and note your pick in your own reasoning. If your pick
   turns out to be broken (OOMs, loops, never returns) on your very first
   request to it, that failure itself is a finding — log it (see the
   append-finding step under Loop below), cross it off, and pick the next
   untried tag from the list.
   Once a model produces a real, sane response, stop switching: every
   remaining experiment in this run uses that same tag.

## Loop

Every experiment below sends the locked-in model from Step 0:
```
curl -s -X POST $FASTLLM_BASE_URL/v1/messages \
  -H "Content-Type: application/json" \
  -d '{"model":"<the tag you locked in>","max_tokens":200,"messages":[{"role":"user","content":"..."}]}'
```

1. Vary one thing at a time and observe what breaks or improves the
   response, using programming-flavored prompts throughout — e.g. "write
   a function that...", "explain what this snippet does...", "fix the bug
   in this code...", "here's a 3-step refactor, do it in order...". Try
   axes like: single-step vs. multi-step coding instructions, short vs.
   long code context, zero-shot vs. few-shot examples, free-form vs.
   structured (JSON/diff/fenced-code-block) output, explicit formatting
   constraints, which languages it handles better or worse, how much
   surrounding code it can hold onto without losing track.
2. **Include real file read/write/edit tasks, not just inline snippets.**
   Use your own Read tool to pull a real chunk of code from the project
   you're currently in, embed it in the prompt, and ask the local model
   to do something with it — read/explain a real file, write a new file's
   worth of content from a spec, or edit/patch a real snippet in place.
   This is the more realistic case (the model reasoning over real
   project code, not a toy example) and belongs in the mix alongside the
   synthetic prompts from step 1, not as a replacement for them.

   Never use your own Write/Edit tools to apply the local model's output
   directly to real project files — that's untrusted, unreviewed output
   from a model you're actively finding failure modes in. Instead, if you
   want to inspect a full generated/edited file, write the local model's
   raw output to a throwaway scratch file (e.g. under a `/tmp` path or
   similar) with your own Write tool, and note in your finding whether it
   would have been safe to apply as-is or not.
3. As soon as you learn something concrete, append it immediately —
   don't wait until the end and try to remember everything:
   ```
   curl -s -X POST $FASTLLM_BASE_URL/api/notes/append -d "finding: ..."
   ```
   Write findings as actionable rules ("keep instructions to one step at a
   time — it drops the second half of compound asks"), not narration of
   what you did. Include which model tag the finding applies to.
4. **Respect the experiment budget above** (one prompt-and-observe cycle
   from step 1/2 each; Step 0's model-selection probes don't count against
   it unless they required a full prompting experiment to diagnose). Stop
   early once you can already describe a reliable workflow. Either way,
   end by appending one final summary note laying out the recommended
   prompting approach **for the specific model tag you used** on
   programming tasks — including file read/write/edit tasks specifically.

Every request you send through `$FASTLLM_BASE_URL/v1/messages` also shows
up live in fastllm's own Chat panel ("Live Terminal" conversation), so the
user can watch the experiments happen in real time.
