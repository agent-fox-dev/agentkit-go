package agentkit

import (
	"fmt"

	"github.com/agentfox/agentkit-go/core"
	"github.com/agentfox/agentkit-go/session"
)

// NewAgentFromSession constructs an agent from folded session state.
//
// The fold's outputs are applied to cfg BEFORE construction:
//
//	Model         resolved from the recovered (provider, api, model) TRIPLE
//	ThinkingLevel from the last thinking_level_change entry
//	History       the flattened active branch, with entry ids marked
//	Checkpoint    so REQ-GO-15's skip rule (c) can still fire after a resume
//
// resolve maps a recovered provenance triple back to a *core.Model. It is a
// parameter rather than a catalog call so this package does not depend on the
// catalog, and so an embedder with its own model registry can resume without
// one.
//
// If the caller already set cfg.Model and the log names a different one, the
// LOG WINS: the transcript was produced by that model, and replaying it as
// though another produced it is what REQ-PROV-11 rule 1 exists to prevent.
func NewAgentFromSession(cfg core.AgentConfig, r *session.Resume, resolve func(provider string, api core.API, modelID string) (*core.Model, error)) (*Agent, error) {
	if r == nil {
		return nil, fmt.Errorf("agentkit: NewAgentFromSession(nil resume)")
	}

	// The Resume must be the branch the store will append to. A store forked
	// (ForkFrom) after Open has moved its head, and an agent built from the
	// Resume Open returned would send the model the old branch while the log
	// records the new one; a later resume would show a conversation that never
	// ran. session.FoldLeaf forks and folds together.
	if cfg.SessionStore != nil {
		if head := cfg.SessionStore.Head(); head != r.LeafID {
			return nil, fmt.Errorf(
				"agentkit: the session store's head is %q but this Resume was folded from %q; "+
					"resume a branch with session.FoldLeaf(store, leafID), which moves the head and "+
					"folds that branch together", head, r.LeafID)
		}
	}

	if r.ModelID != "" {
		if resolve == nil {
			// The whole provenance TRIPLE (P-4), not the model id alone:
			// REQ-PROV-11 rule 1's same_model is computed over (provider,
			// api, model), so a config that matches only the id strips the
			// session's signed thinking on its first request. A field the
			// log does not record cannot be compared, and is not.
			if cfg.Model == nil || cfg.Model.ID != r.ModelID ||
				(r.Provider != "" && cfg.Model.Provider != r.Provider) ||
				(r.API != "" && cfg.Model.API != r.API) {
				return nil, fmt.Errorf(
					"agentkit: session was produced by model %q (provider %q, api %q) but no "+
						"resolver was supplied and cfg.Model does not match; replaying a "+
						"transcript as though another model produced it strips its reasoning "+
						"(REQ-PROV-11 rule 1)", r.ModelID, r.Provider, r.API)
			}
		} else {
			m, err := resolve(r.Provider, r.API, r.ModelID)
			if err != nil {
				return nil, fmt.Errorf("agentkit: resolving the session's model %q: %w", r.ModelID, err)
			}
			cfg.Model = m
		}
	}
	if r.ThinkingLevel != "" {
		cfg.ThinkingLevel = r.ThinkingLevel
	}

	h := r.History
	if h == nil {
		h = core.NewConversationHistory()
	}
	if r.HasCheckpoint {
		h.SetCheckpoint(r.Checkpoint)
	}

	a, err := NewAgentWithHistory(cfg, h)
	if err != nil {
		return nil, err
	}
	return a, nil
}
