package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// ErrSessionNotFound is returned when no session matches the supplied token.
var ErrSessionNotFound = errors.New("session not found")

// ErrClientNotFound is returned when no registered client matches the id.
var ErrClientNotFound = errors.New("client not found")

const (
	// sessionKeyPrefix / clientKeyPrefix namespace the two item types that
	// share the single DynamoDB session table (partition key "pk").
	sessionKeyPrefix = "sess#"
	clientKeyPrefix  = "client#"
	// refreshIndexName is the GSI used to look sessions up by refresh token.
	refreshIndexName = "refresh-index"
)

// Session is a persisted OAuth login session for an MCP client. The opaque
// AccessToken is the bearer token presented to the resource server; the
// embedded GoogleIDToken is forwarded to the Config API for downstream
// authentication (the Config API verifies genuine Google ID tokens).
type Session struct {
	AccessToken      string
	RefreshToken     string
	ClientID         string
	Subject          string
	Email            string
	GoogleIDToken    string
	GoogleRefresh    string
	AccessExpiresAt  time.Time
	RefreshExpiresAt time.Time
}

// ClientRegistration is a persisted Dynamic Client Registration (RFC 7591).
type ClientRegistration struct {
	ClientID     string
	RedirectURIs []string
	ClientName   string
	ExpiresAt    time.Time
}

// SessionStore persists OAuth sessions and registered clients so issued
// access/refresh tokens survive restarts and are shared across replicas.
// Short-lived authorization codes and pending authorizations are intentionally
// NOT stored here; they remain process-local in the OAuth server.
type SessionStore interface {
	PutSession(ctx context.Context, s *Session) error
	GetSessionByAccessToken(ctx context.Context, accessToken string) (*Session, error)
	GetSessionByRefreshToken(ctx context.Context, refreshToken string) (*Session, error)
	DeleteSession(ctx context.Context, accessToken string) error

	PutClient(ctx context.Context, c *ClientRegistration) error
	GetClient(ctx context.Context, clientID string) (*ClientRegistration, error)
}

// NewSessionStore returns a DynamoDB-backed store when tableName is set and AWS
// configuration can be loaded; otherwise it falls back to an in-memory store
// (suitable for local/stdio use and tests) with a warning.
func NewSessionStore(ctx context.Context, tableName string, logger *slog.Logger) (SessionStore, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if tableName == "" {
		logger.Warn("session table not configured; using in-memory session store " +
			"(sessions will not survive a restart or span replicas). " +
			"Set EMAILMCP_SESSION_TABLE or the SSM parameter /emailmcp/session-table/name")
		return newMemorySessionStore(), nil
	}

	var opts []func(*awsconfig.LoadOptions) error
	if region := os.Getenv("AWS_REGION"); region != "" {
		opts = append(opts, awsconfig.WithRegion(region))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("load aws config for session store: %w", err)
	}
	logger.Info("dynamodb session store enabled", "table", tableName, "region", cfg.Region)
	return &dynamoSessionStore{
		client:    dynamodb.NewFromConfig(cfg),
		tableName: tableName,
	}, nil
}

// --- DynamoDB implementation ---

type dynamoSessionStore struct {
	client    *dynamodb.Client
	tableName string
}

type sessionItem struct {
	PK               string `dynamodbav:"pk"`
	Type             string `dynamodbav:"type"`
	AccessToken      string `dynamodbav:"accessToken"`
	RefreshToken     string `dynamodbav:"refreshToken"`
	ClientID         string `dynamodbav:"clientId"`
	Subject          string `dynamodbav:"subject"`
	Email            string `dynamodbav:"email"`
	GoogleIDToken    string `dynamodbav:"googleIdToken"`
	GoogleRefresh    string `dynamodbav:"googleRefresh"`
	AccessExpiresAt  int64  `dynamodbav:"accessExpiresAt"`
	RefreshExpiresAt int64  `dynamodbav:"refreshExpiresAt"`
	TTL              int64  `dynamodbav:"ttl"`
}

type clientItem struct {
	PK           string   `dynamodbav:"pk"`
	Type         string   `dynamodbav:"type"`
	ClientID     string   `dynamodbav:"clientId"`
	RedirectURIs []string `dynamodbav:"redirectUris"`
	ClientName   string   `dynamodbav:"clientName"`
	ExpiresAt    int64    `dynamodbav:"expiresAt"`
	TTL          int64    `dynamodbav:"ttl"`
}

func (it sessionItem) toSession() *Session {
	return &Session{
		AccessToken:      it.AccessToken,
		RefreshToken:     it.RefreshToken,
		ClientID:         it.ClientID,
		Subject:          it.Subject,
		Email:            it.Email,
		GoogleIDToken:    it.GoogleIDToken,
		GoogleRefresh:    it.GoogleRefresh,
		AccessExpiresAt:  time.Unix(it.AccessExpiresAt, 0),
		RefreshExpiresAt: time.Unix(it.RefreshExpiresAt, 0),
	}
}

func (d *dynamoSessionStore) PutSession(ctx context.Context, s *Session) error {
	it := sessionItem{
		PK:               sessionKeyPrefix + s.AccessToken,
		Type:             "session",
		AccessToken:      s.AccessToken,
		RefreshToken:     s.RefreshToken,
		ClientID:         s.ClientID,
		Subject:          s.Subject,
		Email:            s.Email,
		GoogleIDToken:    s.GoogleIDToken,
		GoogleRefresh:    s.GoogleRefresh,
		AccessExpiresAt:  s.AccessExpiresAt.Unix(),
		RefreshExpiresAt: s.RefreshExpiresAt.Unix(),
		TTL:              s.RefreshExpiresAt.Unix(),
	}
	av, err := attributevalue.MarshalMap(it)
	if err != nil {
		return fmt.Errorf("marshal session: %w", err)
	}
	if _, err := d.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(d.tableName),
		Item:      av,
	}); err != nil {
		return fmt.Errorf("put session: %w", err)
	}
	return nil
}

func (d *dynamoSessionStore) GetSessionByAccessToken(ctx context.Context, accessToken string) (*Session, error) {
	out, err := d.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      aws.String(d.tableName),
		ConsistentRead: aws.Bool(true),
		Key: map[string]ddbtypes.AttributeValue{
			"pk": &ddbtypes.AttributeValueMemberS{Value: sessionKeyPrefix + accessToken},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("get session: %w", err)
	}
	if out.Item == nil {
		return nil, ErrSessionNotFound
	}
	var it sessionItem
	if err := attributevalue.UnmarshalMap(out.Item, &it); err != nil {
		return nil, fmt.Errorf("unmarshal session: %w", err)
	}
	return it.toSession(), nil
}

func (d *dynamoSessionStore) GetSessionByRefreshToken(ctx context.Context, refreshToken string) (*Session, error) {
	out, err := d.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(d.tableName),
		IndexName:              aws.String(refreshIndexName),
		KeyConditionExpression: aws.String("refreshToken = :rt"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":rt": &ddbtypes.AttributeValueMemberS{Value: refreshToken},
		},
		Limit: aws.Int32(1),
	})
	if err != nil {
		return nil, fmt.Errorf("query session by refresh token: %w", err)
	}
	if len(out.Items) == 0 {
		return nil, ErrSessionNotFound
	}
	var it sessionItem
	if err := attributevalue.UnmarshalMap(out.Items[0], &it); err != nil {
		return nil, fmt.Errorf("unmarshal session: %w", err)
	}
	return it.toSession(), nil
}

func (d *dynamoSessionStore) DeleteSession(ctx context.Context, accessToken string) error {
	if _, err := d.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(d.tableName),
		Key: map[string]ddbtypes.AttributeValue{
			"pk": &ddbtypes.AttributeValueMemberS{Value: sessionKeyPrefix + accessToken},
		},
	}); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

func (d *dynamoSessionStore) PutClient(ctx context.Context, c *ClientRegistration) error {
	it := clientItem{
		PK:           clientKeyPrefix + c.ClientID,
		Type:         "client",
		ClientID:     c.ClientID,
		RedirectURIs: append([]string(nil), c.RedirectURIs...),
		ClientName:   c.ClientName,
		ExpiresAt:    c.ExpiresAt.Unix(),
		TTL:          c.ExpiresAt.Unix(),
	}
	av, err := attributevalue.MarshalMap(it)
	if err != nil {
		return fmt.Errorf("marshal client: %w", err)
	}
	if _, err := d.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(d.tableName),
		Item:      av,
	}); err != nil {
		return fmt.Errorf("put client: %w", err)
	}
	return nil
}

func (d *dynamoSessionStore) GetClient(ctx context.Context, clientID string) (*ClientRegistration, error) {
	out, err := d.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      aws.String(d.tableName),
		ConsistentRead: aws.Bool(true),
		Key: map[string]ddbtypes.AttributeValue{
			"pk": &ddbtypes.AttributeValueMemberS{Value: clientKeyPrefix + clientID},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("get client: %w", err)
	}
	if out.Item == nil {
		return nil, ErrClientNotFound
	}
	var it clientItem
	if err := attributevalue.UnmarshalMap(out.Item, &it); err != nil {
		return nil, fmt.Errorf("unmarshal client: %w", err)
	}
	return &ClientRegistration{
		ClientID:     it.ClientID,
		RedirectURIs: it.RedirectURIs,
		ClientName:   it.ClientName,
		ExpiresAt:    time.Unix(it.ExpiresAt, 0),
	}, nil
}

// --- In-memory implementation (local/stdio + tests) ---

type memorySessionStore struct {
	mu        sync.Mutex
	sessions  map[string]*Session // accessToken -> session
	byRefresh map[string]string   // refreshToken -> accessToken
	clients   map[string]*ClientRegistration
}

func newMemorySessionStore() *memorySessionStore {
	return &memorySessionStore{
		sessions:  make(map[string]*Session),
		byRefresh: make(map[string]string),
		clients:   make(map[string]*ClientRegistration),
	}
}

func cloneSession(s *Session) *Session {
	cp := *s
	return &cp
}

func (m *memorySessionStore) PutSession(_ context.Context, s *Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[s.AccessToken] = cloneSession(s)
	m.byRefresh[s.RefreshToken] = s.AccessToken
	return nil
}

func (m *memorySessionStore) GetSessionByAccessToken(_ context.Context, accessToken string) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[accessToken]
	if !ok {
		return nil, ErrSessionNotFound
	}
	return cloneSession(s), nil
}

func (m *memorySessionStore) GetSessionByRefreshToken(_ context.Context, refreshToken string) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	accessToken, ok := m.byRefresh[refreshToken]
	if !ok {
		return nil, ErrSessionNotFound
	}
	s, ok := m.sessions[accessToken]
	if !ok {
		return nil, ErrSessionNotFound
	}
	return cloneSession(s), nil
}

func (m *memorySessionStore) DeleteSession(_ context.Context, accessToken string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[accessToken]; ok {
		// Only drop the refresh mapping if it still points at this access token;
		// a rotation may have already repointed it to a newer access token.
		if m.byRefresh[s.RefreshToken] == accessToken {
			delete(m.byRefresh, s.RefreshToken)
		}
		delete(m.sessions, accessToken)
	}
	return nil
}

func (m *memorySessionStore) PutClient(_ context.Context, c *ClientRegistration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *c
	cp.RedirectURIs = append([]string(nil), c.RedirectURIs...)
	m.clients[c.ClientID] = &cp
	return nil
}

func (m *memorySessionStore) GetClient(_ context.Context, clientID string) (*ClientRegistration, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.clients[clientID]
	if !ok {
		return nil, ErrClientNotFound
	}
	cp := *c
	cp.RedirectURIs = append([]string(nil), c.RedirectURIs...)
	return &cp, nil
}
