package model

// DatabaseSearchRequest scopes the diagnostic cross-database search without
// exposing storage-specific types to the HTTP or application layers.
type DatabaseSearchRequest struct {
	Keyword string
	Mode    string
	Limit   int
	Offset  int
	Groups  []string
	Files   []string
	Match   string
	Sort    string
}

type DatabaseSearchError struct {
	Group string `json:"group"`
	File  string `json:"file"`
	Table string `json:"table,omitempty"`
	Error string `json:"error"`
}

type DatabaseSearchStats struct {
	Path            string                `json:"path"`
	DurationMS      float64               `json:"duration_ms"`
	FilesScanned    int                   `json:"files_scanned"`
	TablesScanned   int                   `json:"tables_scanned"`
	FTSTables       int                   `json:"fts_shadow_tables"`
	Partial         bool                  `json:"partial"`
	CandidateCapped bool                  `json:"candidate_capped"`
	Errors          []DatabaseSearchError `json:"errors,omitempty"`
}

type DatabaseSearchResult struct {
	Items []map[string]interface{} `json:"items"`
	Total int                      `json:"total"`
	Stats DatabaseSearchStats      `json:"stats"`
}
