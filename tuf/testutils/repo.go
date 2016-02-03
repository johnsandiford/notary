package testutils

import (
	"math/rand"
	"sort"
	"time"

	"github.com/docker/go/canonical/json"
	"github.com/docker/notary/cryptoservice"
	"github.com/docker/notary/passphrase"
	"github.com/docker/notary/trustmanager"
	"github.com/docker/notary/tuf/data"
	"github.com/docker/notary/tuf/utils"
	fuzz "github.com/google/gofuzz"

	tuf "github.com/docker/notary/tuf"
	"github.com/docker/notary/tuf/keys"
	"github.com/docker/notary/tuf/signed"
)

func createKey(cs signed.CryptoService, gun, role string) (data.PublicKey, error) {
	key, err := cs.Create(role, data.ECDSAKey)
	if err != nil {
		return nil, err
	}
	if role == data.CanonicalRootRole {
		start := time.Now().AddDate(0, 0, -1)
		privKey, _, err := cs.GetPrivateKey(key.ID())
		if err != nil {
			return nil, err
		}
		cert, err := cryptoservice.GenerateCertificate(
			privKey, gun, start, start.AddDate(1, 0, 0),
		)
		if err != nil {
			return nil, err
		}
		key = data.NewECDSAx509PublicKey(trustmanager.CertToPEM(cert))
	}
	return key, nil
}

// EmptyRepo creates an in memory key database, crypto service
// and initializes a repo with no targets.  Delegations are only created
// if delegation roles are passed in.
func EmptyRepo(gun string, delegationRoles ...string) (*keys.KeyDB, *tuf.Repo, signed.CryptoService, error) {
	cs := cryptoservice.NewCryptoService(
		gun, trustmanager.NewKeyMemoryStore(passphrase.ConstantRetriever("")))
	kdb := keys.NewDB()
	r := tuf.NewRepo(kdb, cs)

	for _, role := range data.BaseRoles {
		key, err := createKey(cs, gun, role)
		if err != nil {
			return nil, nil, nil, err
		}
		role, _ := data.NewRole(role, 1, []string{key.ID()}, nil, nil)
		kdb.AddKey(key)
		kdb.AddRole(role)
	}

	r.InitRepo(false)

	// sort the delegation roles so that we make sure to create the parents
	// first
	sort.Strings(delegationRoles)
	for _, delgName := range delegationRoles {
		// create a delegations key and a delegation in the tuf repo
		delgKey, err := createKey(cs, gun, delgName)
		if err != nil {
			return nil, nil, nil, err
		}
		role, err := data.NewRole(delgName, 1, []string{}, []string{""}, []string{})
		if err != nil {
			return nil, nil, nil, err
		}
		if err := r.UpdateDelegations(role, []data.PublicKey{delgKey}); err != nil {
			return nil, nil, nil, err
		}
	}

	return kdb, r, cs, nil
}

// NewRepoMetadata creates a TUF repo and returns the metadata
func NewRepoMetadata(gun string, delegationRoles ...string) (map[string][]byte, signed.CryptoService, error) {
	_, tufRepo, cs, err := EmptyRepo(gun, delegationRoles...)
	if err != nil {
		return nil, nil, err
	}

	meta := make(map[string][]byte)

	for _, delgName := range delegationRoles {
		// is there metadata yet?  if empty, it may not be created
		if _, ok := tufRepo.Targets[delgName]; ok {
			signedThing, err := tufRepo.SignTargets(delgName, data.DefaultExpires("targets"))
			if err != nil {
				return nil, nil, err
			}
			metaBytes, err := json.MarshalCanonical(signedThing)
			if err != nil {
				return nil, nil, err
			}

			meta[delgName] = metaBytes
		}
	}

	// these need to be generated after the delegations are created and signed so
	// the snapshot will have the delegation metadata
	rs, tgs, ss, ts, err := Sign(tufRepo)
	if err != nil {
		return nil, nil, err
	}

	rf, tgf, sf, tf, err := Serialize(rs, tgs, ss, ts)
	if err != nil {
		return nil, nil, err
	}

	meta[data.CanonicalRootRole] = rf
	meta[data.CanonicalSnapshotRole] = sf
	meta[data.CanonicalTargetsRole] = tgf
	meta[data.CanonicalTimestampRole] = tf

	return meta, cs, nil
}

// CopyRepoMetadata makes a copy of a metadata->bytes mapping
func CopyRepoMetadata(from map[string][]byte) map[string][]byte {
	copied := make(map[string][]byte)
	for roleName, metaBytes := range from {
		copied[roleName] = metaBytes
	}
	return copied
}

// AddTarget generates a fake target and adds it to a repo.
func AddTarget(role string, r *tuf.Repo) (name string, meta data.FileMeta, content []byte, err error) {
	randness := fuzz.Continue{}
	content = RandomByteSlice(1024)
	name = randness.RandString()
	t := data.FileMeta{
		Length: int64(len(content)),
		Hashes: data.Hashes{
			"sha256": utils.DoHash("sha256", content),
			"sha512": utils.DoHash("sha512", content),
		},
	}
	files := data.Files{name: t}
	_, err = r.AddTargets(role, files)
	return
}

// RandomByteSlice generates some random data to be used for testing only
func RandomByteSlice(maxSize int) []byte {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	contentSize := r.Intn(maxSize)
	content := make([]byte, contentSize)
	for i := range content {
		content[i] = byte(r.Int63() & 0xff)
	}
	return content
}

// Sign signs all top level roles in a repo in the appropriate order
func Sign(repo *tuf.Repo) (root, targets, snapshot, timestamp *data.Signed, err error) {
	root, err = repo.SignRoot(data.DefaultExpires("root"))
	if err != nil {
		return nil, nil, nil, nil, err
	}
	targets, err = repo.SignTargets("targets", data.DefaultExpires("targets"))
	if err != nil {
		return nil, nil, nil, nil, err
	}
	snapshot, err = repo.SignSnapshot(data.DefaultExpires("snapshot"))
	if err != nil {
		return nil, nil, nil, nil, err
	}
	timestamp, err = repo.SignTimestamp(data.DefaultExpires("timestamp"))
	if err != nil {
		return nil, nil, nil, nil, err
	}
	return
}

// Serialize takes the Signed objects for the 4 top level roles and serializes them all to JSON
func Serialize(sRoot, sTargets, sSnapshot, sTimestamp *data.Signed) (root, targets, snapshot, timestamp []byte, err error) {
	root, err = json.Marshal(sRoot)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	targets, err = json.Marshal(sTargets)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	snapshot, err = json.Marshal(sSnapshot)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	timestamp, err = json.Marshal(sTimestamp)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	return
}
