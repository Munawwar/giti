# Commit review and AI review helper

Build review tools into Giti, starting with manual review of a single committed snapshot. A review belongs to a repository, full commit SHA, and comparison view. Amending a commit starts a new review; old notes remain available. Branch and range review are deferred because stacked branches require an explicit base.

## Manual review

- Enter or exit review mode through Start Review / Exit Review beside the commit title. Mode changes never mark files or lines reviewed. Order title controls as Preview, Export, Start/Exit Review, then diff counts; keep Preview and Export visible throughout review mode (disabled until there are marks or comments) so marking never shifts the title. Replace author details with progress in a fixed-height area, and draw a 3px file-progress bar (green good to go, blue with comments, grey partial) in permanently reserved space below the header. Show previous/next file-to-review arrows, Add Comment, and Mark Reviewed / Unmark Reviewed (Alt+R) for only the selected file at its right. The title tooltip and an always-present Commit message expander show the full message.
- A file is in one of four states: not reviewed, partially reviewed, reviewed (good to go), or reviewed with comments. Commenting reviews the commented lines until the comment is deleted; a file comment makes the file at least partially reviewed. Unmarking clears marks but keeps comments, so a commented file stays partial. Reserve a narrow column after the change counts for the state: empty, grey ◐, green ✓, or blue ✓. Folders show their weakest descendant, independently of search and expansion.
- Select text across lines to mark or unmark those lines or comment on them. The context menu offers the hunk under the pointer as a shortcut to mark, comment on, or select it. Hunks are selection shortcuts; stored marks and comments refer to lines.
- Retain old and new line numbers and exact captured content. Added and removed lines keep their signs. Reviewed additions use muted gray-green; reviewed removals use muted gray-red; commented lines use a light blue that also marks them in the overview strip.
- Show comments inline as cards below their last line, pushing following lines down; file comments sit above the diff. Cards render Markdown (with typed line breaks) and offer Edit and Delete (confirmed in place). New comments and edits use the same inline card editor with Ctrl+Enter to save and Escape to cancel. Preview the same Markdown document as export. The preview has one flat Exit Preview action.
- Persist marks and comments in user application data, outside the AI cache. Save after every action, use atomic writes and serialize competing updates. Failed writes must be visible. At startup, clean up reviews last updated more than 30 days ago in the background, at most once every 24 hours across processes. Skip reviews being saved; export Markdown for longer retention.
- Keep merge-resolution and first-parent comparison reviews separate. Full-file context and whitespace toggles do not change the review identity.
- Export a single Markdown document using a save dialog that defaults to the repository root. Start with the commit subject, SHA, author, comparison, repository, and overall status; list comments by file (file comments first, then by line as `path:line` with the diff excerpt and a quoted comment); end with a file status table. Do not silently create files in the repository.
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
