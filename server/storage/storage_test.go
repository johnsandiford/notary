package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/docker/notary/tuf/data"
	"github.com/stretchr/testify/require"
)

type StoredTUFMeta struct {
	Gun     string
	Role    string
	Sha256  string
	Data    []byte
	Version int
}

func SampleCustomTUFObj(gun, role string, version int, tufdata []byte) StoredTUFMeta {
	if tufdata == nil {
		tufdata = []byte(fmt.Sprintf("%s_%s_%d", gun, role, version))
	}
	checksum := sha256.Sum256(tufdata)
	hexChecksum := hex.EncodeToString(checksum[:])
	return StoredTUFMeta{
		Gun:     gun,
		Role:    role,
		Version: version,
		Sha256:  hexChecksum,
		Data:    tufdata,
	}
}

func MakeUpdate(tufObj StoredTUFMeta) MetaUpdate {
	return MetaUpdate{
		Role:    tufObj.Role,
		Version: tufObj.Version,
		Data:    tufObj.Data,
	}
}

func assertExpectedTUFMetaInStore(t *testing.T, s MetaStore, expected []StoredTUFMeta, current bool) {
	for _, tufObj := range expected {
		if current {
			_, tufdata, err := s.GetCurrent(tufObj.Gun, tufObj.Role)
			require.NoError(t, err)
			require.Equal(t, tufObj.Data, tufdata)
		}

		checksumBytes := sha256.Sum256(tufObj.Data)
		checksum := hex.EncodeToString(checksumBytes[:])

		_, tufdata, err := s.GetChecksum(tufObj.Gun, tufObj.Role, checksum)
		require.NoError(t, err)
		require.Equal(t, tufObj.Data, tufdata)
	}
}

// UpdateCurrent should succeed if there was no previous metadata of the same
// gun and role.  They should be gettable.
func testUpdateCurrentEmptyStore(t *testing.T, s MetaStore) []StoredTUFMeta {
	expected := make([]StoredTUFMeta, 0, 10)
	for _, role := range append(data.BaseRoles, "targets/a") {
		for _, gun := range []string{"gun1", "gun2"} {
			// Adding a new TUF file should succeed
			tufObj := SampleCustomTUFObj(gun, role, 1, nil)
			require.NoError(t, s.UpdateCurrent(tufObj.Gun, MakeUpdate(tufObj)))
			expected = append(expected, tufObj)
		}
	}

	assertExpectedTUFMetaInStore(t, s, expected, true)
	return expected
}

// UpdateCurrent will successfully add a new (higher) version of an existing TUF file,
// but will return an error if there is an older version of a TUF file.
func testUpdateCurrentVersionCheck(t *testing.T, s MetaStore) []StoredTUFMeta {
	role, gun := data.CanonicalRootRole, "testGUN"

	expected := []StoredTUFMeta{
		SampleCustomTUFObj(gun, role, 1, nil),
		SampleCustomTUFObj(gun, role, 2, nil),
		SampleCustomTUFObj(gun, role, 4, nil),
	}

	// starting meta is version 1
	require.NoError(t, s.UpdateCurrent(gun, MakeUpdate(expected[0])))

	// inserting meta version immediately above it and skipping ahead will succeed
	require.NoError(t, s.UpdateCurrent(gun, MakeUpdate(expected[1])))
	require.NoError(t, s.UpdateCurrent(gun, MakeUpdate(expected[2])))

	// Inserting a version that already exists, or that is lower than the current version, will fail
	for _, version := range []int{3, 4} {
		tufObj := SampleCustomTUFObj(gun, role, version, nil)
		err := s.UpdateCurrent(gun, MakeUpdate(tufObj))
		require.Error(t, err, "Error should not be nil")
		require.IsType(t, &ErrOldVersion{}, err,
			"Expected ErrOldVersion error type, got: %v", err)
	}

	assertExpectedTUFMetaInStore(t, s, expected[:2], false)
	assertExpectedTUFMetaInStore(t, s, expected[2:], true)
	return expected
}

// UpdateMany succeeds if the updates do not conflict with each other or with what's
// already in the DB
func testUpdateManyNoConflicts(t *testing.T, s MetaStore) []StoredTUFMeta {
	gun := "testGUN"
	firstBatch := make([]StoredTUFMeta, 4)
	updates := make([]MetaUpdate, 4)
	for i, role := range data.BaseRoles {
		firstBatch[i] = SampleCustomTUFObj(gun, role, 1, nil)
		updates[i] = MakeUpdate(firstBatch[i])
	}

	require.NoError(t, s.UpdateMany(gun, updates))
	assertExpectedTUFMetaInStore(t, s, firstBatch, true)

	secondBatch := make([]StoredTUFMeta, 4)
	// no conflicts with what's in DB or with itself
	for i, role := range data.BaseRoles {
		secondBatch[i] = SampleCustomTUFObj(gun, role, 2, nil)
		updates[i] = MakeUpdate(secondBatch[i])
	}

	require.NoError(t, s.UpdateMany(gun, updates))
	// the first batch is still there, but are no longer the current ones
	assertExpectedTUFMetaInStore(t, s, firstBatch, false)
	assertExpectedTUFMetaInStore(t, s, secondBatch, true)

	// and no conflicts if the same role and gun but different version is included
	// in the same update.  Even if they're out of order.
	thirdBatch := make([]StoredTUFMeta, 2)
	role := data.CanonicalRootRole
	updates = updates[:2]
	for i, version := range []int{4, 3} {
		thirdBatch[i] = SampleCustomTUFObj(gun, role, version, nil)
		updates[i] = MakeUpdate(thirdBatch[i])
	}

	require.NoError(t, s.UpdateMany(gun, updates))

	// all the other data is still there, but are no longer the current ones
	assertExpectedTUFMetaInStore(t, s, append(firstBatch, secondBatch...), false)
	assertExpectedTUFMetaInStore(t, s, thirdBatch[:1], true)
	assertExpectedTUFMetaInStore(t, s, thirdBatch[1:], false)

	return append(append(firstBatch, secondBatch...), thirdBatch...)
}

// UpdateMany does not insert any rows (or at least rolls them back) if there
// are any conflicts.
func testUpdateManyConflictRollback(t *testing.T, s MetaStore) []StoredTUFMeta {
	gun := "testGUN"
	successBatch := make([]StoredTUFMeta, 4)
	updates := make([]MetaUpdate, 4)
	for i, role := range data.BaseRoles {
		successBatch[i] = SampleCustomTUFObj(gun, role, 1, nil)
		updates[i] = MakeUpdate(successBatch[i])
	}

	require.NoError(t, s.UpdateMany(gun, updates))

	// conflicts with what's in DB
	badBatch := make([]StoredTUFMeta, 4)
	for i, role := range data.BaseRoles {
		version := 2
		if role == data.CanonicalTargetsRole {
			version = 1
		}
		tufdata := []byte(fmt.Sprintf("%s_%s_%d_bad", gun, role, version))
		badBatch[i] = SampleCustomTUFObj(gun, role, version, tufdata)
		updates[i] = MakeUpdate(badBatch[i])
	}

	err := s.UpdateMany(gun, updates)
	require.Error(t, err)
	require.IsType(t, &ErrOldVersion{}, err)

	// self-conflicting, in that it's a duplicate, but otherwise no DB conflicts
	duplicate := SampleCustomTUFObj(gun, data.CanonicalTimestampRole, 3, []byte("duplicate"))
	duplicateUpdate := MakeUpdate(duplicate)
	err = s.UpdateMany(gun, []MetaUpdate{duplicateUpdate, duplicateUpdate})
	require.Error(t, err)
	require.IsType(t, &ErrOldVersion{}, err)

	assertExpectedTUFMetaInStore(t, s, successBatch, true)

	for _, tufObj := range append(badBatch, duplicate) {
		checksumBytes := sha256.Sum256(tufObj.Data)
		checksum := hex.EncodeToString(checksumBytes[:])

		_, _, err = s.GetChecksum(tufObj.Gun, tufObj.Role, checksum)
		require.Error(t, err)
		require.IsType(t, ErrNotFound{}, err)
	}

	return successBatch
}

// Delete will remove all TUF metadata, all versions, associated with a gun
func testDeleteSuccess(t *testing.T, s MetaStore) {
	gun := "testGUN"
	// If there is nothing in the DB, delete is a no-op success
	require.NoError(t, s.Delete(gun))

	// If there is data in the DB, all versions are deleted
	unexpected := make([]StoredTUFMeta, 0, 10)
	updates := make([]MetaUpdate, 0, 10)
	for _, role := range append(data.BaseRoles, "targets/a") {
		for version := 1; version < 3; version++ {
			tufObj := SampleCustomTUFObj(gun, role, version, nil)
			unexpected = append(unexpected, tufObj)
			updates = append(updates, MakeUpdate(tufObj))
		}
	}
	require.NoError(t, s.UpdateMany(gun, updates))
	require.NoError(t, s.Delete(gun))

	for _, tufObj := range unexpected {
		_, _, err := s.GetCurrent(tufObj.Gun, tufObj.Role)
		require.IsType(t, ErrNotFound{}, err)

		checksumBytes := sha256.Sum256(tufObj.Data)
		checksum := hex.EncodeToString(checksumBytes[:])

		_, _, err = s.GetChecksum(tufObj.Gun, tufObj.Role, checksum)
		require.Error(t, err)
		require.IsType(t, ErrNotFound{}, err)
	}
}
