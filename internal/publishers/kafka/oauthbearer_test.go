package kafka

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

func TestOAuthBearer_Start(t *testing.T) {
	t.Run("sends the token without extensions", func(t *testing.T) {
		m := &oauthBearer{tokens: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "tok"})}

		_, ir, err := m.Start(context.Background())
		require.NoError(t, err)
		assert.Equal(t, "n,,\x01auth=Bearer tok\x01\x01", string(ir))
	})

	t.Run("sends extensions sorted by key", func(t *testing.T) {
		m := &oauthBearer{
			tokens: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "tok"}),
			extensions: map[string]string{
				"logicalCluster": "lkc-1",
				"identityPoolId": "pool-2",
			},
		}

		_, ir, err := m.Start(context.Background())
		require.NoError(t, err)
		assert.Equal(t,
			"n,,\x01auth=Bearer tok\x01identityPoolId=pool-2\x01logicalCluster=lkc-1\x01\x01",
			string(ir),
		)
	})
}

func TestOAuthBearer_Next(t *testing.T) {
	m := &oauthBearer{}

	done, resp, err := m.Next(context.Background(), nil)
	require.NoError(t, err)
	assert.True(t, done)
	assert.Nil(t, resp)

	_, _, err = m.Next(context.Background(), []byte(`{"status":"invalid_token"}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid_token")
}

func TestKafka_OAuthBearer_FetchesToken(t *testing.T) {
	var requests int
	tokenServer := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests++
			require.NoError(t, r.ParseForm())

			id, secret, ok := r.BasicAuth()
			assert.True(t, ok)
			assert.Equal(t, "relay", id)
			assert.Equal(t, "s3cret", secret)
			assert.Equal(t, "client_credentials", r.Form.Get("grant_type"))
			assert.Equal(t, "kafka produce", r.Form.Get("scope"))

			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "issued-token",
				"token_type":   "Bearer",
				"expires_in":   3600,
			})
		}),
	)
	t.Cleanup(tokenServer.Close)

	k, err := New(Config{
		Brokers:           []string{"localhost:9092"},
		SASLMechanism:     "oauthbearer",
		OAuthTokenURL:     tokenServer.URL,
		OAuthClientID:     "relay",
		OAuthClientSecret: "s3cret",
		OAuthScopes:       []string{"kafka", "produce"},
	})
	require.NoError(t, err)

	require.NotNil(t, k.transport.SASL, "writer transport must authenticate")
	assert.Equal(t, "OAUTHBEARER", k.transport.SASL.Name())
	assert.Same(t, k.dialer.SASLMechanism, k.transport.SASL)

	for range 2 {
		_, ir, err := k.transport.SASL.Start(context.Background())
		require.NoError(t, err)
		assert.Equal(t, "n,,\x01auth=Bearer issued-token\x01\x01", string(ir))
	}
	assert.Equal(t, 1, requests, "the token must be cached until it expires")
}

func TestKafka_OAuthBearer_TokenErrorFailsStart(t *testing.T) {
	tokenServer := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}),
	)
	t.Cleanup(tokenServer.Close)

	k, err := New(Config{
		Brokers:       []string{"localhost:9092"},
		SASLMechanism: "oauthbearer",
		OAuthTokenURL: tokenServer.URL,
		OAuthClientID: "relay",
	})
	require.NoError(t, err)

	_, _, err = k.transport.SASL.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "oauthbearer: failed to get a token")
}

func TestKafka_OAuthBearer_InvalidConfig(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{
			name:    "missing token URL",
			cfg:     Config{OAuthClientID: "relay"},
			wantErr: "KAFKA_OAUTH_TOKEN_URL",
		},
		{
			name:    "missing client ID",
			cfg:     Config{OAuthTokenURL: "https://idp/token"},
			wantErr: "KAFKA_OAUTH_CLIENT_ID",
		},
		{
			name: "reserved extension key",
			cfg: Config{
				OAuthTokenURL:   "https://idp/token",
				OAuthClientID:   "relay",
				OAuthExtensions: map[string]string{"auth": "x"},
			},
			wantErr: `invalid OAuth extension key "auth"`,
		},
		{
			name: "non-letter extension key",
			cfg: Config{
				OAuthTokenURL:   "https://idp/token",
				OAuthClientID:   "relay",
				OAuthExtensions: map[string]string{"pool_id": "x"},
			},
			wantErr: "only letters are allowed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.cfg
			cfg.Brokers = []string{"localhost:9092"}
			cfg.SASLMechanism = "oauthbearer"

			_, err := New(cfg)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}
