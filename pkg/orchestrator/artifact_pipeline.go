package orchestrator

import (
	"github.com/odvcencio/buckley/pkg/artifact"
	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/docs"
)

// artifactPipeline centralizes documentation and artifact generation.
type artifactPipeline struct {
	docs     *docs.HierarchyManager
	planning *artifact.PlanningGenerator
	review   *artifact.ReviewGenerator
	chain    *artifact.ChainManager
}

func newArtifactPipeline(cfg *config.Config, docsRoot string) *artifactPipeline {
	return &artifactPipeline{
		docs:     docs.NewHierarchyManager(docsRoot),
		planning: artifact.NewPlanningGenerator(cfg.Artifacts.PlanningDir),
		review:   artifact.NewReviewGenerator(cfg.Artifacts.ReviewDir),
		chain:    artifact.NewChainManager(docsRoot),
	}
}

func (p *artifactPipeline) ensureDocs() error {
	if !p.docs.Exists() {
		return p.docs.Initialize()
	}
	return p.docs.ValidateStructure()
}

func (p *artifactPipeline) planningGenerator() *artifact.PlanningGenerator {
	return p.planning
}

func (p *artifactPipeline) reviewGenerator() *artifact.ReviewGenerator {
	return p.review
}

func (p *artifactPipeline) chainManager() *artifact.ChainManager {
	return p.chain
}
