package cmds

import (
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"

	"encoding/json"

	"github.com/fabric8io/gofabric8/util"
	"github.com/spf13/cobra"
	cmdutil "k8s.io/kubernetes/pkg/kubectl/cmd/util"
)

type Namespace struct {
	Name string
	Type string
}

type AllTenantJson struct {
	Namespaces []Namespace `json:"namespaces"`
}

type uninstallFlags struct {
	cmd  *cobra.Command
	args []string

	confirm bool
}

func NewCmdUninstall(f cmdutil.Factory) *cobra.Command {
	p := &uninstallFlags{}
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Delete all your tenant resources",
		Run: func(cmd *cobra.Command, args []string) {
			p.cmd = cmd
			p.args = args
			if cmd.Flags().Lookup(yesFlag).Value.String() == "true" {
				p.confirm = true
			}
			handleError(p.uninstall(f))
		},
	}
	return cmd
}

func (p *uninstallFlags) uninstall(f cmdutil.Factory) error {
	url := "http://f8tenant-fabric8.openshift.chmouel.com/api/tenant/all"
	if !p.confirm {
		confirm := ""
		util.Warn("WARNING this command will delete all resources from *ALL TENANTS*\n")
		util.Warn("\nContinue [y/N]: ")
		fmt.Scanln(&confirm)
		if confirm != "y" {
			util.Warn("Aborted\n")
			return nil
		}
	}
	cfg, err := f.ClientConfig()
	cmdutil.CheckErr(err)

	client := &http.Client{}
	req, err := http.NewRequest("GET", url, nil)
	cmdutil.CheckErr(err)

	req.Header.Set("Authorization", "Bearer "+cfg.BearerToken)
	res, err := client.Do(req)
	cmdutil.CheckErr(err)

	body, err := ioutil.ReadAll(res.Body)
	defer res.Body.Close()
	cmdutil.CheckErr(err)

	if res.StatusCode >= 300 {
		cmdutil.CheckErr(errors.New(fmt.Sprintf("Failed to GET all tenants from init-tenant service on %s got status code to: %d output: %s", url, res.StatusCode, string(body))))
	}

	var alltenants AllTenantJson
	gofabric8Cli := "gofabric8"

	json.Unmarshal(body, &alltenants)

	for _, value := range alltenants.Namespaces {
		if value.Type == "user" {
			commands := []string{"delete", "tenant", "-t", value.Name, "--as=system:admin"}
			if p.confirm {
				commands = append(commands, "-y")
			}

			err = runCommand(gofabric8Cli, commands...)
			cmdutil.CheckErr(err)
		}
	}

	return nil
}
