package security

import (
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"
)

func TestJwt(t *testing.T) {
	if _, err := (Jwt[string]{}).CreateToken("x", jwt.RegisteredClaims{}); err == nil {
		t.Fatal("empty key must not sign")
	}
	if _, err := NewJwt[string]("short"); err == nil {
		t.Fatal("short secret must not build a Jwt")
	}

	secret := strings.Repeat("k", MinKeySize)
	tokens, err := NewJwt[string](secret)
	if err != nil {
		t.Fatal(err)
	}
	token, err := tokens.CreateToken("x", jwt.RegisteredClaims{})
	if err != nil {
		t.Fatal(err)
	}
	if claims, err := tokens.DecodeToken(token); err != nil || claims.Payload != "x" {
		t.Fatalf("decode: %+v %v", claims, err)
	}

	hs512, _ := jwt.NewWithClaims(jwt.SigningMethodHS512, jwt.MapClaims{}).SignedString([]byte(secret))
	if _, err := tokens.DecodeToken(hs512); err == nil {
		t.Fatal("only HS256 is accepted")
	}
}

func TestJwtKeysAreIsolated(t *testing.T) {
	a, err := NewJwt[string](strings.Repeat("a", MinKeySize))
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewJwt[string](strings.Repeat("b", MinKeySize))
	if err != nil {
		t.Fatal(err)
	}

	token, err := a.CreateToken("x", jwt.RegisteredClaims{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.DecodeToken(token); err == nil {
		t.Fatal("a token signed by one Jwt must not decode with another key")
	}
	if claims, err := a.DecodeToken(token); err != nil || claims.Payload != "x" {
		t.Fatalf("same Jwt must still decode: %+v %v", claims, err)
	}
}

func TestCheckSignKey(t *testing.T) {
	if err := CheckSignKey("short"); err == nil {
		t.Fatal("short secret must fail")
	}
	if err := CheckSignKey(strings.Repeat("k", MinKeySize)); err != nil {
		t.Fatal(err)
	}
}

func TestErrorStatus(t *testing.T) {
	if got := ErrInvalidToken.(interface{ HTTPStatus() int }).HTTPStatus(); got != 401 {
		t.Fatalf("core.Error must reply 401 to an invalid token: %d", got)
	}
}
