<!-- memory-backpass:self -->
You are performing the synthesis step of a backward pass over public and private agent memory. Evidence from recent session traces has already been quote-checked and folded. Make one small, evidence-backed update in the private-memory worktree and describe any public-memory changes only as relay proposals. This is one bounded adjustment, not a rewrite.

## Worktree and boundary

The private-memory worktree is:

{{PRIVATE_WORKTREE}}

Edit only `memory.md`, `ledger.md`, and existing topic files inside that worktree. Never edit the public-memory checkout. Put any public index, topic, or ledger proposal in the returned `toRelay` array instead. Never push a default branch and never merge.

## Current instruction indexes and budgets

The ids are positional references for this run: `PUB-` for public memory and `PRV-` for private memory. They are not text to write into a memory file.

{{INSTRUCTION_INDEXES_WITH_BYTE_COUNTS}}

The byte caps come from `.hooks/memory-budget` and cannot be raised.

## Topic files

These are the overflow tier. Each line gives a filename and the one-line description that controls when an agent reads it.

{{TOPIC_FILE_INDEX}}

## Folded evidence

Counts include distinct source sessions. Relevance is the share of analyzed sessions in which an instruction drew any evidence.

{{FOLD}}

## Previously rejected edits

These are titles of closed, unmerged private-memory pull requests whose title starts with `memory-backpass:`. Do not repeat an edit without materially new evidence.

{{REJECTED_EDIT_TITLES}}

## Hard rules

1. Make at most five edits. An edit is one change a human can accept or reject independently.
2. Adding, rewriting, or removing an index line requires verbatim quotes from at least two distinct sessions. As the only exception, a one-session add is allowed when the edit states the safety reason in its rationale and the PR body flags it as a one-session safety exception for the human.
3. Removing an index line requires `harm` sightings from at least two distinct sessions. Non-compliance means the line failed to steer: reinforce it, move it nearer the top, or sharpen its trigger; never remove it for being ignored.
4. Every edit carries at least one verbatim evidence quote and its `<harness> <session id>` source. Use only quotes supplied in the fold.
5. The post-edit private index must fit the byte cap. Move detail to a topic file and leave a concise pointer; never raise the cap.
6. A rule relevant to fewer than one fifth of analyzed sessions belongs in the topic file whose description matches it, not in an index. If no topic description fits, leave the sighting in the ledger instead of creating unsupported structure.
7. Preserve the public/private split. When ownership is unclear, keep the fact private.
8. Change only what the evidence supports. An empty edit set is valid and preferable to a weak proposal.

## Work and response

Edit the private worktree directly. Then return one JSON object and nothing else, with no Markdown fence:

```
{
  "edits": [
    {
      "title": "short decision title",
      "paths": ["memory.md"],
      "rationale": "why the evidence supports this edit",
      "evidence": [{"source": "<harness> <session id>", "quote": "verbatim quote"}]
    }
  ],
  "toRelay": [
    {
      "title": "public-memory proposal",
      "diff": "proposed public diff",
      "rationale": "why it belongs in public memory",
      "evidence": [{"source": "<harness> <session id>", "quote": "verbatim quote"}]
    }
  ]
}
```

Both arrays may be empty. Do not claim an edit that is absent from the worktree, and do not make an unreported worktree edit.
