# Project Instructions

> **IMPORTANT**: This project uses `AGENT.md` for agent instructions.

## Instructions for Claude Code

**You MUST read `AGENT.md` before doing anything else.**

The `AGENT.md` file contains all project-specific instructions, workflows, and guidelines. Read it first to keep the full context in memory for this session.

All project instructions, tech stack details, architecture notes, and workflow guidelines are in:
- **`AGENT.md`** - Main project instructions (read this first)
- **`AgentUsage/`** - Detailed workflow guides and best practices

---

*This structure aligns with industry-standard agentic coding practices.*

## Pull request reviews

`pi-review` reviews every pull request. It checks out the merge commit, installs, and runs the code, so it
takes minutes rather than seconds. Its check is the **`review / review`** row of `gh pr checks`.

**Do not push while `pi-review` is reading.** The `pi-review` workflow cancels the run it superseded — it is
the only workflow here that does — so a push cancels the review in flight, and a cancelled run has already
read most of the pull request and spent most of what it was going to spend. Pushing to be fast is the
expensive choice, not the cheap one.

Read the `review / review` row and act on the state it reports:

| `review / review` on this head                   | What to do                                                                                                     |
| ------------------------------------------------ | -------------------------------------------------------------------------------------------------------------- |
| pending                                          | **Wait. Do not push.** A push cancels it and the wait starts over.                                              |
| skipped                                          | A draft, or a branch that is not in this repository. A draft can be marked ready; a fork is never reviewed.      |
| a review for this head                           | Push **once**, with every finding from it. One push per finding restarts the cycle and pays for the read again.  |
| failed, or cancelled by a push that was not yours | It will not post for this head. Say so on the thread, then push.                                               |
| still pending 35 minutes after your push          | The job allows 33 minutes from when it starts, and queue time is on top of that; past it, it is not coming. Say so on the thread, then push. |

A green `review / review` tick means a review was posted — the job exits non-zero when it cannot post — so
read the review rather than trusting the tick. A red check from CI is the one exception to all of the
above: fix it and push.
