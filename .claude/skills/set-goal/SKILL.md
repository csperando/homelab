---
name: set-goal
description: Defines the workflow for creating a goal.md from user's description of a new feature
argument-hint: <feature description>
---

## Context
User Input: $ARGUMENTS

## Current Goal
Read `.claude/tmp/goal.md` for the full context on why this work is being done. This might not exist.

## Audience

goal.md is written primarily for you (the agent) to read back in future sessions, and
secondarily for the user to skim for accuracy. It is not a narrative document. Prefer
short factual/technical bullets — decisions, constraints, file/service names — over
prose sections like "Why" essays or restated philosophy. If a sentence doesn't change
what gets built or reviewed, cut it.

## Instructions

Work sequentially through the list, one item at a time:

1. Review the user input ($ARGUMENTS) describing the requested feature.
2. Review the existing .claude/tmp/goal.md if present — determine whether this is a new goal or a revision to one already in progress.
3. Review the codebase for relevant existing context (related code, prior conventions). Do not pull specifics (file paths, project names, configuration details) from gitignored or otherwise private directories (e.g. a workspace/volume mount holding the user's own projects) into goal.md — that file is committed to this repo. Only reference this repo's own tracked files by name.
4. If the request is ambiguous or missing key details (scope, constraints, what's explicitly out of scope), ask the user before drafting.
5. Draft the goal as concise technical bullets: problem, why it's needed, in-scope/out-of-scope, and success criteria. State concrete decisions (names, file locations, conventions) rather than leaving them open where the request already implies an answer.
6. Review the draft from the perspective of a software developer, security expert, and end user — check for missing context, unclear scope, unstated assumptions, and any leaked references to private/gitignored content. Update as needed.
7. Write/update .claude/tmp/goal.md.
