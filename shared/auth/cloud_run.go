package auth

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"google.golang.org/api/idtoken"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/credentials/oauth"
)

// GRPCTransportOptions returns local plaintext transport when audience is empty
// and Cloud Run TLS plus Google ID-token authentication when it is set.
func GRPCTransportOptions(ctx context.Context, audience string) ([]grpc.DialOption, error) {
	audience = strings.TrimSpace(audience)
	if audience == "" {
		return []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}, nil
	}

	parsed, err := cloudRunAudience(audience)
	if err != nil {
		return nil, err
	}
	tokenSource, err := idtoken.NewTokenSource(ctx, audience)
	if err != nil {
		return nil, fmt.Errorf("create Cloud Run identity token source: %w", err)
	}

	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: parsed.Hostname()}
	return []grpc.DialOption{
		grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)),
		grpc.WithPerRPCCredentials(oauth.TokenSource{TokenSource: tokenSource}),
	}, nil
}

// AddCloudRunIdentity supplies an ID token for a protected Cloud Run HTTP
// service. An empty audience keeps local and Kubernetes requests unchanged.
func AddCloudRunIdentity(req *http.Request, audience string) error {
	audience = strings.TrimSpace(audience)
	if audience == "" {
		return nil
	}
	if _, err := cloudRunAudience(audience); err != nil {
		return err
	}
	tokenSource, err := idtoken.NewTokenSource(req.Context(), audience)
	if err != nil {
		return fmt.Errorf("create Cloud Run identity token source: %w", err)
	}
	token, err := tokenSource.Token()
	if err != nil {
		return fmt.Errorf("get Cloud Run identity token: %w", err)
	}
	req.Header.Set("X-Serverless-Authorization", "Bearer "+token.AccessToken)
	return nil
}

func cloudRunAudience(audience string) (*url.URL, error) {
	parsed, err := url.Parse(audience)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.Port() != "" || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("Cloud Run audience must be an HTTPS origin: %q", audience)
	}
	return parsed, nil
}
