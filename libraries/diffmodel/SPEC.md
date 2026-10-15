# Diff model

## Purpose and scope

Represent and combine captured file differences independently of storage,
execution, and terminal presentation. Inputs are supplied evidence and content;
operations never reopen a workspace or execute a recorded action.

## Behavior

File identities distinguish creation, update, deletion, and movement. Missing
content remains incomplete evidence; directory metadata supplies no invented
rows. Binary and link metadata remain distinct from text. Source rows preserve
line endings and missing final newlines.

Composition follows captured identities and order, retaining only known rows.
Hidden and highlighted regions follow source changes rather than painted lines.
An inconsistent chain fails without publishing a partial replacement. Merge
returns content, conflicts, satisfied hunks, and rejected hunks for its caller to
apply; it performs no filesystem mutation.

Status classification is shared by command output and terminal views. Display
header substitution uses caller-supplied paths and leaves retained identities
and source rows unchanged. Workspace path policy and terminal styling belong to
consumers.

## Acceptance

Pure tests cover diff construction, row parsing, composition, highlights,
consistency, merge conflicts, and metadata-only evidence. Command and renderer
acceptance verify shared status, path presentation, and unchanged retained views.
The [change interface](../../doc/spec/changes.md) owns user-facing review and
mutation guarantees.
