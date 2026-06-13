package handlers

import (
	"context"
	"net/http"
	"strconv"

	"appa_payments/internal/domains"
	"appa_payments/internal/models"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

const (
	webhookJobBuffer = 25
	webhookWorkers   = 5
)

type WebhookHandler struct {
	WebhookService domains.WebhookService
	jobQueue       chan webhookJob
	logger         *zap.Logger
}

type webhookJob struct {
	OrderID int
}

// NewWebhookHandler builds the handler and starts the background worker pool.
// Workers run for the lifetime of the process.
func NewWebhookHandler(webhookService domains.WebhookService, logger *zap.Logger) *WebhookHandler {
	h := &WebhookHandler{
		WebhookService: webhookService,
		jobQueue:       make(chan webhookJob, webhookJobBuffer),
		logger:         logger,
	}
	for i := range webhookWorkers {
		go h.worker(i)
	}
	return h
}

// HandleOrdersCreated binds the Shopify orders/create payload, enqueues a job
// for asynchronous processing, and always returns 200 so Shopify does not retry.
// HMAC validation is performed upstream by the middleware on the route.
func (h *WebhookHandler) HandleOrdersCreated(c *gin.Context) {
	var payload models.Webhook
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.logger.Error("webhook: failed to bind JSON", zap.Error(err))
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	job := webhookJob{OrderID: payload.ID}
	select {
	case h.jobQueue <- job:
	default:
		h.logger.Warn("webhook: job queue full, dropping job", zap.Int("orderID", payload.ID))
	}

	c.JSON(http.StatusOK, gin.H{"status": "accepted"})
}

func (h *WebhookHandler) worker(id int) {
	defer func() {
		if r := recover(); r != nil {
			h.logger.Error("webhook worker panicked", zap.Int("workerID", id), zap.Any("panic", r))
		}
	}()
	for job := range h.jobQueue {
		orderID := strconv.Itoa(job.OrderID)
		if err := h.WebhookService.OrdersCreated(context.Background(), orderID); err != nil {
			h.logger.Error("webhook: OrdersCreated returned error",
				zap.Int("workerID", id),
				zap.String("orderID", orderID),
				zap.Error(err))
		}
	}
}
