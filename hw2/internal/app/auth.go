package app

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/crypto/bcrypt"

	"marketplace/internal/api"
)

const (
	tokenTypeAccess  = "access"
	tokenTypeRefresh = "refresh"

	accessTokenTTL  = 15 * time.Minute
	refreshTokenTTL = 7 * 24 * time.Hour
)

// tokenClaims — содержимое JWT: sub (id пользователя), exp, role и тип токена.
// Тип нужен, чтобы refresh token нельзя было использовать как access и наоборот.
type tokenClaims struct {
	Role api.Role `json:"role"`
	Type string   `json:"type"`
	jwt.RegisteredClaims
}

func (s *Server) issueToken(userID uuid.UUID, role api.Role, tokenType string, ttl time.Duration) (string, error) {
	now := time.Now()
	claims := tokenClaims{
		Role: role,
		Type: tokenType,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.jwtSecret)
}

func (s *Server) issueTokenPair(userID uuid.UUID, role api.Role) (api.TokenResponse, error) {
	access, err := s.issueToken(userID, role, tokenTypeAccess, accessTokenTTL)
	if err != nil {
		return api.TokenResponse{}, err
	}
	refresh, err := s.issueToken(userID, role, tokenTypeRefresh, refreshTokenTTL)
	if err != nil {
		return api.TokenResponse{}, err
	}
	return api.TokenResponse{
		AccessToken:  access,
		RefreshToken: refresh,
		TokenType:    "Bearer",
		ExpiresIn:    int(accessTokenTTL.Seconds()),
	}, nil
}

// parseToken проверяет подпись, срок действия и тип токена.
// Просроченный токен возвращает ошибку, совместимую с jwt.ErrTokenExpired.
func (s *Server) parseToken(token, wantType string) (authUser, error) {
	var claims tokenClaims
	_, err := jwt.ParseWithClaims(token, &claims, func(*jwt.Token) (any, error) {
		return s.jwtSecret, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}), jwt.WithExpirationRequired())
	if err != nil {
		return authUser{}, err
	}
	if claims.Type != wantType {
		return authUser{}, errors.New("wrong token type")
	}
	id, err := uuid.Parse(claims.Subject)
	if err != nil {
		return authUser{}, err
	}
	return authUser{ID: id, Role: claims.Role}, nil
}

func (s *Server) Register(ctx context.Context, req api.RegisterRequestObject) (api.RegisterResponseObject, error) {
	role := api.RoleUSER
	if req.Body.Role != nil {
		role = api.Role(*req.Body.Role)
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Body.Password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}

	resp := api.UserResponse{Email: strings.ToLower(string(req.Body.Email)), Role: role}
	err = s.db.QueryRow(ctx,
		`INSERT INTO users (email, password_hash, role) VALUES ($1, $2, $3) RETURNING id`,
		resp.Email, string(hash), role,
	).Scan(&resp.Id)

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation
		return nil, newError(http.StatusConflict, api.EMAILALREADYEXISTS, "email already registered")
	}
	if err != nil {
		return nil, err
	}
	return api.Register201JSONResponse(resp), nil
}

func (s *Server) Login(ctx context.Context, req api.LoginRequestObject) (api.LoginResponseObject, error) {
	var (
		id   uuid.UUID
		hash string
		role api.Role
	)
	err := s.db.QueryRow(ctx,
		`SELECT id, password_hash, role FROM users WHERE email = $1`,
		strings.ToLower(string(req.Body.Email)),
	).Scan(&id, &hash, &role)

	invalid := newError(http.StatusUnauthorized, api.INVALIDCREDENTIALS, "invalid email or password")
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, invalid
	}
	if err != nil {
		return nil, err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Body.Password)) != nil {
		return nil, invalid
	}

	tokens, err := s.issueTokenPair(id, role)
	if err != nil {
		return nil, err
	}
	return api.Login200JSONResponse(tokens), nil
}

func (s *Server) Refresh(ctx context.Context, req api.RefreshRequestObject) (api.RefreshResponseObject, error) {
	invalid := newError(http.StatusUnauthorized, api.REFRESHTOKENINVALID, "invalid refresh token")

	user, err := s.parseToken(req.Body.RefreshToken, tokenTypeRefresh)
	if err != nil {
		return nil, invalid
	}

	// Роль берём из БД, а не из старого токена: вдруг пользователя удалили или роль поменялась.
	var role api.Role
	err = s.db.QueryRow(ctx, `SELECT role FROM users WHERE id = $1`, user.ID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, invalid
	}
	if err != nil {
		return nil, err
	}

	tokens, err := s.issueTokenPair(user.ID, role)
	if err != nil {
		return nil, err
	}
	return api.Refresh200JSONResponse(tokens), nil
}
