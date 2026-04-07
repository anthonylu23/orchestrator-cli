Build tests as you work when needed. If test coverage is insufficient, update/add tests.

Ask questions when ambiguity materially affects implementation, behavior, safety, or scope. Otherwise proceed with clear, documented assumptions.

When starting with fresh context, review docs in `/docs` that are relevant to the current task.

Update docs and `README.md` as needed while implementing. Document task status and codebase changes (architecture, implementation details, usage, and workflow impacts). Add new files to `/docs` as necessary.

Update next steps in docs.

If you are working in parallel with other agents:

- Define clear file ownership boundaries.
- Avoid overlapping edits when possible.
- If overlap is required, coordinate ordering and rebase/merge expectations.

**Feel free to modify `AGENTS.md` when needed.** Reread it whenever it changes. If unchanged, do not reread just to avoid context bloat.

Be sensible with typing, file naming, file organization, etc.

When reviewing code, be detailed and rigorous about typing, logic, robustness, behavior consistency, and tests. Report findings clearly with severity and impact. Don't make assumptions: ask questions:

Before making changes, inspect existing git changes for task context.

If changes should not go directly to `main`, tell the human what to do next (for example: branch, unstage, split commits).

Approvals are not needed for **web search**; use web search when necessary.

**Run tests** following implementation. Ensure types, lint checks, etc.

**If deemed necessary suggest refactors, and I will verify whether or not to make the changes.**

When debugging errors, first determine whether the failure is due to an unimplemented feature versus a regression.

### Committing Practices

When committing to git, avoid bulk commits by default. Group files into coherent, scoped commits so history is useful.

If the human explicitly asks for a single bulk commit, follow that instruction.

Follow standard git best practices.

Don't auto commit without human approval.
