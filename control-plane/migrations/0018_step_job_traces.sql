-- 0018 · Phase 2 hardening (stream H): the trace a step job's lease continues (docs/spec/06-platform.md "Worker
-- protocol"). The step handler stores the W3C traceparent of its job span when it queues the step; a claim hands it
-- to the worker, so the worker's step spans are children of the control plane's job span, which continues the
-- request that started the pipeline run. Null for jobs queued without a span: the lease then derives one.
ALTER TABLE step_jobs ADD COLUMN traceparent text;
