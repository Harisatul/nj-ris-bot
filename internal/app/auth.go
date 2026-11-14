package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type identity struct {
	CredentialID       string
	Agent              string
	OwnerID            string
	PrincipalID        string
	DefaultWorkspaceID string
	Legacy             bool
}
type identityKey struct{}

func withIdentity(ctx context.Context, v identity) context.Context {
	return context.WithValue(ctx, identityKey{}, v)
}
func identityFrom(ctx context.Context) (identity, bool) {
	v, ok := ctx.Value(identityKey{}).(identity)
	return v, ok
}
func actor(ctx context.Context, fallback string) identity {
	if v, ok := identityFrom(ctx); ok {
		return v
	}
	return identity{OwnerID: fallback, Agent: "legacy", Legacy: true}
}
func (a *App) owner(ctx context.Context) string { return actor(ctx, a.C.OwnerID).OwnerID }

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (a *App) authorize(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			jsonErr(w, http.StatusUnauthorized, "Bearer token required")
			return
		}
		token := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
		if token == "" {
			jsonErr(w, http.StatusUnauthorized, "invalid API token")
			return
		}
		var in identity
		err := a.DB.QueryRow(r.Context(), `SELECT c.id::text,p.slug,coalesce(c.owner_id,''),p.id::text,coalesce(c.default_workspace_id::text,'')
 FROM mimo.agent_credentials c JOIN mimo.principals p ON p.id=c.agent_principal_id
	 WHERE c.token_hash=$1 AND c.revoked_at IS NULL AND (c.expires_at IS NULL OR c.expires_at>now())`, tokenHash(token)).Scan(&in.CredentialID, &in.Agent, &in.OwnerID, &in.PrincipalID, &in.DefaultWorkspaceID)
		// A credential with no owner or no default workspace is intentionally
		// still authenticated for shared-domain checks, but can never authorize
		// personal-domain access.
		if err == nil {
			_, _ = a.DB.Exec(r.Context(), `UPDATE mimo.agent_credentials SET last_used_at=now() WHERE id=$1::uuid`, in.CredentialID)
			if permission := personalRoutePermission(r); permission != "" && !a.hasBoundPersonalPermission(r.Context(), in, permission) {
				jsonErr(w, http.StatusForbidden, "insufficient personal-data permission")
				return
			}
			next.ServeHTTP(w, r.WithContext(withIdentity(r.Context(), in)))
			return
		}
		// Compatibility path for already deployed v0.2 installations. It is intentionally
		// single-owner only and can be removed after credentials are provisioned.
		if len(token) == len(a.C.APIToken) && subtleCompare(token, a.C.APIToken) {
			next.ServeHTTP(w, r.WithContext(withIdentity(r.Context(), identity{Agent: "legacy", OwnerID: a.C.OwnerID, Legacy: true})))
			return
		}
		if err != pgx.ErrNoRows {
			jsonErr(w, http.StatusUnauthorized, "invalid API token")
			return
		}
		jsonErr(w, http.StatusUnauthorized, "invalid API token")
	})
}

func personalRoutePermission(r *http.Request) string {
	path := r.URL.Path
	if strings.HasPrefix(path, "/v1/finance/") {
		if r.Method == http.MethodGet {
			return "finance:read"
		}
		return "finance:write"
	}
	if strings.HasPrefix(path, "/v1/notes") || strings.HasPrefix(path, "/v1/memories") || strings.HasPrefix(path, "/v1/memory/") || path == "/v1/search" {
		if r.Method == http.MethodGet {
			return "memory:read"
		}
		return "memory:write"
	}
	if strings.HasPrefix(path, "/v1/objects") || strings.HasPrefix(path, "/v1/assets") {
		if r.Method == http.MethodGet {
			return "memory:read"
		}
		return "memory:write"
	}
	return ""
}

func (a *App) hasBoundPersonalPermission(ctx context.Context, in identity, permission string) bool {
	var ok bool
	err := a.DB.QueryRow(ctx, `SELECT EXISTS(
 SELECT 1
 FROM mimo.agent_credentials c
 JOIN mimo.principals agent ON agent.id=c.agent_principal_id
 JOIN mimo.personal_workspace_owners pwo ON pwo.owner_id=c.owner_id AND pwo.workspace_id=c.default_workspace_id
 JOIN mimo.workspaces w ON w.id=pwo.workspace_id AND w.kind='personal'
 JOIN mimo.principals human ON human.id=pwo.human_principal_id AND human.kind='human'
 JOIN mimo.workspace_members hm ON hm.workspace_id=w.id AND hm.principal_id=human.id AND hm.role='owner' AND hm.revoked_at IS NULL
 JOIN mimo.workspace_members am ON am.workspace_id=w.id AND am.principal_id=c.agent_principal_id AND am.revoked_at IS NULL
 JOIN mimo.permissions p ON p.workspace_id=w.id AND p.principal_id=c.agent_principal_id AND p.permission=$2
 WHERE c.id=$1::uuid
   AND nullif(btrim(c.owner_id),'') IS NOT NULL
   AND nullif(c.default_workspace_id::text,'') IS NOT NULL
   AND agent.slug <> 'nara'
   AND c.owner_id=$3
   AND c.default_workspace_id=$4::uuid
)`, in.CredentialID, permission, in.OwnerID, in.DefaultWorkspaceID).Scan(&ok)
	return err == nil && ok
}

func subtleCompare(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var x byte
	for i := range a {
		x |= a[i] ^ b[i]
	}
	return x == 0
}
func (a *App) requirePermission(w http.ResponseWriter, r *http.Request, workspace, permission string) bool {
	in, ok := identityFrom(r.Context())
	if !ok {
		jsonErr(w, 401, "authentication required")
		return false
	}
	if in.Legacy {
		return true
	}
	var allowed bool
	err := a.DB.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM mimo.permissions p JOIN mimo.workspaces w ON w.id=p.workspace_id JOIN mimo.workspace_members m ON m.workspace_id=w.id AND m.principal_id=p.principal_id WHERE p.principal_id=$1::uuid AND w.slug=$2 AND p.permission=$3 AND m.revoked_at IS NULL)`, in.PrincipalID, workspace, permission).Scan(&allowed)
	if err != nil || !allowed {
		jsonErr(w, http.StatusForbidden, "insufficient permission")
		return false
	}
	return true
}
func requestTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, 25*time.Second)
}
