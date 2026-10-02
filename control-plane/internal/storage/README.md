# internal/storage
Mount drivers (local path, NFS/SMB path, S3-compatible, Hugging Face Hub), content-hash index, NVMe cache with pinning and LRU eviction — phase 4.

Today: the pgx pool (`Open`), the migration runner (`Migrate`) and `BeginRetry`, which reruns an idempotent
transaction that lost a race (`IsRace`: unique violation, serialization failure, deadlock; worker registration uses it). Migrations are forward-only SQL files in `control-plane/migrations/NNNN_name.sql`, embedded in the binary, applied at start under `pg_advisory_lock`, each in its own transaction and recorded in `schema_migrations`; the server refuses to start on a database that has a migration it does not know.
