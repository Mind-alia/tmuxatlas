package server

import "github.com/sirupsen/logrus"

// pushOwnerAccount binds subscriptions made outside the accounts mode, where
// the single signed-in owner is the administrator.
const pushOwnerAccount = "owner"

// pushAudience tells the push sender, at send time, what each account may
// receive: ops and sudo go to active administrators only, tool events to the
// active accounts that can see the host.
type pushAudience struct {
	policy *accountPolicy
}

func (a pushAudience) IsAdmin(accountID string) bool {
	user, ok := a.policy.currentUser(accountID)
	return ok && user.IsAdmin()
}

func (a pushAudience) CanSeeHost(accountID, host string) bool {
	user, ok := a.policy.currentUser(accountID)
	return ok && a.policy.guestMayUseHost(user, host)
}

// purgePush forgets the subscriptions of an account that was demoted,
// disabled or deleted. The audience already filters at send time: this only
// keeps the store from holding endpoints nobody may use any more.
func purgePush(opts *Options, accountID string) {
	if opts.PushStore == nil || accountID == "" {
		return
	}
	if err := opts.PushStore.RemoveAccount(accountID); err != nil {
		logrus.WithError(err).WithField("account", accountID).Warn("could not purge push subscriptions")
	}
}
