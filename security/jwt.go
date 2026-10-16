package security

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/golang-jwt/jwt/v5"
)

const (
	DefaultAlg string = "HS256"
	MinKeySize        = 32
)

var (
	ErrInvalidToken       error = &statusError{"invalid token", http.StatusUnauthorized} // bad signature, expired, wrong algorithm
	ErrInvalidHash              = errors.New("invalid hash")                             // stored password hash is malformed
	ErrInvalidHashVersion       = errors.New("invalid algorithm version")                // stored hash from another argon2 version
)

// statusError gives core.Error the status to reply with, without core
// importing this package.
type statusError struct {
	msg    string
	status int
}

func (e *statusError) Error() string   { return e.msg }
func (e *statusError) HTTPStatus() int { return e.status }

// Jwt signs and checks tokens that carry a payload of type M. Each value has
// its own key; create one with NewJwt so two apps in the same process never
// share or overwrite a secret.
type Jwt[M any] struct {
	key []byte
}

// NewJwt returns a Jwt that signs with secret. The secret must have at least
// MinKeySize bytes.
func NewJwt[M any](secret string) (Jwt[M], error) {
	key, err := parseKey(secret)
	if err != nil {
		return Jwt[M]{}, err
	}
	return Jwt[M]{key: key}, nil
}

// CheckSignKey validates a secret at startup without keeping it. Call it when
// security.jwt.secret is set so a short key stops the app before it serves.
func CheckSignKey(secret string) error {
	_, err := parseKey(secret)
	return err
}

func parseKey(secret string) ([]byte, error) {
	if len(secret) < MinKeySize {
		return nil, fmt.Errorf("security: the jwt secret must have at least %d bytes (got %d): set security.jwt.secret", MinKeySize, len(secret))
	}
	key := make([]byte, len(secret))
	copy(key, secret)
	return key, nil
}

func (j Jwt[M]) signKey() ([]byte, error) {
	if len(j.key) < MinKeySize {
		return nil, fmt.Errorf("security: the jwt secret must have at least %d bytes (got %d): use security.NewJwt", MinKeySize, len(j.key))
	}
	return j.key, nil
}

type DataClaims[M any] struct {
	Payload M `json:"payload"`
	jwt.RegisteredClaims
}

func (j Jwt[M]) CreateToken(object M, claims jwt.RegisteredClaims) (string, error) {
	token := jwt.New(jwt.GetSigningMethod(DefaultAlg))

	token.Claims = &DataClaims[M]{
		object,
		claims,
	}

	key, err := j.signKey()
	if err != nil {
		return "", err
	}
	encoded, err := token.SignedString(key)
	if err != nil {
		return "", err
	}

	return encoded, nil
}

func (j Jwt[M]) DecodeToken(token string) (DataClaims[M], error) {
	keyFunc := func(token *jwt.Token) (interface{}, error) {
		return j.signKey()
	}

	parsed, err := jwt.ParseWithClaims(token, &DataClaims[M]{}, keyFunc, jwt.WithValidMethods([]string{DefaultAlg}))
	if err != nil {
		return DataClaims[M]{}, fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}

	return *parsed.Claims.(*DataClaims[M]), nil
}
