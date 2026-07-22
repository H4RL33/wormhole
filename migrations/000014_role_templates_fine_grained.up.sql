-- Issue #21: fine-grained per-tool permissions. Re-seed role_templates
-- permission bundles from the coarse resource-verb strings (000010) to the
-- fine-grained tool-action strings HandleToolsCall now enforces. Alpha
-- hard-cut: already-registered agents keep their (now inert) coarse strings
-- and must re-register/re-join to obtain these.

UPDATE role_templates SET permission_bundle =
 '["task.list","task.create","task.update_status","kb.search","kb.get","kb.get_links","kb.write","channel.list","channel.subscribe","channel.create","channel.post","git.link_commit","git.request_review"]'::jsonb
 WHERE name IN ('backend-engineer','frontend-engineer','contributor');

UPDATE role_templates SET permission_bundle =
 '["task.list","task.create","task.update_status","kb.search","kb.get","kb.get_links","kb.write","channel.list","channel.subscribe","channel.create","channel.post","task.assign"]'::jsonb
 WHERE name = 'project-manager';

UPDATE role_templates SET permission_bundle =
 '["task.list","task.create","task.update_status","kb.search","kb.get","kb.get_links","kb.write","channel.list","channel.subscribe","channel.create","channel.post","task.assign","git.link_commit","git.request_review"]'::jsonb
 WHERE name = 'maintainer';

UPDATE role_templates SET permission_bundle =
 '["task.list","kb.search","kb.get","kb.get_links","kb.write","channel.list","channel.subscribe","channel.create","channel.post"]'::jsonb
 WHERE name = 'reviewer';
