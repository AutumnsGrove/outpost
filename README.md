<!-- TEMPLATE: START - This section will be removed after setup -->

# ⚡ Use This Template

[![Use this template](https://img.shields.io/badge/Use_this_template-2ea44f?style=for-the-badge&logo=github)](https://github.com/AutumnsGrove/BaseProject/generate)

**Quick Start:** Click the green button above → Clone your new repo → Run `bash setup.sh`

---

# BaseProject - Claude Code Template

A comprehensive project template with built-in Claude Code workflows, best practices, and extensive documentation for rapid development setup.

**What you get:** Git hooks • Multi-language support • Security defaults • 18 comprehensive guides • Claude-optimized workflows

## 🚀 Quick Start

### Option 1: New Project Setup

**Copy this prompt into Claude Code:**
```
I want to create a new project from the BaseProject template. Follow this workflow:

1. First, ask me for: project name, description, tech stack (Python/JS/Go), and what API keys I'll need
2. Clone https://github.com/AutumnsGrove/BaseProject (main branch) to /tmp
3. Copy to ~/Projects/[PROJECT_NAME] (exclude .git/)
4. Customize AGENT.md with my project details (Purpose, Tech Stack, Architecture)
5. Update README.md with project-specific info (title, description, features)
6. Init language dependencies (uv init for Python, npm init for JS, go mod init for Go)
7. Create directory structure: src/ and tests/ with proper init files for the chosen language
8. Generate secrets_template.json with my API key placeholders
9. Create TODOS.md with 3-5 starter tasks based on the project description
10. Run git init using global git config (user.name and user.email)
11. Ask if I want to install git hooks (recommended: yes, auto-detects language from files created in step 6)
12. If yes, run ./AgentUsage/pre_commit_hooks/install_hooks.sh
13. Ask if I want to install house-agents (recommended: yes, includes house-coder and house-planner)
14. If yes, check if ~/.claude/agents/house-research.md exists; if not, clone https://github.com/AutumnsGrove/house-agents.git and copy agents to ~/.claude/agents/
15. Make initial commit: "feat: initialize [PROJECT] from BaseProject template"
16. Display project summary and next steps including reminder about installed hooks and agents

Start by asking me for the project details.
```

Claude will interactively:
- Ask for project name, tech stack, and requirements
- Copy BaseProject template to your chosen location
- Customize AGENT.md with your project details
- Set up language-specific dependencies (pyproject.toml, package.json, etc.)
- Create proper project structure (src/, tests/)
- Generate secrets_template.json with your needed API keys
- Initialize git with proper configuration
- **Install git hooks (recommended)** - auto-detects your language and installs:
  - Code quality checks (Black/Ruff for Python, Prettier/ESLint for JS, gofmt for Go)
  - Security scanner (prevents committing API keys/secrets)
  - Test runner (blocks push if tests fail)
  - Dependency auto-updater (runs on branch switch)
- **Install house-agents (recommended)** - includes all 5 specialized agents:
  - house-research, house-git, house-bash (from upstream)
  - house-coder, house-planner (from AutumnsGrove fork)
  - Skips if agents already installed
- Create initial commit following our standards

---

### Option 2: Add to Existing Project

**Copy this prompt into Claude Code (run in your project directory):**

```
I want to add BaseProject structure to my CURRENT project. Follow this workflow:

1. Analyze my existing project: read README.md, AGENT.md, git history for commit patterns, detect tech stack and package managers, identify architecture (monorepo/single/etc), read TODOS.md if exists
2. Clone https://github.com/AutumnsGrove/BaseProject (main branch) to /tmp/bp
3. Copy AgentUsage/ to my project (preserve any existing AgentUsage/ files, only add new guides)
4. Intelligently merge AGENT.md: if exists, parse sections and merge BaseProject sections using markers like "<!-- BaseProject: Git Workflow -->". If doesn't exist, create from template with detected project details
5. Enhance .gitignore by merging entries (preserve existing, add missing from BaseProject)
6. Analyze commit messages and suggest adopting BaseProject conventional commit style if inconsistent
7. Check if using branches like dev/main and suggest workflow if not
8. Ask if I want to install git hooks (they auto-detect my language and back up existing hooks first)
9. If yes, run ./AgentUsage/pre_commit_hooks/install_hooks.sh interactively
10. Ask if I want to install house-agents (includes house-coder and house-planner for enhanced workflows)
11. If yes, check if ~/.claude/agents/house-research.md exists; if not, clone https://github.com/AutumnsGrove/house-agents.git and copy agents to ~/.claude/agents/
12. Generate/update TODOS.md with project-aware tasks
13. Create integration-summary.md report showing what was added/merged/skipped
14. Backup all modified files to ./.baseproject-backup-[TIMESTAMP]/
15. Cleanup /tmp/bp
16. Display next steps

Start by analyzing my current project.
```

Claude will intelligently:
- Analyze your existing project structure and conventions
- Detect tech stack from package files (package.json, pyproject.toml, etc.)
- Copy AgentUsage/ guides without overwriting existing files
- Merge AGENT.md sections with clear markers (preserves your content)
- Append missing .gitignore entries without removing existing ones
- Compare your commit style to BaseProject standards and offer suggestions
- **Optionally install git hooks** - backs up existing hooks, auto-detects language, installs appropriate quality/security hooks
- **Optionally install house-agents** - includes house-coder and house-planner, skips if already installed
- Create backup of all modified files before making changes
- Generate integration-summary.md showing exactly what was changed
- Respect your existing README.md (won't overwrite)
- Adapt to your project's existing structure

---

### Option 3: Update/Sync Existing BaseProject Installation

**Already using BaseProject? Keep your AgentUsage docs and hooks up to date!**

This option is for projects that already have BaseProject installed and want to sync with the latest updates.

**Quick Update (Automated Script):**

```bash
# In your project directory
curl -sSL https://raw.githubusercontent.com/AutumnsGrove/BaseProject/main/update_baseproject.sh | bash

# OR if you have BaseProject cloned locally:
bash /path/to/BaseProject/update_baseproject.sh
```

**What the Update Script Does:**

1. **Detects and migrates old folders**:
   - Finds old `ClaudeUsage` folders and migrates them to `AgentUsage`
   - Backs up everything before making changes

2. **Syncs AgentUsage folder**:
   - Updates all documentation guides with latest versions
   - Refreshes git hooks with newest implementations
   - Updates templates and examples
   - Shows detailed summary of added/updated/unchanged files

3. **Merges .gitignore entries**:
   - Adds new patterns from BaseProject
   - Preserves all your existing entries

4. **Preserves your customizations**:
   - ✅ Keeps your `AGENT.md` (project instructions)
   - ✅ Keeps your `README.md` (project documentation)
   - ✅ Keeps your `TODOS.md` (task list)
   - ✅ Keeps your `secrets.json` and `secrets_template.json`
   - ✅ Keeps all language-specific files (pyproject.toml, package.json, etc.)
   - ✅ Keeps all your source code

5. **Provides update summary**:
   - Creates `baseproject-update-summary.md` with detailed changes
   - Creates backup in `.baseproject-backup-[TIMESTAMP]/`
   - Shows exactly what was changed and why

**Manual Update (via Claude Code):**

```
I want to update my BaseProject installation. Follow this workflow:

1. Check current directory for existing AgentUsage or old ClaudeUsage folders
2. Clone https://github.com/AutumnsGrove/BaseProject (main branch) to /tmp/bp-update
3. Create backup of existing AgentUsage to .baseproject-backup-[TIMESTAMP]/
4. If ClaudeUsage folder exists:
   - Back it up to .baseproject-backup-[TIMESTAMP]/
   - Migrate contents to AgentUsage (preserve custom files, merge intelligently)
5. Sync AgentUsage folder from BaseProject:
   - Compare each file (add new, update changed, preserve unchanged)
   - Show summary of added/updated/unchanged files
   - Keep any custom guides I've added
6. Update .gitignore by merging new entries (don't remove my existing entries)
7. Ask if I want to reinstall git hooks with latest versions
8. If yes, run ./AgentUsage/pre_commit_hooks/install_hooks.sh
9. Generate baseproject-update-summary.md report showing all changes
10. DO NOT touch: AGENT.md, README.md, TODOS.md, secrets files, language configs, source code
11. Cleanup phase:
    - Delete old ClaudeUsage folder (if it was migrated)
    - Cleanup /tmp/bp-update
12. Commit the updates with message "chore: sync BaseProject AgentUsage guides" (do NOT include baseproject-update-summary.md in the commit - only commit the actual file changes)
13. Display summary and next steps

Start the update process.
```

**When to Use This Option:**

- ✅ Your project already has BaseProject installed (AgentUsage folder exists)
- ✅ You want to get the latest documentation guides and git hooks
- ✅ You're migrating from old "ClaudeUsage" naming to "AgentUsage"
- ✅ You want to keep your project setup but refresh the docs

**Example Output:**

```bash
$ bash update_baseproject.sh

╔══════════════════════════════════════════════════════════════╗
║         BaseProject Update/Sync Tool                        ║
╚══════════════════════════════════════════════════════════════╝

Checking for Old Folders
─────────────────────────
⚠ Found old 'ClaudeUsage' folder
✓ Backed up ClaudeUsage to .baseproject-backup-20251119-143500/
✓ Renamed ClaudeUsage → AgentUsage

Syncing AgentUsage Folder
──────────────────────────
✓ Added: house_agents.md
✓ Updated: git_guide.md
✓ Updated: pre_commit_hooks/pre-commit-secrets-scanner

Sync Summary:
✓ Added: 3 files
✓ Updated: 8 files
ℹ Unchanged: 12 files

Updating .gitignore
───────────────────
✓ Added 4 new entries to .gitignore

Update Complete!
────────────────
✓ AgentUsage folder synced with latest BaseProject
✓ Backup saved to: .baseproject-backup-20251119-143500/
✓ Update summary: baseproject-update-summary.md
```

---

### Option 4: Project Content Imported - Build From What's Here

**Already copied your project files in but still have all the template scaffolding? Use this!**

This is for when you've imported your existing project content (specs, license, scripts, tools, etc.) but the BaseProject template files (AGENT.md, README.md, TODOS.md) still have placeholder text.

**Copy this prompt into Claude Code:**
```
I've imported my project content into the BaseProject template, but the template files still have placeholders. Analyze everything and build out the project properly. Follow this workflow:

1. Deep Analysis Phase:
   - Read ALL files in the project to understand what exists
   - Identify: project specs, documentation, licenses, scripts, tools, configs
   - Detect tech stack from existing files (package.json, pyproject.toml, go.mod, etc.)
   - Map the current file structure and identify what belongs where
   - Note any existing README, SPEC, or documentation files that should replace template placeholders

2. Create Reorganization Plan:
   - Propose a clean directory structure based on what you found
   - Plan where specs/docs should live (e.g., move SPEC.md to docs/)
   - Identify which template files to replace vs. populate
   - List all files that will be created, moved, or modified
   - Present the plan and ask for confirmation before proceeding

3. Execute the Plan (after confirmation):
   - Create necessary directories (src/, docs/, tests/, etc.)
   - Move/reorganize files according to the plan
   - Update all file path references in moved files
   - Replace template AGENT.md with real project details extracted from specs
   - Replace template README.md with project-appropriate content
   - Generate TODOS.md with actual next steps based on project state
   - Update .gitignore for the detected tech stack
   - Create any missing standard files (LICENSE, CONTRIBUTING.md, etc. if content exists)

4. Cross-Reference & Validation:
   - Verify all internal links/references are updated
   - Check for orphaned files or broken references
   - Ensure no duplicate documentation exists
   - Validate directory structure matches tech stack conventions

5. Finalize:
   - Initialize/update language dependencies if needed
   - Offer to install git hooks appropriate for detected tech stack
   - Create initial commit with message "feat: scaffold project from imported content"
   - Display summary of changes: files created, moved, modified, deleted
   - List recommended next steps

Start by reading everything in the project directory.
```

Claude will:
- Discover your project by reading all imported files
- Understand specs, docs, scripts, and architecture from what you've brought in
- Create a tailored reorganization plan (asks for approval first)
- Move files to proper locations (specs → docs/, source → src/, etc.)
- Update all references in moved files so nothing breaks
- Transform template placeholders into real project documentation
- Generate actionable TODOS.md based on actual project state
- Set up proper structure for your detected tech stack
- Optionally install appropriate git hooks

**When to Use This Option:**
- ✅ You copied an existing project into the BaseProject template
- ✅ Template files (AGENT.md, README.md) still have placeholder text
- ✅ You have specs, licenses, scripts, or tools that need proper organization
- ✅ You want Claude to figure out the structure from what's there
- ✅ Files need to be reorganized (e.g., SPEC.md should move to docs/)

**Example Scenario:**
```
Your imported content:
├── SPEC.md              # Full project specification
├── my-tool.py           # Main script
├── LICENSE              # Your real license
├── ProjectReadme.md     # Your actual readme (not the template one)
├── config.yaml          # Project config
├── AGENT.md             # ← Still has template placeholders
├── README.md            # ← Still the template README
└── AgentUsage/          # Template guides

After running the prompt:
├── docs/
│   └── SPEC.md          # Moved, references updated
├── src/
│   └── my-tool.py       # Organized into src/
├── config.yaml
├── LICENSE
├── AGENT.md             # ← Populated with real project details from SPEC
├── README.md            # ← Replaced with ProjectReadme.md content + enhancements
├── TODOS.md             # ← Generated from actual project state
├── AgentUsage/
└── pyproject.toml       # Created for detected Python project
```

---

### Manual Setup

For full control over the setup process, see [TEMPLATE_USAGE.md](TEMPLATE_USAGE.md) for detailed step-by-step instructions.

<!-- TEMPLATE: END -->

---

## 📁 What's Included

```
BaseProject/
├── CLAUDE.md                   # Redirect to AGENT.md
├── AGENT.md                    # Main project instructions file
├── AgentUsage/                # Comprehensive workflow guides
│   ├── README.md               # Guide index
│   ├── git_guide.md            # Unified git workflow and conventional commits
│   ├── db_usage.md             # SQLite database with database.py interface
│   ├── secrets_management.md  # API key handling
│   ├── code_style_guide.md    # Code style principles
│   ├── project_setup.md       # Project initialization patterns
│   ├── uv_usage.md            # Python UV package manager
│   ├── testing_strategies.md  # Test patterns
│   ├── docker_guide.md        # Containerization
│   ├── ci_cd_patterns.md      # GitHub Actions
│   ├── house_agents.md        # Claude subagent usage
│   ├── pre_commit_hooks/      # Git hooks for code quality
│   ├── templates/             # Template files for common configs
│   └── ... (18 total guides)
└── .gitignore                  # Comprehensive gitignore
```

---

## 🏠 House Agents Integration

This template works seamlessly with [house-agents](https://github.com/AutumnsGrove/house-agents) - specialized Claude Code sub-agents that keep your context clean.

### What Are House Agents?

Specialized sub-agents that run heavy operations in separate context windows:
- **house-research** - Search 70k+ tokens across files, return 3k summary (95% savings)
- **house-git** - Analyze 43k token diffs, return 500 token summary (98% savings)
- **house-bash** - Process 21k+ command output, return 700 token summary (97% savings)
- **house-coder** - Small code patches (0-250 lines), instant TODO implementations
- **house-planner** - Task orchestration & planning for multi-file changes

### Quick Install

**Project-Level (this project only):**
```bash
# Clone the fork with house-coder and house-planner agents
git clone https://github.com/AutumnsGrove/house-agents.git /tmp/house-agents
cp -r /tmp/house-agents/.claude .
```

**User-Wide (all projects):**
```bash
# Clone the fork with house-coder and house-planner agents
git clone https://github.com/AutumnsGrove/house-agents.git /tmp/house-agents
mkdir -p ~/.claude/agents

# Skip if agents already exist, otherwise copy them
if [ ! -f ~/.claude/agents/house-research.md ]; then
  cp /tmp/house-agents/.claude/agents/*.md ~/.claude/agents/
  echo "House agents installed successfully!"
else
  echo "House agents already exist, skipping installation"
fi
```

**Test Installation:**
```
Use house-research to find all TODO comments in the codebase
```

See [AgentUsage/house_agents.md](AgentUsage/house_agents.md) for usage patterns and examples.

**Credit:** House Agents originally by [@houseworthe](https://github.com/houseworthe/house-agents), enhanced fork with house-coder and house-planner by [@AutumnsGrove](https://github.com/AutumnsGrove/house-agents)

---

## 🪝 Extended Hook Collection

For additional Claude Code hooks and advanced git automation, check out [**Hooks**](https://github.com/AutumnsGrove/Hooks) - a comprehensive collection of productivity-enhancing hooks:

**Key Features:**
- **Comprehensive automation toolkit** - Claude Code event hooks (PreToolUse, PostToolUse, UserPromptSubmit) plus Git pre-commit hooks with auto-language detection
- **Built-in security safeguards** - Pre-commit secrets scanner blocks 15+ secret patterns (Anthropic, OpenAI, AWS, GitHub, etc.) with remediation guidance
- **Cross-machine sync** - Git-based hook management for easy deployment across multiple development environments

**Quick Install:**
```bash
git clone https://github.com/AutumnsGrove/Hooks.git ~/.claude/hooks-collection
# Follow repo README for specific hook installation
```

This pairs perfectly with BaseProject's built-in git hooks for a complete automation setup.

---

## 🎯 What You Get

### Instant Best Practices
- **Git workflow patterns** - Conventional commits, unified git guide
- **Database architecture** - SQLite with isolated database.py interface
- **Security by default** - API key management, secrets scanning hooks
- **Code quality hooks** - 8 production-ready git hooks for Python, JS, Go, multi-language
  - `pre-commit-secrets-scanner` - Prevents committing API keys (15+ patterns)
  - Language-specific formatters (Black, Prettier, gofmt) and linters
  - Auto-run tests before push, auto-update deps on branch switch
  - Interactive installer with auto-detection
- **Testing strategies** - Unit, integration, and E2E test patterns
- **CI/CD templates** - GitHub Actions workflows
- **Documentation standards** - Consistent, scannable docs

### Claude-Optimized Workflows
- **House agents** - Specialized agents for research, coding, git analysis
- **Context7 integration** - Automatic library documentation fetching
- **TODO management** - Task tracking integrated into workflow
- **Subagent patterns** - Breaking down complex tasks

### Multi-Language Support
Guides and patterns for:
- Python (with UV package manager)
- JavaScript/TypeScript
- Go
- Rust
- Docker containerization

---

## 📚 Documentation Structure

All guides follow a consistent, scannable format:

1. **Overview** - What the guide covers
2. **When to Use** - Specific triggers and scenarios
3. **Core Concepts** - Key principles
4. **Practical Examples** - Real-world code
5. **Common Pitfalls** - What to avoid
6. **Related Guides** - Cross-references

See [AgentUsage/README.md](AgentUsage/README.md) for the complete index.

---

<!-- TEMPLATE: START -->

## 🛠️ Customization Workflow

After running setup:

1. **Edit AGENT.md** - Fill in your project specifics
   - Project purpose
   - Tech stack
   - Architecture notes

2. **Create secrets files** (if needed)
   ```bash
   # For Python projects
   cp AgentUsage/templates/secrets_template.json secrets_template.json
   cp secrets_template.json secrets.json
   # Edit secrets.json with real API keys
   ```

3. **Set up dependencies**
   ```bash
   # Python with UV
   uv init

   # JavaScript/Node
   npm init -y

   # Go
   go mod init yourproject
   ```

4. **Install git hooks** (recommended)
   ```bash
   # Interactive installer (auto-detects your language)
   ./AgentUsage/pre_commit_hooks/install_hooks.sh

   # This installs:
   # - Code quality checks (formatters + linters)
   # - Security scanner (prevents API key leaks)
   # - Test runner (blocks push if tests fail)
   # - Dependency auto-updater
   ```

5. **Update TODOS.md** - Add your specific tasks

<!-- TEMPLATE: END -->

---

## 💡 Key Workflows

### Starting a New Feature
1. Check `TODOS.md` for pending tasks
2. Use Context7 to fetch relevant library docs
3. Follow git workflow for commits
4. Update TODOS.md as you progress

### Managing Secrets
1. Read `AgentUsage/secrets_management.md`
2. Create `secrets.json` (gitignored)
3. Provide `secrets_template.json` for team
4. Use environment variable fallbacks

### Large Codebase Search
1. Use house-research agent for 20+ file searches
2. Check `AgentUsage/house_agents.md` for patterns
3. Use subagents for complex multi-step tasks

### Writing Tests
1. Review `AgentUsage/testing_strategies.md`
2. Follow framework-specific patterns
3. Use test-strategist agent for planning

---

## 🔐 Security Defaults

This template includes security best practices by default:

- ✅ `secrets.json` in `.gitignore`
- ✅ **Pre-commit secrets scanner** - Detects 15+ secret patterns before commit
  - Anthropic, OpenAI, AWS, GitHub, Google API keys
  - JWT tokens, bearer tokens, private keys
  - Hardcoded passwords and database credentials
  - Actionable fix instructions when secrets detected
- ✅ Environment variable fallback patterns
- ✅ Security audit guides in `secrets_advanced.md`

---

## 🤝 Working with Claude Code

This template is optimized for Claude Code CLI. Key features:

- **AGENT.md** contains all project instructions (read via CLAUDE.md redirect)
- **Structured guides** for quick reference without token bloat
- **Subagent workflows** for complex tasks
- **Git commit standards** with auto-formatting

### Example Session
```bash
cd ~/Projects/MyNewProject/

# Claude automatically reads CLAUDE.md → AGENT.md and knows your project context
claude "Add user authentication with JWT tokens"

# Claude will:
# 1. Check TODOS.md
# 2. Use Context7 to fetch JWT library docs
# 3. Implement following your git commit standards
# 4. Update TODOS.md
# 5. Commit with proper message format
```

---

## 📖 Learning Path

Recommended reading order for new projects:

1. [project_structure.md](AgentUsage/project_structure.md) - Directory layouts
2. [git_guide.md](AgentUsage/git_guide.md) - Version control and conventional commits
3. [db_usage.md](AgentUsage/db_usage.md) - Database setup (if using databases)
4. [secrets_management.md](AgentUsage/secrets_management.md) - API keys
5. [uv_usage.md](AgentUsage/uv_usage.md) - Python dependencies (if applicable)
6. [testing_strategies.md](AgentUsage/testing_strategies.md) - Test setup
7. [house_agents.md](AgentUsage/house_agents.md) - Advanced workflows

---

## 🆘 Troubleshooting

<!-- TEMPLATE: START -->

### "Git not initialized"
```bash
git init
git add .
git commit -m "Initial commit"
```

### "AGENT.md not found"
If you see this error, the setup script may not have run properly. Make sure you've run `bash setup.sh` in your project directory.

<!-- TEMPLATE: END -->

### "Pre-commit hooks not working"
```bash
chmod +x AgentUsage/pre_commit_hooks/*
./AgentUsage/pre_commit_hooks/install_hooks.sh
```

See [AgentUsage/pre_commit_hooks/TROUBLESHOOTING.md](AgentUsage/pre_commit_hooks/TROUBLESHOOTING.md) for comprehensive hook troubleshooting.

---

<!-- TEMPLATE: START -->

## 🔄 Keeping BaseProject Updated

To get updates from BaseProject while preserving your customizations, use the **automated update script** (see [Option 3](#option-3-updatesync-existing-baseproject-installation) above):

```bash
# Recommended: Use the update script
bash update_baseproject.sh

# OR download and run directly:
curl -sSL https://raw.githubusercontent.com/AutumnsGrove/BaseProject/main/update_baseproject.sh | bash
```

**What gets updated:**
- ✅ AgentUsage/ guides and documentation
- ✅ Git hooks in AgentUsage/pre_commit_hooks/
- ✅ .gitignore entries (merged, not replaced)

**What stays unchanged:**
- ✅ Your AGENT.md, README.md, TODOS.md
- ✅ Your secrets files
- ✅ Your source code and configs

**Manual update (for specific files only):**
```bash
# Copy a specific guide
cp /path/to/BaseProject/AgentUsage/new_guide.md AgentUsage/

# Review and commit
git diff AgentUsage/
git add AgentUsage/
git commit -m "chore: update specific AgentUsage guide"
```

## 🎉 What's Next?

After setup:

1. **Customize** - Edit AGENT.md with your project details
2. **Explore** - Read guides in AgentUsage/ directory
3. **Build** - Start coding with Claude Code
4. **Iterate** - Update TODOS.md and guides as needed

<!-- TEMPLATE: END -->

---

## 📝 Contributing

Found a better pattern? Want to add a guide?

This template uses a **two-branch strategy**:
- **`main` branch** - Clean, user-facing template (you're here)
- **`dev` branch** - Template development and maintenance

### For Template Development:
1. Check out the [dev branch](https://github.com/AutumnsGrove/BaseProject/tree/dev)
2. Read [CONTRIBUTING.md](https://github.com/AutumnsGrove/BaseProject/blob/dev/CONTRIBUTING.md) for full workflow
3. Make changes in dev branch
4. Test thoroughly before merging to main

### For Quick Improvements:
1. Add your guide to `AgentUsage/`
2. Update `AgentUsage/README.md` index
3. Follow the documentation standards in `AgentUsage/documentation_standards.md`
4. Commit with proper message format

---

## 📄 License

This template is provided as-is for use with Claude Code. Customize freely for your projects.

---

**Last updated:** 2025-10-19
**Maintained for:** Claude Code CLI
**Guides:** 16 comprehensive workflow documents
