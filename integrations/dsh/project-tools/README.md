# Isolated project tools for Harness

Install this optional bundle from the source checkout in a dedicated evaluation profile. It exposes `project_open`, `project_save` and `project_check` for a host-selected project. The same tools work with UM-MEM, another memory backend or memory disabled.

Configure `directory` with the evaluation workspace, `node` with a Node.js 24 executable, and `sources` with the relative paths of the authoritative input documents. The verification tools accept quotes actually delivered to the session and the configured documents as primary sources. They preserve exact date literals where they can match a calendar date in the source.

`project_check` batches existing scripts, checks JSON values by key or JSON Pointer, and probes a CLI with malformed rows derived from a valid sample. A failed probe can trigger one repair pass. `project_save` can verify a batch after writing and prevents invalid JSON from replacing valid JSON or entering a checked output.
