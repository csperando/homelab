---
name: fix-plan
description: Defines the workflow for diagnosing and recovering from an unexpected issue hit while executing plan.md via the tdd skill
argument-hint: <description of the unexpected issue>
---

## Context
User Input: $ARGUMENTS

## Current Goal
Read `.claude/tmp/goal.md` for the full context on why this work is being done. Treat it as
fixed, read-only context — this skill never edits goal.md. If the issue seems to actually
invalidate the goal itself (not just the plan), stop and tell the user to reconsider it via
the set-goal skill instead; that's out of scope here.

## Current Plan
Read `.claude/tmp/plan.md` for the remaining, not-yet-completed steps. If it's missing or
empty, stop — there's nothing left to fix.

## Format

Any rewrite of plan.md must follow the same format set-plan enforces: ONLY a numbered
list — no top-level title, no section headers, no preamble, no bolded per-task titles.
Fold detail into the single line for each item.

## Instructions

1. Review the user's description of the unexpected issue ($ARGUMENTS).
2. Review goal.md and the remaining items in plan.md for context.
3. Read `.claude/tmp/tdd-start-sha` and review `git log <that-sha>..HEAD` to see exactly
   which commits belong to this tdd session — never consider commits before this point. If
   the marker file doesn't exist, stop and ask the user for the correct commit range
   instead of guessing.
4. Diagnose the issue as one of:
   - Local: the remaining plan can adapt without touching anything already committed.
   - Structural: something already committed rests on a wrong assumption and needs to be
     undone before continuing.
5. If local, draft the revised plan.md: update, add, reorder, or remove remaining steps to
   route around the issue. Leave completed history untouched.
6. If structural, identify exactly which commit(s) within the session's range are
   implicated, and draft the corresponding plan.md item(s) back in — rewritten to reflect
   what's now known — since tdd deletes each item once completed and nothing else records
   that it needs redoing.
7. Present the diagnosis and proposed fix to the user in plain terms: what went wrong,
   what you propose, and what it means for already-completed work. Never run git yourself
   — if a rollback is warranted, give the user the exact commands (prefer `git revert`
   over `git reset --hard` unless they ask otherwise), scoped strictly to commits within
   this session's range, and let them decide whether and how to run them.
8. Ask the user to explicitly approve, adjust, or reject the proposal. Stop and wait for
   their reply.
9. Repeat steps 5-8, incorporating their feedback, until they explicitly approve.
10. Once approved, write the updated plan.md, and tell the user they can resume with the
    tdd skill.
