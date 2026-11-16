package accounts

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	RoleAdmin = "admin"
	RoleGuest = "guest"
	// legacyRoleUser is the role of non-admin accounts created before guests
	// existed. Such accounts saw every session; they now get guest access.
	legacyRoleUser = "user"
)

var (
	ErrInvalidRole       = errors.New("rôle invalide")
	ErrHostTaken         = errors.New("cet hôte est déjà attribué à un autre invité")
	ErrHostForbidden     = errors.New("cet hôte ne peut pas être attribué à un invité")
	ErrInvalidHost       = errors.New("identifiant d'hôte invalide")
	ErrInvalidUnixUser   = errors.New("nom de compte Linux invalide")
	ErrInvitationInvalid = errors.New("invitation invalide ou expirée")
)

const (
	DefaultInvitationTTL = 24 * time.Hour
	MaxInvitationTTL     = 7 * 24 * time.Hour
	maxHostsPerGuest     = 16
)

var (
	hostIDPattern   = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	unixUserPattern = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
)

// NormalizeRole maps stored roles to the two current roles. Anything that is
// not exactly "admin" becomes a guest, so an unknown value never grants more.
func NormalizeRole(role string) string {
	if role == RoleAdmin {
		return RoleAdmin
	}
	return RoleGuest
}

// ValidRole accepts the roles an administrator may assign. The legacy "user"
// role is accepted as a synonym of guest.
func ValidRole(role string) (string, error) {
	switch role {
	case RoleAdmin, RoleGuest:
		return role, nil
	case legacyRoleUser:
		return RoleGuest, nil
	}
	return "", ErrInvalidRole
}

func ValidUnixUser(name string) bool { return name == "" || unixUserPattern.MatchString(name) }

// Invitation is a one-time link that lets a person choose their password. Only
// the SHA-256 of the token is stored; the token itself is shown once.
type Invitation struct {
	TokenSHA256 string    `json:"token_sha256"`
	ExpiresAt   time.Time `json:"expires_at"`
	CreatedBy   string    `json:"created_by,omitempty"`
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// CreatePending creates an account without a password. Nobody can sign in to
// it until an invitation for it is accepted.
func (s *Store) CreatePending(username, role, unixUser string) (User, error) {
	if !usernamePattern.MatchString(username) {
		return User{}, ErrInvalidUsername
	}
	role, err := ValidRole(role)
	if err != nil {
		return User{}, err
	}
	if !ValidUnixUser(unixUser) {
		return User{}, ErrInvalidUnixUser
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return User{}, err
	}
	user := User{ID: hex.EncodeToString(id[:]), Username: username, Role: role, UnixUser: unixUser, CreatedAt: time.Now().UTC()}
	err = s.withLock(func(users []User) ([]User, bool, error) {
		for _, existing := range users {
			if strings.EqualFold(existing.Username, username) {
				return nil, false, ErrDuplicateUsername
			}
		}
		return append(users, user), true, nil
	})
	if err != nil {
		return User{}, err
	}
	return user, nil
}

// SetRole changes an account's role. An administrator cannot change their own
// role, and the last enabled administrator cannot be demoted.
func (s *Store) SetRole(id, actor, role string) (User, error) {
	role, err := ValidRole(role)
	if err != nil {
		return User{}, err
	}
	var result User
	err = s.withLock(func(users []User) ([]User, bool, error) {
		for i := range users {
			if users[i].ID != id {
				continue
			}
			if users[i].Role == role {
				result = users[i]
				return nil, false, nil
			}
			if id == actor || (users[i].IsAdmin() && !users[i].Disabled && activeAdmins(users) <= 1) {
				return nil, false, ErrProtectedAdmin
			}
			if role == RoleGuest {
				for _, host := range users[i].Hosts {
					if err := hostFreeFor(users, i, host); err != nil {
						return nil, false, err
					}
				}
			}
			users[i].Role = role
			result = users[i]
			return users, true, nil
		}
		return nil, false, ErrNotFound
	})
	return result, err
}

// hostFreeFor enforces one Linux account per person: a host may be granted to
// at most one guest. Administrators see every host anyway.
func hostFreeFor(users []User, index int, host string) error {
	for j, other := range users {
		if j == index || other.IsAdmin() {
			continue
		}
		for _, granted := range other.Hosts {
			if granted == host {
				return ErrHostTaken
			}
		}
	}
	return nil
}

// SetAccess replaces an account's host grants and Linux account name. allow
// rejects hosts that must never be given to a guest, such as the hub's own.
func (s *Store) SetAccess(id string, hosts []string, unixUser string, allow func(host string) error) (User, error) {
	if !ValidUnixUser(unixUser) {
		return User{}, ErrInvalidUnixUser
	}
	if len(hosts) > maxHostsPerGuest {
		return User{}, ErrInvalidHost
	}
	seen := make(map[string]bool)
	clean := make([]string, 0, len(hosts))
	for _, host := range hosts {
		if !hostIDPattern.MatchString(host) {
			return User{}, ErrInvalidHost
		}
		if allow != nil {
			if err := allow(host); err != nil {
				return User{}, err
			}
		}
		if !seen[host] {
			seen[host] = true
			clean = append(clean, host)
		}
	}
	var result User
	err := s.withLock(func(users []User) ([]User, bool, error) {
		for i := range users {
			if users[i].ID != id {
				continue
			}
			if !users[i].IsAdmin() {
				for _, host := range clean {
					if err := hostFreeFor(users, i, host); err != nil {
						return nil, false, err
					}
				}
			}
			users[i].Hosts = clean
			users[i].UnixUser = unixUser
			result = users[i]
			return users, true, nil
		}
		return nil, false, ErrNotFound
	})
	return result, err
}

// GrantHost adds one host to an account, for example after a pairing code
// created for that account was used.
func (s *Store) GrantHost(id, host string, allow func(host string) error) (User, error) {
	if !hostIDPattern.MatchString(host) {
		return User{}, ErrInvalidHost
	}
	if allow != nil {
		if err := allow(host); err != nil {
			return User{}, err
		}
	}
	var result User
	err := s.withLock(func(users []User) ([]User, bool, error) {
		for i := range users {
			if users[i].ID != id {
				continue
			}
			for _, granted := range users[i].Hosts {
				if granted == host {
					result = users[i]
					return nil, false, nil
				}
			}
			if len(users[i].Hosts) >= maxHostsPerGuest {
				return nil, false, ErrInvalidHost
			}
			if !users[i].IsAdmin() {
				if err := hostFreeFor(users, i, host); err != nil {
					return nil, false, err
				}
			}
			users[i].Hosts = append(append([]string{}, users[i].Hosts...), host)
			result = users[i]
			return users, true, nil
		}
		return nil, false, ErrNotFound
	})
	return result, err
}

// CreateInvitation replaces any pending invitation of the account and returns
// the new token. Accepting it sets the password, so it also serves to reset a
// forgotten one.
func (s *Store) CreateInvitation(id, actor string, ttl time.Duration) (string, Invitation, error) {
	if ttl <= 0 {
		ttl = DefaultInvitationTTL
	}
	if ttl > MaxInvitationTTL {
		ttl = MaxInvitationTTL
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", Invitation{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	invitation := Invitation{TokenSHA256: hashToken(token), ExpiresAt: time.Now().UTC().Add(ttl), CreatedBy: actor}
	err := s.withLock(func(users []User) ([]User, bool, error) {
		for i := range users {
			if users[i].ID == id {
				users[i].Invite = &invitation
				return users, true, nil
			}
		}
		return nil, false, ErrNotFound
	})
	if err != nil {
		return "", Invitation{}, err
	}
	return token, invitation, nil
}

// CancelInvitation removes a pending invitation.
func (s *Store) CancelInvitation(id string) error {
	return s.withLock(func(users []User) ([]User, bool, error) {
		for i := range users {
			if users[i].ID == id {
				if users[i].Invite == nil {
					return nil, false, nil
				}
				users[i].Invite = nil
				return users, true, nil
			}
		}
		return nil, false, ErrNotFound
	})
}

func findInvitation(users []User, token string, now time.Time) int {
	if token == "" || len(token) > 128 {
		return -1
	}
	digest := hashToken(token)
	found := -1
	for i := range users {
		invite := users[i].Invite
		if invite == nil || users[i].Disabled {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(invite.TokenSHA256), []byte(digest)) == 1 && now.Before(invite.ExpiresAt) {
			found = i
		}
	}
	return found
}

// InspectInvitation returns the account an invitation is for, without using it.
func (s *Store) InspectInvitation(token string) (User, error) {
	var result User
	err := s.withLock(func(users []User) ([]User, bool, error) {
		i := findInvitation(users, token, time.Now())
		if i < 0 {
			return nil, false, ErrInvitationInvalid
		}
		result = users[i]
		return nil, false, nil
	})
	return result, err
}

// AcceptInvitation sets the password and consumes the invitation in the same
// locked update, so a token can be used only once.
func (s *Store) AcceptInvitation(token, hash string) (User, error) {
	if _, err := bcrypt.Cost([]byte(hash)); err != nil {
		return User{}, ErrInvalidHash
	}
	var result User
	err := s.withLock(func(users []User) ([]User, bool, error) {
		i := findInvitation(users, token, time.Now())
		if i < 0 {
			return nil, false, ErrInvitationInvalid
		}
		users[i].PasswordHash = hash
		users[i].PasswordChangedAt = time.Now().UTC()
		users[i].Invite = nil
		result = users[i]
		return users, true, nil
	})
	return result, err
}
