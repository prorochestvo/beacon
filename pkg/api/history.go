package api

// HistoryResponse is the JSON representation of a single execution history record.
type HistoryResponse struct {
	ID         string `json:"id"`
	SourceName string `json:"source_name"`
	Success    bool   `json:"success"`
	Error      string `json:"error,omitempty"`
	Timestamp  string `json:"timestamp"`
}

// ExecutionErrorResponse is the JSON shape of one execution error record.
type ExecutionErrorResponse struct {
	ID         string `json:"id"`
	SourceName string `json:"source_name"`
	Error      string `json:"error,omitempty"`
	Timestamp  string `json:"timestamp"`
}
