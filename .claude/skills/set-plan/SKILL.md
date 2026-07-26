---
name: set-plan
description: Defines the workflow for creating a plan.md file based on a provided goal.md
---

## Current Goal
Read `.claude/tmp/goal.md` for the full context on why this work is being done before starting.

## Current Plan
Read `.claude/tmp/plan.md` for the ordered list of tasks to work through. It might not exist.

## Format

plan.md contains ONLY a numbered list — nothing else. No top-level title, no section
headers, no preamble/context block, no bolded per-task titles. Each numbered item is one
plain-text line/paragraph describing a single actionable task; no nested sub-bullets.
Fold any detail (file paths, config keys, concrete decisions) into that single line
rather than breaking it out into its own structure.

## Instructions

A summary of the goal is provided in `.claude/tmp/goal.md`. If not, then stop.

Do not report plan.md as finished until it has been refined from every perspective below
AND the user has explicitly approved it in this session — do not stop merely because you
have run out of changes to make yourself.

"Refine" means critically re-examining the current plan for gaps, risks, unnecessary
steps, or drift from goal.md, then editing plan.md to fix what you find — not simply
re-reading it unchanged. Every refine pass must keep strictly to the bare numbered-list
format above; never reintroduce headers, titles, or preamble while editing. If any refine
pass surfaces something you can't confidently resolve yourself, don't guess silently —
note it as an open question to raise at step 11.

Work sequentially through the list, one item at a time. Do not skip any steps:

1. Review the contents of the goal file — this is the reason the plan exists; every step below must trace back to it.
2. Review the codebase for relevant existing context.
3. Think deeply and determine what steps are required to accomplish the goal.
4. Write the plan markdown file with a numbered list of small actionable steps to complete the goal, following the Format section above. These steps should be the size of a jira ticket.
5. Refine the plan from the perspective of a software developer — look specifically for edge cases, missing error handling, and tech debt the approach would introduce.
6. Refine the plan from the perspective of a network/devops engineer — look specifically for deployment/rollback gaps, observability, and infrastructure failure modes.
7. Refine the plan from the perspective of a CEO.
8. Refine the plan from the perspective of a security expert — look specifically for antipatterns, vulnerabilities, and secrets handling.
9. Refine the plan from the perspective of an end user — look specifically for UX regressions and workflow disruption.
10. Refine the plan once more specifically against goal.md: does every step still serve the goal's "what" and "why"? Cut steps that no longer do, and add steps for anything the goal requires that's missing.
11. Present the current plan.md to the user and ask them to explicitly approve it or request changes — don't leave it open-ended. Stop and wait for their reply before proceeding.
12. Repeat steps 5-11, incorporating the user's feedback, until they explicitly approve the plan. If you and the user have not converged after 3 full iterations, stop and tell them directly you're stuck, summarizing the open disagreements, rather than continuing to iterate on your own.
