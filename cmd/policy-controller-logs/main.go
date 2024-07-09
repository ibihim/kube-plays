package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	controllerName = "pod-security-admission-label-synchronization-controller"
)

func main() {
	pattern := flag.String("pattern", fmt.Sprintf("= %s =", controllerName), "Pattern to search for in logs")
	createResources := flag.Bool("create", false, "Create new namespaces and pods before searching")
	getLogs := flag.Bool("logs", true, "Get logs for the controller")
	debug := flag.Bool("debug", false, "Enable debug logging")
	flag.Parse()

	// Use the current context in kubeconfig
	kubeconfig := filepath.Join(os.Getenv("HOME"), ".kube", "config")
	if envVar := os.Getenv("KUBECONFIG"); envVar != "" {
		kubeconfig = envVar
	}

	if *debug {
		fmt.Printf(`
Options:
	pattern: %s
	createResources: %t
	getLogs: %t
	debug: %t
	kubeconfig: %s
`,
			*pattern,
			*createResources,
			*getLogs,
			*debug,
			kubeconfig,
		)
	}

	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		panic(err.Error())
	}

	// Create the clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}

	// Create namespaces and pods
	if *createResources {
		// Namespace 1
		err = createNamespaceAndPod(clientset, "test-namespace-1", map[string]string{
			"pod-security.kubernetes.io/warn":                "restricted",
			"pod-security.kubernetes.io/audit":               "restricted",
			"security.openshift.io/scc.podSecurityLabelSync": "false",
		}, controllerName)
		if err != nil {
			fmt.Printf("Error creating namespace and pod 1: %v\n", err)
			return
		}

		// Namespace 2
		err = createNamespaceAndPod(clientset, "openshift-test-namespace-2", nil, "")
		if err != nil {
			fmt.Printf("Error creating namespace and pod 2: %v\n", err)
			return
		}

		// Namespace 3
		err = createNamespaceAndPod(clientset, "test-namespace-3", map[string]string{
			"pod-security.kubernetes.io/warn":  "restricted",
			"pod-security.kubernetes.io/audit": "restricted",
		}, "kubectl-edit")
		if err != nil {
			fmt.Printf("Error creating namespace and pod 3: %v\n", err)
			return
		}
	}

	if *getLogs {
		// Get all pods in all namespaces
		pods, err := clientset.CoreV1().Pods("").List(context.TODO(), metav1.ListOptions{})
		if err != nil {
			panic(err.Error())
		}

		var wg sync.WaitGroup
		for _, pod := range pods.Items {
			wg.Add(1)
			go func(pod corev1.Pod) {
				defer wg.Done()
				searchPodLogs(clientset, &pod, *pattern)
			}(pod)
		}

		wg.Wait()
		fmt.Println("Search completed.")
	}
}

func createNamespaceAndPod(
	clientset *kubernetes.Clientset,
	nsName string,
	nsLabels map[string]string,
	fieldManager string,
) error {
	// Create namespace
	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: nsName,
		},
	}

	if nsLabels != nil {
		namespace.ObjectMeta.Labels = nsLabels
	}

	opts := metav1.CreateOptions{}

	if fieldManager != "" {
		opts.FieldManager = fieldManager
	}

	_, err := clientset.CoreV1().Namespaces().Create(context.TODO(), namespace, opts)
	if err != nil {
		return fmt.Errorf("error creating namespace: %v", err)
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: nsName,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "test-container",
					Image: "busybox",
					Command: []string{
						"sh",
						"-c",
						"echo 'Pod is running'; sleep infinity",
					},
					SecurityContext: &corev1.SecurityContext{
						AllowPrivilegeEscalation: boolPtr(true),
					},
				},
			},
		},
	}
	_, err = clientset.CoreV1().Pods(nsName).Create(context.TODO(), pod, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("error creating pod: %v", err)
	}
	fmt.Printf("Pod created successfully in namespace %s\n", nsName)

	// Wait for the pod to be running
	err = waitForPodRunning(clientset, nsName, "test-pod")
	if err != nil {
		return fmt.Errorf("error waiting for pod to be running: %v", err)
	}
	fmt.Printf("Pod is now running in namespace %s\n", nsName)

	return nil
}

func waitForPodRunning(clientset *kubernetes.Clientset, namespace, name string) error {
	return wait.PollImmediate(time.Second, time.Minute, func() (bool, error) {
		pod, err := clientset.CoreV1().Pods(namespace).Get(context.TODO(), name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		return pod.Status.Phase == corev1.PodRunning, nil
	})
}

func searchPodLogs(clientset *kubernetes.Clientset, pod *corev1.Pod, pattern string) {
	podLogOpts := corev1.PodLogOptions{}
	req := clientset.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, &podLogOpts)
	podLogs, err := req.Stream(context.TODO())
	if err != nil {
		fmt.Printf("Error opening log stream for %s/%s: %v\n", pod.Namespace, pod.Name, err)
		return
	}
	defer podLogs.Close()

	buf := new(bytes.Buffer)
	_, err = io.Copy(buf, podLogs)
	if err != nil {
		fmt.Printf("Error reading logs for %s/%s: %v\n", pod.Namespace, pod.Name, err)
		return
	}

	logs := buf.String()
	re := regexp.MustCompile(pattern)
	matches := re.FindAllString(logs, -1)

	if len(matches) > 0 {
		fmt.Printf("Found %d matches in %s/%s. Saving logs...\n", len(matches), pod.Namespace, pod.Name)
		filename := fmt.Sprintf("logs_%s_%s_%s.txt", pod.Namespace, pod.Name, time.Now().Format("20060102_150405"))
		err := os.WriteFile(filename, buf.Bytes(), 0644)
		if err != nil {
			fmt.Printf("Error saving logs for %s/%s: %v\n", pod.Namespace, pod.Name, err)
		} else {
			fmt.Printf("Logs saved to %s\n", filename)
		}
	} else {
		fmt.Printf("No matches found in %s/%s\n", pod.Namespace, pod.Name)
	}
}

func boolPtr(b bool) *bool    { return &b }
func int32Ptr(i int32) *int32 { return &i }
