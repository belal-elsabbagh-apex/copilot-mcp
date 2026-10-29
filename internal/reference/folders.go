package reference

import "github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"

// UiPathFolder is one UiPath folder (per env).
type UiPathFolder struct {
	Name               string `json:"name"`
	OrganizationUnitID string `json:"organizationUnitId"` // numeric folder id (fid / X-UIPATH-OrganizationUnitId)
	FolderKey          string `json:"folderKey"`          // GUID FolderKey
	RealBECalls        bool   `json:"realBeCalls"`        // every BackendApi POST is real in BOTH folders (to the item's serverURL); only IsApproved=false stops a submit
}

// uiPathFolders is ordered like the TS record (prod first) so the resource JSON matches.
type uiPathFolders struct {
	Prod    UiPathFolder `json:"prod"`
	PreProd UiPathFolder `json:"pre_prod"`
}

// UiPathFolders are the UiPath folders per env. Marshals as the TS record; index with For.
var UiPathFolders = uiPathFolders{
	Prod: UiPathFolder{
		Name:               "Authorization",
		OrganizationUnitID: "231517",
		FolderKey:          "9ecc9792-b3f1-4c3b-bae9-51b6f31384a5",
		RealBECalls:        true,
	},
	PreProd: UiPathFolder{
		Name:               "Authorization Dev Clone",
		OrganizationUnitID: "434039",
		FolderKey:          "db6a5e70-7d8b-4113-afbc-a425eb81f015",
		RealBECalls:        true,
	},
}

// For returns the folder for env; the zero value for an unknown env.
func (f uiPathFolders) For(env config.Env) UiPathFolder {
	switch env {
	case config.Prod:
		return f.Prod
	case config.PreProd:
		return f.PreProd
	}
	return UiPathFolder{}
}

// FolderIDFor is the numeric OrganizationUnitId for an env (the fid the QueueItems endpoints expect).
func FolderIDFor(env config.Env) string { return UiPathFolders.For(env).OrganizationUnitID }

// FolderNameFor is the folder name (X-UIPATH-FolderPath) for an env.
func FolderNameFor(env config.Env) string { return UiPathFolders.For(env).Name }

// EnvForFolderID maps a numeric folder id (fid / OrganizationUnitId) back to an env. Defaults to
// pre_prod (the only folder this server may write to) when the id isn't one we know.
func EnvForFolderID(folderID string) config.Env {
	for _, env := range config.Envs {
		if FolderIDFor(env) == folderID {
			return env
		}
	}
	return config.PreProd
}

// FolderNameForID is the folder name for a numeric folder id ("" if unknown — the header is then
// omitted).
func FolderNameForID(folderID string) string {
	for _, env := range config.Envs {
		if FolderIDFor(env) == folderID {
			return FolderNameFor(env)
		}
	}
	return ""
}
