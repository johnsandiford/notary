package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/docker/notary/certs"
	"github.com/docker/notary/passphrase"
	"github.com/docker/notary/tuf/client"
	"github.com/docker/notary/tuf/data"
	"github.com/docker/notary/tuf/signed"
	"github.com/docker/notary/tuf/store"
	"github.com/docker/notary/tuf/testutils"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
)

func newBlankRepo(t *testing.T, url string) *NotaryRepository {
	// Temporary directory where test files will be created
	tempBaseDir, err := ioutil.TempDir("", "notary-test-")
	require.NoError(t, err, "failed to create a temporary directory: %s", err)

	repo, err := NewNotaryRepository(tempBaseDir, "docker.com/notary", url,
		http.DefaultTransport, passphrase.ConstantRetriever("pass"))
	require.NoError(t, err)
	return repo
}

var metadataDelegations = []string{"targets/a", "targets/a/b", "targets/b", "targets/a/b/c", "targets/b/c"}
var delegationsWithNonEmptyMetadata = []string{"targets/a", "targets/a/b", "targets/b"}

func newServerSwizzler(t *testing.T) (map[string][]byte, *testutils.MetadataSwizzler) {
	serverMeta, cs, err := testutils.NewRepoMetadata("docker.com/notary", metadataDelegations...)
	require.NoError(t, err)

	serverSwizzler := testutils.NewMetadataSwizzler("docker.com/notary", serverMeta, cs)
	require.NoError(t, err)

	return serverMeta, serverSwizzler
}

// bumps the versions of everything in the metadata cache, to force local cache
// to update
func bumpVersions(t *testing.T, s *testutils.MetadataSwizzler, offset int) {
	// bump versions of everything on the server, to force everything to update
	for _, r := range s.Roles {
		require.NoError(t, s.OffsetMetadataVersion(r, offset))
	}
	require.NoError(t, s.UpdateSnapshotHashes())
	require.NoError(t, s.UpdateTimestampHash())
}

// create a server that just serves static metadata files from a metaStore
func readOnlyServer(t *testing.T, cache store.MetadataStore, notFoundStatus int, gun string) *httptest.Server {
	m := mux.NewRouter()
	handler := func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)
		metaBytes, err := cache.GetMeta(vars["role"], -1)
		if _, ok := err.(store.ErrMetaNotFound); ok {
			w.WriteHeader(notFoundStatus)
		} else {
			require.NoError(t, err)
			w.Write(metaBytes)
		}
	}
	m.HandleFunc(fmt.Sprintf("/v2/%s/_trust/tuf/{role:.*}.{checksum:.*}.json", gun), handler)
	m.HandleFunc(fmt.Sprintf("/v2/%s/_trust/tuf/{role:.*}.json", gun), handler)
	return httptest.NewServer(m)
}

type unwritableStore struct {
	store.MetadataStore
	roleToNotWrite string
}

func (u *unwritableStore) SetMeta(role string, serverMeta []byte) error {
	if role == u.roleToNotWrite {
		return fmt.Errorf("Non-writable")
	}
	return u.MetadataStore.SetMeta(role, serverMeta)
}

// Update can succeed even if we cannot write any metadata to the repo (assuming
// no data in the repo)
func TestUpdateSucceedsEvenIfCannotWriteNewRepo(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode")
	}

	serverMeta, _, err := testutils.NewRepoMetadata("docker.com/notary", metadataDelegations...)
	require.NoError(t, err)

	ts := readOnlyServer(t, store.NewMemoryStore(serverMeta), http.StatusNotFound, "docker.com/notary")
	defer ts.Close()

	for role := range serverMeta {
		repo := newBlankRepo(t, ts.URL)
		repo.fileStore = &unwritableStore{MetadataStore: repo.fileStore, roleToNotWrite: role}
		_, err := repo.Update(false)

		if role == data.CanonicalRootRole {
			require.Error(t, err) // because checkRoot loads root from cache to check hashes
			continue
		} else {
			require.NoError(t, err)
		}

		for r, expected := range serverMeta {
			actual, err := repo.fileStore.GetMeta(r, -1)
			if r == role {
				require.Error(t, err)
				require.IsType(t, store.ErrMetaNotFound{}, err,
					"expected no data because unable to write for %s", role)
			} else {
				require.NoError(t, err, "problem getting repo metadata for %s", r)
				require.True(t, bytes.Equal(expected, actual),
					"%s: expected to update since only %s was unwritable", r, role)
			}
		}

		os.RemoveAll(repo.baseDir)
	}
}

// Update can succeed even if we cannot write any metadata to the repo (assuming
// existing data in the repo)
func TestUpdateSucceedsEvenIfCannotWriteExistingRepo(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode")
	}
	serverMeta, serverSwizzler := newServerSwizzler(t)
	ts := readOnlyServer(t, serverSwizzler.MetadataCache, http.StatusNotFound, "docker.com/notary")
	defer ts.Close()

	// download existing metadata
	repo := newBlankRepo(t, ts.URL)
	defer os.RemoveAll(repo.baseDir)

	_, err := repo.Update(false)
	require.NoError(t, err)

	origFileStore := repo.fileStore

	for role := range serverMeta {
		for _, forWrite := range []bool{true, false} {
			// bump versions of everything on the server, to force everything to update
			bumpVersions(t, serverSwizzler, 1)

			// update fileStore
			repo.fileStore = &unwritableStore{MetadataStore: origFileStore, roleToNotWrite: role}
			_, err := repo.Update(forWrite)

			if role == data.CanonicalRootRole {
				require.Error(t, err) // because checkRoot loads root from cache to check hashes
				continue
			}
			require.NoError(t, err)

			for r, expected := range serverMeta {
				actual, err := repo.fileStore.GetMeta(r, -1)
				require.NoError(t, err, "problem getting repo metadata for %s", r)
				if role == r {
					require.False(t, bytes.Equal(expected, actual),
						"%s: expected to not update because %s was unwritable", r, role)
				} else {
					require.True(t, bytes.Equal(expected, actual),
						"%s: expected to update since only %s was unwritable", r, role)
				}
			}
		}
	}
}

type swizzleFunc func(*testutils.MetadataSwizzler, string) error
type swizzleExpectations struct {
	desc       string
	swizzle    swizzleFunc
	expectErrs []interface{}
}

var waysToMessUpLocalMetadata = []swizzleExpectations{
	// for instance if the metadata got truncated or otherwise block corrupted
	{desc: "invalid JSON", swizzle: (*testutils.MetadataSwizzler).SetInvalidJSON},
	// if the metadata was accidentally deleted
	{desc: "missing metadata", swizzle: (*testutils.MetadataSwizzler).RemoveMetadata},
	// if the signature was invalid - maybe the user tried to modify something manually
	// that they forgot (add a key, or something)
	{desc: "signed with right key but wrong hash",
		swizzle: (*testutils.MetadataSwizzler).InvalidateMetadataSignatures},
	// if the user copied the wrong root.json over it by accident or something
	{desc: "signed with wrong key", swizzle: (*testutils.MetadataSwizzler).SignMetadataWithInvalidKey},
	// self explanatory
	{desc: "expired metadata", swizzle: (*testutils.MetadataSwizzler).ExpireMetadata},

	// Not trying any of the other repoSwizzler methods, because those involve modifying
	// and re-serializing, and that means a user has the root and other keys and was trying to
	// actively sabotage and break their own local repo (particularly the root.json)
}

// If a repo has corrupt metadata (in that the hash doesn't match the snapshot) or
// missing metadata, an update will replace all of it
func TestUpdateReplacesCorruptOrMissingMetadata(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode")
	}
	serverMeta, cs, err := testutils.NewRepoMetadata("docker.com/notary", metadataDelegations...)
	require.NoError(t, err)

	ts := readOnlyServer(t, store.NewMemoryStore(serverMeta), http.StatusNotFound, "docker.com/notary")
	defer ts.Close()

	repo := newBlankRepo(t, ts.URL)
	defer os.RemoveAll(repo.baseDir)

	_, err = repo.Update(false) // ensure we have all metadata to start with
	require.NoError(t, err)

	// we want to swizzle the local cache, not the server, so create a new one
	repoSwizzler := testutils.NewMetadataSwizzler("docker.com/notary", serverMeta, cs)
	repoSwizzler.MetadataCache = repo.fileStore

	for _, role := range repoSwizzler.Roles {
		for _, expt := range waysToMessUpLocalMetadata {
			text, messItUp := expt.desc, expt.swizzle
			for _, forWrite := range []bool{true, false} {
				require.NoError(t, messItUp(repoSwizzler, role), "could not fuzz %s (%s)", role, text)
				_, err := repo.Update(forWrite)
				require.NoError(t, err)
				for r, expected := range serverMeta {
					actual, err := repo.fileStore.GetMeta(r, -1)
					require.NoError(t, err, "problem getting repo metadata for %s", role)
					require.True(t, bytes.Equal(expected, actual),
						"%s for %s: expected to recover after update", text, role)
				}
			}
		}
	}
}

// If a repo has an invalid root (signed by wrong key, expired, invalid version,
// invalid number of signatures, etc.), the repo will just get the new root from
// the server, whether or not the update is for writing (forced update), but
// it will refuse to update if the root key has changed and the new root is
// not signed by the old and new key
func TestUpdateFailsIfServerRootKeyChangedWithoutMultiSign(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode")
	}

	serverMeta, serverSwizzler := newServerSwizzler(t)
	origMeta := testutils.CopyRepoMetadata(serverMeta)

	ts := readOnlyServer(t, serverSwizzler.MetadataCache, http.StatusNotFound, "docker.com/notary")
	defer ts.Close()

	repo := newBlankRepo(t, ts.URL)
	defer os.RemoveAll(repo.baseDir)

	_, err := repo.Update(false) // ensure we have all metadata to start with
	require.NoError(t, err)

	// rotate the server's root.json root key so that they no longer match trust anchors
	require.NoError(t, serverSwizzler.ChangeRootKey())
	// bump versions, update snapshot and timestamp too so it will not fail on a hash
	bumpVersions(t, serverSwizzler, 1)

	// we want to swizzle the local cache, not the server, so create a new one
	repoSwizzler := &testutils.MetadataSwizzler{
		MetadataCache: repo.fileStore,
		CryptoService: serverSwizzler.CryptoService,
		Roles:         serverSwizzler.Roles,
	}

	for _, expt := range waysToMessUpLocalMetadata {
		text, messItUp := expt.desc, expt.swizzle
		for _, forWrite := range []bool{true, false} {
			require.NoError(t, messItUp(repoSwizzler, data.CanonicalRootRole), "could not fuzz root (%s)", text)
			messedUpMeta, err := repo.fileStore.GetMeta(data.CanonicalRootRole, -1)

			if _, ok := err.(store.ErrMetaNotFound); ok { // one of the ways to mess up is to delete metadata

				_, err = repo.Update(forWrite)
				require.Error(t, err) // the new server has a different root key, update fails

			} else {

				require.NoError(t, err)

				_, err = repo.Update(forWrite)
				require.Error(t, err) // the new server has a different root, update fails

				// we can't test that all the metadata is the same, because we probably would
				// have downloaded a new timestamp and maybe snapshot.  But the root should be the
				// same because it has failed to update.
				for role, expected := range origMeta {
					if role != data.CanonicalTimestampRole && role != data.CanonicalSnapshotRole {
						actual, err := repo.fileStore.GetMeta(role, -1)
						require.NoError(t, err, "problem getting repo metadata for %s", role)

						if role == data.CanonicalRootRole {
							expected = messedUpMeta
						}
						require.True(t, bytes.Equal(expected, actual),
							"%s for %s: expected to not have updated", text, role)
					}
				}

			}

			// revert our original root metadata
			require.NoError(t,
				repo.fileStore.SetMeta(data.CanonicalRootRole, origMeta[data.CanonicalRootRole]))
		}
	}
}

type updateOpts struct {
	notFoundCode     int    // what code to return when the cache doesn't have the metadata
	serverHasNewData bool   // whether the server should have the same or new version than the local cache
	localCache       bool   // whether the repo should have a local cache before updating
	forWrite         bool   // whether the update is for writing or not (force check remote root.json)
	role             string // the role to mess up on the server
}

// If there's no local cache, we go immediately to check the remote server for
// root, and if it doesn't exist, we return ErrRepositoryNotExist. This happens
// with or without a force check (update for write).
func TestUpdateRemoteRootNotExistNoLocalCache(t *testing.T) {
	testUpdateRemoteNon200Error(t, updateOpts{
		notFoundCode: http.StatusNotFound,
		forWrite:     false,
		role:         data.CanonicalRootRole,
	}, ErrRepositoryNotExist{})
	testUpdateRemoteNon200Error(t, updateOpts{
		notFoundCode: http.StatusNotFound,
		forWrite:     true,
		role:         data.CanonicalRootRole,
	}, ErrRepositoryNotExist{})
}

// If there is a local cache, we use the local root as the trust anchor and we
// then an update. If the server has no root.json, and we don't need to force
// check (update for write), we can used the cached root because the timestamp
// has not changed.
// If we force check (update for write), then it hits the server first, and
// still returns an ErrRepositoryNotExist.  This is the
// case where the server has the same data as the client, in which case we might
// be able to just used the cached data and not have to download.
func TestUpdateRemoteRootNotExistCanUseLocalCache(t *testing.T) {
	// if for-write is false, then we don't need to check the root.json on bootstrap,
	// and hence we can just use the cached version on update
	testUpdateRemoteNon200Error(t, updateOpts{
		notFoundCode: http.StatusNotFound,
		localCache:   true,
		forWrite:     false,
		role:         data.CanonicalRootRole,
	}, nil)
	// fails because bootstrap requires a check to remote root.json and fails if
	// the check fails
	testUpdateRemoteNon200Error(t, updateOpts{
		notFoundCode: http.StatusNotFound,
		localCache:   true,
		forWrite:     true,
		role:         data.CanonicalRootRole,
	}, ErrRepositoryNotExist{})
}

// If there is a local cache, we use the local root as the trust anchor and we
// then an update. If the server has no root.json, we return an ErrRepositoryNotExist.
// If we force check (update for write), then it hits the server first, and
// still returns an ErrRepositoryNotExist. This is the case where the server
// has new updated data, so we cannot default to cached data.
func TestUpdateRemoteRootNotExistCannotUseLocalCache(t *testing.T) {
	testUpdateRemoteNon200Error(t, updateOpts{
		notFoundCode:     http.StatusNotFound,
		serverHasNewData: true,
		localCache:       true,
		forWrite:         false,
		role:             data.CanonicalRootRole,
	}, ErrRepositoryNotExist{})
	testUpdateRemoteNon200Error(t, updateOpts{
		notFoundCode:     http.StatusNotFound,
		serverHasNewData: true,
		localCache:       true,
		forWrite:         true,
		role:             data.CanonicalRootRole,
	}, ErrRepositoryNotExist{})
}

// If there's no local cache, we go immediately to check the remote server for
// root, and if it 50X's, we return ErrServerUnavailable. This happens
// with or without a force check (update for write).
func TestUpdateRemoteRoot50XNoLocalCache(t *testing.T) {
	testUpdateRemoteNon200Error(t, updateOpts{
		notFoundCode: http.StatusServiceUnavailable,
		forWrite:     false,
		role:         data.CanonicalRootRole,
	}, store.ErrServerUnavailable{})
	testUpdateRemoteNon200Error(t, updateOpts{
		notFoundCode: http.StatusServiceUnavailable,
		forWrite:     true,
		role:         data.CanonicalRootRole,
	}, store.ErrServerUnavailable{})
}

// If there is a local cache, we use the local root as the trust anchor and we
// then an update. If the server 50X's on root.json, and we don't force check,
// then because the timestamp is the same we can just use our cached root.json
// and don't have to download another.
// If we force check (update for write), we return an ErrServerUnavailable.
// This is the case where the server has the same data as the client
func TestUpdateRemoteRoot50XCanUseLocalCache(t *testing.T) {
	// if for-write is false, then we don't need to check the root.json on bootstrap,
	// and hence we can just use the cached version on update.
	testUpdateRemoteNon200Error(t, updateOpts{
		notFoundCode: http.StatusServiceUnavailable,
		localCache:   true,
		forWrite:     false,
		role:         data.CanonicalRootRole,
	}, nil)
	// fails because bootstrap requires a check to remote root.json and fails if
	// the check fails
	testUpdateRemoteNon200Error(t, updateOpts{
		notFoundCode: http.StatusServiceUnavailable,
		localCache:   true,
		forWrite:     true,
		role:         data.CanonicalRootRole,
	}, store.ErrServerUnavailable{})
}

// If there is a local cache, we use the local root as the trust anchor and we
// then an update. If the server 50X's on root.json, we return an ErrServerUnavailable.
// This happens with or without a force check (update for write)
func TestUpdateRemoteRoot50XCannotUseLocalCache(t *testing.T) {
	// if for-write is false, then we don't need to check the root.json on bootstrap,
	// and hence we can just use the cached version on update
	testUpdateRemoteNon200Error(t, updateOpts{
		notFoundCode:     http.StatusServiceUnavailable,
		serverHasNewData: true,
		localCache:       true,
		forWrite:         false,
		role:             data.CanonicalRootRole,
	}, store.ErrServerUnavailable{})
	// fails because of bootstrap
	testUpdateRemoteNon200Error(t, updateOpts{
		notFoundCode:     http.StatusServiceUnavailable,
		serverHasNewData: true,
		localCache:       true,
		forWrite:         true,
		role:             data.CanonicalRootRole,
	}, store.ErrServerUnavailable{})
}

// If there is no local cache, we just update. If the server has a root.json,
// but is missing other data, then we propagate the ErrMetaNotFound.  Skipping
// force check, because that only matters for root.
func TestUpdateNonRootRemoteMissingMetadataNoLocalCache(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode")
	}
	for _, role := range append(data.BaseRoles, delegationsWithNonEmptyMetadata...) {
		if role == data.CanonicalRootRole {
			continue
		}
		testUpdateRemoteNon200Error(t, updateOpts{
			notFoundCode: http.StatusNotFound,
			role:         role,
		}, store.ErrMetaNotFound{})
	}
}

// If there is a local cache, we update anyway and see if anything's different
// (assuming remote has a root.json).  If the timestamp is missing, we use the
// local timestamp and already have all data, so nothing needs to be downloaded.
// If the timestamp is present, but the same, we already have all the data, so
// nothing needs to be downloaded.
// Skipping force check, because that only matters for root.
func TestUpdateNonRootRemoteMissingMetadataCanUseLocalCache(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode")
	}
	// really we can delete everything at once except for the timestamp, but
	// it's better to check one by one in case we change the download code
	// somewhat.
	for _, role := range append(data.BaseRoles, delegationsWithNonEmptyMetadata...) {
		if role == data.CanonicalRootRole {
			continue
		}
		testUpdateRemoteNon200Error(t, updateOpts{
			notFoundCode: http.StatusNotFound,
			localCache:   true,
			role:         role,
		}, nil)
	}
}

// If there is a local cache, we update anyway and see if anything's different
// (assuming remote has a root.json).  If the server has new data, we cannot
// use the local cache so if the server is missing any metadata we cannot update.
// Skipping force check, because that only matters for root.
func TestUpdateNonRootRemoteMissingMetadataCannotUseLocalCache(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode")
	}
	for _, role := range append(data.BaseRoles, delegationsWithNonEmptyMetadata...) {
		if role == data.CanonicalRootRole {
			continue
		}
		var errExpected interface{} = store.ErrMetaNotFound{}
		if role == data.CanonicalTimestampRole {
			// if we can't download the timestamp, we use the cached timestamp.
			// it says that we have all the local data already, so we download
			// nothing.  So the update won't error, it will just fail to update
			// to the latest version.  We log a warning in this case.
			errExpected = nil
		}

		testUpdateRemoteNon200Error(t, updateOpts{
			notFoundCode:     http.StatusNotFound,
			serverHasNewData: true,
			localCache:       true,
			role:             role,
		}, errExpected)
	}
}

// If there is no local cache, we just update. If the server 50X's when getting
// metadata, we propagate ErrServerUnavailable.
func TestUpdateNonRootRemote50XNoLocalCache(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode")
	}
	for _, role := range append(data.BaseRoles, delegationsWithNonEmptyMetadata...) {
		if role == data.CanonicalRootRole {
			continue
		}
		testUpdateRemoteNon200Error(t, updateOpts{
			notFoundCode: http.StatusServiceUnavailable,
			role:         role,
		}, store.ErrServerUnavailable{})
	}
}

// If there is a local cache, we update anyway and see if anything's different
// (assuming remote has a root.json).  If the timestamp is 50X's, we use the
// local timestamp and already have all data, so nothing needs to be downloaded.
// If the timestamp is present, but the same, we already have all the data, so
// nothing needs to be downloaded.
func TestUpdateNonRootRemote50XCanUseLocalCache(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode")
	}
	// actually everything can error at once, but it's better to check one by
	// one in case we change the download code somewhat.
	for _, role := range append(data.BaseRoles, delegationsWithNonEmptyMetadata...) {
		if role == data.CanonicalRootRole {
			continue
		}
		testUpdateRemoteNon200Error(t, updateOpts{
			notFoundCode: http.StatusServiceUnavailable,
			localCache:   true,
			role:         role,
		}, nil)
	}
}

// If there is a local cache, we update anyway and see if anything's different
// (assuming remote has a root.json).  If the server has new data, we cannot
// use the local cache so if the server 50X's on any metadata we cannot update.
// This happens whether or not we force a remote check (because that's on the
// root)
func TestUpdateNonRootRemote50XCannotUseLocalCache(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode")
	}
	for _, role := range append(data.BaseRoles, delegationsWithNonEmptyMetadata...) {
		if role == data.CanonicalRootRole {
			continue
		}

		var errExpected interface{} = store.ErrServerUnavailable{}
		if role == data.CanonicalTimestampRole {
			// if we can't download the timestamp, we use the cached timestamp.
			// it says that we have all the local data already, so we download
			// nothing.  So the update won't error, it will just fail to update
			// to the latest version.  We log a warning in this case.
			errExpected = nil
		}

		testUpdateRemoteNon200Error(t, updateOpts{
			notFoundCode:     http.StatusServiceUnavailable,
			serverHasNewData: true,
			localCache:       true,
			role:             role,
		}, errExpected)
	}
}

func testUpdateRemoteNon200Error(t *testing.T, opts updateOpts, errExpected interface{}) {
	_, serverSwizzler := newServerSwizzler(t)
	ts := readOnlyServer(t, serverSwizzler.MetadataCache, opts.notFoundCode, "docker.com/notary")
	defer ts.Close()

	repo := newBlankRepo(t, ts.URL)
	defer os.RemoveAll(repo.baseDir)

	if opts.localCache {
		_, err := repo.Update(false) // acquire local cache
		require.NoError(t, err)
	}

	if opts.serverHasNewData {
		bumpVersions(t, serverSwizzler, 1)
	}

	require.NoError(t, serverSwizzler.RemoveMetadata(opts.role), "failed to remove %s", opts.role)

	_, err := repo.Update(opts.forWrite)
	if errExpected == nil {
		require.NoError(t, err, "expected no failure updating when %s is %v (forWrite: %v)",
			opts.role, opts.notFoundCode, opts.forWrite)
	} else {
		require.Error(t, err, "expected failure updating when %s is %v (forWrite: %v)",
			opts.role, opts.notFoundCode, opts.forWrite)
		require.IsType(t, errExpected, err, "wrong update error when %s is %v (forWrite: %v)",
			opts.role, opts.notFoundCode, opts.forWrite)
		if notFound, ok := err.(store.ErrMetaNotFound); ok {
			require.True(t, strings.HasPrefix(notFound.Resource, opts.role), "wrong resource missing (forWrite: %v)", opts.forWrite)
		}
	}
}

// If there's no local cache, we go immediately to check the remote server for
// root. If the root is corrupted in transit in such a way that the signature is
// wrong, but it is correct in all other ways, then it validates during bootstrap,
// but it will fail validation during update. So it will fail with or without
// a force check (update for write).  If any of the other roles (except
// timestamp, because there is no checksum for that) are corrupted in the same
// way, they will also fail during update with the same error.
func TestUpdateRemoteChecksumWrongNoLocalCache(t *testing.T) {
	for _, role := range append(data.BaseRoles, delegationsWithNonEmptyMetadata...) {
		testUpdateRemoteFileChecksumWrong(t, updateOpts{
			serverHasNewData: false,
			localCache:       false,
			forWrite:         false,
			role:             role,
		}, role != data.CanonicalTimestampRole) // timestamp role should not fail

		if role == data.CanonicalRootRole {
			testUpdateRemoteFileChecksumWrong(t, updateOpts{
				serverHasNewData: false,
				localCache:       false,
				forWrite:         true,
				role:             role,
			}, true)
		}
	}
}

// If there's is a local cache, and the remote server has the same data (except
// corrupted), then we can just use our local cache.  So update succeeds (
// with or without a force check (update for write))
func TestUpdateRemoteChecksumWrongCanUseLocalCache(t *testing.T) {
	for _, role := range append(data.BaseRoles, delegationsWithNonEmptyMetadata...) {
		testUpdateRemoteFileChecksumWrong(t, updateOpts{
			serverHasNewData: false,
			localCache:       true,
			forWrite:         false,
			role:             role,
		}, false)

		if role == data.CanonicalRootRole {
			testUpdateRemoteFileChecksumWrong(t, updateOpts{
				serverHasNewData: false,
				localCache:       true,
				forWrite:         true,
				role:             role,
			}, false)
		}
	}
}

// If there's is a local cache, but the remote server has new data (some
// corrupted), we go immediately to check the remote server for root.  If the
// root is corrupted in transit in such a way that the signature is wrong, but
// it is correct in all other ways, it from validates during bootstrap,
// but it will fail validation during update. So it will fail with or without
// a force check (update for write).  If any of the other roles (except
// timestamp, because there is no checksum for that) is corrupted in the same
// way, they will also fail during update with the same error.
func TestUpdateRemoteChecksumWrongCannotUseLocalCache(t *testing.T) {
	for _, role := range append(data.BaseRoles, delegationsWithNonEmptyMetadata...) {
		testUpdateRemoteFileChecksumWrong(t, updateOpts{
			serverHasNewData: true,
			localCache:       true,
			forWrite:         false,
			role:             role,
		}, role != data.CanonicalTimestampRole) // timestamp role should not fail

		if role == data.CanonicalRootRole {
			testUpdateRemoteFileChecksumWrong(t, updateOpts{
				serverHasNewData: true,
				localCache:       true,
				forWrite:         true,
				role:             role,
			}, true)
		}
	}
}

func testUpdateRemoteFileChecksumWrong(t *testing.T, opts updateOpts, errExpected bool) {
	_, serverSwizzler := newServerSwizzler(t)
	ts := readOnlyServer(t, serverSwizzler.MetadataCache, http.StatusNotFound, "docker.com/notary")
	defer ts.Close()

	repo := newBlankRepo(t, ts.URL)
	defer os.RemoveAll(repo.baseDir)

	if opts.localCache {
		_, err := repo.Update(false) // acquire local cache
		require.NoError(t, err)
	}

	if opts.serverHasNewData {
		bumpVersions(t, serverSwizzler, 1)
	}

	require.NoError(t, serverSwizzler.AddExtraSpace(opts.role), "failed to checksum-corrupt to %s", opts.role)

	_, err := repo.Update(opts.forWrite)
	if !errExpected {
		require.NoError(t, err, "expected no failure updating when %s has the wrong checksum (forWrite: %v)",
			opts.role, opts.forWrite)
	} else {
		require.Error(t, err, "expected failure updating when %s has the wrong checksum (forWrite: %v)",
			opts.role, opts.forWrite)

		// it could be ErrMaliciousServer (if the server sent the metadata with a content length)
		// or a checksum error (if the server didn't set content length because transfer-encoding
		// was specified).  For the timestamp, which is really short, it should be the content-length.

		var rightError bool
		if opts.role == data.CanonicalTimestampRole {
			_, rightError = err.(store.ErrMaliciousServer)
		} else {
			_, isErrChecksum := err.(client.ErrChecksumMismatch)
			_, isErrMaliciousServer := err.(store.ErrMaliciousServer)
			rightError = isErrChecksum || isErrMaliciousServer
		}
		require.True(t, rightError, err,
			"wrong update error (%v) when %s has the wrong checksum (forWrite: %v)",
			err, opts.role, opts.forWrite)
	}
}

// --- these tests below assume the checksums are correct (since the server can sign snapshots and
// timestamps, so can be malicious) ---

// this does not include delete, which is tested separately so we can try to get
// 404s and 503s
var waysToMessUpServer = []swizzleExpectations{
	{desc: "invalid JSON", expectErrs: []interface{}{&json.SyntaxError{}},
		swizzle: (*testutils.MetadataSwizzler).SetInvalidJSON},

	{desc: "an invalid Signed", expectErrs: []interface{}{&json.UnmarshalTypeError{}},
		swizzle: (*testutils.MetadataSwizzler).SetInvalidSigned},

	{desc: "an invalid SignedMeta",
		// it depends which field gets unmarshalled first
		expectErrs: []interface{}{&json.UnmarshalTypeError{}, &time.ParseError{}},
		swizzle:    (*testutils.MetadataSwizzler).SetInvalidSignedMeta},

	// for the errors below, when we bootstrap root, we get cert.ErrValidationFail failures
	// for everything else, the errors come from tuf/signed

	{desc: "invalid SignedMeta Type", expectErrs: []interface{}{
		&certs.ErrValidationFail{}, signed.ErrWrongType, data.ErrInvalidMetadata{}},
		swizzle: (*testutils.MetadataSwizzler).SetInvalidMetadataType},

	{desc: "invalid signatures", expectErrs: []interface{}{
		&certs.ErrValidationFail{}, signed.ErrRoleThreshold{}},
		swizzle: (*testutils.MetadataSwizzler).InvalidateMetadataSignatures},

	{desc: "meta signed by wrong key", expectErrs: []interface{}{
		&certs.ErrValidationFail{}, signed.ErrRoleThreshold{}},
		swizzle: (*testutils.MetadataSwizzler).SignMetadataWithInvalidKey},

	{desc: "expired metadata", expectErrs: []interface{}{
		&certs.ErrValidationFail{}, signed.ErrExpired{}},
		swizzle: (*testutils.MetadataSwizzler).ExpireMetadata},

	{desc: "lower metadata version", expectErrs: []interface{}{
		&certs.ErrValidationFail{}, signed.ErrLowVersion{}},
		swizzle: func(s *testutils.MetadataSwizzler, role string) error {
			return s.OffsetMetadataVersion(role, -3)
		}},

	{desc: "insufficient signatures", expectErrs: []interface{}{
		&certs.ErrValidationFail{}, signed.ErrRoleThreshold{}},
		swizzle: func(s *testutils.MetadataSwizzler, role string) error {
			return s.SetThreshold(role, 2)
		}},
}

var _waysToMessUpServerRoot []swizzleExpectations

// We also want to remove a every role from root once, or remove the role's keys.
// This function generates once and caches the result for later re-use.
func waysToMessUpServerRoot() []swizzleExpectations {
	if _waysToMessUpServerRoot == nil {
		_waysToMessUpServerRoot = waysToMessUpServer
		for _, roleName := range data.BaseRoles {
			_waysToMessUpServerRoot = append(_waysToMessUpServerRoot,
				swizzleExpectations{
					desc: fmt.Sprintf("no %s keys", roleName),
					expectErrs: []interface{}{
						&certs.ErrValidationFail{}, signed.ErrRoleThreshold{}},
					swizzle: func(s *testutils.MetadataSwizzler, role string) error {
						return s.MutateRoot(func(r *data.Root) {
							r.Roles[roleName].KeyIDs = []string{}
						})
					}},
				swizzleExpectations{
					desc:       fmt.Sprintf("no %s role", roleName),
					expectErrs: []interface{}{data.ErrInvalidMetadata{}},
					swizzle: func(s *testutils.MetadataSwizzler, role string) error {
						return s.MutateRoot(func(r *data.Root) { delete(r.Roles, roleName) })
					}},
			)
		}
	}
	return _waysToMessUpServerRoot
}

// If there's no local cache, we go immediately to check the remote server for
// root, and if it invalid (corrupted), we cannot update.  This happens
// with and without a force check (update for write).
func TestUpdateRootRemoteCorruptedNoLocalCache(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode")
	}

	for _, testData := range waysToMessUpServerRoot() {
		if testData.desc == "insufficient signatures" {
			// Currently if we download the root during the bootstrap phase,
			// we don't check for enough signatures to meet the threshold.  We
			// are also not entirely sure if we want to support threshold.
			continue
		}

		testUpdateRemoteCorruptValidChecksum(t, updateOpts{
			forWrite: false,
			role:     data.CanonicalRootRole,
		}, testData, true)
		testUpdateRemoteCorruptValidChecksum(t, updateOpts{
			forWrite: true,
			role:     data.CanonicalRootRole,
		}, testData, true)
	}
}

// Having a local cache, if the server has the same data (timestamp has not changed),
// should succeed in all cases if whether forWrite (force check) is true or not
// because the fact that the timestamp hasn't changed should mean that we don't
// have to re-download the root.
func TestUpdateRootRemoteCorruptedCanUseLocalCache(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode")
	}

	for _, testData := range waysToMessUpServerRoot() {
		testUpdateRemoteCorruptValidChecksum(t, updateOpts{
			localCache: true,
			forWrite:   false,
			role:       data.CanonicalRootRole,
		}, testData, false)
		testUpdateRemoteCorruptValidChecksum(t, updateOpts{
			localCache: true,
			forWrite:   true,
			role:       data.CanonicalRootRole,
		}, testData, false)
	}
}

// Having a local cache, if the server has new same data should fail in all cases
// because the metadata is re-downloaded.
func TestUpdateRootRemoteCorruptedCannotUseLocalCache(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode")
	}
	for _, testData := range waysToMessUpServerRoot() {
		testUpdateRemoteCorruptValidChecksum(t, updateOpts{
			serverHasNewData: true,
			localCache:       true,
			forWrite:         false,
			role:             data.CanonicalRootRole,
		}, testData, true)
		testUpdateRemoteCorruptValidChecksum(t, updateOpts{
			serverHasNewData: true,
			localCache:       true,
			forWrite:         true,
			role:             data.CanonicalRootRole,
		}, testData, true)
	}
}

func waysToMessUpServerNonRootPerRole(t *testing.T) map[string][]swizzleExpectations {
	perRoleSwizzling := make(map[string][]swizzleExpectations)
	for _, missing := range []string{data.CanonicalRootRole, data.CanonicalTargetsRole} {
		perRoleSwizzling[data.CanonicalSnapshotRole] = append(
			perRoleSwizzling[data.CanonicalSnapshotRole],
			swizzleExpectations{
				desc:       fmt.Sprintf("snapshot missing root meta checksum"),
				expectErrs: []interface{}{data.ErrInvalidMetadata{}},
				swizzle: func(s *testutils.MetadataSwizzler, role string) error {
					return s.MutateSnapshot(func(sn *data.Snapshot) {
						delete(sn.Meta, missing)
					})
				},
			})
	}
	perRoleSwizzling[data.CanonicalTargetsRole] = []swizzleExpectations{{
		desc:       fmt.Sprintf("target missing delegations data"),
		expectErrs: []interface{}{client.ErrChecksumMismatch{}},
		swizzle: func(s *testutils.MetadataSwizzler, role string) error {
			return s.MutateTargets(func(tg *data.Targets) {
				tg.Delegations.Roles = tg.Delegations.Roles[1:]
			})
		},
	}}
	perRoleSwizzling[data.CanonicalTimestampRole] = []swizzleExpectations{{
		desc:       fmt.Sprintf("timestamp missing snapshot meta checksum"),
		expectErrs: []interface{}{data.ErrInvalidMetadata{}},
		swizzle: func(s *testutils.MetadataSwizzler, role string) error {
			return s.MutateTimestamp(func(ts *data.Timestamp) {
				delete(ts.Meta, data.CanonicalSnapshotRole)
			})
		},
	}}
	perRoleSwizzling["targets/a"] = []swizzleExpectations{{
		desc:       fmt.Sprintf("delegation has invalid role"),
		expectErrs: []interface{}{data.ErrInvalidMetadata{}},
		swizzle: func(s *testutils.MetadataSwizzler, role string) error {
			return s.MutateTargets(func(tg *data.Targets) {
				var keyIDs []string
				for k := range tg.Delegations.Keys {
					keyIDs = append(keyIDs, k)
				}
				// add the keys from root too
				rootMeta, err := s.MetadataCache.GetMeta(data.CanonicalRootRole, -1)
				require.NoError(t, err)

				signedRoot := &data.SignedRoot{}
				require.NoError(t, json.Unmarshal(rootMeta, signedRoot))

				for k := range signedRoot.Signed.Keys {
					keyIDs = append(keyIDs, k)
				}

				// add an invalid role (root) to delegation
				tg.Delegations.Roles = append(tg.Delegations.Roles,
					&data.Role{RootRole: data.RootRole{KeyIDs: keyIDs, Threshold: 1},
						Name: data.CanonicalRootRole})
			})
		},
	}}
	return perRoleSwizzling
}

// If there's no local cache, we just download from the server and if anything
// is corrupt, we cannot update.
func TestUpdateNonRootRemoteCorruptedNoLocalCache(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode")
	}
	for _, role := range append(data.BaseRoles, "targets/a", "targets/a/b") {
		if role == data.CanonicalRootRole {
			continue
		}
		for _, testData := range waysToMessUpServer {
			testUpdateRemoteCorruptValidChecksum(t, updateOpts{
				role: role,
			}, testData, true)
		}
	}
	for role, expectations := range waysToMessUpServerNonRootPerRole(t) {
		for _, testData := range expectations {
			switch role {
			case data.CanonicalSnapshotRole:
				testUpdateRemoteCorruptValidChecksum(t, updateOpts{
					role: role,
				}, testData, true)
			case data.CanonicalTargetsRole:
				// if there are no delegation target roles, we're fine, we just don't
				// download them
				testUpdateRemoteCorruptValidChecksum(t, updateOpts{
					role: role,
				}, testData, false)
			case data.CanonicalTimestampRole:
				testUpdateRemoteCorruptValidChecksum(t, updateOpts{
					role: role,
				}, testData, true)
			case "targets/a":
				testUpdateRemoteCorruptValidChecksum(t, updateOpts{
					role: role,
				}, testData, true)
			}
		}
	}
}

// Having a local cache, if the server has the same data (timestamp has not changed),
// should succeed in all cases if whether forWrite (force check) is true or not.
// If the timestamp is fine, it hasn't changed and we don't have to download
// anything. If it's broken, we used the cached timestamp and again download
// nothing.
func TestUpdateNonRootRemoteCorruptedCanUseLocalCache(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode")
	}
	for _, role := range append(data.BaseRoles, "targets/a", "targets/a/b") {
		if role == data.CanonicalRootRole {
			continue
		}
		for _, testData := range waysToMessUpServer {
			testUpdateRemoteCorruptValidChecksum(t, updateOpts{
				localCache: true,
				role:       role,
			}, testData, false)
		}
	}
	for role, expectations := range waysToMessUpServerNonRootPerRole(t) {
		for _, testData := range expectations {

			switch role {
			case data.CanonicalSnapshotRole:
				testUpdateRemoteCorruptValidChecksum(t, updateOpts{
					localCache: true,
					role:       role,
				}, testData, false)
			case data.CanonicalTargetsRole:
				testUpdateRemoteCorruptValidChecksum(t, updateOpts{
					localCache: true,
					role:       role,
				}, testData, false)
			case data.CanonicalTimestampRole:
				testUpdateRemoteCorruptValidChecksum(t, updateOpts{
					localCache: true,
					role:       role,
				}, testData, false)
			case "targets/a":
				testUpdateRemoteCorruptValidChecksum(t, updateOpts{
					localCache: true,
					role:       role,
				}, testData, false)
			}
		}
	}
}

// Having a local cache, if the server has new same data should fail in all cases
// (except if we modify the timestamp) because the metadata is re-downloaded.
// In the case of the timestamp, we'd default to our cached timestamp, and
// not have to redownload anything (usually)
func TestUpdateNonRootRemoteCorruptedCannotUseLocalCache(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode")
	}
	for _, role := range append(data.BaseRoles, "targets/a", "targets/a/b") {
		if role == data.CanonicalRootRole {
			continue
		}
		for _, testData := range waysToMessUpServer {
			// in general the cached timsestamp will always succeed, but if the threshold has been
			// increased, it fails because when we download the new timestamp, it validates as per our
			// previous root.  But the root hash doesn't match.  So we download a new root and
			// try the update again.  In this case, both the old and new timestamps won't have enough
			// signatures.
			shouldFail := role != data.CanonicalTimestampRole || testData.desc == "insufficient signatures"
			testUpdateRemoteCorruptValidChecksum(t, updateOpts{
				serverHasNewData: true,
				localCache:       true,
				role:             role,
			}, testData, shouldFail)
		}
	}

	for role, expectations := range waysToMessUpServerNonRootPerRole(t) {
		for _, testData := range expectations {
			switch role {
			case data.CanonicalSnapshotRole:
				testUpdateRemoteCorruptValidChecksum(t, updateOpts{
					serverHasNewData: true,
					localCache:       true,
					role:             role,
				}, testData, true)
			case data.CanonicalTargetsRole:
				// if there are no delegation target roles, we're fine, we just don't
				// download them
				testUpdateRemoteCorruptValidChecksum(t, updateOpts{
					serverHasNewData: true,
					localCache:       true,
					role:             role,
				}, testData, false)
			case data.CanonicalTimestampRole:
				// If the timestamp is invalid, we just default to the previous
				// cached version of the timestamp, so the update succeeds
				testUpdateRemoteCorruptValidChecksum(t, updateOpts{
					serverHasNewData: true,
					localCache:       true,
					role:             role,
				}, testData, false)
			case "targets/a":
				testUpdateRemoteCorruptValidChecksum(t, updateOpts{
					serverHasNewData: true,
					localCache:       true,
					role:             role,
				}, testData, true)
			}
		}
	}
}

func testUpdateRemoteCorruptValidChecksum(t *testing.T, opts updateOpts, expt swizzleExpectations, shouldErr bool) {
	_, serverSwizzler := newServerSwizzler(t)
	ts := readOnlyServer(t, serverSwizzler.MetadataCache, http.StatusNotFound, "docker.com/notary")
	defer ts.Close()

	repo := newBlankRepo(t, ts.URL)
	defer os.RemoveAll(repo.baseDir)

	if opts.localCache {
		_, err := repo.Update(false)
		require.NoError(t, err)
	}

	if opts.serverHasNewData {
		bumpVersions(t, serverSwizzler, 1)
	}

	msg := fmt.Sprintf("swizzling %s to return: %v (forWrite: %v)", opts.role, expt.desc, opts.forWrite)

	require.NoError(t, expt.swizzle(serverSwizzler, opts.role),
		"failed %s", msg)

	// update the snapshot and timestamp hashes to make sure it's not an involuntary checksum failure
	// unless we want the server to not actually have any new data
	if !opts.localCache || opts.serverHasNewData {
		// we don't want to sign if we are trying to swizzle one of these roles to
		// have a different signature - updating hashes would be pointless (because
		// nothing else has changed) and would just overwrite the signature.
		isSignatureSwizzle := expt.desc == "invalid signatures" || expt.desc == "meta signed by wrong key"
		// just try to do these - if they fail (probably because they've been swizzled), that's fine
		if opts.role != data.CanonicalSnapshotRole || !isSignatureSwizzle {
			// if we are purposely editing out some snapshot metadata, don't re-generate
			if !strings.HasPrefix(expt.desc, "snapshot missing") {
				serverSwizzler.UpdateSnapshotHashes()
			}
		}
		if opts.role != data.CanonicalTimestampRole || !isSignatureSwizzle {
			// if we are purposely editing out some timestamp metadata, don't re-generate
			if !strings.HasPrefix(expt.desc, "timestamp missing") {
				serverSwizzler.UpdateTimestampHash()
			}
		}
	}
	_, err := repo.Update(opts.forWrite)
	if shouldErr {
		require.Error(t, err, "expected failure updating when %s", msg)

		errType := reflect.TypeOf(err)
		isExpectedType := false
		var expectedTypes []string
		for _, expectErr := range expt.expectErrs {
			expectedType := reflect.TypeOf(expectErr)
			isExpectedType = isExpectedType || errType == expectedType
			expectedTypes = append(expectedTypes, expectedType.String())
		}
		require.True(t, isExpectedType, "expected one of %v when %s: got %s",
			expectedTypes, msg, errType)

	} else {
		require.NoError(t, err, "expected no failure updating when %s", msg)
	}
}

// If the local root is corrupt, and the remote root is corrupt, we should fail
// to update.  Note - this one is really slow.
func TestUpdateLocalAndRemoteRootCorrupt(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode")
	}
	for _, localExpt := range waysToMessUpLocalMetadata {
		for _, serverExpt := range waysToMessUpServer {
			if localExpt.desc == "expired metadata" && serverExpt.desc == "lower metadata version" {
				// TODO: bug right now where if the local metadata is invalid, we just download a
				// new version - we verify the signatures and everything, but don't check the version
				// against the previous if we can
				continue
			}
			if serverExpt.desc == "insufficient signatures" {
				// Currently if we download the root during the bootstrap phase,
				// we don't check for enough signatures to meet the threshold.
				// We are also not sure if we want to support thresholds.
				continue
			}
			testUpdateLocalAndRemoteRootCorrupt(t, true, localExpt, serverExpt)
			testUpdateLocalAndRemoteRootCorrupt(t, false, localExpt, serverExpt)
		}
	}
}

func testUpdateLocalAndRemoteRootCorrupt(t *testing.T, forWrite bool, localExpt, serverExpt swizzleExpectations) {
	_, serverSwizzler := newServerSwizzler(t)
	ts := readOnlyServer(t, serverSwizzler.MetadataCache, http.StatusNotFound, "docker.com/notary")
	defer ts.Close()

	repo := newBlankRepo(t, ts.URL)
	defer os.RemoveAll(repo.baseDir)

	// get local cache
	_, err := repo.Update(false)
	require.NoError(t, err)
	repoSwizzler := &testutils.MetadataSwizzler{
		Gun:           serverSwizzler.Gun,
		MetadataCache: repo.fileStore,
		CryptoService: serverSwizzler.CryptoService,
		Roles:         serverSwizzler.Roles,
	}

	bumpVersions(t, serverSwizzler, 1)

	require.NoError(t, localExpt.swizzle(repoSwizzler, data.CanonicalRootRole),
		"failed to swizzle local root to %s", localExpt.desc)
	require.NoError(t, serverExpt.swizzle(serverSwizzler, data.CanonicalRootRole),
		"failed to swizzle remote root to %s", serverExpt.desc)

	// update the hashes on both
	require.NoError(t, serverSwizzler.UpdateSnapshotHashes())
	require.NoError(t, serverSwizzler.UpdateTimestampHash())

	msg := fmt.Sprintf("swizzling root locally to return <%v> and remotely to return: <%v> (forWrite: %v)",
		localExpt.desc, serverExpt.desc, forWrite)

	_, err = repo.Update(forWrite)
	require.Error(t, err, "expected failure updating when %s", msg)

	errType := reflect.TypeOf(err)
	isExpectedType := false
	var expectedTypes []string
	for _, expectErr := range serverExpt.expectErrs {
		expectedType := reflect.TypeOf(expectErr)
		isExpectedType = isExpectedType || errType == expectedType
		expectedTypes = append(expectedTypes, expectedType.String())
	}
	require.True(t, isExpectedType, "expected one of %v when %s: got %s",
		expectedTypes, msg, errType)
}
