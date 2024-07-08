package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	applyconfigurationsv1 "k8s.io/client-go/applyconfigurations/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"
)

const (
	ownerName string = "ibihim"
)

func main() {
	if err := app(); err != nil {
		panic(err)
	}
}

func app() error {
	clientset, err := createClientSet()
	if err != nil {
		return fmt.Errorf("Error creating clientset: %w", err)
	}

	ctx := context.Background()
	nsName := "test-namespace-" + time.Now().Format("20060102-150405")

	if err := createNamespace(ctx, clientset, nsName); err != nil {
		return err
	}

	if err := printNamespaceLabels(ctx, clientset, nsName); err != nil {
		return err
	}

	if err := applyConfiguration(ctx, clientset, nsName); err != nil {
		return err
	}

	if err := printNamespaceLabels(ctx, clientset, nsName); err != nil {
		return err
	}

	if err := applyConfigurationLabelCheck(ctx, clientset, nsName); err != nil {
		return err
	}

	if err := cleanUp(ctx, clientset, nsName); err != nil {
		return err
	}

	return nil
}

func cleanUp(ctx context.Context, clientset *kubernetes.Clientset, nsName string) error {
	err := clientset.CoreV1().Namespaces().Delete(ctx, nsName, metav1.DeleteOptions{})
	if err != nil {
		return fmt.Errorf("Error deleting namespace: %w", err)
	}

	return nil
}

func applyConfigurationLabelCheck(ctx context.Context, clientset *kubernetes.Clientset, nsName string) error {
	ns, err := clientset.CoreV1().Namespaces().Get(ctx, nsName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("Error getting namespace: %w", err)
	}

	nsApplyConfig, err := applyconfigurationsv1.ExtractNamespace(ns, ownerName)
	if err != nil {
		return err
	}

	fmt.Println("---")
	fmt.Println("Labels from", nsName)
	for k, v := range nsApplyConfig.Labels {
		fmt.Printf("- %s: %s\n", k, v)
	}

	return nil
}

func applyConfiguration(ctx context.Context, clientset *kubernetes.Clientset, nsName string) error {
	nsApply := applyconfigurationsv1.Namespace(nsName).WithLabels(map[string]string{
		"my-enforce": "restricted",
	})

	_, err := clientset.CoreV1().Namespaces().Apply(ctx, nsApply, metav1.ApplyOptions{
		FieldManager: ownerName,
	})
	if err != nil {
		return fmt.Errorf("Error applying configuration: %w", err)
	}

	return nil
}

func printNamespaceLabels(ctx context.Context, clientset *kubernetes.Clientset, nsName string) error {
	ns, err := clientset.CoreV1().Namespaces().Get(ctx, nsName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("Error getting namespace: %w", err)
	}

	fmt.Printf("---\nLabels for namespace %s:\n", nsName)

	for k, v := range ns.Labels {
		fmt.Printf("- %s: %s\n", k, v)
	}

	return nil
}

func createNamespace(ctx context.Context, clientset *kubernetes.Clientset, nsName string) error {
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: nsName,
			Labels: map[string]string{
				"foo": "bar",
			},
		},
	}

	_, err := clientset.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("Error creating namespace: %w", err)
	}

	// Wait for the namespace to be fully created
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		_, err := clientset.CoreV1().Namespaces().Get(ctx, nsName, metav1.GetOptions{})
		return err
	})
	if err != nil {
		return fmt.Errorf("Error waiting for namespace to be created: %w", err)
	}

	return nil
}

func createClientSet() (*kubernetes.Clientset, error) {
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		return nil, errors.New("KUBECONFIG environment variable not set")
	}

	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("Error building kubeconfig: %w", err)
	}

	return kubernetes.NewForConfig(config)
}
