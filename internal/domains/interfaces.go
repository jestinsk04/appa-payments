package domains

import (
	"context"

	"appa_payments/internal/models"
)

// StoreService defines methods for order persistence
// Implement in infrastructure layer if needed
type StoreService interface {
	GetBCVTasa(ctx context.Context) (float64, error)
	GetOrderByID(ctx context.Context, id string) (*models.OrderResponse, error)
	GetOrderByName(ctx context.Context, name string) (*models.OrderResponse, error)
	UpdateCustomerParentID(ctx context.Context, req models.UpdateCustomerParentIDRequest) error
}
