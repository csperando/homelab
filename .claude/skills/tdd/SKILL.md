---
name: tdd
description: Defines the workflow for how to accomplish tasks laid out by a provided plan.md file
---

## Current Goal
Read `.claude/tmp/goal.md` for the full context on why this work is being done before starting.

## Current Plan
Read `.claude/tmp/plan.md` for the ordered list of tasks to work through.

## Instructions

A list of work items is provided in `.claude/tmp/plan.md`. If not, then stop.

Before working through any items: if `.claude/tmp/tdd-start-sha` doesn't already exist, create it containing the current `git rev-parse HEAD`. This marks where this session's commits begin, so the fix-plan skill can bound its git history review to only this
session's work — do not overwrite it if it already exists, since a re-invocation partway through plan.md must keep the original start point.

A plan item is not complete, and you must not move on to the next item, until it has been
deleted from `plan.md` — a passing test run and a commit alone do not finish an item. If
you're using TaskCreate/TaskUpdate to track progress in this session, that is separate and
never a substitute for actually editing `plan.md`.

If at any point during an item you discover the issue is structural — a wrong assumption
already baked into completed work, or something the remaining plan can't simply route
around — stop immediately and tell the user, suggesting they run the fix-plan skill,
rather than continuing to force the original approach.

Work sequentially through the list, one item at a time:

1. If the item requires action outside the agent's own tools (e.g. registering an OAuth app, updating a GitHub secret, any manual/human step), stop and ask the user to confirm it's been done. Once confirmed, skip to step 6.
2. If the item touches an area of the codebase with an established test framework/harness, write the appropriate test first, following that area's existing test conventions. Skip this step for areas with no established test setup — implement directly instead.
3. Implement the item.
4. Reconsider whether the tests from step 2 actually cover the edge cases for this item, and update them if they miss something. Don't stop to ask — just reassess and fix.
5. If a test suite applies (per step 2), run it using this project's actual test command (check `package.json` scripts, a Makefile target, README, or existing CI config for the right one — e.g. `go test ./...`, `npm test`). On failure, fix and retry, up to 3 attempts total. If no test suite applies, instead verify the change builds/vets cleanly with whatever tooling exists (e.g. `go build ./...`, `go vet`, a linter). If it's still failing after 3 attempts, stop and ask the user for help — if the failures point to a structural problem rather than a simple bug, suggest they run fix-plan instead of continuing to retry.
6. The moment step 5 passes (or, for manual items, the moment step 1 is confirmed): delete the completed item from `plan.md`, then read the file back to confirm it's actually gone. If it's still there, delete it now — do not proceed until confirmed.
7. If this item involved code changes, stage them and create a new git commit — one commit per plan item. Do not add Claude as a coauthor. Manual/human-confirmation items handled via step 1 have nothing to commit — skip this step for those.
8. If `plan.md` is now empty, delete `.claude/tmp/tdd-start-sha`, `.claude/tmp/plan.md`, and `.claude/tmp/goal.md` — this session is complete. Otherwise, move on to the next item.
