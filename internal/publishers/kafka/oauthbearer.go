package kafka

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/segmentio/kafka-go/sasl"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

// defaultTokenTimeout is used for token requests when the connection timeout is zero.
const defaultTokenTimeout = 10 * time.Second

type oauthBearer struct {
	tokens     oauth2.TokenSource
	extensions map[string]string
}

func (m *oauthBearer) Name() string { return "OAUTHBEARER" }

func (m *oauthBearer) Start(_ context.Context) (sasl.StateMachine, []byte, error) {
	token, err := m.tokens.Token()
	if err != nil {
		return nil, nil, fmt.Errorf("oauthbearer: failed to get a token: %w", err)
	}

	var b strings.Builder
	b.WriteString("n,,\x01auth=Bearer ")
	b.WriteString(token.AccessToken)
	b.WriteString("\x01")

	keys := make([]string, 0, len(m.extensions))
	for key := range m.extensions {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		b.WriteString(key + "=" + m.extensions[key] + "\x01")
	}
	b.WriteString("\x01")

	return m, []byte(b.String()), nil
}

func (m *oauthBearer) Next(_ context.Context, challenge []byte) (bool, []byte, error) {
	// The broker answers a rejected token with a JSON error challenge, and an
	// accepted one with an empty response.
	if len(challenge) > 0 {
		return true, nil, fmt.Errorf("oauthbearer: broker rejected the token: %s", challenge)
	}
	return true, nil, nil
}

func (k *Publisher) newOAuthBearer() (sasl.Mechanism, error) {
	if k.cfg.OAuthTokenURL == "" || k.cfg.OAuthClientID == "" {
		return nil, errors.New(
			"oauthbearer requires KAFKA_OAUTH_TOKEN_URL and KAFKA_OAUTH_CLIENT_ID",
		)
	}

	for key, value := range k.cfg.OAuthExtensions {
		if err := validateOAuthExtension(key, value); err != nil {
			return nil, err
		}
	}

	timeout := k.cfg.ConnectionTimeout
	if timeout <= 0 {
		timeout = defaultTokenTimeout
	}
	// The token source has no per-call context, so its HTTP client carries
	// the timeout.
	ctx := context.WithValue(
		context.Background(),
		oauth2.HTTPClient,
		&http.Client{Timeout: timeout},
	)

	cc := clientcredentials.Config{
		ClientID:     k.cfg.OAuthClientID,
		ClientSecret: k.cfg.OAuthClientSecret,
		TokenURL:     k.cfg.OAuthTokenURL,
		Scopes:       k.cfg.OAuthScopes,
	}

	return &oauthBearer{
		tokens:     cc.TokenSource(ctx),
		extensions: k.cfg.OAuthExtensions,
	}, nil
}

func validateOAuthExtension(key, value string) error {
	if key == "" || key == "auth" {
		return fmt.Errorf("invalid OAuth extension key %q", key)
	}
	for _, r := range key {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') {
			return fmt.Errorf("invalid OAuth extension key %q: only letters are allowed", key)
		}
	}
	if strings.ContainsRune(value, '\x01') {
		return fmt.Errorf("invalid OAuth extension value for %q", key)
	}
	return nil
}
