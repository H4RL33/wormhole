-- Add default_capabilities and default_roles to role_templates.
-- Stored as JSONB arrays to match internal/core/roles decoding (json.Unmarshal
-- into []string). Supersedes the misplaced internal/db/migrations/000011 file,
-- which was never applied by `migrate -path migrations` and collided with
-- 000011_viewer_keys. DROP IF EXISTS first so any DB that received these columns
-- out-of-band (e.g. as text[]) converges to the correct JSONB type.

ALTER TABLE role_templates DROP COLUMN IF EXISTS default_capabilities;
ALTER TABLE role_templates DROP COLUMN IF EXISTS default_roles;

ALTER TABLE role_templates ADD COLUMN default_capabilities jsonb NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE role_templates ADD COLUMN default_roles jsonb NOT NULL DEFAULT '[]'::jsonb;

-- Backfill the six seeded templates. Engineers read+write; the rest read-only.
-- Every template defaults to the "agent" role.
UPDATE role_templates
   SET default_capabilities = '["read", "write"]'::jsonb,
       default_roles        = '["agent"]'::jsonb
 WHERE name IN ('backend-engineer', 'frontend-engineer');

UPDATE role_templates
   SET default_capabilities = '["read"]'::jsonb,
       default_roles        = '["agent"]'::jsonb
 WHERE name IN ('project-manager', 'contributor', 'reviewer', 'maintainer');
