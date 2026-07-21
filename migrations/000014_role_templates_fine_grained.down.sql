-- Restore the coarse resource-verb bundles seeded by 000010.

UPDATE role_templates SET permission_bundle =
 '["task.read","task.write","kb.read","kb.write","channel.read","channel.write"]'::jsonb
 WHERE name IN ('backend-engineer','frontend-engineer','contributor');

UPDATE role_templates SET permission_bundle =
 '["task.read","task.write","kb.read","kb.write","channel.read","channel.write","task.assign"]'::jsonb
 WHERE name IN ('project-manager','maintainer');

UPDATE role_templates SET permission_bundle =
 '["task.read","kb.read","kb.write","channel.read","channel.write"]'::jsonb
 WHERE name = 'reviewer';
