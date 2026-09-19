package provision

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type registryReadiness interface {
	Wait(context.Context, string) error
}

type httpRegistryReadiness struct{}

func (httpRegistryReadiness) Wait(ctx context.Context, host string) error {
	parsedHost, port, err := net.SplitHostPort(host)
	if err != nil || parsedHost != "127.0.0.1" || port == "" || strings.ContainsAny(host, "\r\n\x00") {
		return errors.New("registry readiness endpoint is invalid")
	}
	endpoint := (&url.URL{Scheme: "http", Host: host, Path: "/v2/"}).String()
	transport := &http.Transport{
		Proxy:             nil,
		DisableKeepAlives: true,
		DialContext: (&net.Dialer{
			Timeout:   500 * time.Millisecond,
			KeepAlive: -1,
		}).DialContext,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	for {
		request, requestErr := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if requestErr != nil {
			return errors.New("registry readiness request is invalid")
		}
		response, requestErr := client.Do(request)
		if requestErr == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1024))
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK && strings.EqualFold(strings.TrimSpace(response.Header.Get("Docker-Distribution-API-Version")), "registry/2.0") {
				return nil
			}
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return errors.New("registry readiness timed out")
		case <-timer.C:
		}
	}
}
