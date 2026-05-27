---
name: phase-documentation
description: Workflow for creating and maintaining implementation phase documents in docs/phases/. Use when planning features, tracking implementation status, or ensuring documentation stays synchronized with code changes. Always run this skill when the user requests a new feature, a phase status change, or after completing significant implementation work.
---

# Phase Documentation — Keep `docs/phases/` Alive

The `docs/phases/` directory and its `README.md` are the **canonical source of truth** for implementation progress in the Limen project. Each phase is a self-contained design doc + checklist. The README is the global index of phases, dependencies, statuses, and cross-cutting decisions.

**This skill must be practiced every time code is written or changed.** Phase documents are not optional afterthoughts — they are living artifacts that must stay in lockstep with the codebase.

---

## The Two Artifacts

### 1. Phase Document (`docs/phases/phase-NN-short-name.md`)

Every phase is a standalone markdown file with **YAML frontmatter** followed by structured content:

```markdown
---
phase: "NN"
title: "Descriptive Title"
status: planned | in_progress | completed | deferred
progress: 0
depends_on: ["N", "M"]
updated: "2026-05-27"
---

# Phase NN — Descriptive Title

> **Depends on**: Phase N (link), Phase M (link) — or "nothing"
> **Unblocks**: Phase X (link), Phase Y (link)

## Goal

One to three sentences. What does this phase deliver? Why does it matter?

## Background (optional)

Context, cross-references, why this design was chosen.

## Design

Architecture decisions, code patterns, proto definitions, data models, file layout. Use code blocks, tables, and diagrams as needed. This is the "how" of the phase.

## Sub-phases (optional, used for complex phases)

Break a phase into sub-sections (e.g., NN-a, NN-b) when it spans multiple independent chunks. Each sub-phase gets its own checklist section.

### NN-a: First Sub-Phase

- [ ] Task description — file-level, specific, actionable
- [ ] Another task

### NN-b: Second Sub-Phase

- [ ] ...

## Checklist

- [ ] Specific, verifiable task (mention files, functions, or behaviors)
- [ ] Another task
- [x] Completed tasks stay checked — never uncheck

## Design Decisions (optional)

### Why decision X

Explanation of a non-obvious tradeoff made in this phase.

## Deliverables (optional)

Table of files touched:

| File | Change |
|------|--------|
| `path/to/file.go` | What changed |
| `docs/phases/phase-NN-name.md` | This file |
| `docs/phases/README.md` | Updated index |

## Risks (optional)

Known risks or explicit out-of-scope items.
```

### Frontmatter Fields

| Field | Description | Required |
|-------|-------------|----------|
| `phase` | Phase identifier string (e.g., `"9b"`, `"10"`) | Yes |
| `title` | Human-readable title matching the H1 | Yes |
| `status` | One of: `planned`, `in_progress`, `completed`, `deferred` | Yes |
| `progress` | Integer 0–100, percentage complete (only meaningful when `status: in_progress`) | Yes |
| `depends_on` | YAML list of phase identifiers this phase depends on | Yes |
| `updated` | ISO 8601 date (`YYYY-MM-DD`) of last substantive update | Yes |

The frontmatter is the **machine-parseable source of truth** for a phase's metadata. The inline `Depends on` / `Unblocks` / `Status` lines in the body remain as human-readable complements but must stay in sync with the frontmatter.

### 2. The README Index (`docs/phases/README.md`)

The README is a **generated document** — not hand-maintained in isolation. It is the union of:

1. **Phase index table** — every phase gets one row with: `#`, `Name (linked)`, `Depends on`, `Status`
2. **Global checklist** — a mirror of every per-phase checklist, organized by phase
3. **Cross-cutting decisions** — architectural choices that span multiple phases
4. **Out of scope** — features explicitly deferred or rejected

When a phase file changes, the README must be updated in lockstep.

---

## When to Activate This Skill

Invoke this skill proactively whenever you:

| Trigger | Action |
|---------|--------|
| **Starting a new feature** | Create a new phase document (or sub-phase section inside an existing one). Add the row to the README index table. |
| **Completing a checklist item** | Check the box in the phase document. If the item exists in the README global checklist, check it there too. |
| **Changing a completed item** | Uncheck it in both places, add a note about what changed. |
| **Adding/removing a phase dependency** | Update the `Depends on` / `Unblocks` headers AND the README dependency table. |
| **Status change** (e.g., 80% → 92%) | Update the status emoji/percentage in the README phase index table. Use these status markers: |
| | `✅` — fully complete, all checkboxes checked |
| | `🔶 (XX%)` — in progress, with approximate percentage |
| | `☐` — not started |
| | `☐ (deferred)` — explicitly deferred to a future iteration |
| **Making a cross-cutting decision** | Add it to the "Cross-cutting decisions" section of the README. |
| **Deciding something is out of scope** | Add it to "Explicitly out of scope" in the README. |
| **Completing a full phase** | Mark it `✅` in the README table. Update the prose summary below the table. |
| **User explicitly calls this skill** | Run the full verification checklist below. |
| **Responding to a PR that touches phases** | Verify that phase documents and README are updated consistently. |

---

## The Phase-Creation Workflow

When creating a **new phase**:

1. **Pick the next number.** Look at `docs/phases/README.md` — the highest phase number + 1. Use the next available integer. If this is a sub-phase of an existing number (e.g., 9j after 9i), use the next letter.

2. **Name the file.** `docs/phases/phase-NN-descriptive-name.md`. Use lowercase, hyphens, no special characters. The name should capture the essence in 3-5 words.

3. **Write the phase document** using the template above. Minimum required sections: `# Phase NN — Title`, `Goal`, `Checklist`. Everything else is optional but encouraged.

4. **Register in README.md:**
   - Add a row to the phase index table with the correct number, linked title, dependencies, and initial status (`☐`).
   - Add the phase's checklist as a new `### Phase NN — Title` section under `## Global checklist`.
   - Update the "Phase NN (in progress / planned)" prose in the narrative summary paragraph below the table.

5. **Cross-reference.** If any existing phase's `Unblocks` section should mention this new phase, update that phase document.

---

## The Status-Update Workflow

When updating an **existing phase**:

1. **Check the box** in the phase document: `- [x] Completed item`.
2. **Mirror in README** — if the item is in the global checklist, check it there too.
3. **Recalculate percentage** — count `[x]` vs total `[ ]` items. Exclude items marked as deferred. Round to nearest whole percent.
4. **Update the status column** in the README phase index table:
   - `✅` if 100% of non-deferred items are checked.
   - `🔶 (XX%)` otherwise.
   - Never lower a percentage without a clear comment explaining why.
5. **Update the prose summary** below the table if the overall status narrative has changed.

---

## The README Integrity Rules

These rules prevent the README from drifting from the phase documents:

1. **Every phase in `docs/phases/` MUST have a row in the README index table.** No orphaned phase files.
2. **Every row in the README index table MUST correspond to a real phase file.** No dead links.
3. **The global checklist MUST be a faithful mirror.** Every `[x]` in a phase file that appears in the README checklist must match. If an item only exists in the phase file (and not the README), that's fine — the README mirrors _summary_ items, not necessarily every sub-task.
4. **Dependencies must be consistent.** If Phase A says "Depends on: Phase B" in its file, the README table must say the same. If the README says Phase C unblocks Phase D, Phase D's file must agree.
5. **Status must be honest.** Don't mark `✅` if there are unchecked boxes in the phase file. Don't claim 92% if 7 of 10 items are done (that's 70%).

---

## The Consistency Check (Run When Skill is Called)

When the user explicitly invokes this skill, or when you finish a significant change, run this audit:

1. **List all phase files:** `ls docs/phases/phase-*.md`
2. **Read the README index table.** Extract every row's phase number and link.
3. **Cross-check:** Every phase file appears in the table. Every table row points to an existing file.
4. **Spot-check 3 phases:** Pick 3 phases at random. For each:
   - Count `[x]` items in the phase file.
   - Verify the README status badge matches.
   - Verify dependencies in the phase file header match the README "Depends on" column.
5. **Check the prose summary:** The narrative paragraph below the table should accurately describe the current state of active work.
6. **Report findings.** If anything is inconsistent, flag it and offer to fix.

---

## Adherence Checklist

Before considering any task complete where code was changed, verify:

- [ ] If a new feature/phase was started, a phase document exists in `docs/phases/`
- [ ] If a new phase was created, it has a row in the README index table
- [ ] If checklist items were completed, boxes are checked in both the phase document AND the README global checklist (if mirrored there)
- [ ] If a phase's completion percentage changed, the README status column was updated
- [ ] If dependencies changed, both the phase file header AND the README table were updated
- [ ] If a cross-cutting decision was made, it's recorded in the README's "Cross-cutting decisions" section
- [ ] If something is out of scope, it's recorded in the README's "Explicitly out of scope" section
- [ ] The README prose summary accurately reflects the current state of work
- [ ] No phase file exists without a README row, and no README row points to a missing file
- [ ] Status percentages are accurate (count `[x]` vs `[ ]` excluding deferred items)
