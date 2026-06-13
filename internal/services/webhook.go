package services

import (
	"context"

	"appa_payments/internal/domains"
	"appa_payments/internal/models"
	"appa_payments/pkg/shopify"

	"go.uber.org/zap"
)

type webhookService struct {
	shopifyRepo               shopify.Repository
	paymentService            domains.PaymentService
	recurrentDirectDebitAppID string
	logger                    *zap.Logger
}

func NewWebhookService(
	shopifyRepo shopify.Repository,
	paymentService domains.PaymentService,
	recurrentDirectDebitAppID string,
	logger *zap.Logger,
) domains.WebhookService {
	return &webhookService{
		shopifyRepo:               shopifyRepo,
		paymentService:            paymentService,
		recurrentDirectDebitAppID: recurrentDirectDebitAppID,
		logger:                    logger,
	}
}

// OrdersCreated handles a Shopify orders/create webhook delivery. It auto-charges
// the order through the recurrent direct-debit flow when:
//   - the order was created by the recurrent direct-debit app, and
//   - the customer is already affiliated (has the direct_debit_account metafield), and
//   - no prior successful charge exists for the order.
//
// Non-recurrent orders, non-affiliated customers, and already-charged orders are
// skipped silently. Charge failures are persisted by the underlying payment
// service and do not propagate as errors here — the webhook always succeeds.
func (s *webhookService) OrdersCreated(ctx context.Context, orderID string) error {
	resp, err := s.shopifyRepo.GetOrderByID(ctx, orderID)
	if err != nil {
		s.logger.Error("webhook: failed to fetch shopify order", zap.String("orderID", orderID), zap.Error(err))
		return nil
	}

	order := resp.Order

	if order.App == nil || !order.App.IsID(s.recurrentDirectDebitAppID) {
		s.logger.Debug("webhook: order not from recurrent app, skipping", zap.String("order", order.Name))
		return nil
	}

	if order.Customer.DirectDebitAccount == nil || order.Customer.DirectDebitAccount.JsonValue == nil {
		s.logger.Info("webhook: customer not affiliated, skipping",
			zap.String("order", order.Name),
			zap.String("customerID", order.Customer.ID))
		return nil
	}

	alreadyCharged, err := s.paymentService.HasSuccessfulRecurrentCharge(ctx, order.ID)
	if err != nil {
		s.logger.Error("webhook: dedup check failed", zap.String("order", order.Name), zap.Error(err))
		return nil
	}
	if alreadyCharged {
		s.logger.Info("webhook: order already charged successfully, skipping",
			zap.String("order", order.Name))
		return nil
	}

	chargeResp, err := s.paymentService.DirectDebitAccountWithOTP(ctx, models.DirectDebitAccountWithOTPRequest{
		OrderID: orderID,
		OTP:     "",
	})
	if err != nil {
		s.logger.Error("webhook: recurrent charge errored",
			zap.String("order", order.Name),
			zap.Error(err))
		return nil
	}

	s.logger.Info("webhook: recurrent charge completed",
		zap.String("order", order.Name),
		zap.Bool("success", chargeResp.Success),
		zap.String("code", chargeResp.Code))
	return nil
}
