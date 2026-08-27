package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"time"

	runtimeconfig "github.com/H4RL33/wormhole/internal/runtime/config"
	"github.com/H4RL33/wormhole/internal/runtime/config/connector"
	"github.com/H4RL33/wormhole/internal/runtime/localapi"
	"github.com/H4RL33/wormhole/internal/runtime/projectstate"
	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

var orderedCLISetupStages = []runtimeconfig.SetupStage{
	runtimeconfig.StageProjectValidated,
	runtimeconfig.StageGatewayReady,
	runtimeconfig.StageWorkspaceRegistered,
	runtimeconfig.StageIdentitySelected,
	runtimeconfig.StagePublicationClassified,
	runtimeconfig.StageBaseImported,
	runtimeconfig.StageConnectorsApplied,
	runtimeconfig.StageFinalVerified,
}

type setupOptions struct {
	yes         bool
	publication string
	name        string
	email       string
}

type setupPlan struct {
	Selection  runtimeconfig.SetupSelection
	ProjectID  string
	Commit     string
	TreeDigest string
	Details    any
}

type setupStageResult struct {
	WorkspaceID         types.WorkspaceID
	IdentityPrincipalID string
	ConnectorBackups    []runtimeconfig.BackupReference
}

type setupJournal interface {
	Begin(context.Context, string) (runtimeconfig.SetupJournal, error)
	Resumable(context.Context, string) (runtimeconfig.SetupJournal, bool, error)
	SetSelection(context.Context, string, runtimeconfig.SetupSelection) error
	BindWorkspace(context.Context, string, types.WorkspaceID) error
	BindIdentity(context.Context, string, string) error
	RecordConnectorBackup(context.Context, string, runtimeconfig.BackupReference) error
	MarkCompleted(context.Context, string, runtimeconfig.SetupStage) error
	RecordLastError(context.Context, string, runtimeconfig.SetupStage, error) error
	Complete(context.Context, string) error
}

type setupDriver interface {
	CanonicalRoot(context.Context) (string, error)
	Plan(context.Context, setupOptions, *runtimeconfig.SetupSelection) (setupPlan, error)
	ReconcileStage(context.Context, runtimeconfig.SetupStage, setupPlan, runtimeconfig.SetupJournal) (setupStageResult, error)
}

type setupDependencies struct {
	journal setupJournal
	driver  setupDriver
}

func productionSetupDependencies() (setupDependencies, error) {
	if runtime.GOOS != "linux" {
		return setupDependencies{}, runtimeconfig.ErrSetupJournalFilesystemUnsupported
	}
	journal, err := runtimeconfig.OpenSetupJournalStore()
	if err != nil {
		return setupDependencies{}, err
	}
	paths, err := runtimeconfig.ResolveRuntimePaths()
	if err != nil {
		return setupDependencies{}, err
	}
	connectors, err := newProductionConnectorCommands()
	if err != nil {
		return setupDependencies{}, err
	}
	runner := runtimeconfig.NewCommandRunner()
	driver := &productionSetupDriver{
		runner: runner, service: runtimeconfig.NewGatewayService(runner),
		gateway: &unixSetupGateway{socketPath: paths.SocketPath}, connectors: connectors.(*productionConnectorCommands),
	}
	return setupDependencies{journal: journal, driver: driver}, nil
}

type setupGateway interface {
	Ready(context.Context) error
	Register(context.Context, localapi.SetupWorkspaceRequest) (localapi.SetupWorkspaceReadback, error)
	EnsureIdentity(context.Context, localapi.SetupIdentityRequest) (localapi.SetupIdentityReadback, error)
	Classify(context.Context, localapi.SetupPublicationRequest) (localapi.SetupPublicationReadback, error)
	Import(context.Context, localapi.SetupImportRequest) (localapi.SetupImportReadback, error)
	Verify(context.Context, localapi.SetupWorkingDirectoryRequest) (localapi.SetupVerifyReadback, error)
}

type productionSetupDriver struct {
	root       string
	runner     runtimeconfig.CommandRunner
	service    runtimeconfig.GatewayService
	gateway    setupGateway
	connectors *productionConnectorCommands
}

type setupProjectObservation struct {
	Root       string
	ProjectID  string
	Repository types.RepositoryIdentity
	Commit     string
	TreeDigest state.Digest
}

type setupConnectorObservation struct {
	Availability connector.Availability
	Prior        connector.ConnectorEntry
	Desired      connector.ConnectorEntry
}

type productionSetupDetails struct {
	Project          setupProjectObservation
	GatewayReady     bool
	GatewayPrior     runtimeconfig.ServiceState
	GatewayChange    *runtimeconfig.ConfirmedServiceChange
	Connectors       map[connector.AdapterName]setupConnectorObservation
	PublicationClass types.PublicationClassification
	Existing         *localapi.SetupVerifyReadback
}

func (driver *productionSetupDriver) CanonicalRoot(ctx context.Context) (string, error) {
	if driver.root != "" {
		return driver.root, nil
	}
	current, err := canonicalCurrentDirectory()
	if err != nil {
		return "", err
	}
	root, err := runGitSingle(ctx, driver.runner, current, "rev-parse", "--show-toplevel")
	if err != nil || !filepath.IsAbs(root) || filepath.Clean(root) != root || root == string(filepath.Separator) {
		return "", errors.New("canonical Git root is unavailable")
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil || resolved != root {
		return "", errors.New("canonical Git root is unavailable")
	}
	driver.root = root
	return root, nil
}

func (driver *productionSetupDriver) Plan(ctx context.Context, options setupOptions, frozen *runtimeconfig.SetupSelection) (setupPlan, error) {
	project, err := driver.observeProject(ctx)
	if err != nil {
		return setupPlan{}, err
	}
	publication := types.PublicationClassification(options.publication)
	identity := types.ConfirmedIdentitySelection{DisplayName: options.name, Email: options.email}
	if frozen != nil {
		publication = types.PublicationClassification(frozen.PublicationVisibility)
		identity = frozen.Identity
	} else if identity.DisplayName == "" {
		suggestion, suggestErr := runtimeconfig.SuggestGitIdentity(ctx, driver.runner, project.Root)
		if suggestErr != nil {
			return setupPlan{}, suggestErr
		}
		identity.DisplayName, identity.Email = suggestion.DisplayName, suggestion.Email
	}
	if identity.Validate() != nil {
		return setupPlan{}, types.ErrInvalidConfirmedIdentitySelection
	}
	origin, err := projectstate.InspectPublicationOrigin(ctx, project.Root)
	if err != nil {
		return setupPlan{}, err
	}
	binding, err := projectstate.DigestPublicationBindingConstraint(project.Repository, origin)
	if err != nil {
		return setupPlan{}, err
	}
	details := productionSetupDetails{Project: project, PublicationClass: publication, Connectors: map[connector.AdapterName]setupConnectorObservation{}}
	details.GatewayReady = driver.gateway.Ready(ctx) == nil
	if !details.GatewayReady {
		if frozen == nil && !gatewayStateKnownAbsent() {
			return setupPlan{}, runtimeconfig.ErrConfirmedPlanDrift
		}
		details.GatewayPrior, err = driver.service.Inspect(ctx)
		if err != nil {
			return setupPlan{}, err
		}
		serviceOwned := isDesiredGatewayService(details.GatewayPrior, details.GatewayPrior.UnitDigest)
		shouldConfirm := frozen == nil && !serviceOwned
		if frozen != nil {
			if change, exists := setupSelectionChange(*frozen, runtimeconfig.StageGatewayReady, "gateway-service"); exists {
				alreadyOwned := serviceOwned && runtimeconfig.StateDigest(details.GatewayPrior.UnitDigest) == change.DesiredDigest
				shouldConfirm = !alreadyOwned && digestServiceState(details.GatewayPrior) == change.PriorDigest
			}
		}
		if shouldConfirm {
			executable, executableErr := canonicalGatewayExecutable()
			if executableErr != nil {
				return setupPlan{}, executableErr
			}
			confirmed, confirmErr := runtimeconfig.ConfirmGatewayServiceChange(ctx, driver.service, executable)
			if confirmErr != nil {
				return setupPlan{}, confirmErr
			}
			details.GatewayChange = &confirmed
		}
	}
	if details.GatewayReady {
		readback, verifyErr := driver.gateway.Verify(ctx, localapi.SetupWorkingDirectoryRequest{WorkingDirectory: project.Root, Identity: identity, ExpectedTree: project.TreeDigest})
		if verifyErr == nil && readback.Workspace.ProjectID == project.ProjectID && readback.Workspace.AcceptedCommitSHA == project.Commit &&
			readback.Workspace.AcceptedTreeDigest == string(project.TreeDigest) && readback.Identity.DisplayName == identity.DisplayName &&
			readback.Publication.Classification == publication && readback.Publication.BindingDigest == runtimeconfig.StateDigest(binding) && readback.CandidatePresent && readback.CandidateDigest == project.TreeDigest {
			details.Existing = &readback
		}
		if frozen == nil && details.Existing == nil {
			return setupPlan{}, runtimeconfig.ErrConfirmedPlanDrift
		}
	}

	adapterNames := []connector.AdapterName{connector.AdapterCodex, connector.AdapterClaude}
	if frozen != nil {
		adapterNames = adapterNames[:0]
		for _, name := range frozen.ConnectorAdapters {
			adapterNames = append(adapterNames, connector.AdapterName(name))
		}
	}
	for _, name := range adapterNames {
		availability, prior, inspectErr := driver.connectors.Inspect(ctx, name)
		if inspectErr != nil {
			return setupPlan{}, inspectErr
		}
		if !availability.Available {
			if frozen != nil {
				return setupPlan{}, runtimeconfig.ErrConfirmedPlanDrift
			}
			continue
		}
		details.Connectors[name] = setupConnectorObservation{Availability: availability, Prior: prior, Desired: driver.connectors.desired}
	}
	if frozen != nil {
		if frozen.Identity != identity || frozen.PublicationVisibility != string(publication) || frozen.PublicationBindingDigest != runtimeconfig.StateDigest(binding) || !sameSetupAdapterSet(frozen.ConnectorAdapters, details.Connectors) || !validFrozenPlanDigest(*frozen) {
			return setupPlan{}, runtimeconfig.ErrConfirmedPlanDrift
		}
		return setupPlan{Selection: cloneSetupSelection(*frozen), ProjectID: project.ProjectID, Commit: project.Commit, TreeDigest: string(project.TreeDigest), Details: details}, nil
	}

	selection := runtimeconfig.SetupSelection{
		ConnectorAdapters: sortedSetupAdapterNames(details.Connectors), PublicationVisibility: string(publication),
		PublicationBindingDigest: runtimeconfig.StateDigest(binding), Identity: identity, Changes: []runtimeconfig.ConfirmedChange{},
	}
	if !details.GatewayReady && details.GatewayChange == nil && isDesiredGatewayService(details.GatewayPrior, details.GatewayPrior.UnitDigest) {
		selection.Changes = append(selection.Changes, confirmedSetupChange(runtimeconfig.StageGatewayReady, "gateway-service", "ensure", digestServiceState(details.GatewayPrior), runtimeconfig.StateDigest(details.GatewayPrior.UnitDigest)))
	} else if !details.GatewayReady {
		selection.Changes = append(selection.Changes, confirmedSetupChange(runtimeconfig.StageGatewayReady, "gateway-service", "ensure", digestServiceState(details.GatewayPrior), runtimeconfig.StateDigest(details.GatewayChange.DesiredUnitDigest)))
	}
	if details.Existing == nil {
		selection.Changes = append(selection.Changes,
			confirmedSetupChange(runtimeconfig.StageWorkspaceRegistered, "workspace", "register", localapi.DigestSetupWorkspaceAbsent(), digestWorkspaceDesired(project)),
			confirmedSetupChange(runtimeconfig.StageIdentitySelected, "identity", "ensure-selected", localapi.DigestSetupIdentityUnselected(), digestValue(identity)),
			confirmedSetupChange(runtimeconfig.StagePublicationClassified, "publication", "classify", localapi.DigestSetupPublicationPredicate(localapi.SetupPublicationPredicate{
				Classification: types.PublicationUnclassified, PolicyRevision: 1, ObservedOriginDigest: origin, TransitionKind: "bootstrap",
			}), digestPublicationDesired(publication, runtimeconfig.StateDigest(binding))),
			confirmedSetupChange(runtimeconfig.StageBaseImported, "base", "import",
				localapi.DigestSetupBasePredicate(localapi.SetupBasePredicate{CandidatePresent: false, CandidateDigest: project.TreeDigest, WorkspaceState: "clean"}),
				localapi.DigestSetupBasePredicate(localapi.SetupBasePredicate{CandidatePresent: true, CandidateDigest: project.TreeDigest, WorkspaceState: "pending"})),
		)
	}
	for _, name := range []connector.AdapterName{connector.AdapterCodex, connector.AdapterClaude} {
		observation, exists := details.Connectors[name]
		if !exists {
			continue
		}
		priorDigest, priorErr := connector.DigestConnectorEntry(observation.Prior)
		desiredDigest, desiredErr := connector.DigestConnectorEntry(observation.Desired)
		if priorErr != nil || desiredErr != nil {
			return setupPlan{}, connector.ErrInvalidConnectorEntry
		}
		if priorDigest != desiredDigest {
			selection.Changes = append(selection.Changes, confirmedSetupChange(runtimeconfig.StageConnectorsApplied, "connector:"+string(name), "install", priorDigest, desiredDigest))
		}
	}
	selection.PlanDigest = setupPlanDigest(selection)
	return setupPlan{Selection: selection, ProjectID: project.ProjectID, Commit: project.Commit, TreeDigest: string(project.TreeDigest), Details: details}, nil
}

// gatewayStateKnownAbsent proves the only production Gateway authorities do
// not exist before an unavailable-Gateway plan freezes absent predicates.
// Existing, unsafe, or unreadable state fails closed; active journals resume
// their already-frozen selection and do not use this proof.
func gatewayStateKnownAbsent() bool {
	dataRoot := os.Getenv("XDG_DATA_HOME")
	if dataRoot == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return false
		}
		dataRoot = filepath.Join(home, ".local", "share")
	}
	if !filepath.IsAbs(dataRoot) || filepath.Clean(dataRoot) != dataRoot {
		return false
	}
	info, err := os.Lstat(dataRoot)
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	root := filepath.Join(dataRoot, "wormhole")
	info, err = os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == "identities" || name == "wormholed.db" || strings.HasPrefix(name, "wormholed.db-") {
			return false
		}
	}
	return true
}

func (driver *productionSetupDriver) ReconcileStage(ctx context.Context, stage runtimeconfig.SetupStage, plan setupPlan, journal runtimeconfig.SetupJournal) (setupStageResult, error) {
	details, ok := plan.Details.(productionSetupDetails)
	if !ok || journal.Selection == nil || !reflect.DeepEqual(plan.Selection, *journal.Selection) {
		return setupStageResult{}, runtimeconfig.ErrConfirmedPlanDrift
	}
	switch stage {
	case runtimeconfig.StageProjectValidated:
		observed, err := driver.observeProject(ctx)
		if err != nil || observed != details.Project {
			return setupStageResult{}, runtimeconfig.ErrConfirmedPlanDrift
		}
		return setupStageResult{}, nil
	case runtimeconfig.StageGatewayReady:
		return setupStageResult{}, driver.reconcileGateway(ctx, plan.Selection, details)
	case runtimeconfig.StageWorkspaceRegistered:
		change, exists := setupSelectionChange(plan.Selection, stage, "workspace")
		if !exists {
			readback, err := driver.verifyExistingSetup(ctx, plan.Selection, details)
			if err != nil {
				return setupStageResult{}, err
			}
			return setupStageResult{WorkspaceID: readback.Workspace.WorkspaceID}, nil
		}
		if change.DesiredDigest != digestWorkspaceDesired(details.Project) {
			return setupStageResult{}, runtimeconfig.ErrConfirmedPlanDrift
		}
		readback, err := driver.gateway.Register(ctx, localapi.SetupWorkspaceRequest{
			WorkingDirectory: details.Project.Root, ExpectedProjectID: details.Project.ProjectID,
			ExpectedRepository: details.Project.Repository, ExpectedCommit: details.Project.Commit,
			ExpectedPriorDigest: change.PriorDigest,
		})
		if err != nil || readback.ProjectID != details.Project.ProjectID || readback.AcceptedCommitSHA != details.Project.Commit || readback.AcceptedTreeDigest != string(details.Project.TreeDigest) || !types.CanonicalUUID(string(readback.WorkspaceID)) {
			if errors.Is(err, runtimeconfig.ErrConfirmedPlanDrift) {
				return setupStageResult{}, err
			}
			return setupStageResult{}, runtimeconfig.ErrConfirmedPlanDrift
		}
		return setupStageResult{WorkspaceID: readback.WorkspaceID}, nil
	case runtimeconfig.StageIdentitySelected:
		change, exists := setupSelectionChange(plan.Selection, stage, "identity")
		if !exists {
			readback, err := driver.verifyExistingSetup(ctx, plan.Selection, details)
			if err != nil {
				return setupStageResult{}, err
			}
			return setupStageResult{IdentityPrincipalID: readback.Identity.HumanPrincipalID}, nil
		}
		if change.DesiredDigest != digestValue(plan.Selection.Identity) {
			return setupStageResult{}, runtimeconfig.ErrConfirmedPlanDrift
		}
		readback, err := driver.gateway.EnsureIdentity(ctx, localapi.SetupIdentityRequest{WorkingDirectory: details.Project.Root, JournalID: journal.JournalID, Selection: plan.Selection.Identity, ExpectedPriorDigest: change.PriorDigest})
		if err != nil || !types.CanonicalUUID(readback.HumanPrincipalID) || readback.DisplayName != plan.Selection.Identity.DisplayName || len(readback.PublicKey) == 0 || (journal.IdentityPrincipalID != "" && journal.IdentityPrincipalID != readback.HumanPrincipalID) {
			if errors.Is(err, runtimeconfig.ErrConfirmedPlanDrift) {
				return setupStageResult{}, err
			}
			return setupStageResult{}, runtimeconfig.ErrConfirmedPlanDrift
		}
		return setupStageResult{IdentityPrincipalID: readback.HumanPrincipalID}, nil
	case runtimeconfig.StagePublicationClassified:
		change, exists := setupSelectionChange(plan.Selection, stage, "publication")
		if !exists {
			_, err := driver.verifyExistingSetup(ctx, plan.Selection, details)
			return setupStageResult{}, err
		}
		if change.DesiredDigest != digestPublicationDesired(details.PublicationClass, plan.Selection.PublicationBindingDigest) {
			return setupStageResult{}, runtimeconfig.ErrConfirmedPlanDrift
		}
		readback, err := driver.gateway.Classify(ctx, localapi.SetupPublicationRequest{
			WorkingDirectory: details.Project.Root, Classification: details.PublicationClass,
			ExpectedBindingDigest: plan.Selection.PublicationBindingDigest, ExpectedPriorDigest: change.PriorDigest,
		})
		if err != nil || readback.Classification != details.PublicationClass || readback.BindingDigest != plan.Selection.PublicationBindingDigest || readback.TransitionKind != "configured" || readback.PolicyRevision < 1 || !types.CanonicalUUID(readback.ChangedByHumanID) || (journal.IdentityPrincipalID != "" && readback.ChangedByHumanID != journal.IdentityPrincipalID) {
			if errors.Is(err, runtimeconfig.ErrConfirmedPlanDrift) {
				return setupStageResult{}, err
			}
			return setupStageResult{}, runtimeconfig.ErrConfirmedPlanDrift
		}
		return setupStageResult{}, nil
	case runtimeconfig.StageBaseImported:
		change, exists := setupSelectionChange(plan.Selection, stage, "base")
		if !exists {
			_, err := driver.verifyExistingSetup(ctx, plan.Selection, details)
			return setupStageResult{}, err
		}
		if change.DesiredDigest != localapi.DigestSetupBasePredicate(localapi.SetupBasePredicate{CandidatePresent: true, CandidateDigest: details.Project.TreeDigest, WorkspaceState: "pending"}) {
			return setupStageResult{}, runtimeconfig.ErrConfirmedPlanDrift
		}
		readback, err := driver.gateway.Import(ctx, localapi.SetupImportRequest{
			WorkingDirectory: details.Project.Root, ExpectedCommitSHA: details.Project.Commit, ExpectedTreeDigest: details.Project.TreeDigest,
			ExpectedPriorDigest: change.PriorDigest, DesiredDigest: change.DesiredDigest,
		})
		if err != nil || readback.AcceptedCommitSHA != details.Project.Commit || readback.AcceptedTreeDigest != string(details.Project.TreeDigest) || readback.ImportedCandidateDigest != details.Project.TreeDigest || readback.Conflicted {
			if errors.Is(err, runtimeconfig.ErrConfirmedPlanDrift) {
				return setupStageResult{}, err
			}
			return setupStageResult{}, runtimeconfig.ErrConfirmedPlanDrift
		}
		return setupStageResult{}, nil
	case runtimeconfig.StageConnectorsApplied:
		return driver.reconcileConnectors(ctx, plan.Selection, setupConnectorOwner(journal.JournalID, plan.Selection.PlanDigest))
	case runtimeconfig.StageFinalVerified:
		if err := driver.gateway.Ready(ctx); err != nil {
			return setupStageResult{}, runtimeconfig.ErrConfirmedPlanDrift
		}
		readback, err := driver.gateway.Verify(ctx, localapi.SetupWorkingDirectoryRequest{WorkingDirectory: details.Project.Root, Identity: plan.Selection.Identity, ExpectedTree: details.Project.TreeDigest})
		if err != nil || readback.Workspace.ProjectID != details.Project.ProjectID || readback.Workspace.AcceptedCommitSHA != details.Project.Commit || readback.Workspace.AcceptedTreeDigest != string(details.Project.TreeDigest) || readback.Workspace.WorkspaceID != journal.WorkspaceID || readback.Identity.HumanPrincipalID != journal.IdentityPrincipalID || readback.Identity.DisplayName != plan.Selection.Identity.DisplayName || readback.Publication.Classification != details.PublicationClass || readback.Publication.BindingDigest != plan.Selection.PublicationBindingDigest || !readback.CandidatePresent || readback.CandidateDigest != details.Project.TreeDigest {
			return setupStageResult{}, runtimeconfig.ErrConfirmedPlanDrift
		}
		for _, name := range plan.Selection.ConnectorAdapters {
			adapter := driver.connectors.adapters[connector.AdapterName(name)]
			if adapter == nil || adapter.Verify(ctx, driver.connectors.desired) != nil {
				return setupStageResult{}, runtimeconfig.ErrConfirmedPlanDrift
			}
		}
		return setupStageResult{}, nil
	default:
		return setupStageResult{}, runtimeconfig.ErrConfirmedPlanDrift
	}
}

func (driver *productionSetupDriver) verifyExistingSetup(ctx context.Context, selection runtimeconfig.SetupSelection, details productionSetupDetails) (localapi.SetupVerifyReadback, error) {
	readback, err := driver.gateway.Verify(ctx, localapi.SetupWorkingDirectoryRequest{WorkingDirectory: details.Project.Root, Identity: selection.Identity, ExpectedTree: details.Project.TreeDigest})
	if err != nil || readback.Workspace.ProjectID != details.Project.ProjectID || readback.Workspace.AcceptedCommitSHA != details.Project.Commit || readback.Workspace.AcceptedTreeDigest != string(details.Project.TreeDigest) ||
		readback.Identity.DisplayName != selection.Identity.DisplayName || readback.Publication.Classification != details.PublicationClass || readback.Publication.BindingDigest != selection.PublicationBindingDigest || !readback.CandidatePresent || readback.CandidateDigest != details.Project.TreeDigest {
		return localapi.SetupVerifyReadback{}, runtimeconfig.ErrConfirmedPlanDrift
	}
	return readback, nil
}

func (driver *productionSetupDriver) reconcileGateway(ctx context.Context, selection runtimeconfig.SetupSelection, details productionSetupDetails) error {
	change, exists := setupSelectionChange(selection, runtimeconfig.StageGatewayReady, "gateway-service")
	if !exists {
		if details.GatewayReady && driver.gateway.Ready(ctx) == nil {
			return nil
		}
		return runtimeconfig.ErrConfirmedPlanDrift
	}
	ready := driver.gateway.Ready(ctx) == nil
	desiredUnit := runtimeconfig.ServiceUnitDigest(change.DesiredDigest)
	stateNow, err := driver.service.Inspect(ctx)
	if err != nil {
		return err
	}
	if isDesiredGatewayService(stateNow, desiredUnit) {
		executable, executableErr := canonicalGatewayExecutable()
		if executableErr != nil {
			return executableErr
		}
		confirmed, active, recoverErr := runtimeconfig.RecoverGatewayServiceChange(ctx, driver.service, executable, desiredUnit)
		if recoverErr != nil {
			return recoverErr
		}
		if active {
			if digestServiceState(confirmed.ExpectedPrior) != change.PriorDigest || runtimeconfig.StateDigest(confirmed.DesiredUnitDigest) != change.DesiredDigest {
				return runtimeconfig.ErrConfirmedPlanDrift
			}
			if err := driver.service.Install(ctx, confirmed); err != nil {
				return err
			}
		}
		if !stateNow.Active {
			if err := driver.service.Start(ctx); err != nil {
				return err
			}
		}
		if !ready {
			if err := driver.service.WaitReady(ctx); err != nil {
				return err
			}
		}
		return nil
	}
	executable, err := canonicalGatewayExecutable()
	if err != nil {
		return err
	}
	var confirmed runtimeconfig.ConfirmedServiceChange
	if digestServiceState(stateNow) == change.PriorDigest {
		if details.GatewayChange != nil {
			confirmed = *details.GatewayChange
		} else {
			confirmed, err = runtimeconfig.ConfirmGatewayServiceChange(ctx, driver.service, executable)
			if err != nil {
				return err
			}
		}
	} else {
		confirmed, err = runtimeconfig.ResumeGatewayServiceChange(ctx, driver.service, executable, desiredUnit)
		if err != nil {
			return err
		}
	}
	if digestServiceState(confirmed.ExpectedPrior) != change.PriorDigest || runtimeconfig.StateDigest(confirmed.DesiredUnitDigest) != change.DesiredDigest {
		return runtimeconfig.ErrConfirmedPlanDrift
	}
	if err := driver.service.Install(ctx, confirmed); err != nil {
		return err
	}
	if err := driver.service.Start(ctx); err != nil {
		return err
	}
	if err := driver.service.WaitReady(ctx); err != nil {
		return err
	}
	readback, err := driver.service.Inspect(ctx)
	if err != nil || !isDesiredGatewayService(readback, desiredUnit) || driver.gateway.Ready(ctx) != nil {
		return runtimeconfig.ErrConfirmedPlanDrift
	}
	return nil
}

func (driver *productionSetupDriver) reconcileConnectors(ctx context.Context, selection runtimeconfig.SetupSelection, owner runtimeconfig.StateDigest) (setupStageResult, error) {
	result := setupStageResult{ConnectorBackups: []runtimeconfig.BackupReference{}}
	applied := []connector.ConfirmedConnectorChange{}
	fail := func(cause error) (setupStageResult, error) {
		for index := len(applied) - 1; index >= 0; index-- {
			change := applied[index]
			adapter := driver.connectors.adapters[change.Adapter]
			if driver.connectors.store == nil || adapter == nil {
				return setupStageResult{}, runtimeconfig.ErrConfirmedPlanDrift
			}
			if err := connector.RollbackCompletedTransactional(ctx, adapter, change, owner, driver.connectors.store, driver.connectors.store, driver.connectors.store); err != nil {
				return setupStageResult{}, err
			}
		}
		return setupStageResult{}, cause
	}
	for _, rawName := range selection.ConnectorAdapters {
		name := connector.AdapterName(rawName)
		adapter := driver.connectors.adapters[name]
		if adapter == nil {
			return fail(runtimeconfig.ErrConfirmedPlanDrift)
		}
		if driver.connectors.store == nil {
			store, openErr := connector.OpenStore()
			if openErr != nil {
				return fail(openErr)
			}
			driver.connectors.store = store
		}
		if err := connector.RecoverTransactions(ctx, adapter, "wormhole", driver.connectors.store, driver.connectors.store, driver.connectors.store); err != nil {
			return fail(err)
		}
		prior, err := adapter.Inspect(ctx)
		if err != nil {
			return fail(err)
		}
		observedDigest, err := connector.DigestConnectorEntry(prior)
		if err != nil {
			return fail(err)
		}
		desiredDigest, err := connector.DigestConnectorEntry(driver.connectors.desired)
		if err != nil {
			return fail(err)
		}
		change, hasChange := setupSelectionChange(selection, runtimeconfig.StageConnectorsApplied, "connector:"+rawName)
		if !hasChange {
			if observedDigest != desiredDigest || adapter.Verify(ctx, driver.connectors.desired) != nil {
				return fail(runtimeconfig.ErrConfirmedPlanDrift)
			}
			continue
		}
		if change.DesiredDigest != desiredDigest {
			return fail(runtimeconfig.ErrConfirmedPlanDrift)
		}
		if observedDigest == change.DesiredDigest {
			if err := adapter.Verify(ctx, driver.connectors.desired); err != nil {
				return fail(err)
			}
			record, found, completedErr := driver.connectors.store.CompletedTransition(ctx, name, "wormhole", connector.OperationInstall, change.PriorDigest, change.DesiredDigest, owner)
			if completedErr != nil || !found {
				if completedErr != nil {
					return fail(completedErr)
				}
				return fail(runtimeconfig.ErrConfirmedPlanDrift)
			}
			confirmed := connector.ConfirmedConnectorChange{Adapter: name, Name: "wormhole", Action: connector.OperationInstall, PlanDigest: record.PlanDigest, ExpectedPriorDigest: change.PriorDigest, DesiredDigest: change.DesiredDigest}
			result.ConnectorBackups = append(result.ConnectorBackups, record.BackupReference)
			applied = append(applied, confirmed)
			continue
		}
		if observedDigest != change.PriorDigest {
			return fail(runtimeconfig.ErrConfirmedPlanDrift)
		}
		completed, found, completedErr := driver.connectors.store.CompletedTransition(ctx, name, "wormhole", connector.OperationInstall, change.PriorDigest, change.DesiredDigest, owner)
		if completedErr != nil {
			return fail(completedErr)
		}
		if found {
			stale := connector.ConfirmedConnectorChange{Adapter: name, Name: "wormhole", Action: connector.OperationInstall, PlanDigest: completed.PlanDigest, ExpectedPriorDigest: change.PriorDigest, DesiredDigest: change.DesiredDigest}
			if err := connector.RollbackCompletedTransactional(ctx, adapter, stale, owner, driver.connectors.store, driver.connectors.store, driver.connectors.store); err != nil {
				return fail(err)
			}
		}
		connectorPlan, err := adapter.Plan(ctx, prior, driver.connectors.desired)
		if err != nil {
			return fail(err)
		}
		confirmed := connector.ConfirmedConnectorChange{Adapter: name, Name: "wormhole", Action: connector.OperationInstall, PlanDigest: connectorPlan.Digest, ExpectedPriorDigest: change.PriorDigest, DesiredDigest: change.DesiredDigest}
		transaction, err := driver.connectors.applyConfirmedFor(ctx, adapter, driver.connectors.desired, confirmed, owner)
		if err != nil {
			return fail(err)
		}
		applied = append(applied, confirmed)
		if transaction.BackupReference != "" {
			result.ConnectorBackups = append(result.ConnectorBackups, transaction.BackupReference)
		}
	}
	return result, nil
}

func setupConnectorOwner(journalID string, planDigest runtimeconfig.StateDigest) runtimeconfig.StateDigest {
	return digestValue(struct {
		JournalID  string
		PlanDigest runtimeconfig.StateDigest
	}{JournalID: journalID, PlanDigest: planDigest})
}

func (driver *productionSetupDriver) observeProject(ctx context.Context) (setupProjectObservation, error) {
	root, err := driver.CanonicalRoot(ctx)
	if err != nil {
		return setupProjectObservation{}, err
	}
	status, stderr, err := driver.runner.Run(ctx, "git", "-C", root, "status", "--porcelain=v1", "--untracked-files=all", "--", ".wormhole")
	if err != nil || len(stderr) != 0 || len(status) != 0 {
		return setupProjectObservation{}, errors.New("tracked Wormhole state must be clean")
	}
	commit, err := runGitSingle(ctx, driver.runner, root, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return setupProjectObservation{}, err
	}
	tree, err := projectstate.ReadWorkingTreeNoFollow(root)
	if err != nil {
		return setupProjectObservation{}, err
	}
	snapshot, err := state.DecodeTree(tree)
	if err != nil {
		return setupProjectObservation{}, err
	}
	treeDigest, err := state.DigestTree(tree)
	if err != nil {
		return setupProjectObservation{}, err
	}
	return setupProjectObservation{Root: root, ProjectID: snapshot.Config.ProjectID, Repository: snapshot.Config.Repository, Commit: commit, TreeDigest: treeDigest}, nil
}

func runGitSingle(ctx context.Context, runner runtimeconfig.CommandRunner, root string, args ...string) (string, error) {
	arguments := append([]string{"-C", root}, args...)
	stdout, stderr, err := runner.Run(ctx, "git", arguments...)
	if err != nil || len(stderr) != 0 || len(stdout) == 0 || len(stdout) > 4097 || bytes.Count(stdout, []byte{'\n'}) != 1 || stdout[len(stdout)-1] != '\n' {
		return "", errors.New("Git observation failed")
	}
	value := string(stdout[:len(stdout)-1])
	if strings.ContainsAny(value, "\x00\r\n") {
		return "", errors.New("Git observation failed")
	}
	return value, nil
}

func canonicalGatewayExecutable() (string, error) {
	current, err := canonicalCurrentExecutable()
	if err != nil {
		return "", err
	}
	candidate := filepath.Join(filepath.Dir(current), "gatewayd")
	if executable, candidateErr := canonicalExecutablePath(candidate); candidateErr == nil {
		return executable, nil
	}
	return canonicalNativeExecutable("gatewayd")
}

func digestValue(value any) runtimeconfig.StateDigest {
	data, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return runtimeconfig.SHA256StateDigest(data)
}

func digestLiteral(value string) runtimeconfig.StateDigest {
	return runtimeconfig.SHA256StateDigest([]byte(value))
}

func digestServiceState(value runtimeconfig.ServiceState) runtimeconfig.StateDigest {
	return digestValue(struct {
		Installed           bool
		Enabled             bool
		Active              bool
		UnitDigest          runtimeconfig.ServiceUnitDigest
		Loaded              bool
		ReloadNeeded        bool
		ManagerFragmentPath string
	}{value.Installed, value.Enabled, value.Active, value.UnitDigest, value.Loaded, value.ReloadNeeded, value.ManagerFragmentPath})
}

func digestWorkspaceDesired(project setupProjectObservation) runtimeconfig.StateDigest {
	return digestValue(struct {
		ProjectID  string
		Repository types.RepositoryIdentity
		Commit     string
		Tree       state.Digest
	}{project.ProjectID, project.Repository, project.Commit, project.TreeDigest})
}

func digestPublicationDesired(classification types.PublicationClassification, binding runtimeconfig.StateDigest) runtimeconfig.StateDigest {
	return digestValue(struct {
		Classification types.PublicationClassification
		Binding        state.Digest
	}{classification, state.Digest(binding)})
}

func digestFinalSetup(project setupProjectObservation, publication types.PublicationClassification) runtimeconfig.StateDigest {
	return digestValue(struct {
		Project     string
		Commit      string
		Tree        state.Digest
		Publication types.PublicationClassification
	}{project.ProjectID, project.Commit, project.TreeDigest, publication})
}

func isDesiredGatewayService(value runtimeconfig.ServiceState, digest runtimeconfig.ServiceUnitDigest) bool {
	recognized := value.Diagnostic == "" || value.Diagnostic == "gatewayd service is enabled but inactive" || value.Diagnostic == "gatewayd service is active but not ready"
	return recognized && value.Installed && value.Enabled && value.Loaded && !value.ReloadNeeded && value.ManagerFragmentPath != "" && value.UnitDigest == digest
}

func confirmedSetupChange(stage runtimeconfig.SetupStage, subject, action string, prior, desired runtimeconfig.StateDigest) runtimeconfig.ConfirmedChange {
	return runtimeconfig.ConfirmedChange{Stage: stage, Subject: subject, Action: action, PriorDigest: prior, DesiredDigest: desired}
}

func setupSelectionChange(selection runtimeconfig.SetupSelection, stage runtimeconfig.SetupStage, subject string) (runtimeconfig.ConfirmedChange, bool) {
	for _, change := range selection.Changes {
		if change.Stage == stage && change.Subject == subject {
			return change, true
		}
	}
	return runtimeconfig.ConfirmedChange{}, false
}

func setupPlanDigest(selection runtimeconfig.SetupSelection) runtimeconfig.StateDigest {
	copy := cloneSetupSelection(selection)
	copy.PlanDigest = ""
	return digestValue(copy)
}

func validFrozenPlanDigest(selection runtimeconfig.SetupSelection) bool {
	return selection.PlanDigest != "" && setupPlanDigest(selection) == selection.PlanDigest
}

func sortedSetupAdapterNames(observations map[connector.AdapterName]setupConnectorObservation) []string {
	names := make([]string, 0, len(observations))
	for name := range observations {
		names = append(names, string(name))
	}
	sort.Strings(names)
	return names
}

func sameSetupAdapterSet(expected []string, observations map[connector.AdapterName]setupConnectorObservation) bool {
	return reflect.DeepEqual(expected, sortedSetupAdapterNames(observations))
}

type unixSetupGateway struct {
	socketPath string
}

func (gateway *unixSetupGateway) Ready(ctx context.Context) error {
	return gateway.call(ctx, "", nil, nil)
}

func (gateway *unixSetupGateway) Register(ctx context.Context, request localapi.SetupWorkspaceRequest) (localapi.SetupWorkspaceReadback, error) {
	var response localapi.SetupWorkspaceReadback
	err := gateway.call(ctx, localapi.PrivateSetupRegisterWorkspaceRPCMethod, request, &response)
	return response, err
}

func (gateway *unixSetupGateway) EnsureIdentity(ctx context.Context, request localapi.SetupIdentityRequest) (localapi.SetupIdentityReadback, error) {
	var response localapi.SetupIdentityReadback
	err := gateway.call(ctx, localapi.PrivateSetupEnsureIdentityRPCMethod, request, &response)
	return response, err
}

func (gateway *unixSetupGateway) Classify(ctx context.Context, request localapi.SetupPublicationRequest) (localapi.SetupPublicationReadback, error) {
	var response localapi.SetupPublicationReadback
	err := gateway.call(ctx, localapi.PrivateSetupPublicationRPCMethod, request, &response)
	return response, err
}

func (gateway *unixSetupGateway) Import(ctx context.Context, request localapi.SetupImportRequest) (localapi.SetupImportReadback, error) {
	var response localapi.SetupImportReadback
	err := gateway.call(ctx, localapi.PrivateSetupImportRPCMethod, request, &response)
	return response, err
}

func (gateway *unixSetupGateway) Verify(ctx context.Context, request localapi.SetupWorkingDirectoryRequest) (localapi.SetupVerifyReadback, error) {
	var response localapi.SetupVerifyReadback
	err := gateway.call(ctx, localapi.PrivateSetupVerifyRPCMethod, request, &response)
	return response, err
}

func (gateway *unixSetupGateway) call(ctx context.Context, method string, parameters, output any) error {
	if gateway == nil || gateway.socketPath == "" {
		return runtimeconfig.ErrServiceNotReady
	}
	dialer := &net.Dialer{}
	connection, err := dialer.DialContext(ctx, "unix", gateway.socketPath)
	if err != nil {
		return runtimeconfig.ErrServiceNotReady
	}
	defer connection.Close()
	deadline := time.Now().Add(2 * time.Second)
	if contextDeadline, exists := ctx.Deadline(); exists && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := connection.SetDeadline(deadline); err != nil {
		return runtimeconfig.ErrServiceNotReady
	}
	initialize := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "wormhole-setup", "version": "1"}}}
	if err := writeSetupRPC(connection, initialize); err != nil {
		return runtimeconfig.ErrServiceNotReady
	}
	response, err := readSetupRPC(connection, 1)
	if err != nil || response.Error != nil {
		return runtimeconfig.ErrServiceNotReady
	}
	var initialized struct {
		ProtocolVersion string `json:"protocolVersion"`
		ServerInfo      struct {
			Name string `json:"name"`
		} `json:"serverInfo"`
	}
	if json.Unmarshal(response.Result, &initialized) != nil || initialized.ProtocolVersion != "2025-11-25" || initialized.ServerInfo.Name != "gatewayd" {
		return runtimeconfig.ErrServiceNotReady
	}
	if method == "" {
		return nil
	}
	if err := writeSetupRPC(connection, map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}); err != nil {
		return runtimeconfig.ErrServiceNotReady
	}
	privateParams, err := marshalGatewayPrivateEnvelope(ctx, parameters)
	if err != nil {
		return runtimeconfig.ErrServiceNotReady
	}
	if err := writeSetupRPC(connection, map[string]any{"jsonrpc": "2.0", "id": 2, "method": method, "params": privateParams}); err != nil {
		return runtimeconfig.ErrServiceNotReady
	}
	response, err = readSetupRPC(connection, 2)
	if err != nil {
		return runtimeconfig.ErrServiceNotReady
	}
	if response.Error != nil {
		if response.Error.Message == runtimeconfig.ErrConfirmedPlanDrift.Error() {
			return runtimeconfig.ErrConfirmedPlanDrift
		}
		return localapi.ErrPrivateSetupRequest
	}
	decoder := json.NewDecoder(bytes.NewReader(response.Result))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return localapi.ErrPrivateSetupRequest
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return localapi.ErrPrivateSetupRequest
	}
	return nil
}

func writeSetupRPC(connection net.Conn, value any) error {
	data, err := json.Marshal(value)
	if err != nil || len(data) > 64<<10 {
		return runtimeconfig.ErrServiceNotReady
	}
	data = append(data, '\n')
	_, err = connection.Write(data)
	return err
}

func readSetupRPC(connection net.Conn, id int) (rpcResponse, error) {
	line, err := bufio.NewReaderSize(connection, (64<<10)+1).ReadBytes('\n')
	if err != nil || len(line) > 64<<10 {
		return rpcResponse{}, runtimeconfig.ErrServiceNotReady
	}
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	var response rpcResponse
	if err := decoder.Decode(&response); err != nil || response.JSONRPC != "2.0" || string(response.ID) != fmt.Sprint(id) {
		return rpcResponse{}, runtimeconfig.ErrServiceNotReady
	}
	return response, nil
}

func runSetup(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, dependencies setupDependencies) int {
	options, err := parseSetupOptions(args, stderr)
	if err != nil {
		return 2
	}
	if dependencies.journal == nil || dependencies.driver == nil {
		dependencies, err = productionSetupDependencies()
		if err != nil {
			fmt.Fprintf(stderr, "wormhole setup: %v\n", err)
			return 1
		}
	}
	root, err := dependencies.driver.CanonicalRoot(ctx)
	if err != nil {
		fmt.Fprintln(stderr, "wormhole setup: project validation failed")
		return 1
	}
	journal, err := dependencies.journal.Begin(ctx, root)
	if err != nil {
		fmt.Fprintln(stderr, "wormhole setup: setup journal unavailable")
		return 1
	}

	var frozen *runtimeconfig.SetupSelection
	if journal.Selection != nil {
		selection := cloneSetupSelection(*journal.Selection)
		frozen = &selection
	}
	plan, err := dependencies.driver.Plan(ctx, options, frozen)
	if err != nil {
		fmt.Fprintf(stderr, "wormhole setup: %v\n", safeSetupError(err))
		return setupExitCode(err)
	}
	if frozen == nil {
		renderSetupPlan(stdout, plan)
		if !options.yes {
			confirmed, confirmErr := confirmSetup(stdin, stdout)
			if confirmErr != nil || !confirmed {
				fmt.Fprintln(stderr, "wormhole setup: plan was not confirmed")
				return 1
			}
		}
		if err := dependencies.journal.SetSelection(ctx, journal.JournalID, plan.Selection); err != nil {
			fmt.Fprintf(stderr, "wormhole setup: %v\n", safeSetupError(err))
			return 1
		}
		persisted, exists, err := dependencies.journal.Resumable(ctx, root)
		if err != nil || !exists || persisted.Selection == nil || !reflect.DeepEqual(*persisted.Selection, plan.Selection) {
			fmt.Fprintln(stderr, "wormhole setup: durable plan readback failed")
			return 1
		}
		journal = persisted
	} else if !reflect.DeepEqual(plan.Selection, *frozen) {
		fmt.Fprintf(stderr, "wormhole setup: %v\n", runtimeconfig.ErrConfirmedPlanDrift)
		return 1
	}

	for _, stage := range orderedCLISetupStages {
		result, stageErr := dependencies.driver.ReconcileStage(ctx, stage, plan, journal)
		if stageErr != nil {
			if !errors.Is(stageErr, runtimeconfig.ErrConfirmedPlanDrift) && setupStageIndexCLI(stage) == len(journal.CompletedStages) {
				_ = dependencies.journal.RecordLastError(ctx, journal.JournalID, stage, stageErr)
			}
			fmt.Fprintf(stderr, "wormhole setup: %s: %v\n", stage, safeSetupError(stageErr))
			return 1
		}
		if result.WorkspaceID != "" {
			if err := dependencies.journal.BindWorkspace(ctx, journal.JournalID, result.WorkspaceID); err != nil {
				fmt.Fprintf(stderr, "wormhole setup: %v\n", safeSetupError(err))
				return 1
			}
			journal.WorkspaceID = result.WorkspaceID
		}
		if result.IdentityPrincipalID != "" {
			if err := dependencies.journal.BindIdentity(ctx, journal.JournalID, result.IdentityPrincipalID); err != nil {
				fmt.Fprintf(stderr, "wormhole setup: %v\n", safeSetupError(err))
				return 1
			}
			journal.IdentityPrincipalID = result.IdentityPrincipalID
		}
		for _, reference := range result.ConnectorBackups {
			if err := dependencies.journal.RecordConnectorBackup(ctx, journal.JournalID, reference); err != nil {
				fmt.Fprintf(stderr, "wormhole setup: %v\n", safeSetupError(err))
				return 1
			}
		}
		if err := dependencies.journal.MarkCompleted(ctx, journal.JournalID, stage); err != nil {
			fmt.Fprintf(stderr, "wormhole setup: %v\n", safeSetupError(err))
			return 1
		}
		if setupStageIndexCLI(stage) == len(journal.CompletedStages) {
			journal.CompletedStages = append(journal.CompletedStages, stage)
		}
		fmt.Fprintf(stdout, "[%d/8] %s\n", setupStageIndexCLI(stage)+1, stage)
	}
	if err := dependencies.journal.Complete(ctx, journal.JournalID); err != nil {
		fmt.Fprintf(stderr, "wormhole setup: %v\n", safeSetupError(err))
		return 1
	}
	fmt.Fprintln(stdout, "Wormhole setup complete.")
	return 0
}

func parseSetupOptions(args []string, stderr io.Writer) (setupOptions, error) {
	flags := flag.NewFlagSet("wormhole setup", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var options setupOptions
	flags.BoolVar(&options.yes, "yes", false, "apply the rendered complete plan")
	flags.StringVar(&options.publication, "publication", "unclassified", "unclassified|local_only|public_git|private_git")
	flags.StringVar(&options.name, "name", "", "confirmed local identity display name")
	flags.StringVar(&options.email, "email", "", "confirmed local identity email")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		if err == nil {
			err = errors.New("unexpected positional arguments")
		}
		return setupOptions{}, err
	}
	classification := types.PublicationClassification(options.publication)
	if classification.Validate() != nil {
		fmt.Fprintln(stderr, "wormhole setup: invalid --publication value")
		return setupOptions{}, types.ErrInvalidPublicationClassification
	}
	return options, nil
}

func renderSetupPlan(output io.Writer, plan setupPlan) {
	fmt.Fprintln(output, "Wormhole setup plan")
	fmt.Fprintf(output, "  project: %s\n", plan.ProjectID)
	fmt.Fprintf(output, "  accepted base: %s (%s)\n", plan.Commit, plan.TreeDigest)
	fmt.Fprintf(output, "  publication: %s (binding %s)\n", plan.Selection.PublicationVisibility, plan.Selection.PublicationBindingDigest)
	fmt.Fprintf(output, "  identity: %s\n", plan.Selection.Identity.DisplayName)
	if plan.Selection.Identity.Email != "" {
		fmt.Fprintf(output, "  identity email: %s\n", plan.Selection.Identity.Email)
	}
	if plan.Selection.PublicationVisibility == string(types.PublicationPublicGit) {
		fmt.Fprintln(output, "  warning: public Git publication can disclose all tracked Wormhole state")
	}
	if plan.Selection.PublicationVisibility == string(types.PublicationUnclassified) {
		fmt.Fprintln(output, "  warning: unclassified publication keeps checkpoint blocked")
	}
	for _, stage := range orderedCLISetupStages {
		count := 0
		for _, change := range plan.Selection.Changes {
			if change.Stage != stage {
				continue
			}
			fmt.Fprintf(output, "  change %s %s %s: %s -> %s\n", change.Stage, change.Subject, change.Action, change.PriorDigest, change.DesiredDigest)
			count++
		}
		if count == 0 {
			fmt.Fprintf(output, "  no-op %s: verify exact desired state\n", stage)
		}
	}
}

func confirmSetup(input io.Reader, output io.Writer) (bool, error) {
	fmt.Fprint(output, "Apply this complete plan? [y/N] ")
	line, err := bufio.NewReader(io.LimitReader(input, 32)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}

func setupStageIndexCLI(stage runtimeconfig.SetupStage) int {
	for index, candidate := range orderedCLISetupStages {
		if candidate == stage {
			return index
		}
	}
	return -1
}

func cloneSetupSelection(selection runtimeconfig.SetupSelection) runtimeconfig.SetupSelection {
	selection.ConnectorAdapters = append([]string{}, selection.ConnectorAdapters...)
	selection.Changes = append([]runtimeconfig.ConfirmedChange{}, selection.Changes...)
	return selection
}

func safeSetupError(err error) error {
	for _, safe := range []error{context.Canceled, context.DeadlineExceeded, runtimeconfig.ErrConfirmedPlanDrift, runtimeconfig.ErrConfirmedPlanRequired, runtimeconfig.ErrInvalidConfirmedPlan, runtimeconfig.ErrSetupJournalFilesystemUnsupported, runtimeconfig.ErrServiceManagerUnavailable} {
		if errors.Is(err, safe) {
			return safe
		}
	}
	return errors.New("setup stage failed; inspect the owning component for details")
}

func setupExitCode(err error) int {
	if errors.Is(err, types.ErrInvalidConfirmedIdentitySelection) || errors.Is(err, types.ErrInvalidPublicationClassification) {
		return 2
	}
	return 1
}
