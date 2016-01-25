package main

import (
	"fmt"
	"io/ioutil"

	"github.com/docker/notary"
	notaryclient "github.com/docker/notary/client"
	"github.com/docker/notary/passphrase"
	"github.com/docker/notary/trustmanager"
	"github.com/docker/notary/tuf/data"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var cmdDelegationTemplate = usageTemplate{
	Use:   "delegation",
	Short: "Operates on delegations.",
	Long:  `Operations on TUF delegations.`,
}

var cmdDelegationListTemplate = usageTemplate{
	Use:   "list [ GUN ]",
	Short: "Lists delegations for the Global Unique Name.",
	Long:  "Lists all delegations known to notary for a specific Global Unique Name.",
}

var cmdDelegationRemoveTemplate = usageTemplate{
	Use:   "remove [ GUN ] [ Role ] <KeyID 1> ...",
	Short: "Remove KeyID(s) from the specified Role delegation.",
	Long:  "Remove KeyID(s) from the specified Role delegation in a specific Global Unique Name.",
}

var cmdDelegationAddTemplate = usageTemplate{
	Use:   "add [ GUN ] [ Role ] <PEM file path 1> ...",
	Short: "Add a keys to delegation using the provided public key certificate PEMs.",
	Long:  "Add a keys to delegation using the provided public key certificate PEMs in a specific Global Unique Name.",
}

var paths []string
var removeAll, removeYes bool

type delegationCommander struct {
	// these need to be set
	configGetter func() *viper.Viper
	retriever    passphrase.Retriever
}

func (d *delegationCommander) GetCommand() *cobra.Command {
	cmd := cmdDelegationTemplate.ToCommand(nil)
	cmd.AddCommand(cmdDelegationListTemplate.ToCommand(d.delegationsList))

	cmdRemDelg := cmdDelegationRemoveTemplate.ToCommand(d.delegationRemove)
	cmdRemDelg.Flags().StringSliceVar(&paths, "paths", nil, "List of paths to remove")
	cmdRemDelg.Flags().BoolVarP(&removeYes, "yes", "y", false, "Answer yes to the removal question (no confirmation)")
	cmd.AddCommand(cmdRemDelg)

	cmdAddDelg := cmdDelegationAddTemplate.ToCommand(d.delegationAdd)
	cmdAddDelg.Flags().StringSliceVar(&paths, "paths", nil, "List of paths to add")
	cmd.AddCommand(cmdAddDelg)
	return cmd
}

// delegationsList lists all the delegations for a particular GUN
func (d *delegationCommander) delegationsList(cmd *cobra.Command, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf(
			"Please provide a Global Unique Name as an argument to list")
	}

	config := d.configGetter()

	gun := args[0]

	// initialize repo with transport to get latest state of the world before listing delegations
	nRepo, err := notaryclient.NewNotaryRepository(config.GetString("trust_dir"), gun, getRemoteTrustServer(config), getTransport(config, gun, true), retriever)
	if err != nil {
		return err
	}

	delegationRoles, err := nRepo.GetDelegationRoles()
	if err != nil {
		return fmt.Errorf("Error retrieving delegation roles for repository %s: %v", gun, err)
	}

	cmd.Println("")
	prettyPrintRoles(delegationRoles, cmd.Out())
	cmd.Println("")
	return nil
}

// delegationRemove removes a public key from a specific role in a GUN
func (d *delegationCommander) delegationRemove(cmd *cobra.Command, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("must specify the Global Unique Name and the role of the delegation along with optional keyIDs and/or a list of paths to remove")
	}

	config := d.configGetter()

	gun := args[0]
	role := args[1]

	// If we're only given the gun and the role, attempt to remove all data for this delegation
	if len(args) == 2 && paths == nil {
		removeAll = true
	}

	keyIDs := []string{}
	// Change nil paths to empty slice for TUF
	if paths == nil {
		paths = []string{}
	}

	if len(args) > 2 {
		keyIDs = args[2:]
	}

	// no online operations are performed by add so the transport argument
	// should be nil
	nRepo, err := notaryclient.NewNotaryRepository(config.GetString("trust_dir"), gun, getRemoteTrustServer(config), nil, retriever)
	if err != nil {
		return err
	}

	if removeAll {
		cmd.Println("\nAre you sure you want to remove all data for this delegation? (yes/no)")
		// Ask for confirmation before force removing delegation
		if !removeYes {
			confirmed := askConfirm()
			if !confirmed {
				fatalf("Aborting action.")
			}
		} else {
			cmd.Println("Confirmed `yes` from flag")
		}
	}

	// Remove the delegation from the repository
	err = nRepo.RemoveDelegation(role, keyIDs, paths, removeAll)
	if err != nil {
		return fmt.Errorf("failed to remove delegation: %v", err)
	}
	cmd.Println("")
	if removeAll {
		cmd.Printf("Forced removal (including all keys and paths) of delegation role %s to repository \"%s\" staged for next publish.\n", role, gun)
	}
	cmd.Printf(
		"Removal of delegation role %s with keys %s and paths %s, to repository \"%s\" staged for next publish.\n",
		role, keyIDs, paths, gun)
	cmd.Println("")

	return nil
}

// delegationAdd creates a new delegation by adding a public key from a certificate to a specific role in a GUN
func (d *delegationCommander) delegationAdd(cmd *cobra.Command, args []string) error {
	if len(args) < 2 || len(args) < 3 && paths == nil {
		return fmt.Errorf("must specify the Global Unique Name and the role of the delegation along with the public key certificate paths and/or a list of paths to add")
	}

	config := d.configGetter()

	gun := args[0]
	role := args[1]

	pubKeys := []data.PublicKey{}
	if len(args) > 2 {
		pubKeyPaths := args[2:]
		for _, pubKeyPath := range pubKeyPaths {
			// Read public key bytes from PEM file
			pubKeyBytes, err := ioutil.ReadFile(pubKeyPath)
			if err != nil {
				return fmt.Errorf("unable to read public key from file: %s", pubKeyPath)
			}

			// Parse PEM bytes into type PublicKey
			pubKey, err := trustmanager.ParsePEMPublicKey(pubKeyBytes)
			if err != nil {
				return fmt.Errorf("unable to parse valid public key certificate from PEM file %s: %v", pubKeyPath, err)
			}
			pubKeys = append(pubKeys, pubKey)
		}
	}

	// no online operations are performed by add so the transport argument
	// should be nil
	nRepo, err := notaryclient.NewNotaryRepository(config.GetString("trust_dir"), gun, getRemoteTrustServer(config), nil, retriever)
	if err != nil {
		return err
	}

	// Add the delegation to the repository
	// Sets threshold to 1 since we only added one key - thresholds are not currently fully supported, though
	// one can use additional client-side validation to check for signatures from a quorum of varying delegation roles
	err = nRepo.AddDelegation(role, notary.MinThreshold, pubKeys, paths)
	if err != nil {
		return fmt.Errorf("failed to create delegation: %v", err)
	}

	// Make keyID slice for better CLI print
	pubKeyIDs := []string{}
	for _, pubKey := range pubKeys {
		pubKeyIDs = append(pubKeyIDs, pubKey.ID())
	}

	cmd.Println("")
	cmd.Printf(
		"Addition of delegation role %s with keys %s and paths %s, to repository \"%s\" staged for next publish.\n",
		role, pubKeyIDs, paths, gun)
	cmd.Println("")
	return nil
}
