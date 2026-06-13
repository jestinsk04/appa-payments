package domains

// DirectDebitAccountRequest is the internal request used by the payment service
// to process a direct debit account charge (first-time or recurring).
type DirectDebitAccountRequest struct {
	Amount      float64
	Account     string
	DNI         string
	DisplayName string
	CustomerID  string
	OrderName   string
	OrderID     string
	IsRecurring bool
}
