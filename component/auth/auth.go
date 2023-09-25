package auth

import (
	"github.com/puzpuzpuz/xsync/v2"
)

type Authenticator interface {
	Verify(user string, pass string) bool
	Users() []string
}

type AuthUser struct {
	User string
	Pass string
}

type inMemoryAuthenticator struct {
	storage   *xsync.MapOf[string, string]
	usernames []string
}

func (au *inMemoryAuthenticator) Verify(user string, pass string) bool {
	realPass, ok := au.storage.Load(user)
	return ok && realPass == pass
}

func (au *inMemoryAuthenticator) Users() []string { return au.usernames }

func NewAuthenticator(users []AuthUser) Authenticator {
	if len(users) == 0 {
		return nil
	}

	au := &inMemoryAuthenticator{storage: xsync.NewMapOf[string]()}
	for _, user := range users {
		au.storage.Store(user.User, user.Pass)
	}
	usernames := make([]string, 0, len(users))
	au.storage.Range(func(key string, value string) bool {
		usernames = append(usernames, key)
		return true
	})
	au.usernames = usernames

	return au
}
