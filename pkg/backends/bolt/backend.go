package bolt

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/greenpau/caddy-auth-jwt"
	"go.uber.org/zap"
	"os"
	"sync"
	"time"
)

// Backend represents authentication provider with BoltDB backend.
type Backend struct {
	Realm         string                   `json:"realm,omitempty"`
	Path          string                   `json:"path,omitempty"`
	TokenProvider *jwt.TokenProviderConfig `json:"jwt,omitempty"`
	Authenticator *Authenticator           `json:"-"`
	logger        *zap.Logger
}

// NewDatabaseBackend return an instance of authentication provider
// with BoltDB backend.
func NewDatabaseBackend() *Backend {
	b := &Backend{
		TokenProvider: jwt.NewTokenProviderConfig(),
		Authenticator: NewAuthenticator(),
	}
	return b
}

// Authenticator represents database connector.
type Authenticator struct {
	mux    sync.Mutex
	path   string
	db     *sql.DB
	logger *zap.Logger
}

// NewAuthenticator returns an instance of Authenticator.
func NewAuthenticator() *Authenticator {
	return &Authenticator{}
}

// SetPath sets database path.
func (sa *Authenticator) SetPath(s string) {
	sa.path = s
	return
}

// Configure check database connectivity and required tables.
func (sa *Authenticator) Configure() error {
	sa.mux.Lock()
	defer sa.mux.Unlock()
	sa.logger.Info("boltdb backend configuration", zap.String("db_path", sa.path))
	fileInfo, err := os.Stat(sa.path)
	if os.IsNotExist(err) {
		sa.logger.Error("boltdb database file does not exists", zap.String("db_path", sa.path))
		return fmt.Errorf("boltdb database file does not exists")
	}
	if fileInfo.IsDir() {
		sa.logger.Error("boltdb database file path points to a directory", zap.String("db_path", sa.path))
		return fmt.Errorf("boltdb database file path points to a directory")
	}
	db, err := sql.Open("boltdb", sa.path)
	if err != nil {
		return fmt.Errorf("failed to open boltdb database at %s: %s", sa.path, err)
	}
	if db == nil {
		return fmt.Errorf("failed to open boltdb database at %s: nil", sa.path)
	}
	sa.db = db

	return nil
}

// AuthenticateUser checks the database for the presence of a username
// and password and returns user claims.
func (sa *Authenticator) AuthenticateUser(username, password string) (*jwt.UserClaims, int, error) {
	sa.mux.Lock()
	defer sa.mux.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	stmt, err := sa.db.PrepareContext(ctx, "SELECT id FROM User WHERE email = ? AND passwordHash = ?")
	if err != nil {
		return nil, 500, err
	}
	defer stmt.Close()

	var userID int
	err = stmt.QueryRowContext(ctx, username, password).Scan(&userID)
	switch {
	case err == sql.ErrNoRows:
		return nil, 401, fmt.Errorf("user identity not found")
	case err != nil:
		return nil, 500, err
	default:
		sa.logger.Info("user identity found", zap.String("username", username), zap.Int("user_id", userID))
	}

	claims := &jwt.UserClaims{}
	claims.Subject = username
	claims.Email = username
	// claims.Name = "Greenberg, Paul"
	claims.Roles = append(claims.Roles, "anonymous")
	claims.Roles = append(claims.Roles, "guest")

	return claims, 200, fmt.Errorf("Authentication is not supported")
}

// ConfigureAuthenticator configures backend for .
func (b *Backend) ConfigureAuthenticator() error {
	if b.Authenticator == nil {
		b.Authenticator = NewAuthenticator()
	}
	b.Authenticator.SetPath(b.Path)
	b.Authenticator.logger = b.logger
	if err := b.Authenticator.Configure(); err != nil {
		return err
	}
	return nil
}

// ValidateConfig checks whether Backend has mandatory configuration.
func (b *Backend) ValidateConfig() error {
	if b.Path == "" {
		return fmt.Errorf("path is empty")
	}
	return nil
}

// Authenticate performs authentication.
func (b *Backend) Authenticate(reqID string, kv map[string]string) (*jwt.UserClaims, int, error) {
	if kv == nil {
		return nil, 400, fmt.Errorf("No input to authenticate")
	}
	if _, exists := kv["username"]; !exists {
		return nil, 400, fmt.Errorf("No username found")
	}
	if _, exists := kv["password"]; !exists {
		return nil, 401, fmt.Errorf("No password found")
	}
	if b.Authenticator == nil {
		return nil, 500, fmt.Errorf("boltdb backend is nil")
	}
	claims, statusCode, err := b.Authenticator.AuthenticateUser(kv["username"], kv["password"])
	if statusCode == 200 {
		claims.Origin = b.TokenProvider.TokenOrigin
		claims.ExpiresAt = time.Now().Add(time.Duration(b.TokenProvider.TokenLifetime) * time.Second).Unix()
		return claims, statusCode, nil
	}
	return nil, statusCode, err
}

// Validate checks whether Backend is functional.
func (b *Backend) Validate() error {
	if err := b.ValidateConfig(); err != nil {
		return err
	}
	if b.logger == nil {
		return fmt.Errorf("backend logger is nil")
	}

	driverFound := false
	for _, driver := range sql.Drivers() {
		if driver == "boltdb" {
			driverFound = true
			break
		}
	}
	if !driverFound {
		b.logger.Error("boltdb driver not found")
		return fmt.Errorf("boltdb driver not found")
	}

	/*
		driverVersion, _, _ := boltdb.Version()
		b.logger.Info(
			"validating BoltDB backend",
			zap.String("boltdb_version", driverVersion),
			zap.String("db_path", b.Path),
		)
	*/

	if b.Authenticator == nil {
		return fmt.Errorf("boltdb authenticator is nil")
	}

	return nil
}

// GetRealm return authentication realm.
func (b *Backend) GetRealm() string {
	return b.Realm
}

// ConfigureTokenProvider configures TokenProvider.
func (b *Backend) ConfigureTokenProvider(upstream *jwt.TokenProviderConfig) error {
	if upstream == nil {
		return fmt.Errorf("upstream token provider is nil")
	}
	if b.TokenProvider == nil {
		b.TokenProvider = jwt.NewTokenProviderConfig()
	}
	if b.TokenProvider.TokenName == "" {
		b.TokenProvider.TokenName = upstream.TokenName
	}
	if b.TokenProvider.TokenSecret == "" {
		b.TokenProvider.TokenSecret = upstream.TokenSecret
	}
	if b.TokenProvider.TokenIssuer == "" {
		b.TokenProvider.TokenIssuer = upstream.TokenIssuer
	}
	if b.TokenProvider.TokenOrigin == "" {
		b.TokenProvider.TokenOrigin = upstream.TokenOrigin
	}
	if b.TokenProvider.TokenLifetime == 0 {
		b.TokenProvider.TokenLifetime = upstream.TokenLifetime
	}
	return nil
}

// ConfigureLogger configures backend with the same logger as its user.
func (b *Backend) ConfigureLogger(logger *zap.Logger) error {
	if logger == nil {
		return fmt.Errorf("upstream logger is nil")
	}
	b.logger = logger
	return nil
}
