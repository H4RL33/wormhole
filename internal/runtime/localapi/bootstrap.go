package localapi

import (
	"context"
	"errors"
	"time"

	runtimeconfig "github.com/H4RL33/wormhole/internal/runtime/config"
	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	syncpkg "github.com/H4RL33/wormhole/internal/runtime/sync"
)

const enrolmentRecoveryCheckpointTimeout = 2 * time.Second

// EnableEnrolmentBootstrap makes the production Gateway continue from the
// credentials_persisted checkpoint through bootstrap and incremental sync.
// It is separate from SetEnrolmentRuntime so contract-only embeddings may keep
// exercising the completed Task 3 boundary without silently doing network work.
func (s *Server) EnableEnrolmentBootstrap(cfg syncpkg.Config) {
	s.enrolmentBootstrapMu.Lock()
	defer s.enrolmentBootstrapMu.Unlock()
	s.enrolmentBootstrapEnabled = true
	s.enrolmentSyncConfig = cfg
	if s.enrolmentSyncEngines == nil {
		s.enrolmentSyncEngines = make(map[string]*syncpkg.Engine)
	}
}

func (s *Server) continueEnrolmentBootstrap(ctx context.Context, req EnrolmentRequest, attempt localstore.EnrolmentAttemptRecord, credentials runtimeconfig.Credentials) EnrolmentResult {
	s.enrolmentBootstrapMu.Lock()
	enabled := s.enrolmentBootstrapEnabled
	cfg := s.enrolmentSyncConfig
	s.enrolmentBootstrapMu.Unlock()
	if !enabled {
		return enrolmentPersisted(req, attempt.AgentID, attempt.PassportID)
	}
	if attempt.State == string(EnrolmentReady) {
		return enrolmentReady(req, attempt.AgentID, attempt.PassportID)
	}
	if s.store == nil || s.tr == nil || s.kb == nil || s.qr == nil {
		if err := s.checkpointEnrolmentRecovery(ctx, attempt); err != nil {
			return enrolmentCheckpointFailure(req, attempt.AgentID, attempt.PassportID)
		}
		return enrolmentFailure(req, EnrolmentBootstrapFailedAfterEnrolment,
			"Gateway could not configure durable bootstrap storage.", attempt.AgentID, attempt.PassportID)
	}
	if err := s.store.UpdateEnrolmentAttempt(ctx, attempt, string(EnrolmentBootstrapInProgress), attempt.AgentID, attempt.PassportID, false); err != nil {
		if checkpointErr := s.checkpointEnrolmentRecovery(ctx, attempt); checkpointErr != nil {
			return enrolmentCheckpointFailure(req, attempt.AgentID, attempt.PassportID)
		}
		return enrolmentFailure(req, EnrolmentBootstrapFailedAfterEnrolment,
			"Gateway could not checkpoint bootstrap startup.", attempt.AgentID, attempt.PassportID)
	}
	engine, err := syncpkg.New(credentials.Server, credentials.Token, req.ProjectID, s.qr,
		syncpkg.NewAuditRepo(s.store.DB()), s.tr, s.kb, cfg)
	if err == nil {
		err = engine.ConfigureBootstrap(s.store, credentials.AgentID, credentials.PassportID, &attempt)
	}
	if err == nil {
		s.configureIntegrationManifestReceiver(engine)
	}
	if err == nil {
		err = engine.Bootstrap(ctx)
	}
	if err != nil {
		if engine != nil {
			engine.Stop()
		}
		if checkpointErr := s.checkpointEnrolmentRecovery(ctx, attempt); checkpointErr != nil {
			return enrolmentCheckpointFailure(req, attempt.AgentID, attempt.PassportID)
		}
		return enrolmentFailure(req, EnrolmentBootstrapFailedAfterEnrolment,
			"Gateway committed credentials but could not commit the bootstrap snapshot.", attempt.AgentID, attempt.PassportID)
	}

	s.enrolmentBootstrapMu.Lock()
	if previous := s.enrolmentSyncEngines[req.CredentialProfile]; previous != nil {
		previous.Stop()
	}
	s.enrolmentSyncEngines[req.CredentialProfile] = engine
	s.enrolmentBootstrapMu.Unlock()
	s.authorizationAgents.Store(req.ProjectID, attempt.AgentID)
	// Bootstrap and the ready checkpoint have committed before the first
	// incremental loop can run. Engine.Stop owns cancellation at Server.Close.
	engine.Start(context.Background())
	return enrolmentReady(req, attempt.AgentID, attempt.PassportID)
}

type integrationManifestReceiverConfigurer interface {
	ConfigureIntegrationManifestReceiver(syncpkg.IntegrationManifestReceiver)
}

func (s *Server) configureIntegrationManifestReceiver(engine integrationManifestReceiverConfigurer) {
	if engine != nil && s.integrationReceiver != nil {
		engine.ConfigureIntegrationManifestReceiver(s.integrationReceiver)
	}
}

func (s *Server) checkpointEnrolmentRecovery(ctx context.Context, attempt localstore.EnrolmentAttemptRecord) error {
	if s.store == nil {
		return errors.New("localapi: recovery checkpoint store unavailable")
	}
	checkpointCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), enrolmentRecoveryCheckpointTimeout)
	defer cancel()
	return s.store.UpdateEnrolmentAttempt(checkpointCtx, attempt, string(EnrolmentRecoveryRequired), attempt.AgentID, attempt.PassportID, false)
}

func enrolmentCheckpointFailure(req EnrolmentRequest, agentID, passportID string) EnrolmentResult {
	return enrolmentFailure(req, EnrolmentCheckpointPersistenceFailed,
		"Gateway could not durably record bootstrap recovery; operator attention is required.", agentID, passportID)
}

func (s *Server) stopEnrolmentSyncEngines() {
	s.enrolmentBootstrapMu.Lock()
	engines := make([]*syncpkg.Engine, 0, len(s.enrolmentSyncEngines))
	for profile, engine := range s.enrolmentSyncEngines {
		engines = append(engines, engine)
		delete(s.enrolmentSyncEngines, profile)
	}
	s.enrolmentBootstrapMu.Unlock()
	for _, engine := range engines {
		engine.Stop()
	}
}

func enrolmentReady(req EnrolmentRequest, agentID, passportID string) EnrolmentResult {
	return EnrolmentResult{
		Version: EnrolmentProtocolVersion, Code: EnrolmentSuccess, State: EnrolmentReady,
		IdempotencyKey: req.IdempotencyKey, Retryable: false, AgentID: agentID, PassportID: passportID,
		CredentialProfile: req.CredentialProfile,
	}
}
