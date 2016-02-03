// +build pkcs11

package main

import (
	"testing"

	"github.com/docker/notary/passphrase"
	"github.com/docker/notary/trustmanager/yubikey"
	"github.com/docker/notary/tuf/data"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
)

var _retriever passphrase.Retriever

func init() {
	yubikey.SetYubikeyKeyMode(yubikey.KeymodeNone)

	regRetriver := passphrase.PromptRetriever()
	_retriever := func(k, a string, c bool, n int) (string, bool, error) {
		if k == "Yubikey" {
			return regRetriver(k, a, c, n)
		}
		return testPassphrase, false, nil
	}

	// best effort at removing keys here, so nil is fine
	s, err := yubikey.NewYubiKeyStore(nil, _retriever)
	if err != nil {
		for k := range s.ListKeys() {
			s.RemoveKey(k)
		}
	}

	NewNotaryCommand = func() *cobra.Command {
		commander := &notaryCommander{
			getRetriever: func() passphrase.Retriever { return _retriever },
		}
		return commander.GetCommand()
	}
}

var rootOnHardware = yubikey.YubikeyAccessible

// Per-test set up deletes all keys on the yubikey
func setUp(t *testing.T) {
	//we're just removing keys here, so nil is fine
	s, err := yubikey.NewYubiKeyStore(nil, _retriever)
	assert.NoError(t, err)
	for k := range s.ListKeys() {
		err := s.RemoveKey(k)
		assert.NoError(t, err)
	}
}

// ensures that the root is actually on the yubikey - this makes sure the
// commands are hooked up to interact with the yubikey, rather than right files
// on disk
func verifyRootKeyOnHardware(t *testing.T, rootKeyID string) {
	// do not bother verifying if there is no yubikey available
	if yubikey.YubikeyAccessible() {
		// //we're just getting keys here, so nil is fine
		s, err := yubikey.NewYubiKeyStore(nil, _retriever)
		assert.NoError(t, err)
		privKey, role, err := s.GetKey(rootKeyID)
		assert.NoError(t, err)
		assert.NotNil(t, privKey)
		assert.Equal(t, data.CanonicalRootRole, role)
	}
}
