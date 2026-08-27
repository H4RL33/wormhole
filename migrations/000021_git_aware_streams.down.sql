REVOKE ALL ON FUNCTION fabric_prune_activities_v1 FROM PUBLIC, wormhole_activity_maintenance;
REVOKE ALL ON FUNCTION fabric_transition_activity_lifecycle_v1 FROM PUBLIC, wormhole_fabric_runtime;
REVOKE ALL ON FUNCTION fabric_accept_activity_v1 FROM PUBLIC, wormhole_fabric_runtime;
REVOKE ALL ON FUNCTION fabric_publish_activity_policy_v1 FROM PUBLIC, wormhole_fabric_runtime;

DROP FUNCTION fabric_prune_activities_v1;
DROP FUNCTION fabric_transition_activity_lifecycle_v1;
DROP FUNCTION fabric_accept_activity_v1;
DROP FUNCTION fabric_publish_activity_policy_v1;

DROP TRIGGER fabric_activity_ingress_receipts_immutable ON fabric_activity_ingress_receipts;
DROP TRIGGER fabric_activities_immutable ON fabric_activities;
DROP TRIGGER fabric_activity_policy_versions_immutable ON fabric_activity_policy_versions;
DROP TABLE fabric_activity_lifecycle;
DROP TABLE fabric_activity_ingress_receipts;
DROP TABLE fabric_activities;
DROP TABLE fabric_activity_stream_sequences;
DROP TABLE fabric_activity_policy_current;
DROP TABLE fabric_activity_policy_versions;

REVOKE SELECT ON TABLE fabric_streams, fabric_workspace_stream_bindings FROM wormhole_activity_owner;
REVOKE UPDATE (updated_at) ON TABLE fabric_streams FROM wormhole_activity_owner;
REVOKE UPDATE (detached_at) ON TABLE fabric_workspace_stream_bindings FROM wormhole_activity_owner;

DROP TRIGGER fabric_stream_requests_immutable ON fabric_stream_requests;
DROP TRIGGER fabric_stream_versions_immutable ON fabric_stream_versions;
DROP FUNCTION reject_fabric_immutable_history();
DROP TABLE public_request_nonces;
DROP TABLE fabric_public_actor_keys;
DROP TABLE fabric_stream_conflicts;
DROP TABLE fabric_stream_requests;
DROP INDEX fabric_workspace_one_live_writable;
DROP TABLE fabric_workspace_stream_bindings;
DROP TABLE fabric_stream_versions;
DROP TABLE fabric_streams;
DROP TABLE project_repository_bindings;
