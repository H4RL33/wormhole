-- Cluster roles are deployment authority, not application-schema authority.
-- The script creates missing roles and validates existing roles; it never silently alters one.
DO $roles$
DECLARE
    expected record;
    actual record;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = 'wormhole_activity_owner') THEN
        CREATE ROLE wormhole_activity_owner NOLOGIN NOSUPERUSER NOBYPASSRLS NOINHERIT NOCREATEDB NOCREATEROLE NOREPLICATION;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = 'wormhole_fabric_runtime') THEN
        CREATE ROLE wormhole_fabric_runtime LOGIN NOSUPERUSER NOBYPASSRLS NOINHERIT NOCREATEDB NOCREATEROLE NOREPLICATION;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = 'wormhole_activity_maintenance') THEN
        CREATE ROLE wormhole_activity_maintenance NOLOGIN NOSUPERUSER NOBYPASSRLS NOINHERIT NOCREATEDB NOCREATEROLE NOREPLICATION;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = 'wormhole_attachment_resolver') THEN
        CREATE ROLE wormhole_attachment_resolver NOLOGIN NOSUPERUSER BYPASSRLS NOINHERIT NOCREATEDB NOCREATEROLE NOREPLICATION;
    END IF;
    FOR expected IN
        SELECT * FROM (VALUES
            ('wormhole_activity_owner', false, false),
            ('wormhole_fabric_runtime', true, false),
            ('wormhole_activity_maintenance', false, false),
            ('wormhole_attachment_resolver', false, true)
        ) AS roles(role_name, can_login, bypass_rls)
    LOOP
        SELECT rolcanlogin, rolsuper, rolbypassrls, rolinherit, rolcreatedb,
               rolcreaterole, rolreplication, rolconfig INTO actual
          FROM pg_catalog.pg_roles WHERE rolname = expected.role_name;
        IF NOT FOUND OR actual.rolcanlogin <> expected.can_login OR actual.rolsuper OR
           actual.rolbypassrls <> expected.bypass_rls OR actual.rolinherit OR
           actual.rolcreatedb OR actual.rolcreaterole OR actual.rolreplication OR
           actual.rolconfig IS NOT NULL THEN
            RAISE EXCEPTION USING ERRCODE = '55000',
                MESSAGE = format('provisioning refuses malformed role %s', expected.role_name);
        END IF;
        IF EXISTS (SELECT 1 FROM pg_catalog.pg_auth_members
            WHERE roleid = expected.role_name::regrole OR member = expected.role_name::regrole) THEN
            RAISE EXCEPTION USING ERRCODE = '55000',
                MESSAGE = format('provisioning refuses role memberships for %s', expected.role_name);
        END IF;
    END LOOP;
END
$roles$;
