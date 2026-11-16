# Live local panel

`um-memory dashboard --repo-root ... --workspace ... --profile ... --backend ...`
opens the selected store on the Go loopback service. `um-memory memory-map`
exports a static HTML map with `--output-path`. No cloud account or frontend
build is needed; the HTML, stylesheet, and JavaScript ship with the Go service.

The frontend is off by default, even while agents use memory. Opening it with
the command or desktop shortcut activates a browser session. The power button
or `um-memory dashboard --stop` closes panel sessions without stopping agent memory.
After the last browser stream disconnects, the panel deactivates within about
60 seconds. A new activation clears the transient feed and rotates its access
capability. An unsaved draft remains visible in an open stopped tab for copying;
it is not persisted to local storage.

`um-memory dashboard` also starts a background Go service when no listener is
running, so its browser session survives the CLI exiting. `--no-browser`
prints the URL for a desktop launcher. The service's idle reaper closes it once
browsers and agents stop using it. After replacing Go binaries, restart the
shared listener and reopen the panel; running processes do not hot-reload code.

## One memory, multiple views

The panel runs on the loopback HTTP listener used by the MCP clients. It does
not create another memory engine, write database rows directly, or introduce a
second content store. Edits use the existing operator lease and canonical writer.
The shared service publishes bounded content-free events after dispatched
operations, including operations whose caller timed out after commit. Background
index refresh produces a separate invalidation; it does not claim that every
native projection has completed.

The sidebar is a collapsible block hierarchy. Its disclosure buttons only expand
or collapse navigation; selecting a name opens that branch and preserves the
current graph/list mode. Counts include descendants, even when a branch is folded.
Selection reveals its ancestors, and disclosure state is saved per workspace/profile.
**Найти блок** filters names case-insensitively and reveals the ancestor paths of
matches without overwriting saved folds. Clearing it restores the ordinary tree.
**Скрыть пустые ветки** removes branches with zero records across all descendants
from navigation and the graph. A parent with records below it remains visible;
the currently focused graph root remains available even when empty. This preference
is saved per workspace/profile. Catalog totals stay unchanged, and graph captions
distinguish visible children from the full child count when some are hidden.
Ordinary selection keeps the selected branch in the whole graph, with its ancestors
and sibling blocks visible. Unrelated expanded subtrees are shown folded while
reading a branch, without discarding their saved expansion state. **Фокус**
explicitly isolates the selection; selecting another block returns to context.
**← Вся память** in the breadcrumb path returns to all memory; ancestor names
in that path move up the hierarchy. The bottom **Весь граф** button fits the full catalog
with its expanded branches without changing the record selection.
Click a graph block or press Enter to inspect it without changing the scope or
camera. Use the sidebar to enter a branch;
use its plus/minus button or Space to expand or collapse. Arrow keys navigate the graph. Use breadcrumbs or Backspace
to go up, and Alt+Enter on a block to inspect its description and catalog links.
Branch colors are presentation only; record trust is shown separately as a
named status.

The atlas uses compact block and record cards on a plain canvas. Branch color is
a small accent in navigation and node icons; selected nodes have a subtle outline.
Familiar branch names such as Research, Findings, Sources and Sessions have distinct
colors. Record cards expose type and status independently of branch color; dates
appear on hover/focus or in the reading column. The toolbar's details action opens the description,
child blocks and up to six direct records in the reading column. Record counts
appear in the list heading rather than a separate statistics panel. **Упорядочить**
restores automatic layout after manual dragging without changing memory topology.
Secondary actions (expand all, collapse, focus, arrange and the optional minimap)
live in the **⋯** menu. The minimap starts hidden. Scope selection is in the
**Фильтры** drawer, while scope counts and active filters remain visible below search.
The project selector lists projects with current committed records in the selected
workspace; an exact project ID can also be entered. Block and record counts follow
the selected workspace or project scope.
The keyboard button in the footer opens the navigation shortcuts.

Tree columns express nesting, and node bounds include wrapped labels so sibling
branches do not overlap. Expanded blocks show their records on the graph, six
at a time with an **Ещё записи** node for more. Toolbar buttons expand all blocks,
collapse branches or fit the whole graph. A large catalog becomes small in the
overview; focus a branch or zoom in to read its labels. **Записи** switches to
record cards for browsing and search. Opening a record uses a separate reading
overlay on desktop and narrow screens. It does not resize the canvas, re-fit the
camera or change node coordinates. The first snapshot includes
the full canonical block catalog, service health and up to 64 recent workspace
records. The record list defaults to **С вложенными блоками**; **Только этот блок**
restricts it to direct membership. The selected scope also applies to search.
The list reads all existing exact-block summary pages in the selected scope
before sorting them by `recorded_at` (**Сначала новые** by default, or
**Сначала старые**). Date sorting crosses block and API-page boundaries; equal
dates retain stable traversal order, and missing/invalid dates stay at the end
in both directions. Cards are displayed in batches of 64 using **Ещё записи**.
Displayed/total counts describe this scope; totals come from the
latest catalog, so concurrent writes can change them on refresh. A failed page
can be retried without skipping earlier results.

The shared **Фильтры** drawer combines status and type using AND in graph and list.
Active removable filter chips remain visible when the drawer is closed. Filtering scans
all exact-block summary pages in the selected scope before reporting the number
of matches, including matches beyond the first 64 records. Progress is displayed
during scanning; changing scope or filters supersedes an unfinished scan. Cards
are still displayed in batches of 64. Completed scope summaries are reused when
changing filters, and live refresh rebuilds them. Unknown status/type values from
loaded summaries are offered alongside the named types. **Сбросить фильтры** clears
both selectors while preserving the selected branch and text query. With an active
text query, filters apply to the returned search results, subject to the existing
recall limit. Expanded graph blocks load complete direct summaries before sorting
and filtering, then reveal matching records six at a time. Filtering does not hide
the surrounding block structure or change catalog counts, membership or authority.
Status, type, date order, block-name search, empty-branch preference, descendant
scope and graph/list mode are restored from browser-local storage before loading
records. Preferences are isolated by workspace/profile (and by active/archive
preview). Unknown type names are retained, invalid preference shapes fall back to
defaults, and unavailable storage does not prevent using the panel. Resetting
status/type filters persists their cleared values. Record summaries, full-text
search queries and editing drafts are not written to local storage.

Full record text
and retained history are read only on demand. The graph draws parent/child
ownership and explicit catalog links between visible blocks. It does not invent
semantic connections or compute all-pairs similarity. Sources and immutable
history are collapsed sections below the record text. Opening a historical
version labels it explicitly and disables editing that version.
The compact vertical toolbar at the left of the graph offers details, edit,
add record, show location in graph (a crosshair icon with a record/block-specific
tooltip), and an overflow menu for record history and the parent
block. Icons have labels/tooltips, and Up/Down/Home/End navigate the toolbar.
The pencil opens the existing record editor or the block's name/description
editor in the same reader. Save/Cancel stay in the editor; opening and closing
it fade/slide without moving the graph. Protected/system blocks and historical
records cannot be edited. Block renaming preserves its ID, parent and record
membership. Both editors submit exact expected revisions. A stale save keeps the
draft, shows the current version for comparison, and requires an explicit choice
before retrying against it. Service errors remain visible beside the form.
Confirmed records use the correction-proposal path; edits do not inherit approval.
An unsaved draft uses an in-page confirmation dialog when cancelling, closing
the reader, selecting another object or switching workspace/archive view. Continue
editing and Escape preserve the draft; discarding resumes only the requested action.
The browser's unload protection remains for closing/reloading the tab itself.
Source references are declarations, not proof that the linked content is valid;
only HTTP(S) references become external links. Other identifiers remain text.
Coordinates and zoom are browser-local presentation preferences, namespaced by
workspace/profile, alongside expanded branches. Dragging a node never changes
canonical topology; released nodes are separated from overlapping neighbours.
Use the zoom buttons or Ctrl/Cmd+wheel to zoom, and drag empty canvas or scroll
to pan. **Весь граф** fits the whole graph; **Фокус** (F) explicitly isolates
the branch at a readable scale. The optional minimap provides navigation on larger graphs.
Graph disclosure keeps the clicked block and camera fixed. Nodes and their
connections move and fade together over 420 ms; interrupted transitions resume
from the displayed positions and opacity. Data is loaded before committing the
new layout. Sidebar rows expand/collapse their actual height over 360 ms, keeping
outgoing rows in document flow until the transition completes. Camera moves for
explicit navigation take 320 ms. Dragging, scrolling or zooming interrupts camera animation.
The system reduced-motion preference disables these transitions.
Record bodies and editing drafts are
not stored in these preferences.

Search combines eligible agent-recall results with title/preview matches among
records already loaded for operator inspection. The latter remain inspection
results: they do not bypass candidate eligibility or promote memory for agents.
Returning to the graph clears the search and restores the current space.

The event cursor contains a listener epoch and monotonically increasing sequence.
The last 256 events can be replayed after a connection interruption. A stale or
foreign cursor requests a fresh canonical snapshot. The browser merges events
which arrive during a snapshot and preserves the loaded topic extent, selected
record, graph layout and editing draft. A listener restart changes its port and
access capability; reopen the panel using the launcher. Direct embedded writes
outside the shared listener do not publish panel events; reopen the view after
such maintenance. Automatic cross-client updates apply to clients using the
shared listener.

## Editing and trust

Candidate edits carry the exact expected revision through `amend`. A conflict
preserves the draft, displays the current source, and requires an explicit choice
to continue against that version. Each save has one stable mutation correlation
ID; retrying the same operation after an ambiguous response cannot create a
second record. Editing controls are locked while the save is in flight.

For trusted records, the panel offers a correction proposal through the existing
`remember(operation="correct")` path. An optional `expected_revision` is checked
inside the Go correction operation, after resolving the canonical head, so queueing
cannot silently retarget a proposal to an unseen revision. The candidate remains
advisory and requires the existing evidence and promotion gates. Historical
versions are inspectable but cannot be edited as if they were current.

## Access and observation boundaries

The CLI authenticates as the local operator before obtaining a random capability
in the URL fragment. The fragment is removed immediately and exchanged for an
HttpOnly, SameSite=Strict cookie scoped to the panel and named per listener port.
API access validates the loopback Host and same Origin; there is no wildcard
CORS, filesystem serving or externally hosted asset. HTML is a static shell
without embedded memory or bootstrap secrets. Session cookies last 12 hours;
live streams are bounded to 16 per listener. Capabilities grant operator access
to this workspace/profile; do not share the launch URL.

The activity feed is a bounded view since panel activation, not a durable
audit ledger. Immutable record history remains in the canonical store. The
context view shows retrieval results and whether a context payload was rendered.
It does not claim that a model read, followed or used a record in its answer.
Explicit session identities are supplied by the host. When absent, each MCP
process gets a unique identity; the legacy `default` and `host-mcp` placeholders
also get this fallback. Clients share durable knowledge while their active masks
stay separate. Hosts that reuse one process for all chats still need to supply
per-chat identities to isolate those chats. Search
similarity is labelled as a score, never as factual confidence.

## Frontend development

The frontend lives in `internal/localservice/panel_assets/` and is embedded in
the Go service. The page loads the graph and branch record ES modules directly;
there is no separate frontend build. The authenticated listener is bound to one
configured store, workspace, profile and backend. The workspace picker appears
only if a read-only discovery snapshot supplies other workspaces; the Go live
service does not scan or switch stores. To open another workspace, launch
`um-memory dashboard` with its identity flags. The **Фильтры** drawer can narrow
records to an exact project ID within the bound workspace. Project search scans
loaded summaries; it does not send a broader semantic recall request.

Edits to blocks and records use the operator lease and canonical Go writer.
Block edits require the current integer revision, a name, description and reason.
A stale revision returns a conflict so the editor retains the draft for review.
The URL fragment contains a one-time capability that is exchanged for an HttpOnly,
SameSite=Strict cookie; the frontend removes the fragment after authentication.

Run `node --check internal/localservice/panel_assets/app.js`,
`node tests/panel_graph_layout.mjs` and `node tests/panel_branch_records.mjs`
for frontend syntax, layout and pagination. Run
`go test ./internal/localservice` for the panel protocol, asset routes and
canonical edit contract.

Run `node tests/panel_graph_layout.mjs` to check nested layout, long labels,
overlap separation, fixed disclosure anchors and interrupted entry/exit transitions,
`node tests/panel_branch_records.mjs` for nested counts,
scope, pagination, failed-page retries, tree filtering, late-page type/status
matches and scan cancellation.

Semantic retrieval revalidates indexed record IDs against current canonical
membership and query eligibility. Historical/retracted records stay available
for audit but cannot return through stale vectors. Parsed vector caches are
separated by index path, model and profile, with transactionally updated version
tokens so same-second writes from another process invalidate them.
