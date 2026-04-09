package github

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/github/github-mcp-server/internal/toolsnaps"
	"github.com/github/github-mcp-server/pkg/translations"
	"github.com/google/go-github/v82/github"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_parseAgentsMD(t *testing.T) {
	content := `---
version: 1
---

# Knowledge Base Configuration

## Structure
- docs/architecture    # System design, diagrams
- docs/decisions       # Architecture Decision Records
- docs/meeting-notes   # Meeting notes and summaries

## Rules
frontmatter: required
naming: kebab-case
date_prefix: true

## Agents
neusis-bot:
  write: [docs/meeting-notes]
  read: [*]
coding-agent:
  write: [docs/architecture, docs/decisions]
  read: [*]
`

	config, err := parseAgentsMD(content)
	require.NoError(t, err)

	assert.Equal(t, 1, config.Version)
	assert.Len(t, config.Structure, 3)
	assert.Equal(t, "docs/architecture", config.Structure[0].Path)
	assert.Equal(t, "System design, diagrams", config.Structure[0].Description)
	assert.Equal(t, "docs/decisions", config.Structure[1].Path)
	assert.Equal(t, "docs/meeting-notes", config.Structure[2].Path)

	assert.Equal(t, "required", config.Rules.Frontmatter)
	assert.Equal(t, "kebab-case", config.Rules.Naming)
	assert.True(t, config.Rules.DatePrefix)

	assert.Len(t, config.Agents, 2)
	assert.Equal(t, []string{"docs/meeting-notes"}, config.Agents["neusis-bot"].Write)
	assert.Equal(t, []string{"*"}, config.Agents["neusis-bot"].Read)
	assert.Equal(t, []string{"docs/architecture", "docs/decisions"}, config.Agents["coding-agent"].Write)
	assert.Equal(t, []string{"*"}, config.Agents["coding-agent"].Read)
}

func Test_parseAgentsMD_defaults(t *testing.T) {
	content := `# Knowledge Base

## Structure
- docs/notes
`
	config, err := parseAgentsMD(content)
	require.NoError(t, err)

	assert.Equal(t, 1, config.Version)
	assert.Len(t, config.Structure, 1)
	assert.Equal(t, "docs/notes", config.Structure[0].Path)
	assert.Equal(t, "required", config.Rules.Frontmatter)
	assert.Equal(t, "kebab-case", config.Rules.Naming)
	assert.False(t, config.Rules.DatePrefix)
}

func Test_parseFrontmatter(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		expected map[string]string
	}{
		{
			name: "full frontmatter",
			content: `---
title: "Auth System Design"
date: "2026-04-08"
author: "alice"
tags: [architecture, auth]
---

# Auth System Design
Some content here.`,
			expected: map[string]string{
				"title":  "Auth System Design",
				"date":   "2026-04-08",
				"author": "alice",
				"tags":   "[architecture, auth]",
			},
		},
		{
			name:     "no frontmatter",
			content:  "# Just a heading\n\nSome content.",
			expected: map[string]string{},
		},
		{
			name: "partial frontmatter",
			content: `---
title: Quick Note
---

Content`,
			expected: map[string]string{
				"title": "Quick Note",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fm := parseFrontmatter(tc.content)
			assert.Equal(t, tc.expected, fm)
		})
	}
}

func Test_generateFrontmatter(t *testing.T) {
	result := generateFrontmatter("Test Doc", "alice", []string{"arch", "design"})
	assert.Contains(t, result, "---")
	assert.Contains(t, result, `title: "Test Doc"`)
	assert.Contains(t, result, `author: "alice"`)
	assert.Contains(t, result, "tags: [arch, design]")
	assert.Contains(t, result, "date:")
}

func Test_generateFrontmatter_noAuthor(t *testing.T) {
	result := generateFrontmatter("Test Doc", "", nil)
	assert.Contains(t, result, `title: "Test Doc"`)
	assert.NotContains(t, result, "author:")
	assert.NotContains(t, result, "tags:")
}

func Test_KBConfigTool(t *testing.T) {
	toolDef := KBConfigTool(translations.NullTranslationHelper)
	require.NoError(t, toolsnaps.Test(toolDef.Tool.Name, toolDef.Tool))

	assert.Equal(t, "kb_config", toolDef.Tool.Name)
	assert.NotEmpty(t, toolDef.Tool.Description)

	schema, ok := toolDef.Tool.InputSchema.(*jsonschema.Schema)
	require.True(t, ok)
	assert.NotContains(t, schema.Properties, "owner")
	assert.NotContains(t, schema.Properties, "repo")
	assert.Contains(t, schema.Properties, "ref")
	assert.Nil(t, schema.Required)

	agentsMDContent := `---
version: 1
---

# Knowledge Base Configuration

## Structure
- docs/architecture    # System design
- docs/decisions       # ADRs

## Rules
frontmatter: required
naming: kebab-case
date_prefix: false
`
	encodedContent := base64.StdEncoding.EncodeToString([]byte(agentsMDContent))

	tests := []struct {
		name         string
		mockedClient *http.Client
		expectError  bool
		expectedMsg  string
	}{
		{
			name: "successful config read",
			mockedClient: MockHTTPClientWithHandlers(map[string]http.HandlerFunc{
				GetReposContentsByOwnerByRepoByPath: func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusOK)
					fileContent := &github.RepositoryContent{
						Name:     github.Ptr("AGENTS.md"),
						Path:     github.Ptr("AGENTS.md"),
						SHA:      github.Ptr("abc123"),
						Type:     github.Ptr("file"),
						Content:  github.Ptr(encodedContent),
						Size:     github.Ptr(len(agentsMDContent)),
						Encoding: github.Ptr("base64"),
					}
					contentBytes, _ := json.Marshal(fileContent)
					_, _ = w.Write(contentBytes)
				},
			}),
			expectError: false,
		},
		{
			name: "AGENTS.md not found",
			mockedClient: MockHTTPClientWithHandlers(map[string]http.HandlerFunc{
				GetReposContentsByOwnerByRepoByPath: func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusNotFound)
					_, _ = w.Write([]byte(`{"message": "Not Found"}`))
				},
			}),
			expectError: true,
			expectedMsg: "AGENTS.md not found",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client := github.NewClient(tc.mockedClient)
			deps := BaseDeps{
				Client: client,
			}
			handler := toolDef.Handler(deps)
			request := createMCPRequest(map[string]any{})
			// Inject KB defaults via context (simulates --kb-owner/--kb-repo)
			ctx := ContextWithKBDefaults(ContextWithDeps(context.Background(), deps), KBDefaults{
				Owner: "testowner",
				Repo:  "testrepo",
			})
			result, err := handler(ctx, &request)

			if tc.expectError {
				require.NoError(t, err)
				require.True(t, result.IsError)
				errorContent := getErrorResult(t, result)
				assert.Contains(t, errorContent.Text, tc.expectedMsg)
			} else {
				require.NoError(t, err)
				require.False(t, result.IsError)
				textContent := getTextResult(t, result)
				var config KBConfig
				err := json.Unmarshal([]byte(textContent.Text), &config)
				require.NoError(t, err)
				assert.Equal(t, 1, config.Version)
				assert.Len(t, config.Structure, 2)
				assert.Equal(t, "docs/architecture", config.Structure[0].Path)
			}
		})
	}
}

func Test_KBWriteTool(t *testing.T) {
	toolDef := KBWriteTool(translations.NullTranslationHelper)
	require.NoError(t, toolsnaps.Test(toolDef.Tool.Name, toolDef.Tool))

	assert.Equal(t, "kb_write", toolDef.Tool.Name)
	assert.NotEmpty(t, toolDef.Tool.Description)

	schema, ok := toolDef.Tool.InputSchema.(*jsonschema.Schema)
	require.True(t, ok)
	assert.NotContains(t, schema.Properties, "owner")
	assert.NotContains(t, schema.Properties, "repo")
	assert.Contains(t, schema.Properties, "path")
	assert.Contains(t, schema.Properties, "title")
	assert.Contains(t, schema.Properties, "content")
	assert.Contains(t, schema.Properties, "author")
	assert.Contains(t, schema.Properties, "tags")
	assert.Contains(t, schema.Properties, "branch")
	assert.Contains(t, schema.Properties, "message")
	assert.Contains(t, schema.Properties, "sha")
	assert.ElementsMatch(t, schema.Required, []string{"path", "title", "content"})
}

func Test_KBListTool(t *testing.T) {
	toolDef := KBListTool(translations.NullTranslationHelper)
	require.NoError(t, toolsnaps.Test(toolDef.Tool.Name, toolDef.Tool))

	assert.Equal(t, "kb_list", toolDef.Tool.Name)
	assert.NotEmpty(t, toolDef.Tool.Description)

	schema, ok := toolDef.Tool.InputSchema.(*jsonschema.Schema)
	require.True(t, ok)
	assert.NotContains(t, schema.Properties, "owner")
	assert.NotContains(t, schema.Properties, "repo")
	assert.Contains(t, schema.Properties, "path")
	assert.Contains(t, schema.Properties, "tag")
	assert.Contains(t, schema.Properties, "ref")
	assert.Nil(t, schema.Required)
}

func Test_KBSearchTool(t *testing.T) {
	toolDef := KBSearchTool(translations.NullTranslationHelper)
	require.NoError(t, toolsnaps.Test(toolDef.Tool.Name, toolDef.Tool))

	assert.Equal(t, "kb_search", toolDef.Tool.Name)
	assert.NotEmpty(t, toolDef.Tool.Description)

	schema, ok := toolDef.Tool.InputSchema.(*jsonschema.Schema)
	require.True(t, ok)
	assert.NotContains(t, schema.Properties, "owner")
	assert.NotContains(t, schema.Properties, "repo")
	assert.Contains(t, schema.Properties, "query")
	assert.Contains(t, schema.Properties, "path")
	assert.Contains(t, schema.Properties, "ref")
	assert.ElementsMatch(t, schema.Required, []string{"query"})
}

func Test_KBInitTool(t *testing.T) {
	toolDef := KBInitTool(translations.NullTranslationHelper)
	require.NoError(t, toolsnaps.Test(toolDef.Tool.Name, toolDef.Tool))

	assert.Equal(t, "kb_init", toolDef.Tool.Name)
	assert.NotEmpty(t, toolDef.Tool.Description)

	schema, ok := toolDef.Tool.InputSchema.(*jsonschema.Schema)
	require.True(t, ok)
	assert.NotContains(t, schema.Properties, "owner")
	assert.NotContains(t, schema.Properties, "repo")
	assert.Contains(t, schema.Properties, "directories")
	assert.Contains(t, schema.Properties, "branch")
	assert.Nil(t, schema.Required)
}
