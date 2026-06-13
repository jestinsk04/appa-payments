package services

import (
	"context"

	"appa_payments/internal/domains"
	"appa_payments/internal/models"

	"go.uber.org/zap"
)

type webhookService struct {
	paymentService domains.PaymentService
	logger         *zap.Logger
}

func NewWebhookService(
	paymentService domains.PaymentService,
	logger *zap.Logger,
) domains.WebhookService {
	return &webhookService{
		paymentService: paymentService,
		logger:         logger,
	}
}

// OrdersCreated handles a Shopify orders/create webhook delivery. It checks the
// database for a prior successful charge on the same order (dedup) and, if
// none, delegates to the payment service. The payment service is the single
// source of truth for the recurrent-app gate, the affiliation gate, and the
// OTP-bypass behaviour, so no order data is fetched here.
//
// Charge failures are persisted by the payment service and do not propagate as
// errors — the webhook always succeeds so Shopify does not retry.
func (s *webhookService) OrdersCreated(ctx context.Context, orderID string) error {
	alreadyCharged, err := s.paymentService.HasSuccessfulRecurrentCharge(ctx, orderID)
	if err != nil {
		s.logger.Error("webhook: dedup check failed",
			zap.String("orderID", orderID),
			zap.Error(err))
		return nil
	}
	if alreadyCharged {
		s.logger.Info("webhook: order already charged successfully, skipping",
			zap.String("orderID", orderID))
		return nil
	}

	chargeResp, err := s.paymentService.DirectDebitAccountWithOTP(ctx, models.DirectDebitAccountWithOTPRequest{
		OrderID: orderID,
		OTP:     "",
	})
	if err != nil {
		s.logger.Error("webhook: recurrent charge errored",
			zap.String("orderID", orderID),
			zap.Error(err))
		return nil
	}

	s.logger.Info("webhook: recurrent charge completed",
		zap.String("orderID", orderID),
		zap.Bool("success", chargeResp.Success),
		zap.String("code", chargeResp.Code))
	return nil
}
