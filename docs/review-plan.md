# Commit review and AI review helper

Build review tools into Giti, starting with manual review of a single committed snapshot. A review belongs to a repository, full commit SHA, and comparison view. Amending a commit starts a new review; old notes remain available. Branch and range review are deferred because stacked branches require an explicit base.

## Manual review

- Mark a whole file reviewed from its header or a checkbox in the file selector. Show partial state for files with line review marks.
- Select text across lines to mark or unmark those lines and add a comment. Right-click any line within a hunk to select the entire hunk. Hunks are selection shortcuts; stored marks and comments refer to lines.
- Retain old and new line numbers and exact captured content. Added and removed lines keep their signs. Reviewed additions use muted gray-green; reviewed removals use muted gray-red. Comments have a separate indicator and never imply review completion.
- Add file comments from the file header and line comments from the selection menu. Provide a review notes view to read, edit, and delete comments.
- Persist marks and comments in durable user application data, outside the AI cache. Save after every action, use atomic writes and serialize competing updates. Failed writes must be visible.
- Keep merge-resolution and first-parent comparison reviews separate. Full-file context and whitespace toggles do not change the review identity.
- Export a single Markdown document using a save dialog. Include repository directory, full SHA, comparison, file paths, reviewed status, old/new line numbers, selected added/removed/context content, and comments. Do not silently create files in the repository.
- Restrict manual review to commits. Staged and unstaged changes remain ordinary diff views.

## AI review helper

- Add an AI review helper button with tooltip: Lost? Let an LLM tell you what to review.
- Detect installed Codex, Claude, OpenCode, and Pi CLIs. On click, discover available models and let the user select provider/model. Claude can use supported aliases and a custom model ID if model discovery is unavailable.
- Review the selected commit. Allow Git and source inspection. Prefer the harness planning/review mode; where unavailable, use the requested full-permission fallback with an explicit instruction not to edit files or the system.
- Stream assistant output into a cached Markdown file. Render Markdown and Mermaid diagrams in a separate AI review guide item above the file selector. The item is available directly whenever a saved or running guide exists.
- Generation continues when the user navigates elsewhere; rendering pauses while the guide is hidden. A new request replaces the previous completed guide for that review after success. Failed output is marked incomplete and preserves the previous completed guide.
- Store one last guide per repository, commit, and comparison, rather than accumulating provider/model variants. Use Go's platform cache directory. Check cleanup at startup at most once per 24 hours; remove guides unused for over 10 days and skip active generations.
- Read relevant history as context without expanding the selected review scope. Inspect source at the selected revision, which may differ from the current checkout. Branch labels are context, not proof of the original branch.

Prompt:

> Help the user review commit <SHA> in <directory>, using the selected <comparison>. Use Git and inspect relevant source at that revision. Do not edit files or modify the system; your job is to help the user review Git changes. First give an ordered review plan grouped by purpose, with exact file paths and 1–2 lines per group. Then explain the changes using Markdown and suitable Mermaid architectural diagrams, sequence diagrams, flowcharts, state diagrams, or UML class diagrams. Choose diagrams that clarify the change. Inspect relevant surrounding history when helpful, but distinguish in-scope changes from historical context and verified facts from inference. Account for changes outside the main groups.

## Delivery order and verification

Implement manual marks, persistence, comments, and export first; AI integration follows separately. Verify with temporary Git repositories and GTK integration tests covering real selection, persistence across reopen, comparison isolation, comment editing, and Markdown output. Avoid tests that merely repeat implementation branches. Build a debug binary for interactive inspection; keep GNOME installation changes explicit.
