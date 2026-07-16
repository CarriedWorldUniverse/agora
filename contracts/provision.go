package contracts

import "encoding/json"

// Provision makes a blank pod specific at attach time — atomic:
// apply-all-or-reject, then a `provisioned {identity_fp, profile}` event.
// Admin capability required. This is why identity/profiles/interpolation are
// parameters: the pod becomes "<aspect> wearing <profile>" purely through
// control-plane data.
// Spec: agora-spec-remote.md §6a.
type Provision struct {
	Identity struct {
		// Source: "keyring:<ref>" or "herald:<name>" (provision mode ⇒
		// Ephemeral identities expiring with the pod).
		Source string `json:"source"`
	} `json:"identity"`
	Profile string `json:"profile"`
	// ModelAliases overlays the alias table for this instance.
	ModelAliases map[string]string   `json:"model_aliases,omitempty"`
	Session      ProvisionSession    `json:"session"`
	Workspace    *ProvisionWorkspace `json:"workspace,omitempty"`
	Context      *ProvisionContext   `json:"context,omitempty"`
}

// ProvisionSession: exactly one of New/Resume. session.resume enables pod
// handoff — a fresh pod resumes the thread from the store.
type ProvisionSession struct {
	New    bool   `json:"new,omitempty"`
	Resume string `json:"resume,omitempty"` // thread_id
	// Ephemeral threads are deleted on deprovision (persistence §4).
	Ephemeral bool `json:"ephemeral,omitempty"`
}

// ProvisionWorkspace sets the session wd (checkout if missing).
type ProvisionWorkspace struct {
	Dir      string `json:"dir"`
	Checkout *struct {
		Repo string `json:"repo"`
		Ref  string `json:"ref,omitempty"`
	} `json:"checkout,omitempty"`
}

// ProvisionContext seeds files/fragments/MCP overlay/env. MCPOverlay entries
// resolve {identity} interpolation against the provisioned identity.
type ProvisionContext struct {
	Files      []string                   `json:"files,omitempty"`
	Fragments  []string                   `json:"fragments,omitempty"`
	MCPOverlay map[string]json.RawMessage `json:"mcp_overlay,omitempty"`
	Env        map[string]string          `json:"env,omitempty"`
}
