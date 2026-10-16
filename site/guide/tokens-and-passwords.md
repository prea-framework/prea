# Tokens & passwords

Preá has the building blocks of authentication: signed tokens to identify a user across requests, and safe password hashing. A complete login flow, with sessions and roles, is [on the way](/about/whats-next). Until then, you assemble one from these pieces.

::: info What you'll learn
- How to store and check passwords
- How to create and read a signed token
- How to configure the signing secret
:::

## Passwords

Never store a password. Store its **hash**, and compare against the hash when the user logs in:

```go
hash, err := security.HashPassword(req.Password)       // store hash
ok, err := security.ComparePassword(req.Password, hash) // true if it matches
```

The hash uses argon2id, with a random salt per password. Two users with the same password get different hashes. Passwords longer than 128 bytes are rejected with `security.ErrPasswordTooLong` before hashing, so a huge body cannot burn CPU and memory.

## Tokens

A token is a signed piece of data that you give the client at login. The client sends it back on every request, and you can trust what's inside because only your server can sign it.

`security.NewJwt[T]` creates a signer that reads and writes tokens carrying a value of type `T`. Each `Jwt` keeps its own key, so two apps in the same process never share a secret:

```go
type Claims struct {
	UserID string `json:"userId"`
	Role   string `json:"role"`
}

tokens, err := security.NewJwt[Claims](secret) // secret from config or the environment
if err != nil {
	return err // shorter than 32 bytes
}

// at login
token, err := tokens.CreateToken(Claims{UserID: u.ID, Role: "admin"}, jwt.RegisteredClaims{
	ExpiresAt: jwt.NewNumericDate(time.Now().Add(24 * time.Hour)),
})

// on each request
claims, err := tokens.DecodeToken(token)
if err != nil {
	core.Error(c, err) // 401 invalid token
	return
}
userID := claims.Payload.UserID
```

`jwt` is `github.com/golang-jwt/jwt/v5`, which defines the standard fields like the expiry. A bad signature, an expired token or one signed with another algorithm returns `security.ErrInvalidToken`, which replies 401. Tokens are signed with HS256, and only HS256 is accepted.

## The secret

Put the signing secret in the configuration (and keep the real value in the environment):

```yaml
# config/application.yaml
security:
  jwt:
    secret: ${JWT_SECRET}
```

- It must have **at least 32 bytes**. When the key is set, `prea.Run` checks the length at startup; `NewJwt` checks it again when you build the signer.
- An app that doesn't use tokens doesn't need it. Without a secret, the app starts normally.
- Keep it in the environment, never in a committed file. Generate one with `openssl rand -base64 48`.
- Pass the secret into `NewJwt` yourself (from `config` or an env var). Preá does not keep a process-wide key.

## Protect routes

A token check is a [middleware](./middlewares) component that reads the `Authorization` header, decodes the token and stores who the user is in the request:

```go
type Auth struct{}

func (a *Auth) Handle(c *gin.Context) {
	token, found := strings.CutPrefix(c.GetHeader("Authorization"), "Bearer ")
	claims, err := tokens.DecodeToken(token)
	if !found || err != nil {
		core.Error(c, security.ErrInvalidToken)
		c.Abort()
		return
	}
	c.Set("userID", claims.Payload.UserID)
	c.Next()
}
```

Use it per preá or per route, and read the user with `c.GetString("userID")` in the handler.

## Next steps

- [HTTP security](./http-security)
- [Logging](./logging)
