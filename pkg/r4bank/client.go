package r4bank

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	helpers "appa_payments/pkg"

	"go.uber.org/zap"
)

const defaultRequestTimeout = 25 * time.Second

// endpointTimeouts overrides defaultRequestTimeout for R4 endpoints known to be slow.
// Only direct-debit-account polls for operation status upstream; validate-immediate
// is a single bank call capped at 20s on the R4 side.
var endpointTimeouts = map[string]time.Duration{
	r4ValidateImmediateEndpoint:  35 * time.Second,
	r4DirectDebitAccountEndpoint: 150 * time.Second,
}

type RestClient struct {
	baseURL string
	client  *http.Client
	logger  *zap.Logger
	token   string
	secret  string
}

// NewClient creates a new instance of RestClient
func NewClient(
	endpoint string,
	token string,
	secret string,
	logger *zap.Logger,
) *RestClient {
	return &RestClient{
		baseURL: endpoint,
		client:  &http.Client{},
		token:   token,
		secret:  secret,
		logger:  logger,
	}
}

// requestTimeout returns the deadline to apply to endpoint.
func requestTimeout(endpoint string) time.Duration {
	if timeout, ok := endpointTimeouts[endpoint]; ok {
		return timeout
	}
	return defaultRequestTimeout
}

// Do executes an HTTP request
func (r *RestClient) Do(
	ctx context.Context,
	payload any,
	endpoint string,
	method string,
) ([]byte, error) {
	var (
		body []byte
		err  error
	)

	if payload == nil {
		payload = map[string]string{}
	}

	body, err = json.Marshal(payload)
	if err != nil {
		r.logger.Error(err.Error(), zap.Any("payload", payload))
		return nil, fmt.Errorf("error marshaling payload: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, requestTimeout(endpoint))
	defer cancel()

	url := fmt.Sprintf("%s/%s", r.baseURL, endpoint)
	req, err := http.NewRequestWithContext(
		ctx, method, url, bytes.NewReader(body),
	)
	if err != nil {
		r.logger.Error(err.Error(), zap.Any("payload", payload))
		return nil, err
	}

	auth := helpers.GenerateAuthToken(r.token, r.secret)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", auth)

	resp, err := r.client.Do(req)
	if err != nil {
		r.logger.Error(err.Error(), zap.Any("payload", payload))
		return nil, fmt.Errorf("error en request: %w", err)
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if endpoint == r4ValidateImmediateEndpoint {
			return nil, fmt.Errorf("%s", string(data))
		}
		r.logger.Error("R4 API error: ", zap.String("body", string(data)), zap.Any("payload", payload))
		return nil, fmt.Errorf("R4 API error: %s", string(data))
	}

	return data, nil
}
