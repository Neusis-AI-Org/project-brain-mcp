package github

import (
	"github.com/Neusis-AI-Org/mcp-project-brain/pkg/inventory"
	"github.com/Neusis-AI-Org/mcp-project-brain/pkg/translations"
)

// AllResources returns all resource templates with their embedded toolset metadata.
// Resource definitions are stateless - handlers are generated on-demand during registration.
func AllResources(t translations.TranslationHelperFunc) []inventory.ServerResourceTemplate {
	return []inventory.ServerResourceTemplate{
		// Repository resources
		GetRepositoryResourceContent(t),
		GetRepositoryResourceBranchContent(t),
		GetRepositoryResourceCommitContent(t),
		GetRepositoryResourceTagContent(t),
		GetRepositoryResourcePrContent(t),
	}
}
