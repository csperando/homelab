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

Work sequentially through the list, one item at a time:

1. If the item requires action outside the agent's own tools (e.g. registering an OAuth app, updating a GitHub secret, any manual/human step), stop and ask the user to confirm it's been done. Once confirmed, skip to step 7.
2. If the item involves writing code to the api, write the appropriate test first. TDD applies only at the api level (jest/supertest) — skip this step for vue/frontend work.
3. Implement the item.
4. Reconsider whether the tests from step 2 actually cover the edge cases for this item, and update them if they miss something. Don't stop to ask — just reassess and fix.
5. Run `npm run test:all` in `api/`. On failure, fix and retry, up to 3 attempts total. If it's still failing after 3 attempts, stop and ask the user for help.
6. Once tests pass, stage the changes and create a new git commit — one commit per plan item.
7. Delete the item from `plan.md` and move on to the next item.
