-- Add default_capabilities and default_roles columns to role_templates

ALTER TABLE role_templates ADD COLUMN default_capabilities JSONB DEFAULT '[]';
ALTER TABLE role_templates ADD COLUMN default_roles JSONB DEFAULT '[]';

-- Backfill sensible defaults for existing templates
UPDATE role_templates SET
  default_capabilities = '["read", "write"]'::jsonb,
  default_roles = '["agent"]'::jsonb
WHERE name IN ('backend-engineer', 'frontend-engineer', 'devops-engineer', 'product-manager');

UPDATE role_templates SET
  default_capabilities = '["read"]'::jsonb,
  default_roles = '["agent"]'::jsonb
WHERE default_capabilities = '[]'::jsonb;
