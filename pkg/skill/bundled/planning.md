---
name: planning
description: Feature planning and task breakdown methodology - breaks complex work into manageable, testable increments. Use before starting any non-trivial feature implementation.
phase: planning
requires_todo: true
priority: 20
todo_template: |
  - Understand requirements and constraints
  - Design high-level architecture
  - Break into implementable tasks
  - Identify risks and dependencies
  - Estimate effort and timeline
---

# Planning

## Purpose

Effective planning transforms vague ideas into concrete, actionable work. Good plans reduce uncertainty, surface risks early, and keep implementation focused.

## The Planning Process

### 1. Understand Requirements

Before designing anything, ensure clarity on:

- **What problem are we solving?** - Core user need or business goal
- **Who is this for?** - Target users and their context
- **What defines success?** - Concrete acceptance criteria
- **What are the constraints?** - Technical limitations, time, resources

**Output:** Clear problem statement and success criteria.

### 2. Design Architecture

Sketch the high-level structure:

- **Components** - What are the major pieces?
- **Interfaces** - How do components interact?
- **Data flow** - How does information move through the system?
- **External dependencies** - What do we rely on?

Keep it high-level. Details come during implementation.

**Output:** Architecture diagram or written description.

### 3. Break Into Tasks

Decompose the work into implementable chunks:

- **Each task should be completable in 1-4 hours**
- **Tasks should be independently testable**
- **Order tasks by dependency** - What must happen first?
- **Identify parallel work** - What can be done concurrently?

Good task breakdown criteria:
- Can I write a test for this task's output?
- Is this small enough to review easily?
- Does this task have a clear "done" state?

**Output:** Ordered list of concrete tasks.

### 4. Identify Risks

What could go wrong?

- **Technical risks** - Unfamiliar tech, complexity, performance
- **Integration risks** - Dependencies on external systems or teams
- **Scope risks** - Feature creep, unclear requirements
- **Timeline risks** - Underestimated effort, blocking dependencies

For each risk, note:
- Likelihood (high/medium/low)
- Impact (high/medium/low)
- Mitigation strategy

**Output:** Risk register with mitigation plans.

### 5. Estimate & Sequence

Put it all together:

- **Estimate each task** - Use person-hours or story points
- **Add buffer for unknowns** - 20-30% for technical uncertainty
- **Sequence work** - Critical path first, parallel work when possible
- **Define milestones** - Checkpoint after every 3-5 tasks

**Output:** Timeline with milestones and checkpoints.

## TODO Integration

This skill **requires TODO tracking**. Your task list becomes the implementation plan:

```
Planning phase:
- Gather requirements and constraints
- Design architecture for [feature]
- Break [feature] into implementable tasks
- Identify risks and mitigation strategies
- Create detailed implementation TODOs

Implementation phase:
- [Task 1 from breakdown]
- [Task 2 from breakdown]
- ...
```

## Key Principles

- **Plan iteratively** - Don't try to plan everything upfront
- **Start simple** - MVP first, enhancements later
- **Build in feedback loops** - Regular checkpoints to course-correct
- **Document decisions** - Future you will thank current you
- **YAGNI ruthlessly** - Don't build what you don't need now

## When to Skip Planning

You can skip formal planning for:
- Single-file changes
- Obvious bug fixes with clear solutions
- Small refactorings with good test coverage

For everything else, at least do quick requirements + task breakdown.

## Anti-Patterns

- **Planning without understanding** - Creating tasks before clarifying requirements
- **Over-planning** - Spending more time planning than implementing
- **Under-planning** - Starting implementation without any task breakdown
- **Ignoring risks** - Assuming everything will go smoothly
- **Fixed estimates** - Not updating estimates as you learn more
