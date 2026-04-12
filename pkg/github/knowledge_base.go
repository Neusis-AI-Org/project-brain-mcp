package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
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
	return `## Project Knowledge Base
This is the project's knowledge base — a shared memory layer for developers and coding agents. It contains project context such as architecture decisions, guides, conventions, and other documentation specific to this project.

**Before starting any significant coding task**, call kb_config to discover the KB structure, then search or browse for relevant context. Understanding prior decisions and existing patterns leads to better code.

**After completing work that changes architecture or introduces new patterns**, write back to the KB so future developers and agents have that context.

The KB structure varies per project — always call kb_config first to discover what categories of documentation are available. All documents use markdown format with YAML frontmatter (title, date, author, tags). The target repository is pre-configured on the server.`
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

// longestCommonPrefix returns the longest common path prefix between two paths.
func longestCommonPrefix(a, b string) string {
	aParts := strings.Split(a, "/")
	bParts := strings.Split(b, "/")
	var common []string
	for i := 0; i < len(aParts) && i < len(bParts); i++ {
		if aParts[i] != bParts[i] {
			break
		}
		common = append(common, aParts[i])
	}
	return strings.Join(common, "/")
}

// enrichResultWithFrontmatter reads a file's frontmatter and boosts the search result score
// based on title and tag matches.
func enrichResultWithFrontmatter(ctx context.Context, client *github.Client, owner, repo, ref string, result *KBSearchResult, queryLower string) {
	contentOpts := &github.RepositoryContentGetOptions{Ref: ref}
	fileContent, _, fileResp, fileErr := client.Repositories.GetContents(ctx, owner, repo, result.Path, contentOpts)
	if fileErr != nil || fileContent == nil {
		return
	}
	if fileResp != nil && fileResp.Body != nil {
		defer func() { _ = fileResp.Body.Close() }()
	}
	text, err := fileContent.GetContent()
	if err != nil {
		return
	}
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
	if strings.Contains(strings.ToLower(result.Title), queryLower) {
		result.Score += 5
	}
	for _, tag := range result.Tags {
		if strings.Contains(strings.ToLower(tag), queryLower) {
			result.Score += 3
		}
	}
}

// searchViaTreeAPI uses the Git Trees API to list all files in one call, filters to
// KB markdown files, then does targeted content reads for scoring. This is the fallback
// when GitHub Code Search is unavailable (token issues, unindexed repos, GHES).
//
// Scale characteristics:
//   - 1 API call for the tree (works for repos up to ~100k files)
//   - Filename/path/frontmatter matching is done locally (zero API cost)
//   - Content reads are bounded: max 50 files read for content matching
func searchViaTreeAPI(ctx context.Context, client *github.Client, owner, repo, ref, queryLower, pathFilter string, kbPaths []string) ([]KBSearchResult, error) {
	// Resolve the tree SHA for the ref
	resolveRef := ref
	if resolveRef == "" {
		resolveRef = "HEAD"
	}
	gitRef, _, err := client.Git.GetRef(ctx, owner, repo, "heads/"+resolveRef)
	if err != nil {
		// Try as-is (could be a tag or "main"/"master")
		gitRef, _, err = client.Git.GetRef(ctx, owner, repo, "heads/main")
		if err != nil {
			return nil, fmt.Errorf("failed to resolve ref: %w", err)
		}
	}
	treeSHA := gitRef.GetObject().GetSHA()

	tree, _, err := client.Git.GetTree(ctx, owner, repo, treeSHA, true)
	if err != nil {
		return nil, fmt.Errorf("failed to get repo tree: %w", err)
	}

	// Filter tree entries to KB markdown files
	var candidates []string
	for _, entry := range tree.Entries {
		path := entry.GetPath()
		if entry.GetType() != "blob" || !strings.HasSuffix(strings.ToLower(path), ".md") {
			continue
		}
		if pathFilter != "" {
			if !strings.HasPrefix(path, strings.Trim(pathFilter, "/")+"/") {
				continue
			}
		} else if len(kbPaths) > 0 {
			inKB := false
			for _, kbPath := range kbPaths {
				if strings.HasPrefix(path, kbPath+"/") {
					inKB = true
					break
				}
			}
			if !inKB {
				continue
			}
		}
		candidates = append(candidates, path)
	}

	// Phase 1: Score by path/filename match (no API calls)
	type candidate struct {
		path  string
		score int
	}
	scored := make([]candidate, 0, len(candidates))
	queryTerms := strings.Fields(queryLower)

	for _, path := range candidates {
		pathLower := strings.ToLower(path)
		score := 0
		for _, term := range queryTerms {
			if strings.Contains(pathLower, term) {
				score += 2
			}
		}
		scored = append(scored, candidate{path: path, score: score})
	}

	// Sort by path score descending so we prioritize reading likely matches
	sort.Slice(scored, func(i, j int) bool {
		return scored[i].score > scored[j].score
	})

	// Phase 2: Read up to maxContentReads files for frontmatter + content matching
	// Prioritize files that already matched by path, but also read unmatched files
	// if we have budget remaining.
	const maxContentReads = 50
	var results []KBSearchResult
	readsUsed := 0

	for _, c := range scored {
		if readsUsed >= maxContentReads {
			// For remaining candidates that matched by path but weren't read,
			// include them with path-only score
			if c.score > 0 {
				results = append(results, KBSearchResult{
					Path:  c.path,
					Score: c.score,
				})
			}
			continue
		}

		contentOpts := &github.RepositoryContentGetOptions{Ref: ref}
		fileContent, _, fileResp, fileErr := client.Repositories.GetContents(ctx, owner, repo, c.path, contentOpts)
		if fileResp != nil && fileResp.Body != nil {
			defer func() { _ = fileResp.Body.Close() }()
		}
		if fileErr != nil || fileContent == nil {
			readsUsed++
			continue
		}
		readsUsed++

		text, err := fileContent.GetContent()
		if err != nil {
			continue
		}

		// Score based on frontmatter and content
		fm := parseFrontmatter(text)
		result := KBSearchResult{
			Path:  c.path,
			Title: fm["title"],
			Score: c.score,
		}

		if tagsStr, ok := fm["tags"]; ok {
			tagsStr = strings.Trim(tagsStr, "[]")
			for _, tag := range strings.Split(tagsStr, ",") {
				tag = strings.TrimSpace(tag)
				if tag != "" {
					result.Tags = append(result.Tags, tag)
				}
			}
		}

		// Title match
		if result.Title != "" && strings.Contains(strings.ToLower(result.Title), queryLower) {
			result.Score += 5
		}
		// Tag match
		for _, tag := range result.Tags {
			for _, term := range queryTerms {
				if strings.Contains(strings.ToLower(tag), term) {
					result.Score += 3
				}
			}
		}
		// Content match — check if query terms appear in body
		textLower := strings.ToLower(text)
		for _, term := range queryTerms {
			if strings.Contains(textLower, term) {
				result.Score += 1
				// Extract a match snippet (first occurrence, up to 200 chars of context)
				idx := strings.Index(textLower, term)
				start := idx - 80
				if start < 0 {
					start = 0
				}
				end := idx + len(term) + 120
				if end > len(text) {
					end = len(text)
				}
				snippet := strings.ReplaceAll(text[start:end], "\n", " ")
				result.MatchLines = append(result.MatchLines, strings.TrimSpace(snippet))
			}
		}

		if result.Score > 0 {
			results = append(results, result)
		}
	}

	return results, nil
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
			Description: t("TOOL_KB_CONFIG_DESCRIPTION", "Understand the project's knowledge base structure, available documentation paths, and rules. Call this at the start of a session before using other KB tools — the KB structure varies per project, so always discover it first."),
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
			Description: t("TOOL_KB_WRITE_DESCRIPTION", "Contribute to the project knowledge base. After making significant changes — new architecture, patterns, decisions, or resolving complex issues — document the context here so future developers and agents benefit from that knowledge."),
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
			Description: t("TOOL_KB_LIST_DESCRIPTION", "Browse available knowledge base documents to discover what project context exists. Filter by folder or tag to find relevant documentation for your current task."),
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
			Description: t("TOOL_KB_SEARCH_DESCRIPTION", "Search the project knowledge base for context relevant to your current task. Use this before making changes to understand prior decisions, existing patterns, and project conventions."),
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

			// Resolve KB paths from AGENTS.md for scoping
			var kbPaths []string
			agentsOpts := &github.RepositoryContentGetOptions{Ref: ref}
			agentsFile, _, agentsResp, agentsErr := client.Repositories.GetContents(ctx, owner, repo, "AGENTS.md", agentsOpts)
			if agentsErr == nil && agentsFile != nil {
				if agentsResp != nil && agentsResp.Body != nil {
					defer func() { _ = agentsResp.Body.Close() }()
				}
				if agentsContent, err := agentsFile.GetContent(); err == nil {
					if config, err := parseAgentsMD(agentsContent); err == nil {
						for _, s := range config.Structure {
							kbPaths = append(kbPaths, strings.Trim(s.Path, "/"))
						}
					}
				}
			}

			queryLower := strings.ToLower(query)
			var results []KBSearchResult

			// --- Strategy 1: GitHub Code Search API (scales to large repos) ---
			searchQuery := fmt.Sprintf("%s repo:%s/%s language:markdown", query, owner, repo)
			if pathFilter != "" {
				searchQuery += fmt.Sprintf(" path:%s", strings.Trim(pathFilter, "/"))
			} else if len(kbPaths) > 0 {
				commonPrefix := kbPaths[0]
				for _, p := range kbPaths[1:] {
					commonPrefix = longestCommonPrefix(commonPrefix, p)
				}
				if commonPrefix != "" {
					searchQuery += fmt.Sprintf(" path:%s", commonPrefix)
				}
			}

			searchOpts := &github.SearchOptions{
				ListOptions: github.ListOptions{PerPage: 20},
			}
			searchResult, searchResp, searchErr := client.Search.Code(ctx, searchQuery, searchOpts)
			if searchResp != nil && searchResp.Body != nil {
				defer func() { _ = searchResp.Body.Close() }()
			}

			if searchErr == nil && searchResult.GetTotal() > 0 {
				// Code Search returned results — enrich with frontmatter
				for _, codeResult := range searchResult.CodeResults {
					filePath := codeResult.GetPath()
					result := KBSearchResult{Path: filePath, Score: 1}

					for _, match := range codeResult.TextMatches {
						if f := match.GetFragment(); f != "" {
							result.MatchLines = append(result.MatchLines, f)
						}
					}

					enrichResultWithFrontmatter(ctx, client, owner, repo, ref, &result, queryLower)
					results = append(results, result)
				}
			} else {
				// --- Strategy 2: Git Tree API fallback ---
				// Single API call to get the full repo tree, then filter locally.
				// This handles cases where Code Search is unavailable (token scope,
				// unindexed repos, GHES without code search, etc.).
				results, err = searchViaTreeAPI(ctx, client, owner, repo, ref, queryLower, pathFilter, kbPaths)
				if err != nil {
					return utils.NewToolResultError(fmt.Sprintf("search failed: %s", err)), nil, nil
				}
			}

			// Sort by score descending, cap at 20 results
			sort.Slice(results, func(i, j int) bool {
				return results[i].Score > results[j].Score
			})
			if len(results) > 20 {
				results = results[:20]
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

// KBReadTool reads a full knowledge base document by path, returning content and parsed frontmatter.
func KBReadTool(t translations.TranslationHelperFunc) inventory.ServerTool {
	return NewTool(
		ToolsetMetadataKnowledgeBase,
		mcp.Tool{
			Name:        "kb_read",
			Description: t("TOOL_KB_READ_DESCRIPTION", "Read a full knowledge base document to get complete context. Use this after finding relevant docs via kb_search or kb_list to understand prior decisions and existing patterns before making implementation choices."),
			Annotations: &mcp.ToolAnnotations{
				Title:        t("TOOL_KB_READ_USER_TITLE", "Read knowledge base document"),
				ReadOnlyHint: true,
			},
			InputSchema: &jsonschema.Schema{
				Type: "object",
				Properties: map[string]*jsonschema.Schema{
					"path": {
						Type:        "string",
						Description: "Path to the document (e.g., 'docs/architecture/kb-context-engine.md')",
					},
					"ref": {
						Type:        "string",
						Description: "Git ref (branch or tag). Defaults to the repository's default branch.",
					},
				},
				Required: []string{"path"},
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
			ref, err := OptionalParam[string](args, "ref")
			if err != nil {
				return utils.NewToolResultError(err.Error()), nil, nil
			}

			// Normalize path
			path = strings.TrimPrefix(path, "/")

			client, err := deps.GetClient(ctx)
			if err != nil {
				return utils.NewToolResultError("failed to get GitHub client"), nil, nil
			}

			opts := &github.RepositoryContentGetOptions{Ref: ref}
			fileContent, _, resp, err := client.Repositories.GetContents(ctx, owner, repo, path, opts)
			if err != nil {
				if resp != nil && resp.StatusCode == http.StatusNotFound {
					return utils.NewToolResultError(fmt.Sprintf("document not found: %s", path)), nil, nil
				}
				return ghErrors.NewGitHubAPIErrorResponse(ctx, "failed to get document", resp, err), nil, nil
			}
			if resp != nil && resp.Body != nil {
				defer func() { _ = resp.Body.Close() }()
			}

			content, err := fileContent.GetContent()
			if err != nil {
				return utils.NewToolResultError(fmt.Sprintf("failed to decode document: %s", err)), nil, nil
			}

			// Parse frontmatter
			fm := parseFrontmatter(content)

			// Build response with metadata and content
			result := map[string]any{
				"path":    path,
				"sha":     fileContent.GetSHA(),
				"size":    fileContent.GetSize(),
				"content": content,
			}
			if fm["title"] != "" {
				result["title"] = fm["title"]
			}
			if fm["date"] != "" {
				result["date"] = fm["date"]
			}
			if fm["author"] != "" {
				result["author"] = fm["author"]
			}
			if tagsStr, ok := fm["tags"]; ok {
				tagsStr = strings.Trim(tagsStr, "[]")
				var tags []string
				for _, tag := range strings.Split(tagsStr, ",") {
					tag = strings.TrimSpace(tag)
					if tag != "" {
						tags = append(tags, tag)
					}
				}
				if len(tags) > 0 {
					result["tags"] = tags
				}
			}

			r, err := json.Marshal(result)
			if err != nil {
				return utils.NewToolResultError("failed to marshal response"), nil, nil
			}
			return utils.NewToolResultText(string(r)), nil, nil
		},
	)
}

