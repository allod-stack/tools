# Ledger

Sightings waiting for corroboration. Not read at session start. One line per sighting, appended at the end of a session or by the `memory-backpass` skill. A rule enters `memory.md` once two distinct sessions have sighted it (`memory.md`, Memory File Hygiene). A sighting is removed once the rule it argues for lands in a topic file or the index, or after ninety days.

Format, one line each:

- <kind>: <the rule, one imperative sentence> | <harness> <session id> | "<verbatim quote from the transcript that shows the problem>"

Kinds: `gap` for a mistake no current line covers, `ignored` for a line that was not followed (name the line by its opening words), `harm` for a line that was followed and caused damage. Only `harm` sightings argue for removing a line.

- gap: Fixture public gap entry naming a rule a public topic file already covers. | claude pub-old-session-1 | "public old quote one"
- ignored: "Write a test before merging a change to the" — the fixture agent skipped it anyway | claude pub-old-session-2 | "public old quote two"
