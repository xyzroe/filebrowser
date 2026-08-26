package versioning

import (
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/jellydator/ttlcache/v3"
)

// checkoutTokenTTL bounds how long a single-use checkout token stays valid
// between the POST that acquires the lock and the GET that redeems it.
const checkoutTokenTTL = 60 * time.Second

// CheckoutToken binds a single download to the user and version that a
// checkout (or checkout-verification) authorized, so the following GET does
// not need to repeat the lock/ownership logic and cannot be replayed.
type CheckoutToken struct {
	FileID        string
	UserID        uint
	VersionNumber int // 0 means "current version"
}

// TokenStore issues and redeems single-use, short-lived checkout tokens. It is
// in-memory only, consistent with the fork's single-instance limitation for
// locking/versioning (spec section 9.4).
type TokenStore struct {
	cache *ttlcache.Cache[string, CheckoutToken]
}

func NewTokenStore() *TokenStore {
	cache := ttlcache.New[string, CheckoutToken]()
	go cache.Start()
	return &TokenStore{cache: cache}
}

func (t *TokenStore) Close() {
	t.cache.Stop()
}

// Issue creates a new single-use token for the given checkout.
func (t *TokenStore) Issue(tok CheckoutToken) (string, error) {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	key := hex.EncodeToString(b[:])
	t.cache.Set(key, tok, checkoutTokenTTL)
	return key, nil
}

// Redeem looks up and immediately invalidates a token (single-use). It
// returns ErrInvalidToken if the token does not exist or already expired.
func (t *TokenStore) Redeem(key string) (CheckoutToken, error) {
	item := t.cache.Get(key)
	if item == nil {
		return CheckoutToken{}, ErrInvalidToken
	}
	tok := item.Value()
	t.cache.Delete(key)
	return tok, nil
}
