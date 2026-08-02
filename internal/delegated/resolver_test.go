package delegated

import (
	"context"
	"crypto/rand"
	"errors"
	"testing"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"

	agenttypes "github.com/dydxprotocol/v4-chain/protocol/x/agent/types"
	"github.com/svpchain/svpdt"
)

// fakeAuthQ serves one account's published key.
type fakeAuthQ struct {
	address string
	key     []byte
	err     error
}

func (f *fakeAuthQ) AccountPubKey(_ context.Context, address string) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	if address != f.address {
		return nil, svpdt.ErrSigInvalid
	}
	return f.key, nil
}

// newUserKey returns a user's signer, compressed pubkey, bech32 address and
// DID, with the address derived from the key the way a Cosmos account is.
func newUserKey(t *testing.T) (*svpdt.PrivateKeySigner, []byte, string, string) {
	t.Helper()
	priv := make([]byte, 32)
	if _, err := rand.Read(priv); err != nil {
		t.Fatal(err)
	}
	signer, err := svpdt.NewPrivateKeySigner(priv)
	if err != nil {
		t.Fatal(err)
	}
	pub := signer.PublicKey()
	addr := sdk.AccAddress((&secp256k1.PubKey{Key: pub}).Address())
	return signer, pub, addr.String(), agenttypes.DIDPrefix + addr.String()
}

func TestResolverPrefersTheRegistry(t *testing.T) {
	_, registryKey, addr, did := newUserKey(t)
	otherKey := make([]byte, svpdt.PubKeyLen)

	r := NewGRPCResolver(
		&fakeAgentQ{agentID: did, pubKey: registryKey},
		&fakeAuthQ{address: addr, key: otherKey},
	).For(context.Background())

	keys, err := r.PublicKeys(did)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || string(keys[0]) != string(registryKey) {
		t.Fatalf("expected the registered key, got %x", keys)
	}
}

func TestResolverFallsBackToTheAccountKey(t *testing.T) {
	_, pub, addr, did := newUserKey(t)

	r := NewGRPCResolver(
		&fakeAgentQ{agentID: "did:svp:someoneelse"},
		&fakeAuthQ{address: addr, key: pub},
	).For(context.Background())

	keys, err := r.PublicKeys(did)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || string(keys[0]) != string(pub) {
		t.Fatalf("expected the account key, got %x", keys)
	}
}

func TestResolverWithoutAuthFallbackRefuses(t *testing.T) {
	_, _, _, did := newUserKey(t)

	r := NewGRPCResolver(&fakeAgentQ{agentID: "did:svp:someoneelse"}, nil).
		For(context.Background())

	if _, err := r.PublicKeys(did); !errors.Is(err, svpdt.ErrSigInvalid) {
		t.Fatalf("expected ErrSigInvalid, got %v", err)
	}
}

func TestResolverRefusesFallbackFailures(t *testing.T) {
	_, pub, addr, did := newUserKey(t)

	cases := map[string]AuthKeyQuerier{
		"query error":    &fakeAuthQ{err: errors.New("unavailable")},
		"unknown addr":   &fakeAuthQ{address: "svp1other", key: pub},
		"malformed key":  &fakeAuthQ{address: addr, key: pub[:16]},
		"no account key": &fakeAuthQ{address: addr, key: nil},
	}
	for name, authQ := range cases {
		r := NewGRPCResolver(&fakeAgentQ{agentID: "did:svp:someoneelse"}, authQ).
			For(context.Background())
		if _, err := r.PublicKeys(did); !errors.Is(err, svpdt.ErrSigInvalid) {
			t.Fatalf("%s: expected ErrSigInvalid, got %v", name, err)
		}
	}

	// A name that is not a DID never reaches the account query.
	r := NewGRPCResolver(&fakeAgentQ{}, &fakeAuthQ{address: addr, key: pub}).
		For(context.Background())
	if _, err := r.PublicKeys("not-a-did"); !errors.Is(err, svpdt.ErrSigInvalid) {
		t.Fatalf("expected ErrSigInvalid for a non-DID name, got %v", err)
	}
}

// A user-issued root credential verifies end to end through the fallback: the
// depth-1 issuer is the user's own DID, unregistered, resolved from x/auth.
func TestVerifyChainAcceptsAUserIssuedRoot(t *testing.T) {
	signer, pub, addr, did := newUserKey(t)
	agentID := "did:svp:agentoperator"
	agentKey := make([]byte, svpdt.PubKeyLen)

	root := [32]byte{0x01}
	caveats := svpdt.Caveats{
		Principal:   addr,
		Actions:     svpdt.StringSet{ActionPlaceOrder},
		Subaccounts: svpdt.Uint32Set{0},
		MaxDepth:    1,
		NotBefore:   testNow - 60,
		Expires:     testNow + 300,
	}
	_, encoded, err := svpdt.Issue(signer, svpdt.IssueParams{
		ChainID:   testChainID,
		Root:      root,
		RootEpoch: 1,
		Issuer:    did,
		Audience:  agentID,
		Caveats:   caveats,
		Nonce:     [16]byte{0x42},
	})
	if err != nil {
		t.Fatal(err)
	}

	resolver := NewGRPCResolver(
		&fakeAgentQ{agentID: agentID, pubKey: agentKey},
		&fakeAuthQ{address: addr, key: pub},
	).For(context.Background())

	verified, err := svpdt.VerifyChain([][]byte{encoded}, resolver, svpdt.VerifyOpts{
		ChainID:  testChainID,
		Now:      testNow,
		MaxDepth: 4,
		Audience: agentID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if verified.Principal != addr {
		t.Fatalf("expected principal %s, got %s", addr, verified.Principal)
	}
}
