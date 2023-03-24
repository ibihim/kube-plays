package main

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"text/template"
)

const (
	experimentPath = "./template/experiment.yaml"
	sccPath        = "./template/scc.yaml"
	outPath        = "./out"

	wildcardUser   = "ibihim"
	unconfinedUser = "kostrows"
)

type SCCTemplate struct {
	Users           []string
	SeccompProfiles []string
}

type DeploymentTemplate struct {
	Namespace      string
	Annotations    []string
	PodField       string
	ContainerField string
}

func main() {
	if err := app(); err != nil {
		panic(err)
	}
}

func app() error {
	if err := os.RemoveAll(outPath); err != nil {
		return err
	}

	if err := os.MkdirAll(outPath, 0755); err != nil {
		return err
	}

	sccUsers := []*SCCTemplate{
		{
			Users:           []string{wildcardUser},
			SeccompProfiles: []string{`"*"`},
		},
		{
			Users:           []string{unconfinedUser},
			SeccompProfiles: []string{"Unconfined"},
		},
	}

	for _, sccData := range sccUsers {
		var yamlBuilder bytes.Buffer

		sccTemplate, err := ioutil.ReadFile(sccPath)
		if err != nil {
			return err
		}
		scc, err := template.New("SCCs").Parse(string(sccTemplate))
		if err != nil {
			return err
		}
		scc.Execute(&yamlBuilder, sccData)

		outputPath := filepath.Join(outPath, fmt.Sprintf("scc-%s.yaml", sccData.Users[0]))
		if err := ioutil.WriteFile(outputPath, yamlBuilder.Bytes(), 0644); err != nil {
			return err
		}
	}

	experiments := []*DeploymentTemplate{
		{
			Namespace: "wildcard-pod-no-annotations-no-fields",
		},
		{
			Namespace: "unconfined-pod-no-annotations-no-fields",
		},
		{
			Namespace:   "wildcard-pod-annotations-no-fields",
			Annotations: []string{`seccomp.security.alpha.kubernetes.io/pod: unconfined`},
			PodField:    "",
		},
		{
			Namespace:   "unconfined-pod-annotations-no-fields",
			Annotations: []string{`seccomp.security.alpha.kubernetes.io/pod: unconfined`},
			PodField:    "",
		},
		{
			Namespace: "wildcard-pod-no-annotations-fields",
			PodField:  "Unconfined",
		},
		{
			Namespace: "unconfined-pod-no-annotations-fields",
			PodField:  "Unconfined",
		},
		{
			Namespace:      "wildcard-container-annotations-no-fields",
			Annotations:    []string{`container.seccomp.security.alpha.kubernetes.io/busybox: unconfined`},
			ContainerField: "",
		},
		{
			Namespace:      "unconfined-container-annotations-no-fields",
			Annotations:    []string{`container.seccomp.security.alpha.kubernetes.io/busybox: unconfined`},
			ContainerField: "",
		},
		{
			Namespace:      "wildcard-container-no-annotations-fields",
			ContainerField: "Unconfined",
		},
		{
			Namespace:      "unconfined-container-no-annotations-fields",
			ContainerField: "Unconfined",
		},
		{
			Namespace:   "unconfined-pod-annotations-fields-conflict",
			Annotations: []string{`seccomp.security.alpha.kubernetes.io/pod: unconfined`},
			PodField:    "RuntimeDefault",
		},
		{
			Namespace:      "unconfined-container-annotations-fields-conflict",
			Annotations:    []string{`container.seccomp.security.alpha.kubernetes.io/busybox: unconfined`},
			ContainerField: "RuntimeDefault",
		},
	}

	for _, experimentData := range experiments {
		var yamlBuilder bytes.Buffer

		experimentTemplate, err := ioutil.ReadFile(experimentPath)
		if err != nil {
			return err
		}
		experiment, err := template.New("SCCs").Parse(string(experimentTemplate))
		if err != nil {
			return err
		}
		experiment.Execute(&yamlBuilder, experimentData)

		outputPath := filepath.Join(outPath, fmt.Sprintf("%s.yaml", experimentData.Namespace))
		if err := ioutil.WriteFile(outputPath, yamlBuilder.Bytes(), 0644); err != nil {
			return err
		}
	}

	return nil
}
