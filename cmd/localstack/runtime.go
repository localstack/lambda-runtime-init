package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func waitForRuntimeAPI(ctx context.Context, targetAddress string) error {
	if !strings.HasPrefix(targetAddress, "http://") {
		targetAddress = fmt.Sprintf("http://%s", targetAddress)
	}

	healthEndpoint, err := url.JoinPath(targetAddress, "2018-06-01", "ping")
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthEndpoint, nil)
	if err != nil {
		return err
	}
	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		resp, err := client.Do(req)
		if err == nil {
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
