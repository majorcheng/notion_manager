package proxy

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const publicModelCreatedAt = int64(1735689600)

type publicModelResponse struct {
	Object string        `json:"object"`
	Data   []publicModel `json:"data"`
}

type publicModel struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

// HandleHealth returns an HTTP handler for the /health endpoint
func HandleHealth(pool *AccountPool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]interface{}{
			"status":    "ok",
			"accounts":  pool.Count(),
			"available": pool.AvailableCount(),
			"quota":     pool.GetQuotaSummary(),
		}
		json.NewEncoder(w).Encode(resp)
	}
}

// HandlePublicModels returns an OpenAI-compatible models list for API clients.
func HandlePublicModels(pool *AccountPool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, `{"error":{"message":"method not allowed","type":"invalid_request_error"}}`, http.StatusMethodNotAllowed)
			return
		}

		resp := publicModelResponse{
			Object: "list",
			Data:   buildPublicModels(pool.AllModels()),
		}
		json.NewEncoder(w).Encode(resp)
	}
}

func buildPublicModels(models []ModelEntry) []publicModel {
	seen := make(map[string]bool, len(models))
	items := make([]publicModel, 0, len(models))
	for _, model := range models {
		id := publicModelID(model)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		items = append(items, publicModel{
			ID:      id,
			Object:  "model",
			Created: publicModelCreatedAt,
			OwnedBy: "notion-manager",
		})
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i].ID < items[j].ID
	})
	return items
}

func publicModelID(model ModelEntry) string {
	if normalized := normalizeModelName(model.Name); normalized != "" {
		return normalized
	}
	return friendlyModelNameByInternalID(model.ID)
}

func friendlyModelNameByInternalID(id string) string {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return ""
	}

	candidates := make([]string, 0, 1)
	for friendly, internalID := range DefaultModelMap {
		if internalID == trimmed {
			candidates = append(candidates, friendly)
		}
	}
	if len(candidates) == 0 {
		return ""
	}

	sort.Strings(candidates)
	return candidates[0]
}

// HandleAdminAccounts returns detailed account info including models, quota, and status
func HandleAdminAccounts(pool *AccountPool, auth *DashboardAuth) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if auth.HasAdminPassword() && !auth.ValidateSession(r) {
			http.Error(w, `{"error":"unauthorized, dashboard login required"}`, http.StatusUnauthorized)
			return
		}
		resp := map[string]interface{}{
			"total":     pool.Count(),
			"available": pool.AvailableCount(),
			"models":    pool.AllModels(),
			"accounts":  pool.GetAccountDetails(),
			"refresh":   pool.GetRefreshStatus(),
		}
		json.NewEncoder(w).Encode(resp)
	}
}

// HandleAdminRefresh handles GET (status) and POST (trigger) for quota refresh
func HandleAdminRefresh(pool *AccountPool, accountsDir string, auth *DashboardAuth) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if auth.HasAdminPassword() && !auth.ValidateSession(r) {
			http.Error(w, `{"error":"unauthorized, dashboard login required"}`, http.StatusUnauthorized)
			return
		}
		switch r.Method {
		case "GET":
			json.NewEncoder(w).Encode(pool.GetRefreshStatus())
		case "POST":
			started := pool.TriggerRefresh(accountsDir)
			resp := map[string]interface{}{
				"started": started,
			}
			if !started {
				resp["message"] = "refresh already in progress"
			}
			json.NewEncoder(w).Encode(resp)
		default:
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		}
	}
}

// HandleAdminModels returns the current model mapping (friendly name -> Notion internal ID)
func HandleAdminModels(pool *AccountPool, auth *DashboardAuth) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if auth.HasAdminPassword() && !auth.ValidateSession(r) {
			http.Error(w, `{"error":"unauthorized, dashboard login required"}`, http.StatusUnauthorized)
			return
		}
		resp := map[string]interface{}{
			"model_map":        DefaultModelMap,
			"available_models": pool.AllModels(),
		}
		json.NewEncoder(w).Encode(resp)
	}
}

// HandleAdminSettings handles GET (read) and PUT (update) for dashboard-controlled settings.
// Settings are persisted to config.yaml using YAML node manipulation to preserve comments.
func HandleAdminSettings(configPath string, auth *DashboardAuth) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		// Require dashboard session (admin password auth)
		if auth.HasAdminPassword() && !auth.ValidateSession(r) {
			http.Error(w, `{"error":"unauthorized, dashboard login required"}`, http.StatusUnauthorized)
			return
		}

		switch r.Method {
		case "GET":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"enable_web_search":       AppConfig.WebSearchEnabled(),
				"enable_workspace_search": AppConfig.WorkspaceSearchEnabled(),
				"disable_notion_prompt":   AppConfig.Proxy.DisableNotionPrompt,
				"debug_logging":           AppConfig.Server.DebugLogging,
			})

		case "PUT":
			var body struct {
				EnableWebSearch       *bool `json:"enable_web_search"`
				EnableWorkspaceSearch *bool `json:"enable_workspace_search"`
				DebugLogging          *bool `json:"debug_logging"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
				return
			}

			changed := false
			if body.EnableWebSearch != nil {
				AppConfig.Proxy.EnableWebSearch = body.EnableWebSearch
				changed = true
				log.Printf("[settings] enable_web_search → %v", *body.EnableWebSearch)
			}
			if body.EnableWorkspaceSearch != nil {
				AppConfig.Proxy.EnableWorkspaceSearch = body.EnableWorkspaceSearch
				changed = true
				log.Printf("[settings] enable_workspace_search → %v", *body.EnableWorkspaceSearch)
			}
			if body.DebugLogging != nil {
				AppConfig.Server.DebugLogging = *body.DebugLogging
				SetDebugLoggingEnabled(*body.DebugLogging)
				changed = true
				log.Printf("[settings] debug_logging → %v", *body.DebugLogging)
			}

			// Persist to config.yaml
			if changed && configPath != "" {
				persistSearchSettings(configPath)
			}

			json.NewEncoder(w).Encode(map[string]interface{}{
				"enable_web_search":       AppConfig.WebSearchEnabled(),
				"enable_workspace_search": AppConfig.WorkspaceSearchEnabled(),
				"disable_notion_prompt":   AppConfig.Proxy.DisableNotionPrompt,
				"debug_logging":           AppConfig.Server.DebugLogging,
			})

		default:
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		}
	}
}

// persistSearchSettings writes the current dashboard settings back to config.yaml.
func persistSearchSettings(configPath string) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		log.Printf("[settings] failed to read %s: %v", configPath, err)
		return
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil || root.Kind == 0 {
		log.Printf("[settings] failed to parse %s: %v", configPath, err)
		return
	}

	if root.Kind == yaml.DocumentNode && len(root.Content) > 0 {
		mapping := root.Content[0]
		proxyNode := getOrCreateYAMLMapping(mapping, "proxy")
		setYAMLBool(proxyNode, "enable_web_search", AppConfig.WebSearchEnabled())
		setYAMLBool(proxyNode, "enable_workspace_search", AppConfig.WorkspaceSearchEnabled())

		serverNode := getOrCreateYAMLMapping(mapping, "server")
		setYAMLBool(serverNode, "debug_logging", AppConfig.Server.DebugLogging)
	}

	out, err := yaml.Marshal(&root)
	if err != nil {
		log.Printf("[settings] failed to marshal config: %v", err)
		return
	}
	if err := os.WriteFile(configPath, out, 0644); err != nil {
		log.Printf("[settings] failed to write %s: %v", configPath, err)
	}
}

func getOrCreateYAMLMapping(mapping *yaml.Node, key string) *yaml.Node {
	for i := 0; i < len(mapping.Content)-1; i += 2 {
		if mapping.Content[i].Value == key {
			node := mapping.Content[i+1]
			if node.Kind != yaml.MappingNode {
				node.Kind = yaml.MappingNode
				node.Tag = "!!map"
				node.Content = nil
			}
			return node
		}
	}

	node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	mapping.Content = append(mapping.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: key},
		node,
	)
	return node
}

// setYAMLBool sets or creates a boolean field in a YAML mapping node
func setYAMLBool(mapping *yaml.Node, key string, value bool) {
	valStr := "false"
	if value {
		valStr = "true"
	}
	for i := 0; i < len(mapping.Content)-1; i += 2 {
		if mapping.Content[i].Value == key {
			mapping.Content[i+1].Value = valStr
			mapping.Content[i+1].Tag = "!!bool"
			return
		}
	}
	// Key not found — append it
	mapping.Content = append(mapping.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Value: valStr, Tag: "!!bool"},
	)
}

// isFreePlan returns true if the account is on a free plan where basic credits (200 lifetime)
// never reset. Paid plans (plus, business, enterprise) have monthly premium credits that reset.
func isFreePlan(acc *Account) bool {
	if acc.QuotaInfo != nil && (acc.QuotaInfo.HasPremium || acc.QuotaInfo.PremiumLimit > 0 || acc.QuotaInfo.PremiumBalance > 0) {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(acc.PlanType)) {
	case "personal", "free", "":
		return true
	default:
		// For team plans, check if they actually have a paid subscription
		// by looking at quota info — if no premium credits exist, treat as free.
		if acc.QuotaInfo != nil && !acc.QuotaInfo.HasPremium {
			return true
		}
		return false
	}
}
