package dto

// RateResponse is the JSON representation of a stored rate value.
type RateResponse struct {
	ID            string  `json:"id"`
	BaseCurrency  string  `json:"base_currency"`
	QuoteCurrency string  `json:"quote_currency"`
	Price         float64 `json:"price"`
	Timestamp     string  `json:"timestamp"`
}
