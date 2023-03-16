package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"regexp"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

func main() {
	if err := app(); err != nil {
		panic(err)
	}
}

func app() error {
	kubeconfig := flag.String("kubeconfig", "/Users/ibihim/.kube/config", "absolute path to the kubeconfig file")
	flag.Parse()

	config, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
	if err != nil {
		return err
	}

	// Setup a client with a custom WarningHandler that collects the warnings,
	// instead of printing them to std...err? stdout?
	wh := &warningsMapper{}
	config.WarningHandler = wh
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return err
	}

	// Get a list of all the namespaces.
	namespaceList, err := client.CoreV1().Namespaces().List(context.Background(), metav1.ListOptions{})
	if err != nil {
		return err
	}

	// Gather all the warnings for each namespace, when enforcing audit-level.
	for _, namespace := range namespaceList.Items {
		stricterNamespace := mapAuditToEnforce(&namespace)
		_, err := client.CoreV1().Namespaces().Update(context.Background(), stricterNamespace, metav1.UpdateOptions{DryRun: []string{"All"}})
		if err != nil {
			return err
		}
	}

	// Iterate through the collected violations by namespace.
	for _, psv := range wh.PSViolations {
		// Iterate through the pods within a namespace that violate the new
		// PodSecurity level and get the pod's deployment.
		for _, podViolation := range psv.PodViolations {
			// Get the pod.
			pod, err := client.CoreV1().Pods(psv.Namespace).Get(context.Background(), podViolation.Name, metav1.GetOptions{})
			if err != nil {
				return err
			}
			podViolation.Pod = pod

			// If the pod is owned by a Deployment, get the deployment.
			// If the pod is owned by a ReplicaSet, get the ReplicaSet's owner.
			switch {
			case pod.OwnerReferences[0].Kind == "Deployment":
				deployment, err := client.AppsV1().Deployments(psv.Namespace).Get(context.Background(), pod.OwnerReferences[0].Name, metav1.GetOptions{})
				if err != nil {
					return err
				}
				podViolation.Deployment = deployment
			case pod.OwnerReferences[0].Kind == "ReplicaSet":
				replicaSet, err := client.AppsV1().ReplicaSets(psv.Namespace).Get(context.Background(), pod.OwnerReferences[0].Name, metav1.GetOptions{})
				if err != nil {
					return err
				}
				deployment, err := client.AppsV1().Deployments(psv.Namespace).Get(context.Background(), replicaSet.OwnerReferences[0].Name, metav1.GetOptions{})
				if err != nil {
					return err
				}
				podViolation.Deployment = deployment
			}
		}
	}

	warnings := wh.String()
	fmt.Println(warnings)

	return nil
}

// Warnings Mapping
type warningsMapper struct {
	defaultHandler rest.WarningHandler
	PSViolations   []*PSViolation
}

type PSViolation struct {
	Namespace     string
	Level         string
	PodViolations []*PodViolation
}

type PodViolation struct {
	Name       string
	Deployment *appsv1.Deployment
	Pod        *corev1.Pod
	Violations []string
}

var titleRegex = regexp.MustCompile(`"([^"]+)"`)

// HandleWarningHeader implements the WarningHandler interface. It stores the
// warning in the handler and forwards to the default handler.
func (w *warningsMapper) HandleWarningHeader(code int, agent string, text string) {
	if text == "" {
		return
	}

	if len(w.PSViolations) == 0 {
		w.PSViolations = []*PSViolation{}
	}

	// Namespace Warning Message
	if strings.HasPrefix(text, "existing pods in namespace") {
		// The text should look like "existing pods in namespace "my-namespace" violate the new PodSecurity enforce level "mylevel:v1.2.3"
		titleMatches := titleRegex.FindAllStringSubmatch(text, -1)
		psv := PSViolation{
			Namespace: titleMatches[0][1],
			Level:     titleMatches[1][1],
		}

		w.PSViolations = append(w.PSViolations, &psv)
	} else {
		// Pod Warning Message, assume last PSViolation is the one we belong to.
		lastPSViolation := w.PSViolations[len(w.PSViolations)-1]
		// The text should look like this: {pod name}: {policy warning A}, {policy warning B}, ...
		textSplit := strings.Split(text, ": ")
		podName := strings.TrimSpace(textSplit[0])
		violations := strings.Split(textSplit[1], ", ")
		podViolation := PodViolation{
			Name:       podName,
			Violations: violations,
		}
		lastPSViolation.PodViolations = append(lastPSViolation.PodViolations, &podViolation)
	}

	if w.defaultHandler == nil {
		return
	}

	w.defaultHandler.HandleWarningHeader(code, agent, text)
}

// String returns the warnings that are stored by the handler.
func (w *warningsMapper) String() string {
	if len(w.PSViolations) == 0 {
		return ""
	}

	// Example Warning
	// [0] existing pods in namespace "p0t-sekurity" violate the new PodSecurity enforce level "restricted:latest"
	// [1] p0t-sekurity: allowPrivilegeEscalation != false, unrestricted capabilities, runAsNonRoot != true, seccompProfile

	var b bytes.Buffer
	if err := json.NewEncoder(&b).Encode(w.PSViolations); err != nil {
		return ""
	}

	return b.String()
}

func mapAuditToEnforce(namespace *corev1.Namespace) *corev1.Namespace {
	ns := namespace.DeepCopy()

	if ns.Labels["pod-security.kubernetes.io/audit"] == "" {
		namespace.Labels["pod-security.kubernetes.io/audit"] = "restricted"
	}

	ns.Labels["pod-security.kubernetes.io/enforce"] = namespace.Labels["pod-security.kubernetes.io/audit"]

	return ns
}
