package monitor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// CLIProfile is the public description of one ai-accounts profile.
type CLIProfile struct {
	ID        string `json:"id"`
	CLI       string `json:"cli"`
	Name      string `json:"name"`
	Person    string `json:"person"`
	Account   string `json:"account,omitempty"`
	Connected bool   `json:"connected"`
}

// ListCLIProfiles reads every claude/codex/agy profile from the ai-accounts
// directory (AI_ACCOUNTS_DIR or ~/.local/share/ai-accounts).
func ListCLIProfiles() []CLIProfile {
	return listCLIProfiles("")
}

func listCLIProfiles(dirOverride string) []CLIProfile {
	root := resolveAccountsDir(dirOverride)
	if root == "" {
		return nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	out := make([]CLIProfile, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		profileDir := filepath.Join(root, entry.Name())
		raw, err := os.ReadFile(filepath.Join(profileDir, "profile.json"))
		if err != nil {
			continue
		}
		var prof accountProfile
		if json.Unmarshal(raw, &prof) != nil {
			continue
		}
		if prof.Provider != "claude" && prof.Provider != "codex" && prof.Provider != "agy" {
			continue
		}
		if strings.TrimSpace(prof.Person) == "" {
			continue
		}
		if prof.ID == "" {
			prof.ID = entry.Name()
		}
		out = append(out, CLIProfile{
			ID:        prof.ID,
			CLI:       prof.Provider,
			Name:      prof.Name,
			Person:    strings.TrimSpace(prof.Person),
			Account:   prof.Account,
			Connected: hasAuthFile(profileDir, prof.Provider),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		pi, pj := strings.ToLower(out[i].Person), strings.ToLower(out[j].Person)
		if pi != pj {
			return pi < pj
		}
		return out[i].CLI < out[j].CLI
	})
	return out
}
