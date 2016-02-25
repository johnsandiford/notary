package main

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"io/ioutil"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/docker/go-connections/tlsconfig"
	"github.com/docker/notary/passphrase"
	"github.com/docker/notary/server/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// the default location for the config file is in ~/.notary/config.json - even if it doesn't exist.
func TestNotaryConfigFileDefault(t *testing.T) {
	commander := &notaryCommander{
		getRetriever: func() passphrase.Retriever { return passphrase.ConstantRetriever("pass") },
	}

	config, err := commander.parseConfig()
	assert.NoError(t, err)
	configFileUsed := config.ConfigFileUsed()
	assert.True(t, strings.HasSuffix(configFileUsed,
		filepath.Join(".notary", "config.json")), "Unknown config file: %s", configFileUsed)
}

// the default server address is notary-server
func TestRemoteServerDefault(t *testing.T) {
	tempDir := tempDirWithConfig(t, "{}")
	defer os.RemoveAll(tempDir)
	configFile := filepath.Join(tempDir, "config.json")

	commander := &notaryCommander{
		getRetriever: func() passphrase.Retriever { return passphrase.ConstantRetriever("pass") },
	}

	// set a blank config file, so it doesn't check ~/.notary/config.json by default
	// and execute a random command so that the flags are parsed
	cmd := commander.GetCommand()
	cmd.SetArgs([]string{"-c", configFile, "list"})
	cmd.SetOutput(new(bytes.Buffer)) // eat the output
	cmd.Execute()

	config, err := commander.parseConfig()
	assert.NoError(t, err)
	assert.Equal(t, "https://notary-server:4443", getRemoteTrustServer(config))
}

// providing a config file uses the config file's server url instead
func TestRemoteServerUsesConfigFile(t *testing.T) {
	tempDir := tempDirWithConfig(t, `{"remote_server": {"url": "https://myserver"}}`)
	defer os.RemoveAll(tempDir)
	configFile := filepath.Join(tempDir, "config.json")

	commander := &notaryCommander{
		getRetriever: func() passphrase.Retriever { return passphrase.ConstantRetriever("pass") },
	}

	// set a config file, so it doesn't check ~/.notary/config.json by default,
	// and execute a random command so that the flags are parsed
	cmd := commander.GetCommand()
	cmd.SetArgs([]string{"-c", configFile, "list"})
	cmd.SetOutput(new(bytes.Buffer)) // eat the output
	cmd.Execute()

	config, err := commander.parseConfig()
	assert.NoError(t, err)
	assert.Equal(t, "https://myserver", getRemoteTrustServer(config))
}

// a command line flag overrides the config file's server url
func TestRemoteServerCommandLineFlagOverridesConfig(t *testing.T) {
	tempDir := tempDirWithConfig(t, `{"remote_server": {"url": "https://myserver"}}`)
	defer os.RemoveAll(tempDir)
	configFile := filepath.Join(tempDir, "config.json")

	commander := &notaryCommander{
		getRetriever: func() passphrase.Retriever { return passphrase.ConstantRetriever("pass") },
	}

	// set a config file, so it doesn't check ~/.notary/config.json by default,
	// and execute a random command so that the flags are parsed
	cmd := commander.GetCommand()
	cmd.SetArgs([]string{"-c", configFile, "-s", "http://overridden", "list"})
	cmd.SetOutput(new(bytes.Buffer)) // eat the output
	cmd.Execute()

	config, err := commander.parseConfig()
	assert.NoError(t, err)
	assert.Equal(t, "http://overridden", getRemoteTrustServer(config))
}

var exampleValidCommands = []string{
	"init repo",
	"list repo",
	"status repo",
	"publish repo",
	"add repo v1 somefile",
	"verify repo v1",
	"key list",
	"key rotate repo",
	"key generate rsa",
	"key backup tempfile.zip",
	"key export e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855 backup.pem",
	"key restore tempfile.zip",
	"key import backup.pem",
	"key remove e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
	"key passwd e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
	"cert list",
	"cert remove e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
	"delegation list repo",
	"delegation add repo targets/releases path/to/pem/file.pem",
	"delegation remove repo targets/releases",
}

// config parsing bugs are propagated in all commands
func TestConfigParsingErrorsPropagatedByCommands(t *testing.T) {
	tempdir, err := ioutil.TempDir("", "empty-dir")
	require.NoError(t, err)
	defer os.RemoveAll(tempdir)

	for _, args := range exampleValidCommands {
		b := new(bytes.Buffer)
		cmd := NewNotaryCommand()
		cmd.SetOutput(b)

		cmd.SetArgs(append(
			[]string{"-c", filepath.Join(tempdir, "idonotexist.json"), "-d", tempdir},
			strings.Fields(args)...))
		err = cmd.Execute()

		require.Error(t, err, "expected error when running `notary %s`", args)
		require.Contains(t, err.Error(), "error opening config file", "running `notary %s`", args)
		require.NotContains(t, b.String(), "Usage:")
	}
}

// insufficient arguments produce an error before any parsing of configs happens
func TestInsufficientArgumentsReturnsErrorAndPrintsUsage(t *testing.T) {
	tempdir, err := ioutil.TempDir("", "empty-dir")
	require.NoError(t, err)
	defer os.RemoveAll(tempdir)

	for _, args := range exampleValidCommands {
		b := new(bytes.Buffer)
		cmd := NewNotaryCommand()
		cmd.SetOutput(b)

		arglist := strings.Fields(args)
		if args == "key list" || args == "cert list" || args == "key generate rsa" {
			// in these case, "key" or "cert" or "key generate" are valid commands, so add an arg to them instead
			arglist = append(arglist, "extraArg")
		} else {
			arglist = arglist[:len(arglist)-1]
		}

		invalid := strings.Join(arglist, " ")

		cmd.SetArgs(append(
			[]string{"-c", filepath.Join(tempdir, "idonotexist.json"), "-d", tempdir}, arglist...))
		err = cmd.Execute()

		require.NotContains(t, err.Error(), "error opening config file", "running `notary %s`", invalid)
		// it's a usage error, so the usage is printed
		require.Contains(t, b.String(), "Usage:", "expected usage when running `notary %s`", invalid)
	}
}

// The bare notary command and bare subcommands all print out usage
func TestBareCommandPrintsUsageAndNoError(t *testing.T) {
	tempdir, err := ioutil.TempDir("", "empty-dir")
	require.NoError(t, err)
	defer os.RemoveAll(tempdir)

	// just the notary command
	b := new(bytes.Buffer)
	cmd := NewNotaryCommand()
	cmd.SetOutput(b)

	cmd.SetArgs([]string{"-c", filepath.Join(tempdir, "idonotexist.json")})
	require.NoError(t, cmd.Execute(), "Expected no error from a help request")
	// usage is printed
	require.Contains(t, b.String(), "Usage:", "expected usage when running `notary`")

	// notary key, notary cert, and notary delegation
	for _, bareCommand := range []string{"key", "cert", "delegation"} {
		b := new(bytes.Buffer)
		cmd := NewNotaryCommand()
		cmd.SetOutput(b)

		cmd.SetArgs([]string{"-c", filepath.Join(tempdir, "idonotexist.json"), bareCommand})
		require.NoError(t, cmd.Execute(), "Expected no error from a help request")
		// usage is printed
		require.Contains(t, b.String(), "Usage:", "expected usage when running `notary %s`", bareCommand)
	}
}

type recordingMetaStore struct {
	gotten []string
	storage.MemStorage
}

// GetCurrent gets the metadata from the underlying MetaStore, but also records
// that the metadata was requested
func (r *recordingMetaStore) GetCurrent(gun, role string) (data []byte, err error) {
	r.gotten = append(r.gotten, fmt.Sprintf("%s.%s", gun, role))
	return r.MemStorage.GetCurrent(gun, role)
}

// GetChecksum gets the metadata from the underlying MetaStore, but also records
// that the metadata was requested
func (r *recordingMetaStore) GetChecksum(gun, role, checksum string) (data []byte, err error) {
	r.gotten = append(r.gotten, fmt.Sprintf("%s.%s", gun, role))
	return r.MemStorage.GetChecksum(gun, role, checksum)
}

// the config can provide all the TLS information necessary - the root ca file,
// the tls client files - they are all relative to the directory of the config
// file, and not the cwd
func TestConfigFileTLSCannotBeRelativeToCWD(t *testing.T) {
	// Set up server that with a self signed cert
	var err error
	// add a handler for getting the root
	m := &recordingMetaStore{MemStorage: *storage.NewMemStorage()}
	s := httptest.NewUnstartedServer(setupServerHandler(m))
	s.TLS, err = tlsconfig.Server(tlsconfig.Options{
		CertFile:   "../../fixtures/notary-server.crt",
		KeyFile:    "../../fixtures/notary-server.key",
		CAFile:     "../../fixtures/root-ca.crt",
		ClientAuth: tls.RequireAndVerifyClientCert,
	})
	assert.NoError(t, err)
	s.StartTLS()
	defer s.Close()

	// test that a config file with certs that are relative to the cwd fail
	tempDir := tempDirWithConfig(t, fmt.Sprintf(`{
		"remote_server": {
			"url": "%s",
			"root_ca": "../../fixtures/root-ca.crt",
			"tls_client_cert": "../../fixtures/notary-server.crt",
			"tls_client_key": "../../fixtures/notary-server.key"
		}
	}`, s.URL))
	defer os.RemoveAll(tempDir)
	configFile := filepath.Join(tempDir, "config.json")

	// set a config file, so it doesn't check ~/.notary/config.json by default,
	// and execute a random command so that the flags are parsed
	cmd := NewNotaryCommand()
	cmd.SetArgs([]string{"-c", configFile, "list", "repo"})
	cmd.SetOutput(new(bytes.Buffer)) // eat the output
	err = cmd.Execute()
	assert.Error(t, err, "expected a failure due to TLS")
	assert.Contains(t, err.Error(), "TLS", "should have been a TLS error")

	// validate that we failed to connect and attempt any downloads at all
	assert.Len(t, m.gotten, 0)
}

// the config can provide all the TLS information necessary - the root ca file,
// the tls client files - they are all relative to the directory of the config
// file, and not the cwd, or absolute paths
func TestConfigFileTLSCanBeRelativeToConfigOrAbsolute(t *testing.T) {
	// Set up server that with a self signed cert
	var err error
	// add a handler for getting the root
	m := &recordingMetaStore{MemStorage: *storage.NewMemStorage()}
	s := httptest.NewUnstartedServer(setupServerHandler(m))
	s.TLS, err = tlsconfig.Server(tlsconfig.Options{
		CertFile:   "../../fixtures/notary-server.crt",
		KeyFile:    "../../fixtures/notary-server.key",
		CAFile:     "../../fixtures/root-ca.crt",
		ClientAuth: tls.RequireAndVerifyClientCert,
	})
	assert.NoError(t, err)
	s.StartTLS()
	defer s.Close()

	tempDir, err := ioutil.TempDir("", "config-test")
	assert.NoError(t, err)
	defer os.RemoveAll(tempDir)
	configFile, err := os.Create(filepath.Join(tempDir, "config.json"))
	assert.NoError(t, err)
	fmt.Fprintf(configFile, `{
		"remote_server": {
			"url": "%s",
			"root_ca": "root-ca.crt",
			"tls_client_cert": "%s",
			"tls_client_key": "notary-server.key"
		}
	}`, s.URL, filepath.Join(tempDir, "notary-server.crt"))
	configFile.Close()

	// copy the certs to be relative to the config directory
	for _, fname := range []string{"notary-server.crt", "notary-server.key", "root-ca.crt"} {
		content, err := ioutil.ReadFile(filepath.Join("../../fixtures", fname))
		assert.NoError(t, err)
		assert.NoError(t, ioutil.WriteFile(filepath.Join(tempDir, fname), content, 0766))
	}

	// set a config file, so it doesn't check ~/.notary/config.json by default,
	// and execute a random command so that the flags are parsed
	cmd := NewNotaryCommand()
	cmd.SetArgs([]string{"-c", configFile.Name(), "list", "repo"})
	cmd.SetOutput(new(bytes.Buffer)) // eat the output
	err = cmd.Execute()
	assert.Error(t, err, "there was no repository, so list should have failed")
	assert.NotContains(t, err.Error(), "TLS", "there was no TLS error though!")

	// validate that we actually managed to connect and attempted to download the root though
	assert.Len(t, m.gotten, 1)
	assert.Equal(t, m.gotten[0], "repo.root")
}

// Whatever TLS config is in the config file can be overridden by the command line
// TLS flags, which are relative to the CWD (not the config) or absolute
func TestConfigFileOverridenByCmdLineFlags(t *testing.T) {
	// Set up server that with a self signed cert
	var err error
	// add a handler for getting the root
	m := &recordingMetaStore{MemStorage: *storage.NewMemStorage()}
	s := httptest.NewUnstartedServer(setupServerHandler(m))
	s.TLS, err = tlsconfig.Server(tlsconfig.Options{
		CertFile:   "../../fixtures/notary-server.crt",
		KeyFile:    "../../fixtures/notary-server.key",
		CAFile:     "../../fixtures/root-ca.crt",
		ClientAuth: tls.RequireAndVerifyClientCert,
	})
	assert.NoError(t, err)
	s.StartTLS()
	defer s.Close()

	tempDir := tempDirWithConfig(t, fmt.Sprintf(`{
		"remote_server": {
			"url": "%s",
			"root_ca": "nope",
			"tls_client_cert": "nope",
			"tls_client_key": "nope"
		}
	}`, s.URL))
	defer os.RemoveAll(tempDir)
	configFile := filepath.Join(tempDir, "config.json")

	// set a config file, so it doesn't check ~/.notary/config.json by default,
	// and execute a random command so that the flags are parsed
	cwd, err := os.Getwd()
	assert.NoError(t, err)

	cmd := NewNotaryCommand()
	cmd.SetArgs([]string{
		"-c", configFile, "list", "repo",
		"--tlscacert", "../../fixtures/root-ca.crt",
		"--tlscert", filepath.Clean(filepath.Join(cwd, "../../fixtures/notary-server.crt")),
		"--tlskey", "../../fixtures/notary-server.key"})
	cmd.SetOutput(new(bytes.Buffer)) // eat the output
	err = cmd.Execute()
	assert.Error(t, err, "there was no repository, so list should have failed")
	assert.NotContains(t, err.Error(), "TLS", "there was no TLS error though!")

	// validate that we actually managed to connect and attempted to download the root though
	assert.Len(t, m.gotten, 1)
	assert.Equal(t, m.gotten[0], "repo.root")
}
