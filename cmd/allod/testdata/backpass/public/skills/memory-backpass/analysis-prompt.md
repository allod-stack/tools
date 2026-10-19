<!-- memory-backpass:self -->
You are auditing one distilled agent-session trace against two memory indexes. Measure how the instructions steered the session and find mistakes an instruction could have prevented. Do not review the code produced by the session.

## Instruction indexes

Every bullet has a run-local stable id in brackets. Public-memory ids begin `PUB-`; private-memory ids begin `PRV-`. Refer to an instruction only by its id.

{{INSTRUCTION_INDEX}}

## Open ledger entries

Each existing sighting has a stable `L-` id. If a gap is the same underlying rule as an entry below—one instruction would prevent both—set `matchesLedger` to that id. Otherwise omit it.

{{OPEN_LEDGER_ENTRIES}}

## Distilled trace

Path: {{TRACE_PATH}}

{{TRACE}}

## Return value

Return one JSON object and nothing else. Do not add prose or a Markdown fence.

```
{
  "positive": [{"id": "PUB-001", "moment": "turn 2", "effect": "what following it achieved", "quote": "verbatim text from the trace"}],
  "negative": [{"id": "PRV-003", "moment": "turn 4", "effect": "what happened and what it cost", "class": "harm|non-compliance|irrelevant", "quote": "verbatim text from the trace"}],
  "gaps": [{"mistake": "what went wrong", "proposedInstruction": "one imperative sentence that would have prevented it", "recurrenceRisk": "high|medium|low", "matchesLedger": "L-1234abcd", "quote": "verbatim text from the trace"}]
}
```

Rules, in order of importance:

1. Every item needs a `quote` copied verbatim from this trace. Downstream code whitespace-folds it and discards the item unless it is a substring of the whitespace-folded trace. A paraphrase is not evidence.
2. Negative evidence is most useful. Classify it precisely: `harm` means the instruction was followed and caused damage or cost; `non-compliance` means the instruction was ignored or violated; `irrelevant` means the event does not actually bear on the instruction. Never label a skipped rule as `harm`.
3. Report a gap only when no current instruction covers the mistake. If a current instruction was ignored, report `non-compliance` against its id instead.
4. Write `proposedInstruction` as one imperative sentence, specific enough to act on and general enough to apply beyond this trace.
5. Use `matchesLedger` only when the proposed instruction expresses the same rule, not merely the same topic.
6. Do not infer influence from a good outcome alone. Positive evidence requires the trace to show the agent doing the specific thing the instruction asks.
7. Empty arrays are valid and useful. Return no weak or unquotable item.
