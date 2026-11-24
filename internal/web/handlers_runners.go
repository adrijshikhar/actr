package web

import (
	"net/http"
	"path/filepath"
	"sort"
	"strings"

	"github.com/adrijshikhar/actr/internal/config"
)

// handleRunners returns the list of registered runner configurations.
func (s *Server) handleRunners(w http.ResponseWriter, r *http.Request) {
	var runners []RunnerInfo
	if s.cfg.RunnerProvider != nil {
		var err error
		runners, err = s.cfg.RunnerProvider.GetRunners(r.Context())
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
	} else {
		runners = s.fallbackRunners()
	}

	if runners == nil {
		runners = []RunnerInfo{}
	}

	// Augment runners with queue depth and active slots from RunStore if available
	if s.cfg.RunStore != nil {
		for i := range runners {
			if runners[i].QueuedJobs == 0 {
				if _, queuedTotal, err := s.cfg.RunStore.GetRuns(r.Context(), 1, 0, runners[i].Repo, "queued", ""); err == nil && queuedTotal > 0 {
					runners[i].QueuedJobs = queuedTotal
				}
			}
			if runners[i].ActiveSlots == 0 {
				if _, activeTotal, err := s.cfg.RunStore.GetRuns(r.Context(), 1, 0, runners[i].Repo, "running", ""); err == nil && activeTotal > 0 {
					runners[i].ActiveSlots = activeTotal
					runners[i].Status = "busy"
				}
			}
		}
	}

	writeJSON(w, http.StatusOK, runners)
}

func (s *Server) fallbackRunners() []RunnerInfo {
	cfgDir := s.cfg.ConfigDir
	if cfgDir == "" {
		cfgDir = config.DefaultConfigDir()
	}

	var runners []RunnerInfo

	// 1. Check for multi-repo supervisor config
	for _, name := range []string{"config.yaml", "config.yml", "supervisor.yaml", "supervisor.yml"} {
		p := filepath.Join(cfgDir, name)
		if config.IsMultiRepoConfig(p) {
			if supCfg, err := config.LoadSupervisorConfig(p); err == nil && supCfg != nil {
				for alias, repo := range supCfg.Repos {
					runnerName := alias
					repoURL := repo.URL
					if repoURL == "" {
						repoURL = alias
					}
					labels := []string{"self-hosted"}
					if repo.ConfigDir != "" {
						if rCfg, err := config.LoadRunnerConfig(repo.ConfigDir); err == nil && rCfg != nil {
							if rCfg.AgentName != "" {
								runnerName = rCfg.AgentName
							}
							if rCfg.GitHubURL != "" {
								repoURL = strings.TrimPrefix(rCfg.GitHubURL, "https://github.com/")
							}
						}
					}
					maxConcurrent := repo.MaxConcurrentJobs
					if maxConcurrent <= 0 {
						maxConcurrent = 1
					}
					runners = append(runners, RunnerInfo{
						Repo:           repoURL,
						Name:           runnerName,
						Labels:         labels,
						MaxConcurrency: maxConcurrent,
						ActiveSlots:    0,
						Status:         "idle",
					})
				}
				if len(runners) > 0 {
					sort.Slice(runners, func(i, j int) bool {
						return runners[i].Name < runners[j].Name
					})
					return runners
				}
			}
		}
	}

	// 2. Check for single runner config in cfgDir
	if rCfg, err := config.LoadRunnerConfig(cfgDir); err == nil && rCfg != nil {
		repoURL := rCfg.GitHubURL
		if repoURL == "" {
			repoURL = rCfg.ServerURL
		}
		repoURL = strings.TrimPrefix(repoURL, "https://github.com/")
		runners = append(runners, RunnerInfo{
			Repo:           repoURL,
			Name:           rCfg.AgentName,
			Labels:         []string{"self-hosted"},
			MaxConcurrency: 1,
			ActiveSlots:    0,
			Status:         "idle",
		})
		return runners
	}

	return []RunnerInfo{}
}
