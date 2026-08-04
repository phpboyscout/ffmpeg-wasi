<!-- Handover / capability request: work in one project needs something this one
     must provide. Apply `origin::handover` plus `bug` or `enhancement`.
     Do NOT solutionise — state the need, leave the how to whoever owns this
     project. See the raise-a-forge-issue skill. -->

## The need

What cannot be done today, in one sentence.

## Use case

The real situation that produced this, named concretely — which project, which
workflow, what was being attempted. A ticket without a use case gets
deprioritised because nobody can judge its weight.

## Expected output shape

What "done" looks like from the caller's side: what goes in, what comes back,
what changes for the consumer.

This is the requirements boundary. It constrains the outcome without dictating
the mechanism — do not name the function, flag, config key or architecture
unless you genuinely require that specific interface, and if you do, say why.

## Evidence

What was tried and what happened. Paste real output, not a paraphrase.

```
```

## Current workaround

What is being done instead, and what it costs. This tells the owner how urgent
it is and what they are replacing. Write "none" if there isn't one.

## Non-goals

Where the scope could plausibly run away, and where the boundary is. Omit this
section if nothing is at risk of creeping.

<!-- Leave out: a proposed API, a suggested patch, an implementation plan, an
     estimate, or a priority you are not entitled to set. No @-mentions — bare
     @tokens notify real people on every reference. -->
