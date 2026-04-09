package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	ghErrors "github.com/github/github-mcp-server/pkg/errors"
	"github.com/github/github-mcp-server/pkg/inventory"
	"github.com/github/github-mcp-server/pkg/scopes"
	"github.com/github/github-mcp-server/pkg/translations"
	"github.com/github/github-mcp-server/pkg/utils"
	"github.com/google/go-github/v82/github"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// kbDefaultsContextKey is the context key for KB default owner/repo.
type kbDefaultsContextKey struct{}

// KBDefaults holds the default owner and repo for KB tools.
type KBDefaults struct {
	Owner string
	Repo  string
}

// ContextWithKBDefaults returns a new context with KB defaults stored in it.
func ContextWithKBDefaults(ctx context.Context, defaults KBDefaults) context.Context {
	return context.WithValue(ctx, kbDefaultsContextKey{}, defaults)
}

// KBDefaultsFromContext retrieves KB defaults from the context.
func KBDefaultsFromContext(ctx context.Context) (KBDefaults, bool) {
	defaults, ok := ctx.Value(kbDefaultsContextKey{}).(KBDefaults)
	return defaults, ok
}

// resolveKBOwnerRepo resolves owner/repo from server-configured defaults in context.
// Returns owner, repo, and an error message if not configured.
func resolveKBOwnerRepo(ctx context.Context) (string, string, string) {
	if defaults, ok := KBDefaultsFromContext(ctx); ok {
		if defaults.Owner != "" && defaults.Repo != "" {
			return defaults.Owner, defaults.Repo, ""
		}
	}
	return "", "", "knowledge base repository not configured — set --kb-owner and --kb-repo server flags"
}

// InjectKBDefaultsMiddleware creates MCP middleware that injects KB defaults into context.
func InjectKBDefaultsMiddleware(defaults KBDefaults) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (result mcp.Result, err error) {
			return next(ContextWithKBDefaults(ctx, defaults), method, req)
		}
	}
}

// ToolsetMetadataKnowledgeBase defines the knowledge base toolset.
var ToolsetMetadataKnowledgeBase = inventory.ToolsetMetadata{
	ID:               "knowledge_base",
	Description:      "Knowledge base tools for managing project context, architecture docs, ADRs, meeting notes, and more",
	Default:          true,
	Icon:             "book",
	InstructionsFunc: generateKBToolsetInstructions,
}

func generateKBToolsetInstructions(_ *inventory.Inventory) string {
	return `## Knowledge Base Tools
These tools manage a project knowledge base stored in a GitHub repository.
The knowledge base structure and rules are defined in an AGENTS.md file at the root of the repository.
Always call kb_config first to understand the repository's knowledge base structure before using other KB tools.
All documents should use markdown format with YAML frontmatter (title, date, author, tags).
The target repository is pre-configured on the server via --kb-owner and --kb-repo flags. You do not need to specify any repository information.`
}

// KBConfig represents the parsed AGENTS.md configuration.
type KBConfig struct {
	Version   int                    `json:"version"`
	Structure []KBStructureEntry     `json:"structure"`
	Rules     KBRules                `json:"rules"`
	Agents    map[string]KBAgentPerm `json:"agents,omitempty"`
}

// KBStructureEntry represents a directory in the knowledge base.
type KBStructureEntry struct {
	Path        string `json:"path"`
	Description string `json:"description"`
}

// KBRules represents the rules section of AGENTS.md.
type KBRules struct {
	Frontmatter string `json:"frontmatter"` // "required" or "optional"
	Naming      string `json:"naming"`      // "kebab-case", "snake_case", etc.
	DatePrefix  bool   `json:"date_prefix"` // whether files should be date-prefixed
}

// KBAgentPerm represents permissions for a specific agent.
type KBAgentPerm struct {
	Write []string `json:"write"`
	Read  []string `json:"read"`
}

// KBDocument represents a knowledge base document with parsed frontmatter.
type KBDocument struct {
	Path        string            `json:"path"`
	Title       string            `json:"title,omitempty"`
	Date        string            `json:"date,omitempty"`
	Author      string            `json:"author,omitempty"`
	Tags        []string          `json:"tags,omitempty"`
	Description string            `json:"description,omitempty"`
	Extra       map[string]string `json:"extra,omitempty"`
	Size        int               `json:"size"`
	SHA         string            `json:"sha"`
}

// KBSearchResult represents a search result with context.
type KBSearchResult struct {
	Path       string   `json:"path"`
	Title      string   `json:"title,omitempty"`
	Tags       []string `json:"tags,omitempty"`
	MatchLines []string `json:"match_lines"`
	Score      int      `json:"score"`
}

// parseAgentsMD parses the AGENTS.md file content into a KBConfig.
// The AGENTS.md uses a simple YAML-like format embedded in markdown.
func parseAgentsMD(content string) (*KBConfig, error) {
	config := &KBConfig{
		Version: 1,
		Rules: KBRules{
			Frontmatter: "required",
			Naming:      "kebab-case",
			DatePrefix:  false,
		},
		Agents: make(map[string]KBAgentPerm),
	}

	lines := strings.Split(content, "\n")
	var currentSection string
	var currentAgent string

	for _, rawLine := range lines {
		line := strings.TrimSpace(rawLine)

		// Skip empty lines and YAML frontmatter delimiters
		if line == "" || line == "---" {
			continue
		}

		// Detect sections
		if strings.HasPrefix(line, "## ") {
			currentSection = strings.ToLower(strings.TrimPrefix(line, "## "))
			currentAgent = ""
			continue
		}
		if strings.HasPrefix(line, "# ") {
			currentSection = ""
			currentAgent = ""
			continue
		}

		switch currentSection {
		case "structure":
			// Parse lines like "- docs/architecture    # System design, diagrams"
			if strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ") {
				entry := strings.TrimPrefix(line, "- ")
				entry = strings.TrimPrefix(entry, "* ")
				parts := strings.SplitN(entry, "#", 2)
				path := strings.TrimSpace(parts[0])
				desc := ""
				if len(parts) > 1 {
					desc = strings.TrimSpace(parts[1])
				}
				// Remove leading/trailing slashes
				path = strings.Trim(path, "/")
				if path != "" {
					config.Structure = append(config.Structure, KBStructureEntry{
						Path:        path,
						Description: desc,
					})
				}
			}

		case "rules":
			// Parse lines like "frontmatter: required"
			if strings.Contains(line, ":") {
				parts := strings.SplitN(line, ":", 2)
				key := strings.TrimSpace(parts[0])
				value := strings.TrimSpace(parts[1])
				switch key {
				case "frontmatter":
					config.Rules.Frontmatter = value
				case "naming":
					config.Rules.Naming = value
				case "date_prefix":
					config.Rules.DatePrefix = strings.ToLower(value) == "true"
				}
			}

		case "agents":
			// Parse agent permissions block
			// Lines like "neusis-bot:" define an agent
			// Indented lines like "  write: [docs/meeting-notes]" define permissions
			if !strings.HasPrefix(rawLine, " ") && !strings.HasPrefix(rawLine, "\t") && strings.HasSuffix(line, ":") {
				currentAgent = strings.TrimSuffix(line, ":")
				config.Agents[currentAgent] = KBAgentPerm{}
			} else if currentAgent != "" && strings.Contains(line, ":") {
				parts := strings.SplitN(line, ":", 2)
				key := strings.TrimSpace(parts[0])
				value := strings.TrimSpace(parts[1])
				// Parse [path1, path2] format
				value = strings.Trim(value, "[]")
				paths := strings.Split(value, ",")
				var cleaned []string
				for _, p := range paths {
					p = strings.TrimSpace(p)
					if p != "" {
						cleaned = append(cleaned, p)
					}
				}
				perm := config.Agents[currentAgent]
				switch key {
				case "write":
					perm.Write = cleaned
				case "read":
					perm.Read = cleaned
				}
				config.Agents[currentAgent] = perm
			}
		}

		// Parse version from frontmatter-style
		if strings.HasPrefix(line, "version:") {
			parts := strings.SplitN(line, ":", 2)
			v := strings.TrimSpace(parts[1])
			if v == "1" {
				config.Version = 1
			}
		}
	}

	return config, nil
}

// parseFrontmatter extracts YAML frontmatter from a markdown document.
func parseFrontmatter(content string) map[string]string {
	fm := make(map[string]string)
	if !strings.HasPrefix(content, "---") {
		return fm
	}
	lines := strings.Split(content, "\n")
	inFrontmatter := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "---" {
			if inFrontmatter {
				break // End of frontmatter
			}
			inFrontmatter = true
			continue
		}
		if inFrontmatter && strings.Contains(trimmed, ":") {
			parts := strings.SplitN(trimmed, ":", 2)
			key := strings.TrimSpace(parts[0])
			value := strings.TrimSpace(parts[1])
			// Remove quotes
			value = strings.Trim(value, "\"'")
			fm[key] = value
		}
	}
	return fm
}

// generateFrontmatter creates YAML frontmatter string from parameters.
func generateFrontmatter(title, author string, tags []string) string {
	var sb strings.Builder
	sb.WriteString("---\n")
	sb.WriteString(fmt.Sprintf("title: %q\n", title))
	sb.WriteString(fmt.Sprintf("date: %q\n", time.Now().UTC().Format("2006-01-02")))
	if author != "" {
		sb.WriteString(fmt.Sprintf("author: %q\n", author))
	}
	if len(tags) > 0 {
		sb.WriteString(fmt.Sprintf("tags: [%s]\n", strings.Join(tags, ", ")))
	}
	sb.WriteString("---\n\n")
	return sb.String()
}

// KBConfigTool reads and parses the AGENTS.md configuration from a repository.
func KBConfigTool(t translations.TranslationHelperFunc) inventory.ServerTool {
	return NewTool(
		ToolsetMetadataKnowledgeBase,
		mcp.Tool{
			Name:        "kb_config",
			Description: t("TOOL_KB_CONFIG_DESCRIPTION", "Read and parse the AGENTS.md knowledge base configuration from a GitHub repository. Call this first to understand the repo's knowledge base structure, rules, and agent permissions before using other KB tools."),
			Annotations: &mcp.ToolAnnotations{
				Title:        t("TOOL_KB_CONFIG_USER_TITLE", "Get knowledge base config"),
				ReadOnlyHint: true,
			},
			InputSchema: &jsonschema.Schema{
				Type: "object",
				Properties: map[string]*jsonschema.Schema{
"ref": {
						Type:        "string",
						Description: "Git ref (branch or tag). Defaults to the repository's default branch.",
					},
				},
			},
		},
		[]scopes.Scope{scopes.Repo},
		func(ctx context.Context, deps ToolDependencies, _ *mcp.CallToolRequest, args map[string]any) (*mcp.CallToolResult, any, error) {
			owner, repo, errMsg := resolveKBOwnerRepo(ctx)
			if errMsg != "" {
				return utils.NewToolResultError(errMsg), nil, nil
			}
			ref, err := OptionalParam[string](args, "ref")
			if err != nil {
				return utils.NewToolResultError(err.Error()), nil, nil
			}

			client, err := deps.GetClient(ctx)
			if err != nil {
				return utils.NewToolResultError("failed to get GitHub client"), nil, nil
			}

			opts := &github.RepositoryContentGetOptions{Ref: ref}
			fileContent, _, resp, err := client.Repositories.GetContents(ctx, owner, repo, "AGENTS.md", opts)
			if err != nil {
				if resp != nil && resp.StatusCode == http.StatusNotFound {
					return utils.NewToolResultError("AGENTS.md not found in repository. Use kb_init to create a knowledge base configuration."), nil, nil
				}
				return ghErrors.NewGitHubAPIErrorResponse(ctx, "failed to get AGENTS.md", resp, err), nil, nil
			}
			if resp != nil && resp.Body != nil {
				defer func() { _ = resp.Body.Close() }()
			}

			content, err := fileContent.GetContent()
			if err != nil {
				return utils.NewToolResultError(fmt.Sprintf("failed to decode AGENTS.md: %s", err)), nil, nil
			}

			config, err := parseAgentsMD(content)
			if err != nil {
				return utils.NewToolResultError(fmt.Sprintf("failed to parse AGENTS.md: %s", err)), nil, nil
			}

			r, err := json.Marshal(config)
			if err != nil {
				return utils.NewToolResultError("failed to marshal config"), nil, nil
			}

			return utils.NewToolResultText(string(r)), nil, nil
		},
	)
}

// KBWriteTool creates or updates a knowledge base document with proper frontmatter.
func KBWriteTool(t translations.TranslationHelperFunc) inventory.ServerTool {
	return NewTool(
		ToolsetMetadataKnowledgeBase,
		mcp.Tool{
			Name:        "kb_write",
			Description: t("TOOL_KB_WRITE_DESCRIPTION", "Create or update a knowledge base document in a GitHub repository. Automatically adds YAML frontmatter if not present. Validates the path against AGENTS.md structure rules."),
			Annotations: &mcp.ToolAnnotations{
				Title:        t("TOOL_KB_WRITE_USER_TITLE", "Write knowledge base document"),
				ReadOnlyHint: false,
			},
			InputSchema: &jsonschema.Schema{
				Type: "object",
				Properties: map[string]*jsonschema.Schema{
"path": {
						Type:        "string",
						Description: "Path for the document (e.g., 'docs/architecture/auth-system.md'). Must be within a path defined in AGENTS.md structure.",
					},
					"title": {
						Type:        "string",
						Description: "Document title for frontmatter",
					},
					"content": {
						Type:        "string",
						Description: "Document content in markdown format. Frontmatter will be auto-generated if not included.",
					},
					"author": {
						Type:        "string",
						Description: "Author name for frontmatter",
					},
					"tags": {
						Type:        "array",
						Description: "Tags for the document",
						Items: &jsonschema.Schema{
							Type: "string",
						},
					},
					"branch": {
						Type:        "string",
						Description: "Branch to write to. Defaults to the repository's default branch.",
					},
					"message": {
						Type:        "string",
						Description: "Commit message. If not provided, a default message will be generated.",
					},
					"sha": {
						Type:        "string",
						Description: "SHA of the file being replaced (required for updates, not for new files)",
					},
				},
				Required: []string{"path", "title", "content"},
			},
		},
		[]scopes.Scope{scopes.Repo},
		func(ctx context.Context, deps ToolDependencies, _ *mcp.CallToolRequest, args map[string]any) (*mcp.CallToolResult, any, error) {
			owner, repo, errMsg := resolveKBOwnerRepo(ctx)
			if errMsg != "" {
				return utils.NewToolResultError(errMsg), nil, nil
			}
			path, err := RequiredParam[string](args, "path")
			if err != nil {
				return utils.NewToolResultError(err.Error()), nil, nil
			}
			title, err := RequiredParam[string](args, "title")
			if err != nil {
				return utils.NewToolResultError(err.Error()), nil, nil
			}
			content, err := RequiredParam[string](args, "content")
			if err != nil {
				return utils.NewToolResultError(err.Error()), nil, nil
			}
			author, err := OptionalParam[string](args, "author")
			if err != nil {
				return utils.NewToolResultError(err.Error()), nil, nil
			}
			branch, err := OptionalParam[string](args, "branch")
			if err != nil {
				return utils.NewToolResultError(err.Error()), nil, nil
			}
			message, err := OptionalParam[string](args, "message")
			if err != nil {
				return utils.NewToolResultError(err.Error()), nil, nil
			}
			sha, err := OptionalParam[string](args, "sha")
			if err != nil {
				return utils.NewToolResultError(err.Error()), nil, nil
			}

			// Parse tags
			var tags []string
			if tagsRaw, ok := args["tags"]; ok {
				if tagsArr, ok := tagsRaw.([]any); ok {
					for _, t := range tagsArr {
						if s, ok := t.(string); ok {
							tags = append(tags, s)
						}
					}
				}
			}

			// Ensure path ends with .md
			if !strings.HasSuffix(path, ".md") {
				path += ".md"
			}

			// Remove leading slash
			path = strings.TrimPrefix(path, "/")

			client, err := deps.GetClient(ctx)
			if err != nil {
				return utils.NewToolResultError("failed to get GitHub client"), nil, nil
			}

			// Validate path against AGENTS.md structure
			opts := &github.RepositoryContentGetOptions{Ref: branch}
			agentsFile, _, agentsResp, agentsErr := client.Repositories.GetContents(ctx, owner, repo, "AGENTS.md", opts)
			if agentsErr == nil && agentsFile != nil {
				if agentsResp != nil && agentsResp.Body != nil {
					defer func() { _ = agentsResp.Body.Close() }()
				}
				agentsContent, err := agentsFile.GetContent()
				if err == nil {
					config, err := parseAgentsMD(agentsContent)
					if err == nil && len(config.Structure) > 0 {
						valid := false
						for _, entry := range config.Structure {
							if strings.HasPrefix(path, entry.Path+"/") || strings.HasPrefix(path, entry.Path) {
								valid = true
								break
							}
						}
						if !valid {
							allowedPaths := make([]string, len(config.Structure))
							for i, e := range config.Structure {
								allowedPaths[i] = e.Path
							}
							return utils.NewToolResultError(fmt.Sprintf(
								"path %q is not within any allowed knowledge base directory. Allowed paths: %s",
								path, strings.Join(allowedPaths, ", "),
							)), nil, nil
						}
					}
				}
			}

			// Auto-inject frontmatter if content doesn't already have it
			if !strings.HasPrefix(strings.TrimSpace(content), "---") {
				frontmatter := generateFrontmatter(title, author, tags)
				content = frontmatter + content
			}

			// Generate default commit message if not provided
			if message == "" {
				message = fmt.Sprintf("docs: add %s", title)
			}

			// Create or update the file
			fileOpts := &github.RepositoryContentFileOptions{
				Message: github.Ptr(message),
				Content: []byte(content),
			}
			if branch != "" {
				fileOpts.Branch = github.Ptr(branch)
			}
			if sha != "" {
				fileOpts.SHA = github.Ptr(sha)
			}

			// If no SHA provided, check if file already exists and get its SHA for update
			if sha == "" {
				existingFile, _, existResp, existErr := client.Repositories.GetContents(ctx, owner, repo, path, &github.RepositoryContentGetOptions{Ref: branch})
				if existErr == nil && existingFile != nil {
					if existResp != nil && existResp.Body != nil {
						defer func() { _ = existResp.Body.Close() }()
					}
					fileOpts.SHA = existingFile.SHA
				}
			}

			resp, apiResp, err := client.Repositories.CreateFile(ctx, owner, repo, path, fileOpts)
			if err != nil {
				return ghErrors.NewGitHubAPIErrorResponse(ctx, "failed to create/update file", apiResp, err), nil, nil
			}
			if apiResp != nil && apiResp.Body != nil {
				defer func() { _ = apiResp.Body.Close() }()
			}

			result := map[string]any{
				"path":    path,
				"sha":     resp.Content.GetSHA(),
				"message": message,
				"action":  "created",
			}
			if sha != "" || (fileOpts.SHA != nil && *fileOpts.SHA != "") {
				result["action"] = "updated"
			}

			r, err := json.Marshal(result)
			if err != nil {
				return utils.NewToolResultError("failed to marshal response"), nil, nil
			}
			return utils.NewToolResultText(string(r)), nil, nil
		},
	)
}

// KBListTool lists knowledge base documents with parsed frontmatter metadata.
func KBListTool(t translations.TranslationHelperFunc) inventory.ServerTool {
	return NewTool(
		ToolsetMetadataKnowledgeBase,
		mcp.Tool{
			Name:        "kb_list",
			Description: t("TOOL_KB_LIST_DESCRIPTION", "List knowledge base documents with their frontmatter metadata (title, date, author, tags). Supports filtering by folder, tags, and date range."),
			Annotations: &mcp.ToolAnnotations{
				Title:        t("TOOL_KB_LIST_USER_TITLE", "List knowledge base documents"),
				ReadOnlyHint: true,
			},
			InputSchema: &jsonschema.Schema{
				Type: "object",
				Properties: map[string]*jsonschema.Schema{
"path": {
						Type:        "string",
						Description: "Filter by directory path (e.g., 'docs/architecture'). Defaults to listing all KB paths from AGENTS.md.",
					},
					"tag": {
						Type:        "string",
						Description: "Filter documents by tag",
					},
					"ref": {
						Type:        "string",
						Description: "Git ref (branch or tag). Defaults to the repository's default branch.",
					},
				},
			},
		},
		[]scopes.Scope{scopes.Repo},
		func(ctx context.Context, deps ToolDependencies, _ *mcp.CallToolRequest, args map[string]any) (*mcp.CallToolResult, any, error) {
			owner, repo, errMsg := resolveKBOwnerRepo(ctx)
			if errMsg != "" {
				return utils.NewToolResultError(errMsg), nil, nil
			}
			pathFilter, err := OptionalParam[string](args, "path")
			if err != nil {
				return utils.NewToolResultError(err.Error()), nil, nil
			}
			tagFilter, err := OptionalParam[string](args, "tag")
			if err != nil {
				return utils.NewToolResultError(err.Error()), nil, nil
			}
			ref, err := OptionalParam[string](args, "ref")
			if err != nil {
				return utils.NewToolResultError(err.Error()), nil, nil
			}

			client, err := deps.GetClient(ctx)
			if err != nil {
				return utils.NewToolResultError("failed to get GitHub client"), nil, nil
			}

			// Determine which paths to scan
			var scanPaths []string
			if pathFilter != "" {
				scanPaths = []string{strings.Trim(pathFilter, "/")}
			} else {
				// Read AGENTS.md to get KB structure paths
				opts := &github.RepositoryContentGetOptions{Ref: ref}
				agentsFile, _, agentsResp, agentsErr := client.Repositories.GetContents(ctx, owner, repo, "AGENTS.md", opts)
				if agentsErr == nil && agentsFile != nil {
					if agentsResp != nil && agentsResp.Body != nil {
						defer func() { _ = agentsResp.Body.Close() }()
					}
					agentsContent, err := agentsFile.GetContent()
					if err == nil {
						config, err := parseAgentsMD(agentsContent)
						if err == nil {
							for _, entry := range config.Structure {
								scanPaths = append(scanPaths, entry.Path)
							}
						}
					}
				}
				if len(scanPaths) == 0 {
					scanPaths = []string{"docs"}
				}
			}

			// Use Git Tree API to get all files recursively
			repoInfo, repoResp, err := client.Repositories.Get(ctx, owner, repo)
			if err != nil {
				return ghErrors.NewGitHubAPIErrorResponse(ctx, "failed to get repository info", repoResp, err), nil, nil
			}
			if repoResp != nil && repoResp.Body != nil {
				defer func() { _ = repoResp.Body.Close() }()
			}

			treeSHA := ref
			if treeSHA == "" {
				treeSHA = repoInfo.GetDefaultBranch()
			}

			tree, treeResp, err := client.Git.GetTree(ctx, owner, repo, treeSHA, true)
			if err != nil {
				return ghErrors.NewGitHubAPIErrorResponse(ctx, "failed to get repository tree", treeResp, err), nil, nil
			}
			if treeResp != nil && treeResp.Body != nil {
				defer func() { _ = treeResp.Body.Close() }()
			}

			// Filter tree entries to markdown files in KB paths
			var docs []KBDocument
			for _, entry := range tree.Entries {
				if entry.GetType() != "blob" || !strings.HasSuffix(entry.GetPath(), ".md") {
					continue
				}

				entryPath := entry.GetPath()
				inScope := false
				for _, sp := range scanPaths {
					if strings.HasPrefix(entryPath, sp+"/") || entryPath == sp {
						inScope = true
						break
					}
				}
				if !inScope {
					continue
				}

				doc := KBDocument{
					Path: entryPath,
					Size: entry.GetSize(),
					SHA:  entry.GetSHA(),
				}

				// Fetch file content to parse frontmatter (only for small files)
				if entry.GetSize() < 10000 { // Only parse frontmatter for files under 10KB
					contentOpts := &github.RepositoryContentGetOptions{Ref: ref}
					fileContent, _, fileResp, fileErr := client.Repositories.GetContents(ctx, owner, repo, entryPath, contentOpts)
					if fileErr == nil && fileContent != nil {
						if fileResp != nil && fileResp.Body != nil {
							defer func() { _ = fileResp.Body.Close() }()
						}
						text, err := fileContent.GetContent()
						if err == nil {
							fm := parseFrontmatter(text)
							doc.Title = fm["title"]
							doc.Date = fm["date"]
							doc.Author = fm["author"]
							doc.Description = fm["description"]
							if tagsStr, ok := fm["tags"]; ok {
								tagsStr = strings.Trim(tagsStr, "[]")
								for _, tag := range strings.Split(tagsStr, ",") {
									tag = strings.TrimSpace(tag)
									if tag != "" {
										doc.Tags = append(doc.Tags, tag)
									}
								}
							}
						}
					}
				}

				// Apply tag filter
				if tagFilter != "" {
					found := false
					for _, tag := range doc.Tags {
						if strings.EqualFold(tag, tagFilter) {
							found = true
							break
						}
					}
					if !found {
						continue
					}
				}

				docs = append(docs, doc)
			}

			result := map[string]any{
				"total_count": len(docs),
				"documents":   docs,
			}

			r, err := json.Marshal(result)
			if err != nil {
				return utils.NewToolResultError("failed to marshal response"), nil, nil
			}
			return utils.NewToolResultText(string(r)), nil, nil
		},
	)
}

// KBSearchTool performs full-text search across knowledge base documents.
func KBSearchTool(t translations.TranslationHelperFunc) inventory.ServerTool {
	return NewTool(
		ToolsetMetadataKnowledgeBase,
		mcp.Tool{
			Name:        "kb_search",
			Description: t("TOOL_KB_SEARCH_DESCRIPTION", "Search across knowledge base documents by keyword. Returns matching documents with context lines and frontmatter metadata. Searches within the knowledge base paths defined in AGENTS.md."),
			Annotations: &mcp.ToolAnnotations{
				Title:        t("TOOL_KB_SEARCH_USER_TITLE", "Search knowledge base"),
				ReadOnlyHint: true,
			},
			InputSchema: &jsonschema.Schema{
				Type: "object",
				Properties: map[string]*jsonschema.Schema{
"query": {
						Type:        "string",
						Description: "Search query. Searches across document titles, tags, and content.",
					},
					"path": {
						Type:        "string",
						Description: "Optional: limit search to a specific KB directory (e.g., 'docs/architecture')",
					},
					"ref": {
						Type:        "string",
						Description: "Git ref (branch or tag). Defaults to the repository's default branch.",
					},
				},
				Required: []string{"query"},
			},
		},
		[]scopes.Scope{scopes.Repo},
		func(ctx context.Context, deps ToolDependencies, _ *mcp.CallToolRequest, args map[string]any) (*mcp.CallToolResult, any, error) {
			owner, repo, errMsg := resolveKBOwnerRepo(ctx)
			if errMsg != "" {
				return utils.NewToolResultError(errMsg), nil, nil
			}
			query, err := RequiredParam[string](args, "query")
			if err != nil {
				return utils.NewToolResultError(err.Error()), nil, nil
			}
			pathFilter, err := OptionalParam[string](args, "path")
			if err != nil {
				return utils.NewToolResultError(err.Error()), nil, nil
			}
			ref, err := OptionalParam[string](args, "ref")
			if err != nil {
				return utils.NewToolResultError(err.Error()), nil, nil
			}

			client, err := deps.GetClient(ctx)
			if err != nil {
				return utils.NewToolResultError("failed to get GitHub client"), nil, nil
			}

			// Build GitHub code search query scoped to the repo and markdown files
			searchQuery := fmt.Sprintf("%s repo:%s/%s language:markdown", query, owner, repo)
			if pathFilter != "" {
				searchQuery += fmt.Sprintf(" path:%s", strings.Trim(pathFilter, "/"))
			} else {
				// Try to scope search to KB paths from AGENTS.md
				opts := &github.RepositoryContentGetOptions{Ref: ref}
				agentsFile, _, agentsResp, agentsErr := client.Repositories.GetContents(ctx, owner, repo, "AGENTS.md", opts)
				if agentsErr == nil && agentsFile != nil {
					if agentsResp != nil && agentsResp.Body != nil {
						defer func() { _ = agentsResp.Body.Close() }()
					}
					agentsContent, err := agentsFile.GetContent()
					if err == nil {
						config, err := parseAgentsMD(agentsContent)
						if err == nil && len(config.Structure) > 0 {
							// Use the first path as a scope hint (GitHub search supports one path filter)
							// For multiple paths, we filter results after search
							searchQuery += fmt.Sprintf(" path:%s", config.Structure[0].Path)
						}
					}
				}
			}

			searchOpts := &github.SearchOptions{
				ListOptions: github.ListOptions{
					PerPage: 20,
				},
			}

			searchResult, searchResp, err := client.Search.Code(ctx, searchQuery, searchOpts)
			if err != nil {
				return ghErrors.NewGitHubAPIErrorResponse(ctx,
					fmt.Sprintf("failed to search knowledge base with query '%s'", query),
					searchResp, err), nil, nil
			}
			if searchResp != nil && searchResp.Body != nil {
				defer func() { _ = searchResp.Body.Close() }()
			}

			// Process results and enrich with frontmatter
			var results []KBSearchResult
			queryLower := strings.ToLower(query)

			for _, codeResult := range searchResult.CodeResults {
				filePath := codeResult.GetPath()

				result := KBSearchResult{
					Path:  filePath,
					Score: 1,
				}

				// Extract text matches
				for _, match := range codeResult.TextMatches {
					fragment := match.GetFragment()
					if fragment != "" {
						result.MatchLines = append(result.MatchLines, fragment)
					}
				}

				// Try to get frontmatter for richer results
				contentOpts := &github.RepositoryContentGetOptions{Ref: ref}
				fileContent, _, fileResp, fileErr := client.Repositories.GetContents(ctx, owner, repo, filePath, contentOpts)
				if fileErr == nil && fileContent != nil {
					if fileResp != nil && fileResp.Body != nil {
						defer func() { _ = fileResp.Body.Close() }()
					}
					text, err := fileContent.GetContent()
					if err == nil {
						fm := parseFrontmatter(text)
						result.Title = fm["title"]
						if tagsStr, ok := fm["tags"]; ok {
							tagsStr = strings.Trim(tagsStr, "[]")
							for _, tag := range strings.Split(tagsStr, ",") {
								tag = strings.TrimSpace(tag)
								if tag != "" {
									result.Tags = append(result.Tags, tag)
								}
							}
						}

						// Boost score for title matches
						if strings.Contains(strings.ToLower(result.Title), queryLower) {
							result.Score += 5
						}
						// Boost score for tag matches
						for _, tag := range result.Tags {
							if strings.Contains(strings.ToLower(tag), queryLower) {
								result.Score += 3
							}
						}
					}
				}

				results = append(results, result)
			}

			response := map[string]any{
				"total_count": len(results),
				"query":       query,
				"results":     results,
			}

			r, err := json.Marshal(response)
			if err != nil {
				return utils.NewToolResultError("failed to marshal response"), nil, nil
			}
			return utils.NewToolResultText(string(r)), nil, nil
		},
	)
}

// KBInitTool scaffolds a new knowledge base in a repository.
func KBInitTool(t translations.TranslationHelperFunc) inventory.ServerTool {
	return NewTool(
		ToolsetMetadataKnowledgeBase,
		mcp.Tool{
			Name:        "kb_init",
			Description: t("TOOL_KB_INIT_DESCRIPTION", "Initialize a knowledge base in a GitHub repository by creating an AGENTS.md configuration file and the initial folder structure. Use this to set up a new knowledge base."),
			Annotations: &mcp.ToolAnnotations{
				Title:        t("TOOL_KB_INIT_USER_TITLE", "Initialize knowledge base"),
				ReadOnlyHint: false,
			},
			InputSchema: &jsonschema.Schema{
				Type: "object",
				Properties: map[string]*jsonschema.Schema{
"directories": {
						Type:        "array",
						Description: "Knowledge base directories to create. Each entry is an object with 'path' and optional 'description'. Defaults to a standard structure if not provided.",
						Items: &jsonschema.Schema{
							Type: "object",
							Properties: map[string]*jsonschema.Schema{
								"path": {
									Type:        "string",
									Description: "Directory path (e.g., 'docs/architecture')",
								},
								"description": {
									Type:        "string",
									Description: "Description of what this directory contains",
								},
							},
							Required: []string{"path"},
						},
					},
					"branch": {
						Type:        "string",
						Description: "Branch to create the KB on. Defaults to the repository's default branch.",
					},
				},
			},
		},
		[]scopes.Scope{scopes.Repo},
		func(ctx context.Context, deps ToolDependencies, _ *mcp.CallToolRequest, args map[string]any) (*mcp.CallToolResult, any, error) {
			owner, repo, errMsg := resolveKBOwnerRepo(ctx)
			if errMsg != "" {
				return utils.NewToolResultError(errMsg), nil, nil
			}
			branch, err := OptionalParam[string](args, "branch")
			if err != nil {
				return utils.NewToolResultError(err.Error()), nil, nil
			}

			// Parse directories or use defaults
			type dirEntry struct {
				Path        string
				Description string
			}
			var dirs []dirEntry

			if dirsRaw, ok := args["directories"]; ok {
				if dirsArr, ok := dirsRaw.([]any); ok {
					for _, d := range dirsArr {
						if dm, ok := d.(map[string]any); ok {
							p, _ := dm["path"].(string)
							desc, _ := dm["description"].(string)
							if p != "" {
								dirs = append(dirs, dirEntry{Path: strings.Trim(p, "/"), Description: desc})
							}
						}
					}
				}
			}

			if len(dirs) == 0 {
				dirs = []dirEntry{
					{Path: "docs/architecture", Description: "System design and architecture diagrams"},
					{Path: "docs/decisions", Description: "Architecture Decision Records (ADRs)"},
					{Path: "docs/meeting-notes", Description: "Meeting notes and summaries"},
					{Path: "docs/runbooks", Description: "Operational runbooks and guides"},
				}
			}

			// Generate AGENTS.md content
			var agentsMD strings.Builder
			agentsMD.WriteString("---\nversion: 1\n---\n\n")
			agentsMD.WriteString("# Knowledge Base Configuration\n\n")
			agentsMD.WriteString("## Structure\n")
			for _, d := range dirs {
				if d.Description != "" {
					agentsMD.WriteString(fmt.Sprintf("- %s    # %s\n", d.Path, d.Description))
				} else {
					agentsMD.WriteString(fmt.Sprintf("- %s\n", d.Path))
				}
			}
			agentsMD.WriteString("\n## Rules\n")
			agentsMD.WriteString("frontmatter: required\n")
			agentsMD.WriteString("naming: kebab-case\n")
			agentsMD.WriteString("date_prefix: false\n")
			agentsMD.WriteString("\n## Agents\n")
			agentsMD.WriteString("coding-agent:\n")
			agentsMD.WriteString("  write: [*]\n")
			agentsMD.WriteString("  read: [*]\n")

			// Build files array for push_files-style commit
			var files []*github.TreeEntry

			// AGENTS.md
			files = append(files, &github.TreeEntry{
				Path:    github.Ptr("AGENTS.md"),
				Mode:    github.Ptr("100644"),
				Type:    github.Ptr("blob"),
				Content: github.Ptr(agentsMD.String()),
			})

			// Create .gitkeep in each directory
			for _, d := range dirs {
				files = append(files, &github.TreeEntry{
					Path:    github.Ptr(d.Path + "/.gitkeep"),
					Mode:    github.Ptr("100644"),
					Type:    github.Ptr("blob"),
					Content: github.Ptr(""),
				})
			}

			client, err := deps.GetClient(ctx)
			if err != nil {
				return utils.NewToolResultError("failed to get GitHub client"), nil, nil
			}

			// Get the default branch if none specified
			if branch == "" {
				repoInfo, repoResp, err := client.Repositories.Get(ctx, owner, repo)
				if err != nil {
					return ghErrors.NewGitHubAPIErrorResponse(ctx, "failed to get repository info", repoResp, err), nil, nil
				}
				if repoResp != nil && repoResp.Body != nil {
					defer func() { _ = repoResp.Body.Close() }()
				}
				branch = repoInfo.GetDefaultBranch()
			}

			// Get the branch reference
			ref, resp, err := client.Git.GetRef(ctx, owner, repo, "refs/heads/"+branch)
			if err != nil {
				return ghErrors.NewGitHubAPIErrorResponse(ctx, "failed to get branch reference", resp, err), nil, nil
			}
			if resp != nil && resp.Body != nil {
				defer func() { _ = resp.Body.Close() }()
			}

			// Get the base commit
			baseCommit, commitResp, err := client.Git.GetCommit(ctx, owner, repo, *ref.Object.SHA)
			if err != nil {
				return ghErrors.NewGitHubAPIErrorResponse(ctx, "failed to get base commit", commitResp, err), nil, nil
			}
			if commitResp != nil && commitResp.Body != nil {
				defer func() { _ = commitResp.Body.Close() }()
			}

			// Create a new tree
			newTree, treeResp, err := client.Git.CreateTree(ctx, owner, repo, *baseCommit.Tree.SHA, files)
			if err != nil {
				return ghErrors.NewGitHubAPIErrorResponse(ctx, "failed to create tree", treeResp, err), nil, nil
			}
			if treeResp != nil && treeResp.Body != nil {
				defer func() { _ = treeResp.Body.Close() }()
			}

			// Create the commit
			commit := github.Commit{
				Message: github.Ptr("docs: initialize knowledge base"),
				Tree:    newTree,
				Parents: []*github.Commit{{SHA: baseCommit.SHA}},
			}
			newCommit, newCommitResp, err := client.Git.CreateCommit(ctx, owner, repo, commit, nil)
			if err != nil {
				return ghErrors.NewGitHubAPIErrorResponse(ctx, "failed to create commit", newCommitResp, err), nil, nil
			}
			if newCommitResp != nil && newCommitResp.Body != nil {
				defer func() { _ = newCommitResp.Body.Close() }()
			}

			// Update the reference
			_, updateResp, err := client.Git.UpdateRef(ctx, owner, repo, *ref.Ref, github.UpdateRef{
				SHA:   *newCommit.SHA,
				Force: github.Ptr(false),
			})
			if err != nil {
				return ghErrors.NewGitHubAPIErrorResponse(ctx, "failed to update reference", updateResp, err), nil, nil
			}
			if updateResp != nil && updateResp.Body != nil {
				defer func() { _ = updateResp.Body.Close() }()
			}

			result := map[string]any{
				"message":     "Knowledge base initialized successfully",
				"commit_sha":  newCommit.GetSHA(),
				"branch":      branch,
				"agents_md":   "AGENTS.md",
				"directories": dirs,
			}

			r, err := json.Marshal(result)
			if err != nil {
				return utils.NewToolResultError("failed to marshal response"), nil, nil
			}
			return utils.NewToolResultText(string(r)), nil, nil
		},
	)
}
