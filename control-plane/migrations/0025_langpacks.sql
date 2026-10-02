-- 0025 · Phase 3 stream L (language packs, boost lists, lineage). Language packs live in project repositories and
-- need no table; lineage (registry.lineage, docs/spec/02-domain-projects-registry.md "Lineage both ways") follows
-- the entity ids registry payloads name. 0023 belongs to stream G, 0024 to E; migrations apply by number.

-- Every Cadence entity id (<three-letter prefix>_<uuid>, e.g. ver_…, src_…, run_…, ckp_…) that occurs as a JSON
-- string anywhere in a document. A registry version is downstream of every id its payload names: a golden set of
-- its dataset version and normalizer, a model of its checkpoint, run and base model, a dataset of its sources.
CREATE FUNCTION entity_refs_in(doc jsonb) RETURNS text[]
    LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
    RETURN ARRAY(SELECT DISTINCT m[1] FROM regexp_matches(doc::text,
        '"([a-z]{3}_[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})"', 'g') AS m);

-- "Used by": which versions name an id, as an index lookup.
CREATE INDEX registry_versions_entity_refs_idx ON registry_versions USING gin (entity_refs_in(payload));
