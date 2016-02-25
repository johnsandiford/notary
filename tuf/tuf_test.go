package tuf

import (
	"crypto/sha256"
	"encoding/json"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"
	"testing"

	"github.com/docker/notary/tuf/data"
	"github.com/docker/notary/tuf/signed"
	"github.com/stretchr/testify/assert"
)

func initRepo(t *testing.T, cryptoService signed.CryptoService) *Repo {
	rootKey, err := cryptoService.Create("root", data.ED25519Key)
	assert.NoError(t, err)
	targetsKey, err := cryptoService.Create("targets", data.ED25519Key)
	assert.NoError(t, err)
	snapshotKey, err := cryptoService.Create("snapshot", data.ED25519Key)
	assert.NoError(t, err)
	timestampKey, err := cryptoService.Create("timestamp", data.ED25519Key)
	assert.NoError(t, err)

	rootRole := data.NewBaseRole(
		data.CanonicalRootRole,
		1,
		rootKey,
	)
	targetsRole := data.NewBaseRole(
		data.CanonicalTargetsRole,
		1,
		targetsKey,
	)
	snapshotRole := data.NewBaseRole(
		data.CanonicalSnapshotRole,
		1,
		snapshotKey,
	)
	timestampRole := data.NewBaseRole(
		data.CanonicalTimestampRole,
		1,
		timestampKey,
	)

	repo := NewRepo(cryptoService)
	err = repo.InitRoot(rootRole, timestampRole, snapshotRole, targetsRole, false)
	assert.NoError(t, err)
	_, err = repo.InitTargets(data.CanonicalTargetsRole)
	assert.NoError(t, err)
	err = repo.InitSnapshot()
	assert.NoError(t, err)
	err = repo.InitTimestamp()
	assert.NoError(t, err)
	return repo
}

func TestInitSnapshotNoTargets(t *testing.T) {
	cs := signed.NewEd25519()
	repo := initRepo(t, cs)

	repo.Targets = make(map[string]*data.SignedTargets)

	err := repo.InitSnapshot()
	assert.Error(t, err)
	assert.IsType(t, ErrNotLoaded{}, err)
}

func writeRepo(t *testing.T, dir string, repo *Repo) {
	err := os.MkdirAll(dir, 0755)
	assert.NoError(t, err)
	signedRoot, err := repo.SignRoot(data.DefaultExpires("root"))
	assert.NoError(t, err)
	rootJSON, _ := json.Marshal(signedRoot)
	ioutil.WriteFile(dir+"/root.json", rootJSON, 0755)

	for r := range repo.Targets {
		signedTargets, err := repo.SignTargets(r, data.DefaultExpires("targets"))
		assert.NoError(t, err)
		targetsJSON, _ := json.Marshal(signedTargets)
		p := path.Join(dir, r+".json")
		parentDir := filepath.Dir(p)
		os.MkdirAll(parentDir, 0755)
		ioutil.WriteFile(p, targetsJSON, 0755)
	}

	signedSnapshot, err := repo.SignSnapshot(data.DefaultExpires("snapshot"))
	assert.NoError(t, err)
	snapshotJSON, _ := json.Marshal(signedSnapshot)
	ioutil.WriteFile(dir+"/snapshot.json", snapshotJSON, 0755)

	signedTimestamp, err := repo.SignTimestamp(data.DefaultExpires("timestamp"))
	assert.NoError(t, err)
	timestampJSON, _ := json.Marshal(signedTimestamp)
	ioutil.WriteFile(dir+"/timestamp.json", timestampJSON, 0755)
}

func TestInitRepo(t *testing.T) {
	ed25519 := signed.NewEd25519()
	repo := initRepo(t, ed25519)
	writeRepo(t, "/tmp/tufrepo", repo)
}

func TestUpdateDelegations(t *testing.T) {
	ed25519 := signed.NewEd25519()
	repo := initRepo(t, ed25519)

	testKey, err := ed25519.Create("targets/test", data.ED25519Key)
	assert.NoError(t, err)
	err = repo.UpdateDelegationKeys("targets/test", []data.PublicKey{testKey}, []string{}, 1)
	assert.NoError(t, err)
	err = repo.UpdateDelegationPaths("targets/test", []string{"test"}, []string{}, false)
	assert.NoError(t, err)

	// no empty metadata is created for this role
	_, ok := repo.Targets["targets/test"]
	assert.False(t, ok, "no empty targets file should be created for deepest delegation")

	r, ok := repo.Targets[data.CanonicalTargetsRole]
	assert.True(t, ok)
	assert.Len(t, r.Signed.Delegations.Roles, 1)
	assert.Len(t, r.Signed.Delegations.Keys, 1)
	keyIDs := r.Signed.Delegations.Roles[0].KeyIDs
	assert.Len(t, keyIDs, 1)
	assert.Equal(t, testKey.ID(), keyIDs[0])

	testDeepKey, err := ed25519.Create("targets/test/deep", data.ED25519Key)
	assert.NoError(t, err)
	err = repo.UpdateDelegationKeys("targets/test/deep", []data.PublicKey{testDeepKey}, []string{}, 1)
	assert.NoError(t, err)
	err = repo.UpdateDelegationPaths("targets/test/deep", []string{"test/deep"}, []string{}, false)
	assert.NoError(t, err)

	// this metadata didn't exist before, but creating targets/test/deep created
	// the targets/test metadata
	r, ok = repo.Targets["targets/test"]
	assert.True(t, ok)
	assert.Len(t, r.Signed.Delegations.Roles, 1)
	assert.Len(t, r.Signed.Delegations.Keys, 1)
	keyIDs = r.Signed.Delegations.Roles[0].KeyIDs
	assert.Len(t, keyIDs, 1)
	assert.Equal(t, testDeepKey.ID(), keyIDs[0])
	assert.True(t, r.Dirty)

	// no empty delegation metadata is created for targets/test/deep
	_, ok = repo.Targets["targets/test/deep"]
	assert.False(t, ok, "no empty targets file should be created for deepest delegation")
}

func TestUpdateDelegationsParentMissing(t *testing.T) {
	ed25519 := signed.NewEd25519()
	repo := initRepo(t, ed25519)

	testDeepKey, err := ed25519.Create("targets/test/deep", data.ED25519Key)
	err = repo.UpdateDelegationKeys("targets/test/deep", []data.PublicKey{testDeepKey}, []string{}, 1)
	assert.Error(t, err)
	assert.IsType(t, data.ErrInvalidRole{}, err)

	r, ok := repo.Targets[data.CanonicalTargetsRole]
	assert.True(t, ok)
	assert.Len(t, r.Signed.Delegations.Roles, 0)

	// no delegation metadata created for non-existent parent
	_, ok = repo.Targets["targets/test"]
	assert.False(t, ok, "no targets file should be created for nonexistent parent delegation")
}

// Updating delegations needs to modify the parent of the role being updated.
// If there is no signing key for that parent, the delegation cannot be added.
func TestUpdateDelegationsMissingParentKey(t *testing.T) {
	ed25519 := signed.NewEd25519()
	repo := initRepo(t, ed25519)

	// remove the target key (all keys)
	repo.cryptoService = signed.NewEd25519()

	roleKey, err := ed25519.Create("Invalid Role", data.ED25519Key)
	assert.NoError(t, err)

	err = repo.UpdateDelegationKeys("targets/role", []data.PublicKey{roleKey}, []string{}, 1)
	assert.Error(t, err)
	assert.IsType(t, signed.ErrNoKeys{}, err)

	// no empty delegation metadata created for new delegation
	_, ok := repo.Targets["targets/role"]
	assert.False(t, ok, "no targets file should be created for empty delegation")
}

func TestUpdateDelegationsInvalidRole(t *testing.T) {
	ed25519 := signed.NewEd25519()
	repo := initRepo(t, ed25519)

	roleKey, err := ed25519.Create("Invalid Role", data.ED25519Key)
	assert.NoError(t, err)

	err = repo.UpdateDelegationKeys("root", []data.PublicKey{roleKey}, []string{}, 1)
	assert.Error(t, err)
	assert.IsType(t, data.ErrInvalidRole{}, err)

	r, ok := repo.Targets[data.CanonicalTargetsRole]
	assert.True(t, ok)
	assert.Len(t, r.Signed.Delegations.Roles, 0)

	// no delegation metadata created for invalid delegation
	_, ok = repo.Targets["root"]
	assert.False(t, ok, "no targets file should be created since delegation failed")
}

// A delegation can be created with a role that is missing a signing key, so
// long as UpdateDelegations is called with the key
func TestUpdateDelegationsRoleThatIsMissingDelegationKey(t *testing.T) {
	ed25519 := signed.NewEd25519()
	repo := initRepo(t, ed25519)

	roleKey, err := ed25519.Create("Invalid Role", data.ED25519Key)
	assert.NoError(t, err)

	// key should get added to role as part of updating the delegation
	err = repo.UpdateDelegationKeys("targets/role", []data.PublicKey{roleKey}, []string{}, 1)
	assert.NoError(t, err)
	err = repo.UpdateDelegationPaths("targets/role", []string{""}, []string{}, false)
	assert.NoError(t, err)

	r, ok := repo.Targets[data.CanonicalTargetsRole]
	assert.True(t, ok)
	assert.Len(t, r.Signed.Delegations.Roles, 1)
	assert.Len(t, r.Signed.Delegations.Keys, 1)
	keyIDs := r.Signed.Delegations.Roles[0].KeyIDs
	assert.Len(t, keyIDs, 1)
	assert.Equal(t, roleKey.ID(), keyIDs[0])
	assert.True(t, r.Dirty)

	// no empty delegation metadata created for new delegation
	_, ok = repo.Targets["targets/role"]
	assert.False(t, ok, "no targets file should be created for empty delegation")
}

func TestUpdateDelegationsNotEnoughKeys(t *testing.T) {
	ed25519 := signed.NewEd25519()
	repo := initRepo(t, ed25519)

	roleKey, err := ed25519.Create("Invalid Role", data.ED25519Key)
	assert.NoError(t, err)

	err = repo.UpdateDelegationKeys("targets/role", []data.PublicKey{roleKey}, []string{}, 2)
	assert.Error(t, err)
	assert.IsType(t, data.ErrInvalidRole{}, err)

	// no delegation metadata created for failed delegation
	_, ok := repo.Targets["targets/role"]
	assert.False(t, ok, "no targets file should be created since delegation failed")
}

func TestUpdateDelegationsAddKeyToRole(t *testing.T) {
	ed25519 := signed.NewEd25519()
	repo := initRepo(t, ed25519)

	testKey, err := ed25519.Create("targets/test", data.ED25519Key)
	assert.NoError(t, err)
	err = repo.UpdateDelegationKeys("targets/test", []data.PublicKey{testKey}, []string{}, 1)
	assert.NoError(t, err)
	err = repo.UpdateDelegationPaths("targets/test", []string{"test"}, []string{}, false)
	assert.NoError(t, err)

	r, ok := repo.Targets[data.CanonicalTargetsRole]
	assert.True(t, ok)
	assert.Len(t, r.Signed.Delegations.Roles, 1)
	assert.Len(t, r.Signed.Delegations.Keys, 1)
	keyIDs := r.Signed.Delegations.Roles[0].KeyIDs
	assert.Len(t, keyIDs, 1)
	assert.Equal(t, testKey.ID(), keyIDs[0])

	testKey2, err := ed25519.Create("targets/test", data.ED25519Key)
	assert.NoError(t, err)

	err = repo.UpdateDelegationKeys("targets/test", []data.PublicKey{testKey2}, []string{}, 1)
	assert.NoError(t, err)

	r, ok = repo.Targets["targets"]
	assert.True(t, ok)
	assert.Len(t, r.Signed.Delegations.Roles, 1)
	assert.Len(t, r.Signed.Delegations.Keys, 2)
	keyIDs = r.Signed.Delegations.Roles[0].KeyIDs
	assert.Len(t, keyIDs, 2)
	// it does an append so the order is deterministic (but not meaningful to TUF)
	assert.Equal(t, testKey.ID(), keyIDs[0])
	assert.Equal(t, testKey2.ID(), keyIDs[1])
	assert.True(t, r.Dirty)
}

func TestDeleteDelegations(t *testing.T) {
	ed25519 := signed.NewEd25519()
	repo := initRepo(t, ed25519)

	testKey, err := ed25519.Create("targets/test", data.ED25519Key)
	assert.NoError(t, err)
	err = repo.UpdateDelegationKeys("targets/test", []data.PublicKey{testKey}, []string{}, 1)
	assert.NoError(t, err)
	err = repo.UpdateDelegationPaths("targets/test", []string{"test"}, []string{}, false)
	assert.NoError(t, err)

	r, ok := repo.Targets[data.CanonicalTargetsRole]
	assert.True(t, ok)
	assert.Len(t, r.Signed.Delegations.Roles, 1)
	assert.Len(t, r.Signed.Delegations.Keys, 1)
	keyIDs := r.Signed.Delegations.Roles[0].KeyIDs
	assert.Len(t, keyIDs, 1)
	assert.Equal(t, testKey.ID(), keyIDs[0])

	// ensure that the metadata is there and snapshot is there
	targets, err := repo.InitTargets("targets/test")
	assert.NoError(t, err)
	targetsSigned, err := targets.ToSigned()
	assert.NoError(t, err)
	assert.NoError(t, repo.UpdateSnapshot("targets/test", targetsSigned))
	_, ok = repo.Snapshot.Signed.Meta["targets/test"]
	assert.True(t, ok)

	assert.NoError(t, repo.DeleteDelegation("targets/test"))
	assert.Len(t, r.Signed.Delegations.Roles, 0)
	assert.Len(t, r.Signed.Delegations.Keys, 0)
	assert.True(t, r.Dirty)

	// metadata should be deleted
	_, ok = repo.Targets["targets/test"]
	assert.False(t, ok)
	_, ok = repo.Snapshot.Signed.Meta["targets/test"]
	assert.False(t, ok)
}

func TestDeleteDelegationsRoleNotExistBecauseNoParentMeta(t *testing.T) {
	ed25519 := signed.NewEd25519()
	repo := initRepo(t, ed25519)

	testKey, err := ed25519.Create("targets/test", data.ED25519Key)
	assert.NoError(t, err)

	err = repo.UpdateDelegationKeys("targets/test", []data.PublicKey{testKey}, []string{}, 1)
	assert.NoError(t, err)
	err = repo.UpdateDelegationPaths("targets/test", []string{"test"}, []string{}, false)
	assert.NoError(t, err)

	// no empty delegation metadata created for new delegation
	_, ok := repo.Targets["targets/test"]
	assert.False(t, ok, "no targets file should be created for empty delegation")

	delRole, err := data.NewRole("targets/test/a", 1, []string{testKey.ID()}, []string{"test"})

	err = repo.DeleteDelegation(delRole.Name)
	assert.NoError(t, err)
	// still no metadata
	_, ok = repo.Targets["targets/test"]
	assert.False(t, ok)
}

func TestDeleteDelegationsRoleNotExist(t *testing.T) {
	ed25519 := signed.NewEd25519()
	repo := initRepo(t, ed25519)

	// initRepo leaves all the roles as Dirty. Set to false
	// to test removing a non-existent role doesn't mark
	// a role as dirty
	repo.Targets[data.CanonicalTargetsRole].Dirty = false

	role, err := data.NewRole("targets/test", 1, []string{}, []string{""})
	assert.NoError(t, err)

	err = repo.DeleteDelegation(role.Name)
	assert.NoError(t, err)
	r, ok := repo.Targets[data.CanonicalTargetsRole]
	assert.True(t, ok)
	assert.Len(t, r.Signed.Delegations.Roles, 0)
	assert.Len(t, r.Signed.Delegations.Keys, 0)
	assert.False(t, r.Dirty)
}

func TestDeleteDelegationsInvalidRole(t *testing.T) {
	ed25519 := signed.NewEd25519()
	repo := initRepo(t, ed25519)

	// data.NewRole errors if the role isn't a valid TUF role so use one of the non-delegation
	// valid roles
	invalidRole, err := data.NewRole("root", 1, []string{}, []string{""})
	assert.NoError(t, err)

	err = repo.DeleteDelegation(invalidRole.Name)
	assert.Error(t, err)
	assert.IsType(t, data.ErrInvalidRole{}, err)

	r, ok := repo.Targets[data.CanonicalTargetsRole]
	assert.True(t, ok)
	assert.Len(t, r.Signed.Delegations.Roles, 0)
}

func TestDeleteDelegationsParentMissing(t *testing.T) {
	ed25519 := signed.NewEd25519()
	repo := initRepo(t, ed25519)

	testRole, err := data.NewRole("targets/test/deep", 1, []string{}, []string{""})
	assert.NoError(t, err)

	err = repo.DeleteDelegation(testRole.Name)
	assert.Error(t, err)
	assert.IsType(t, data.ErrInvalidRole{}, err)

	r, ok := repo.Targets[data.CanonicalTargetsRole]
	assert.True(t, ok)
	assert.Len(t, r.Signed.Delegations.Roles, 0)
}

// Can't delete a delegation if we don't have the parent's signing key
func TestDeleteDelegationsMissingParentSigningKey(t *testing.T) {
	ed25519 := signed.NewEd25519()
	repo := initRepo(t, ed25519)

	testKey, err := ed25519.Create("targets/test", data.ED25519Key)
	assert.NoError(t, err)
	err = repo.UpdateDelegationKeys("targets/test", []data.PublicKey{testKey}, []string{}, 1)
	assert.NoError(t, err)
	err = repo.UpdateDelegationPaths("targets/test", []string{"test"}, []string{}, false)
	assert.NoError(t, err)

	r, ok := repo.Targets[data.CanonicalTargetsRole]
	assert.True(t, ok)
	assert.Len(t, r.Signed.Delegations.Roles, 1)
	assert.Len(t, r.Signed.Delegations.Keys, 1)
	keyIDs := r.Signed.Delegations.Roles[0].KeyIDs
	assert.Len(t, keyIDs, 1)
	assert.Equal(t, testKey.ID(), keyIDs[0])

	// ensure that the metadata is there and snapshot is there
	targets, err := repo.InitTargets("targets/test")
	assert.NoError(t, err)
	targetsSigned, err := targets.ToSigned()
	assert.NoError(t, err)
	assert.NoError(t, repo.UpdateSnapshot("targets/test", targetsSigned))
	_, ok = repo.Snapshot.Signed.Meta["targets/test"]
	assert.True(t, ok)

	// delete all signing keys
	repo.cryptoService = signed.NewEd25519()
	err = repo.DeleteDelegation("targets/test")
	assert.Error(t, err)
	assert.IsType(t, signed.ErrNoKeys{}, err)

	assert.Len(t, r.Signed.Delegations.Roles, 1)
	assert.Len(t, r.Signed.Delegations.Keys, 1)
	assert.True(t, r.Dirty)

	// metadata should be here still
	_, ok = repo.Targets["targets/test"]
	assert.True(t, ok)
	_, ok = repo.Snapshot.Signed.Meta["targets/test"]
	assert.True(t, ok)
}

func TestDeleteDelegationsMidSliceRole(t *testing.T) {
	ed25519 := signed.NewEd25519()
	repo := initRepo(t, ed25519)

	testKey, err := ed25519.Create("targets/test", data.ED25519Key)
	assert.NoError(t, err)
	err = repo.UpdateDelegationKeys("targets/test", []data.PublicKey{testKey}, []string{}, 1)
	assert.NoError(t, err)
	err = repo.UpdateDelegationPaths("targets/test", []string{""}, []string{}, false)
	assert.NoError(t, err)

	err = repo.UpdateDelegationKeys("targets/test2", []data.PublicKey{testKey}, []string{}, 1)
	assert.NoError(t, err)
	err = repo.UpdateDelegationPaths("targets/test2", []string{""}, []string{}, false)
	assert.NoError(t, err)

	err = repo.UpdateDelegationKeys("targets/test3", []data.PublicKey{testKey}, []string{}, 1)
	assert.NoError(t, err)
	err = repo.UpdateDelegationPaths("targets/test3", []string{"test"}, []string{}, false)
	assert.NoError(t, err)

	err = repo.DeleteDelegation("targets/test2")
	assert.NoError(t, err)

	r, ok := repo.Targets[data.CanonicalTargetsRole]
	assert.True(t, ok)
	assert.Len(t, r.Signed.Delegations.Roles, 2)
	assert.Len(t, r.Signed.Delegations.Keys, 1)
	assert.True(t, r.Dirty)
}

// If the parent exists, the metadata exists, and the delegation is in it,
// returns the role that was found
func TestGetDelegationRoleAndMetadataExistDelegationExists(t *testing.T) {
	ed25519 := signed.NewEd25519()
	repo := initRepo(t, ed25519)

	testKey, err := ed25519.Create("meh", data.ED25519Key)
	assert.NoError(t, err)

	err = repo.UpdateDelegationKeys("targets/level1", []data.PublicKey{testKey}, []string{}, 1)
	assert.NoError(t, err)
	err = repo.UpdateDelegationPaths("targets/level1", []string{""}, []string{}, false)
	assert.NoError(t, err)

	err = repo.UpdateDelegationKeys("targets/level1/level2", []data.PublicKey{testKey}, []string{}, 1)
	assert.NoError(t, err)
	err = repo.UpdateDelegationPaths("targets/level1/level2", []string{""}, []string{}, false)
	assert.NoError(t, err)

	gottenRole, err := repo.GetDelegationRole("targets/level1/level2")
	assert.NoError(t, err)
	assert.Equal(t, "targets/level1/level2", gottenRole.Name)
	assert.Equal(t, 1, gottenRole.Threshold)
	assert.Equal(t, []string{""}, gottenRole.Paths)
	_, ok := gottenRole.Keys[testKey.ID()]
	assert.True(t, ok)
}

// If the parent exists, the metadata exists, and the delegation isn't in it,
// returns an ErrNoSuchRole
func TestGetDelegationRoleAndMetadataExistDelegationDoesntExists(t *testing.T) {
	ed25519 := signed.NewEd25519()
	repo := initRepo(t, ed25519)

	testKey, err := ed25519.Create("meh", data.ED25519Key)
	assert.NoError(t, err)

	err = repo.UpdateDelegationKeys("targets/level1", []data.PublicKey{testKey}, []string{}, 1)
	assert.NoError(t, err)
	err = repo.UpdateDelegationPaths("targets/level1", []string{""}, []string{}, false)
	assert.NoError(t, err)

	// ensure metadata exists
	repo.InitTargets("targets/level1")

	_, err = repo.GetDelegationRole("targets/level1/level2")
	assert.Error(t, err)
	assert.IsType(t, data.ErrInvalidRole{}, err)
}

// If the parent exists but the metadata doesn't exist, returns an ErrNoSuchRole
func TestGetDelegationRoleAndMetadataDoesntExists(t *testing.T) {
	ed25519 := signed.NewEd25519()
	repo := initRepo(t, ed25519)

	testKey, err := ed25519.Create("meh", data.ED25519Key)
	assert.NoError(t, err)

	err = repo.UpdateDelegationKeys("targets/level1", []data.PublicKey{testKey}, []string{}, 1)
	assert.NoError(t, err)
	err = repo.UpdateDelegationPaths("targets/level1", []string{""}, []string{}, false)
	assert.NoError(t, err)

	// no empty delegation metadata created for new delegation
	_, ok := repo.Targets["targets/test"]
	assert.False(t, ok, "no targets file should be created for empty delegation")

	_, err = repo.GetDelegationRole("targets/level1/level2")
	assert.Error(t, err)
	assert.IsType(t, data.ErrInvalidRole{}, err)
}

// If the parent role doesn't exist, GetDelegation fails with an ErrInvalidRole
func TestGetDelegationParentMissing(t *testing.T) {
	ed25519 := signed.NewEd25519()
	repo := initRepo(t, ed25519)

	_, err := repo.GetDelegationRole("targets/level1/level2")
	assert.Error(t, err)
	assert.IsType(t, data.ErrInvalidRole{}, err)
}

// Adding targets to a role that exists and has metadata (like targets)
// correctly adds the target
func TestAddTargetsRoleAndMetadataExist(t *testing.T) {
	ed25519 := signed.NewEd25519()
	repo := initRepo(t, ed25519)

	hash := sha256.Sum256([]byte{})
	f := data.FileMeta{
		Length: 1,
		Hashes: map[string][]byte{
			"sha256": hash[:],
		},
	}

	_, err := repo.AddTargets(data.CanonicalTargetsRole, data.Files{"f": f})
	assert.NoError(t, err)

	r, ok := repo.Targets[data.CanonicalTargetsRole]
	assert.True(t, ok)
	targetsF, ok := r.Signed.Targets["f"]
	assert.True(t, ok)
	assert.Equal(t, f, targetsF)
}

// Adding targets to a role that exists and has not metadata first creates the
// metadata and then correctly adds the target
func TestAddTargetsRoleExistsAndMetadataDoesntExist(t *testing.T) {
	hash := sha256.Sum256([]byte{})
	f := data.FileMeta{
		Length: 1,
		Hashes: map[string][]byte{
			"sha256": hash[:],
		},
	}

	ed25519 := signed.NewEd25519()
	repo := initRepo(t, ed25519)

	testKey, err := ed25519.Create("targets/test", data.ED25519Key)
	assert.NoError(t, err)
	err = repo.UpdateDelegationKeys("targets/test", []data.PublicKey{testKey}, []string{}, 1)
	assert.NoError(t, err)
	err = repo.UpdateDelegationPaths("targets/test", []string{""}, []string{}, false)
	assert.NoError(t, err)

	// no empty metadata is created for this role
	_, ok := repo.Targets["targets/test"]
	assert.False(t, ok, "no empty targets file should be created")

	// adding the targets to the role should create the metadata though
	_, err = repo.AddTargets("targets/test", data.Files{"f": f})
	assert.NoError(t, err)

	r, ok := repo.Targets["targets/test"]
	assert.True(t, ok)
	targetsF, ok := r.Signed.Targets["f"]
	assert.True(t, ok)
	assert.Equal(t, f, targetsF)
}

// Adding targets to a role that doesn't exist fails
func TestAddTargetsRoleDoesntExist(t *testing.T) {
	hash := sha256.Sum256([]byte{})
	f := data.FileMeta{
		Length: 1,
		Hashes: map[string][]byte{
			"sha256": hash[:],
		},
	}

	ed25519 := signed.NewEd25519()
	repo := initRepo(t, ed25519)

	_, err := repo.AddTargets("targets/test", data.Files{"f": f})
	assert.Error(t, err)
	assert.IsType(t, data.ErrInvalidRole{}, err)
}

// Adding targets to a role that we don't have signing keys for fails
func TestAddTargetsNoSigningKeys(t *testing.T) {
	hash := sha256.Sum256([]byte{})
	f := data.FileMeta{
		Length: 1,
		Hashes: map[string][]byte{
			"sha256": hash[:],
		},
	}

	ed25519 := signed.NewEd25519()
	repo := initRepo(t, ed25519)

	testKey, err := ed25519.Create("targets/test", data.ED25519Key)
	assert.NoError(t, err)
	err = repo.UpdateDelegationKeys("targets/test", []data.PublicKey{testKey}, []string{}, 1)
	assert.NoError(t, err)
	err = repo.UpdateDelegationPaths("targets/test", []string{"test"}, []string{}, false)
	assert.NoError(t, err)

	// now delete the signing key (all keys)
	repo.cryptoService = signed.NewEd25519()

	// adding the targets to the role should create the metadata though
	_, err = repo.AddTargets("targets/test", data.Files{"f": f})
	assert.Error(t, err)
	assert.IsType(t, signed.ErrNoKeys{}, err)
}

// Removing targets from a role that exists, has targets, and is signable
// should succeed, even if we also want to remove targets that don't exist.
func TestRemoveExistingAndNonexistingTargets(t *testing.T) {
	ed25519 := signed.NewEd25519()
	repo := initRepo(t, ed25519)

	testKey, err := ed25519.Create("targets/test", data.ED25519Key)
	assert.NoError(t, err)
	err = repo.UpdateDelegationKeys("targets/test", []data.PublicKey{testKey}, []string{}, 1)
	assert.NoError(t, err)
	err = repo.UpdateDelegationPaths("targets/test", []string{"test"}, []string{}, false)
	assert.NoError(t, err)

	// no empty metadata is created for this role
	_, ok := repo.Targets["targets/test"]
	assert.False(t, ok, "no empty targets file should be created")

	// now remove a target
	assert.NoError(t, repo.RemoveTargets("targets/test", "f"))

	// still no metadata
	_, ok = repo.Targets["targets/test"]
	assert.False(t, ok)
}

// Removing targets from a role that exists but without metadata succeeds.
func TestRemoveTargetsNonexistentMetadata(t *testing.T) {
	ed25519 := signed.NewEd25519()
	repo := initRepo(t, ed25519)

	err := repo.RemoveTargets("targets/test", "f")
	assert.Error(t, err)
	assert.IsType(t, data.ErrInvalidRole{}, err)
}

// Removing targets from a role that doesn't exist fails
func TestRemoveTargetsRoleDoesntExist(t *testing.T) {
	ed25519 := signed.NewEd25519()
	repo := initRepo(t, ed25519)

	err := repo.RemoveTargets("targets/test", "f")
	assert.Error(t, err)
	assert.IsType(t, data.ErrInvalidRole{}, err)
}

// Removing targets from a role that we don't have signing keys for fails
func TestRemoveTargetsNoSigningKeys(t *testing.T) {
	hash := sha256.Sum256([]byte{})
	f := data.FileMeta{
		Length: 1,
		Hashes: map[string][]byte{
			"sha256": hash[:],
		},
	}

	ed25519 := signed.NewEd25519()
	repo := initRepo(t, ed25519)

	testKey, err := ed25519.Create("targets/test", data.ED25519Key)
	assert.NoError(t, err)
	err = repo.UpdateDelegationKeys("targets/test", []data.PublicKey{testKey}, []string{}, 1)
	assert.NoError(t, err)
	err = repo.UpdateDelegationPaths("targets/test", []string{""}, []string{}, false)
	assert.NoError(t, err)

	// adding the targets to the role should create the metadata though
	_, err = repo.AddTargets("targets/test", data.Files{"f": f})
	assert.NoError(t, err)

	r, ok := repo.Targets["targets/test"]
	assert.True(t, ok)
	_, ok = r.Signed.Targets["f"]
	assert.True(t, ok)

	// now delete the signing key (all keys)
	repo.cryptoService = signed.NewEd25519()

	// now remove the target - it should fail
	err = repo.RemoveTargets("targets/test", "f")
	assert.Error(t, err)
	assert.IsType(t, signed.ErrNoKeys{}, err)
}

// adding a key to a role marks root as dirty as well as the role
func TestAddBaseKeysToRoot(t *testing.T) {
	for _, role := range data.BaseRoles {
		ed25519 := signed.NewEd25519()
		repo := initRepo(t, ed25519)

		key, err := ed25519.Create(role, data.ED25519Key)
		assert.NoError(t, err)

		assert.Len(t, repo.Root.Signed.Roles[role].KeyIDs, 1)

		assert.NoError(t, repo.AddBaseKeys(role, key))

		_, ok := repo.Root.Signed.Keys[key.ID()]
		assert.True(t, ok)
		assert.Len(t, repo.Root.Signed.Roles[role].KeyIDs, 2)
		assert.True(t, repo.Root.Dirty)

		switch role {
		case data.CanonicalSnapshotRole:
			assert.True(t, repo.Snapshot.Dirty)
		case data.CanonicalTargetsRole:
			assert.True(t, repo.Targets[data.CanonicalTargetsRole].Dirty)
		case data.CanonicalTimestampRole:
			assert.True(t, repo.Timestamp.Dirty)
		}
	}
}

func TestGetAllRoles(t *testing.T) {
	ed25519 := signed.NewEd25519()
	repo := initRepo(t, ed25519)

	// After we init, we get the base roles
	roles := repo.GetAllLoadedRoles()
	assert.Len(t, roles, len(data.BaseRoles))
}

func TestGetBaseRoles(t *testing.T) {
	ed25519 := signed.NewEd25519()
	repo := initRepo(t, ed25519)

	// After we init, we get the base roles
	for _, role := range data.BaseRoles {
		baseRole, err := repo.GetBaseRole(role)
		assert.NoError(t, err)

		assert.Equal(t, role, baseRole.Name)
		keyIDs := repo.cryptoService.ListKeys(role)
		for _, keyID := range keyIDs {
			_, ok := baseRole.Keys[keyID]
			assert.True(t, ok)
			assert.Contains(t, baseRole.ListKeyIDs(), keyID)
		}
		// initRepo should set all key thresholds to 1
		assert.Equal(t, 1, baseRole.Threshold)
	}
}

func TestGetBaseRolesInvalidName(t *testing.T) {
	ed25519 := signed.NewEd25519()
	repo := initRepo(t, ed25519)

	_, err := repo.GetBaseRole("invalid")
	assert.Error(t, err)

	_, err = repo.GetBaseRole("targets/delegation")
	assert.Error(t, err)
}

func TestGetDelegationValidRoles(t *testing.T) {
	ed25519 := signed.NewEd25519()
	repo := initRepo(t, ed25519)

	testKey1, err := ed25519.Create("targets/test", data.ED25519Key)
	assert.NoError(t, err)

	err = repo.UpdateDelegationKeys("targets/test", []data.PublicKey{testKey1}, []string{}, 1)
	assert.NoError(t, err)
	err = repo.UpdateDelegationPaths("targets/test", []string{"path", "anotherpath"}, []string{}, false)
	assert.NoError(t, err)

	delgRole, err := repo.GetDelegationRole("targets/test")
	assert.NoError(t, err)
	assert.Equal(t, "targets/test", delgRole.Name)
	assert.Equal(t, 1, delgRole.Threshold)
	assert.Equal(t, []string{testKey1.ID()}, delgRole.ListKeyIDs())
	assert.Equal(t, []string{"path", "anotherpath"}, delgRole.Paths)
	assert.Equal(t, testKey1, delgRole.Keys[testKey1.ID()])

	testKey2, err := ed25519.Create("targets/a", data.ED25519Key)
	assert.NoError(t, err)
	err = repo.UpdateDelegationKeys("targets/a", []data.PublicKey{testKey2}, []string{}, 1)
	assert.NoError(t, err)
	err = repo.UpdateDelegationPaths("targets/a", []string{""}, []string{}, false)
	assert.NoError(t, err)

	delgRole, err = repo.GetDelegationRole("targets/a")
	assert.NoError(t, err)
	assert.Equal(t, "targets/a", delgRole.Name)
	assert.Equal(t, 1, delgRole.Threshold)
	assert.Equal(t, []string{testKey2.ID()}, delgRole.ListKeyIDs())
	assert.Equal(t, []string{""}, delgRole.Paths)
	assert.Equal(t, testKey2, delgRole.Keys[testKey2.ID()])

	testKey3, err := ed25519.Create("targets/test/b", data.ED25519Key)
	assert.NoError(t, err)
	err = repo.UpdateDelegationKeys("targets/test/b", []data.PublicKey{testKey3}, []string{}, 1)
	assert.NoError(t, err)
	err = repo.UpdateDelegationPaths("targets/test/b", []string{"path/subpath", "anotherpath"}, []string{}, false)
	assert.NoError(t, err)

	delgRole, err = repo.GetDelegationRole("targets/test/b")
	assert.NoError(t, err)
	assert.Equal(t, "targets/test/b", delgRole.Name)
	assert.Equal(t, 1, delgRole.Threshold)
	assert.Equal(t, []string{testKey3.ID()}, delgRole.ListKeyIDs())
	assert.Equal(t, []string{"path/subpath", "anotherpath"}, delgRole.Paths)
	assert.Equal(t, testKey3, delgRole.Keys[testKey3.ID()])

	testKey4, err := ed25519.Create("targets/test/c", data.ED25519Key)
	assert.NoError(t, err)
	// Try adding empty paths, ensure this is valid
	err = repo.UpdateDelegationKeys("targets/test/c", []data.PublicKey{testKey4}, []string{}, 1)
	assert.NoError(t, err)
	err = repo.UpdateDelegationPaths("targets/test/c", []string{}, []string{}, false)
	assert.NoError(t, err)
}

func TestGetDelegationRolesInvalidName(t *testing.T) {
	ed25519 := signed.NewEd25519()
	repo := initRepo(t, ed25519)

	_, err := repo.GetDelegationRole("invalid")
	assert.Error(t, err)

	for _, role := range data.BaseRoles {
		_, err = repo.GetDelegationRole(role)
		assert.Error(t, err)
		assert.IsType(t, data.ErrInvalidRole{}, err)
	}
	_, err = repo.GetDelegationRole("targets/doesnt_exist")
	assert.Error(t, err)
	assert.IsType(t, data.ErrInvalidRole{}, err)
}

func TestGetDelegationRolesInvalidPaths(t *testing.T) {
	ed25519 := signed.NewEd25519()
	repo := initRepo(t, ed25519)

	testKey1, err := ed25519.Create("targets/test", data.ED25519Key)
	assert.NoError(t, err)

	err = repo.UpdateDelegationKeys("targets/test", []data.PublicKey{testKey1}, []string{}, 1)
	assert.NoError(t, err)
	err = repo.UpdateDelegationPaths("targets/test", []string{"path", "anotherpath"}, []string{}, false)
	assert.NoError(t, err)

	testKey2, err := ed25519.Create("targets/test/b", data.ED25519Key)
	assert.NoError(t, err)
	// Now we add a delegation with a path that is not prefixed by its parent delegation, the invalid path can't be added so there is an error
	err = repo.UpdateDelegationKeys("targets/test/b", []data.PublicKey{testKey2}, []string{}, 1)
	assert.NoError(t, err)
	err = repo.UpdateDelegationPaths("targets/test/b", []string{"invalidpath"}, []string{}, false)
	assert.Error(t, err)
	assert.IsType(t, data.ErrInvalidRole{}, err)

	delgRole, err := repo.GetDelegationRole("targets/test")
	assert.NoError(t, err)
	assert.Contains(t, delgRole.Paths, "path")
	assert.Contains(t, delgRole.Paths, "anotherpath")
}

func TestDelegationRolesParent(t *testing.T) {
	delgA := data.DelegationRole{
		BaseRole: data.BaseRole{
			Keys:      nil,
			Name:      "targets/a",
			Threshold: 1,
		},
		Paths: []string{"path", "anotherpath"},
	}

	delgB := data.DelegationRole{
		BaseRole: data.BaseRole{
			Keys:      nil,
			Name:      "targets/a/b",
			Threshold: 1,
		},
		Paths: []string{"path/b", "anotherpath/b", "b/invalidpath"},
	}

	// Assert direct parent relationship
	assert.True(t, delgA.IsParentOf(delgB))
	assert.False(t, delgB.IsParentOf(delgA))
	assert.False(t, delgA.IsParentOf(delgA))

	delgC := data.DelegationRole{
		BaseRole: data.BaseRole{
			Keys:      nil,
			Name:      "targets/a/b/c",
			Threshold: 1,
		},
		Paths: []string{"path/b", "anotherpath/b/c", "c/invalidpath"},
	}

	// Assert direct parent relationship
	assert.True(t, delgB.IsParentOf(delgC))
	assert.False(t, delgB.IsParentOf(delgB))
	assert.False(t, delgA.IsParentOf(delgC))
	assert.False(t, delgC.IsParentOf(delgB))
	assert.False(t, delgC.IsParentOf(delgA))
	assert.False(t, delgC.IsParentOf(delgC))

	// Check that parents correctly restrict paths
	restrictedDelgB, err := delgA.Restrict(delgB)
	assert.NoError(t, err)
	assert.Contains(t, restrictedDelgB.Paths, "path/b")
	assert.Contains(t, restrictedDelgB.Paths, "anotherpath/b")
	assert.NotContains(t, restrictedDelgB.Paths, "b/invalidpath")

	_, err = delgB.Restrict(delgA)
	assert.Error(t, err)
	_, err = delgA.Restrict(delgC)
	assert.Error(t, err)
	_, err = delgC.Restrict(delgB)
	assert.Error(t, err)
	_, err = delgC.Restrict(delgA)
	assert.Error(t, err)

	// Make delgA have no paths and check that it changes delgB and delgC accordingly when chained
	delgA.Paths = []string{}
	restrictedDelgB, err = delgA.Restrict(delgB)
	assert.NoError(t, err)
	assert.Empty(t, restrictedDelgB.Paths)
	restrictedDelgC, err := restrictedDelgB.Restrict(delgC)
	assert.NoError(t, err)
	assert.Empty(t, restrictedDelgC.Paths)
}

func TestGetBaseRoleEmptyRepo(t *testing.T) {
	repo := NewRepo(nil)
	_, err := repo.GetBaseRole(data.CanonicalRootRole)
	assert.Error(t, err)
	assert.IsType(t, ErrNotLoaded{}, err)
}

func TestGetBaseRoleKeyMissing(t *testing.T) {
	ed25519 := signed.NewEd25519()
	repo := initRepo(t, ed25519)

	// change root role to have a KeyID that doesn't exist
	repo.Root.Signed.Roles[data.CanonicalRootRole].KeyIDs = []string{"abc"}

	_, err := repo.GetBaseRole(data.CanonicalRootRole)
	assert.Error(t, err)
	assert.IsType(t, data.ErrInvalidRole{}, err)
}

func TestGetDelegationRoleKeyMissing(t *testing.T) {
	ed25519 := signed.NewEd25519()
	repo := initRepo(t, ed25519)

	// add a delegation that has a KeyID that doesn't exist
	// in the relevant key map
	tar := repo.Targets[data.CanonicalTargetsRole]
	tar.Signed.Delegations.Roles = []*data.Role{
		{
			RootRole: data.RootRole{
				KeyIDs:    []string{"abc"},
				Threshold: 1,
			},
			Name:  "targets/missing_key",
			Paths: []string{""},
		},
	}

	_, err := repo.GetDelegationRole("targets/missing_key")
	assert.Error(t, err)
	assert.IsType(t, data.ErrInvalidRole{}, err)
}
