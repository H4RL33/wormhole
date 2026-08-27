-- Cluster roles are deployment authority, not application-schema authority.
-- This script is intentionally separate from migration 000021 and is safe to replay.
DO $roles$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = 'wormhole_activity_owner') THEN
        CREATE ROLE wormhole_activity_owner NOLOGIN NOSUPERUSER NOBYPASSRLS NOINHERIT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = 'wormhole_fabric_runtime') THEN
        CREATE ROLE wormhole_fabric_runtime LOGIN NOSUPERUSER NOBYPASSRLS NOINHERIT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = 'wormhole_activity_maintenance') THEN
        CREATE ROLE wormhole_activity_maintenance NOLOGIN NOSUPERUSER NOBYPASSRLS NOINHERIT;
    END IF;
END
$roles$;
