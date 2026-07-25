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

Work sequentially through the list, one item at a time:

1. Review the contents of the goal file
2. Review the codebase
3. Think deeply and determine what steps are required to accomplish the goal.
4. Update the plan markdown file with a numbered list of small actionable steps to complete the goal, following the Format section above. These steps should be the size of a jira ticket.
5. Review each step and their related files. Try to determine if anything was missed, overlooked, or an unexpected problem/behavior may likely occur from the suggested change. Determine if any steps need to be updated, added, or removed and do so, keeping the file in the bare-list format — don't reintroduce headers or titles while editing.
6. Repeat step 5 with the perspective of a software developer, network/devops engineer, ceo, security expert, and end user.
7. Repeat step 6 until you no longer have any changes to make to the plan. If you cannot come to a conclusion in 3 iterations, then ask for my help.
