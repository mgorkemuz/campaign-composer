# AI sessions

Exports of the Claude Code sessions used to build this project, with API keys and personal data removed.
Each session has two files:

- `NN-name.md`: readable conversation. Tool calls and results are collapsed and truncated.
- `NN-name.jsonl`: the full conversation record (user and assistant turns, tool calls, tool results).

## How they are produced

1. Export the session from the Claude app (session menu → Export). This gives a zip.
2. Run the scrubber:

   ```bash
   make session IN=~/Downloads/session-export-<id>.zip NAME=01-setup-and-scaffold
   ```

The scrubber (`tools/sessionexport`) keeps only the main conversation. It drops subagent transcripts,
app metadata and internal request payloads. Then it:

- replaces the home directory with `~` and the OS username with `<user>`,
- replaces email addresses with `<email>` (commit-trailer and example domains are kept),
- removes the app's list of recently used local folders,
- applies extra `find => replace` rules from a gitignored `.scrub.local` file,
- refuses to write anything if a credential-shaped string or any private term is still present.
