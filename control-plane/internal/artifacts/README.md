# internal/artifacts
The index of the content store (docs/spec/08-resolutions.md R15, R42): one `artifacts` row per artifact — hash (`b3:<64 hex>`), type, size, `directory`, neutral `meta`, the first producer's project (NULL for a registry artifact) and producing pipeline step — plus `artifact_projects`, every project that produced or consumed it. The bytes stay in `internal/cas`; this package never writes blobs.

- `Verify(store, ref)` checks a reference against the store: a file's blob size equals the declared size; a directory artifact is a `cas.Manifest` blob whose files all exist with their sizes, and its size is the sum of its files (the manifest blob's own size is accepted too).
- `Record(ctx, tx, store, ref, projectID, producer)` verifies and indexes an artifact; the first row wins, later producers and consumers only link their project. The pipeline engine records every step output (with its producer) and every run input.
- `Get`, `Projects`, `Files` (a directory's manifest) and `ReadContent` (a file, or one file of a directory, of at most 1 MiB inline — `artifacts.get?content=true&path=`).

Reads are authorised by project: a credential scoped to a project reads artifacts linked to it; an artifact linked to no project needs registry read.

Retention (stream E): `Record` also fills the file index `artifact_files` (hash, path, file_hash, size) for a directory, and clears the eviction of an artifact whose bytes are back. An evicted artifact (`Evicted`: when, by whom, the job) keeps its row: `Files` reads its list from the index, `ReadContent` says it was evicted, and `Record` refuses it as an input with `ErrEvicted` (the pipeline engine answers `artifact-missing`). Eviction itself lives in `internal/eviction`.
