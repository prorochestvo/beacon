package api

// DailyEventResponse is the JSON shape of one daily event summary row.
type DailyEventResponse struct {
	Type         string `json:"type"`
	Date         string `json:"date"`
	SuccessCount int64  `json:"success_count"`
	FailedCount  int64  `json:"failed_count"`
}
