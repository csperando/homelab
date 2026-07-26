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

goal.md is written primarily for you (the agent) to read back in future sessions, and secondarily for the user to skim for accuracy.
It is not a narrative document. Prefer short factual/technical bullets over prose sections.

## Purpose

The goal must communicate "what" the task is and "why" it is being worked on.
It exists to guide the later creation of plan.md; it is not the plan itself. 
Do not include specific actionable implementation steps, task breakdowns, or sequencing here — that decomposition belongs solely to the set-plan skill.
You may still surface constraints: antipatterns to avoid (framed as boundaries, not as
directions toward a specific implementation), relevant prior architecture decisions, and
specific details the user has already provided.

## Instructions

Do not report goal.md as finished, and do not stop asking questions, until the user has
explicitly confirmed it is accurate and complete in this session.

When asked to refine an existing goal, treat every stage of development and
implementation as open for reconsideration. Formulate specific questions and ask the
user directly — do not silently resolve them yourself.

Work sequentially through the list, one item at a time. Do not skip any steps:

1. Review the user input ($ARGUMENTS) describing the requested feature.
2. Review the existing .claude/tmp/goal.md if present — determine whether this is a new goal or a revision to one already in progress.
3. Review the codebase for relevant existing context (related code, prior conventions). Do not pull any project specifics (file paths, project names, configuration details, etc.) from the workspace directory bind-mounted into the container — see the `WORKSPACE` mount in `docker-compose.yml` for its current path. That directory holds the user's own private projects, not this repo's.
4. If the request is ambiguous or missing key details (scope, constraints, what's explicitly out of scope), ask the user before drafting.
5. Draft the goal as concise technical bullets: problem, why it's needed, in-scope/out-of-scope, and success criteria. State concrete decisions (names, file locations, conventions) rather than leaving them open where the request already implies an answer.
6. Refine the draft from the perspective of a software developer
7. Refine the draft from the perspective of a security expert
8. Refine the draft from the perspective of the end user
9. Ask the user to explicitly confirm the goal or request changes — don't leave it as an open-ended "any thoughts?" Stop and wait for their reply before proceeding to step 10.
10. Write/update .claude/tmp/goal.md
11. Repeat steps 6-10 until the user explicitly confirms the goal is accurate and complete — do not stop merely because you have run out of questions of your own.
