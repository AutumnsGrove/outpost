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

`pi-review` reviews every pull request. It checks out the head, installs, and runs the code, so it takes
minutes rather than seconds.

**Do not push while `pi-review` is reading.** Every workflow here cancels the run it superseded, so a push
cancels the review in flight — and a cancelled run has already read most of the pull request and spent
most of what it was going to spend. Pushing to be fast is the expensive choice, not the cheap one.

Look at the `review` row of `gh pr checks <number>` and act on the state it reports:

| `review` on this head                             | What to do                                                                                                      |
| ------------------------------------------------- | --------------------------------------------------------------------------------------------------------------- |
| pending                                           | **Wait. Do not push.** A push cancels it and the wait starts over.                                              |
| skipped                                           | The pull request is a draft. Mark it ready for review, then read this table again.                              |
| a review for this head                            | Push **once**, with every finding from it. One push per finding restarts the cycle and pays for the read again. |
| failed, or cancelled by a push that was not yours | It will not post for this head. Say so on the thread, then push.                                                |
| still pending about half an hour after your push  | It will not post. Say so on the thread, then push.                                                              |

Read the thread rather than only the check: the job can finish green having posted nothing at all, and no
comment is not no findings. A red CI job is the one exception to all of the above — fix it and push.
