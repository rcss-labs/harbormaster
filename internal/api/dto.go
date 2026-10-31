package api

// TagDTO is a tag as the frontend sees it.
type TagDTO struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Color   string `json:"color"`
	Builtin bool   `json:"builtin"`
}

// MeAccountDTO is a connected AWS account visible to the caller.
type MeAccountDTO struct {
	ID        int64    `json:"id"`
	Name      string   `json:"name"`
	AccountID string   `json:"account_id"`
	Regions   []string `json:"regions"`
}

// MeDTO is the signed-in user with everything the SPA needs to render navigation and permissions.
// Perms maps a tag id (as a string) to its ActionSet bitmask (V=1 L=2 O=4 D=8 E=16 T=32).
type MeDTO struct {
	ID                 int64          `json:"id"`
	Username           string         `json:"username"`
	Admin              bool           `json:"admin"`
	MustChangePassword bool           `json:"must_change_password"`
	CSRFToken          string         `json:"csrf_token"`
	Perms              map[string]int `json:"perms"`
	Tags               []TagDTO       `json:"tags"`
	Accounts           []MeAccountDTO `json:"accounts"`
	Regions            []string       `json:"regions"`
}
