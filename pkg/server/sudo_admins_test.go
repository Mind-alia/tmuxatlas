package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/LosFurina/tmuxatlas/pkg/accounts"
	"github.com/LosFurina/tmuxatlas/pkg/auth"
)

func TestSudoAdminOnly(t *testing.T) {
	tests := []struct {
		name    string
		admins  SudoAdmins
		account *accounts.User
		cookie  bool
		status  int
	}{
		{"accounts admin", defaultSudoAdmins{accounts: true, enabled: true}, &accounts.User{ID: "1", Username: "anovisclaw", Role: "admin"}, true, http.StatusOK},
		{"accounts user", defaultSudoAdmins{accounts: true, enabled: true}, &accounts.User{ID: "2", Username: "invite", Role: "user"}, true, http.StatusForbidden},
		{"disabled admin", defaultSudoAdmins{accounts: true, enabled: true}, &accounts.User{ID: "3", Role: "admin", Disabled: true}, true, http.StatusForbidden},
		{"accounts without identity", defaultSudoAdmins{accounts: true, enabled: true}, nil, true, http.StatusForbidden},
		{"passkey owner", defaultSudoAdmins{enabled: true}, nil, true, http.StatusOK},
		{"passkey without session", defaultSudoAdmins{enabled: true}, nil, false, http.StatusForbidden},
		{"no authentication", defaultSudoAdmins{}, nil, true, http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/api/sudo/requests", nil)
			if tt.account != nil {
				request = request.WithContext(auth.WithAccount(request.Context(), auth.AccountIdentity{User: *tt.account}))
			}
			if tt.cookie {
				request.AddCookie(&http.Cookie{Name: "tmuxatlas_session", Value: "x"})
			}
			response := httptest.NewRecorder()
			sudoAdminOnly(tt.admins)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			})).ServeHTTP(response, request)
			if response.Code != tt.status {
				t.Fatalf("status = %d, want %d", response.Code, tt.status)
			}
		})
	}
}
