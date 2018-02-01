/**
 * Copyright (C) 2015 Red Hat, Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *         http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */
package cmds

import (
	"bufio"
	"fmt"
	"io/ioutil"
	"os"
	"strings"

	"github.com/fabric8io/gofabric8/client"
	"github.com/fabric8io/gofabric8/util"
	"github.com/spf13/cobra"
	"k8s.io/kubernetes/pkg/api"
	clientset "k8s.io/kubernetes/pkg/client/clientset_generated/internalclientset"
	cmdutil "k8s.io/kubernetes/pkg/kubectl/cmd/util"
)

var (
	removedExportedLine = []string{
		"selfLink", "resourceVersion", "uid", "creationTimestamp",
		"kubectl.kubernetes.io/last-applied-configuration:",
		"control-plane.alpha.kubernetes.io/leader:",
		"pv.kubernetes.io/",
		"volume.beta.kubernetes.io/",
		"volumeName"}
)

type erasePVCFlags struct {
	cmd    *cobra.Command
	args   []string
	userNS string

	volumeName string
}

// NewCmdErasePVC Erase PVC https://github.com/fabric8io/gofabric8/issues/598
func NewCmdErasePVC(f cmdutil.Factory) *cobra.Command {
	p := &erasePVCFlags{}
	cmd := &cobra.Command{
		Use:   "erase-pvc",
		Short: "Erase PVC",
		Long:  `Erase PVC`,

		Run: func(cmd *cobra.Command, args []string) {
			p.cmd = cmd
			p.args = args
			p.userNS = cmd.Flags().Lookup(namespaceFlag).Value.String()

			if len(p.args) != 1 {
				util.Fatal("We need a PVC to delete as argument.\n")
			}
			p.volumeName = p.args[0]

			handleError(p.erasePVC(f))
		},
	}
	cmd.PersistentFlags().StringP(namespaceFlag, "n", "", "The namespace where the PVC is located. Defaults to the current namespace")
	return cmd
}

// writeLines writes the lines to the given file.
func writeLines(lines []string, path string) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	w := bufio.NewWriter(file)
	for _, line := range lines {
		fmt.Fprintln(w, line)
	}
	return w.Flush()
}

func (p *erasePVCFlags) erasePVC(f cmdutil.Factory) (err error) {
	var userNS string

	c, cfg := client.NewClient(f)
	ns, _, _ := f.DefaultNamespace()
	oc, _ := client.NewOpenShiftClient(cfg)
	initSchema()

	userNS, err = detectCurrentUserNamespace(ns, c, oc)
	cmdutil.CheckErr(err)

	if p.userNS != "" {
		userNS = p.userNS
	}

	// NB(chmou): Trying to get this right, find the pods attached to the pvc before,
	// then Recreate the PVC and then delete the pods attached to it so it
	// attach to the new one. Get then all the pods attached to the service
	// attached to the pod attached to the volume (hard to explain in words you
	// may want to just read the code ;)
	attachedpods, err := findPodsAttachedtoPVC(p.volumeName, c, userNS)
	cmdutil.CheckErr(err)

	for _, pod := range attachedpods {
		serviceName, err := getLabelValueOfPod(c, ns, pod, "service")
		cmdutil.CheckErr(err)
		if serviceName != "" {
			sp, err := getPodsDependOfService(c, ns, serviceName)
			cmdutil.CheckErr(err)
			for _, v := range sp {
				attachedpods = append(attachedpods, v)
			}
		}
	}

	cmd := []string{"get", "-o", "yaml", "-n", userNS, "pvc", p.volumeName}
	output, err := runCommandWithOutput("kubectl", cmd...)
	if err != nil {
		util.Fatal("Error while running cmd: " + strings.Join(cmd, " ") + " Error: " + err.Error() + " Output: " + output + "\n")
	}

	inStatus := false
	scanner := bufio.NewScanner(strings.NewReader(output))
	var outputYAML []string
	for scanner.Scan() {
		text := scanner.Text()
		nsLine := strings.TrimSpace(text)

		stop := false
		for _, l := range removedExportedLine {
			if strings.HasPrefix(nsLine, l) {
				stop = true
			}
		}
		if stop {
			continue
		}
		if text == "status:" {
			inStatus = true
			continue
		}

		if inStatus && string(text[0]) != " " {
			inStatus = false
		} else if inStatus {
			continue
		}
		outputYAML = append(outputYAML, text)
	}
	tmpfile, err := ioutil.TempFile("", "gofabric8")
	cmdutil.CheckErr(err)

	err = writeLines(outputYAML, tmpfile.Name())
	cmdutil.CheckErr(err)

	cmd = []string{"delete", "-n", userNS, "pvc", p.volumeName}
	output, err = runCommandWithOutput("kubectl", cmd...)
	if err != nil {
		util.Fatal("Error while running cmd: " + strings.Join(cmd, " ") + " Error: " + err.Error() + " Output: " + output + "\n")
	}

	cmd = []string{"create", "-n", userNS, "-f", tmpfile.Name()}
	output, err = runCommandWithOutput("kubectl", cmd...)
	if err != nil {
		util.Fatal("Error while running cmd: " + strings.Join(cmd, " ") + " Error: " + err.Error() + " Output: " + output + "\n")
	}

	for _, pod := range attachedpods {
		cmd = []string{"delete", "-n", userNS, "pod", pod}
		output, err = runCommandWithOutput("kubectl", cmd...)
		if err != nil {
			util.Fatal("Error while running cmd: " + strings.Join(cmd, " ") + " Error: " + err.Error() + " Output: " + output + "\n")
		}
		util.Successf("Pod %s attached to %s has been deleted.\n", pod, p.volumeName)
	}

	util.Success("Volume: " + p.volumeName + " has been recreated.\n")
	os.Remove(tmpfile.Name())

	return
}

// findPodsAttachedtoPVC find all pods that are attached to a certain PVC,
// return a list of the pods name
func findPodsAttachedtoPVC(findVolume string, c *clientset.Clientset, ns string) (ret []string, err error) {
	pods, err := c.Pods(ns).List(api.ListOptions{})
	if err != nil {
		return
	}

	if pods != nil {
		for _, item := range pods.Items {
			for _, volume := range item.Spec.Volumes {
				if volume.Name == findVolume {
					ret = append(ret, item.GetName())
				}
			}
		}
	}
	return
}
